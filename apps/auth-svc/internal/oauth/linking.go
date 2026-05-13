// linking.go — Story 2.3 account-linking decision matrix.
//
// Wright Round 1 Q2 ruling (a) — "verify-only auto-link" + 10-branch matrix.
// The decision is the security boundary between OAuth-asserted identity
// (provider says "this email is verified") and local identity (this email
// already exists in `users`, possibly verified locally, possibly linked
// to a different provider).
//
// Branches encoded here (10 total):
//
//   A — existing OAuth user (provider+subject hit on first lookup)
//     A.active           → re-login
//     A.locked           → bypass-lock (BR-3.10; password lock doesn't gate OAuth)
//     A.suspended        → ErrAccountSuspended (handler emits 403)
//     A.pending_deletion → ErrAuthenticationFailed (anti-enum, Story 2.2 m-5)
//
//   B — email matched but provider+subject did not
//     B.1 auto-link      → email_verified_at IS NOT NULL AND oauth_provider IS NULL
//     B.2 unverified     → email_verified_at IS NULL → ErrLinkUnverified
//     B.3 inconsistency  → same provider+subject row visible only via email
//                          (race; very rare — fall through to A semantics)
//     B.4 subject mismatch → same provider, different subject → ErrSubjectMismatch
//     B.5 cross-provider → different provider already linked → ErrCrossProvider
//     B.suspended / B.pending_deletion → anti-enum / suspended sentinel
//
//   C — fresh email; UpsertOAuthUser INSERT (ON CONFLICT DO NOTHING). When
//       the upsert races and returns 0 rows, we re-run Branch B against the
//       freshly-inserted row.
//
// BR-3.5 (2FA hook): RequiresMFA flag is the only branch-orthogonal
// outcome bit — set when users.totp_enabled=true on Branch A re-login.
// Branch B/C never set RequiresMFA because B.1 preserves existing
// totp_enabled (carry-through; tested) and C is a brand-new user.
package oauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// LinkBranch labels each decision outcome for audit + observability. Keep
// the values stable across releases — audit consumers + Grafana panels
// pattern-match on them.
type LinkBranch string

const (
	BranchARelogin           LinkBranch = "A"
	BranchASuspended         LinkBranch = "A.suspended"
	BranchAPendingDeletion   LinkBranch = "A.pending_deletion"
	BranchB1AutoLink         LinkBranch = "B.1"
	BranchB2UnverifiedReject LinkBranch = "B.2"
	BranchB3Inconsistency    LinkBranch = "B.3"
	BranchB4SubjectMismatch  LinkBranch = "B.4"
	BranchB5CrossProvider    LinkBranch = "B.5"
	BranchBSuspended         LinkBranch = "B.suspended"
	BranchBPendingDeletion   LinkBranch = "B.pending_deletion"
	BranchCNewUser           LinkBranch = "C"
)

// LinkOutcome bundles every fact the handler needs to translate the
// decision into a (HTTP response + audit event + JWT claims) trio.
//
// UserID is uuid.Nil on error paths. IsNewUser=true only on fresh
// Branch C (post-race fallback to Branch B clears this).
// RequiresMFA carries the BR-3.5 hook signal. WasLocked is the
// bypass-lock audit marker — Grafana panel #8 (m-3) plots it.
type LinkOutcome struct {
	UserID      uuid.UUID
	Email       string
	Branch      LinkBranch
	IsNewUser   bool
	RequiresMFA bool
	WasLocked   bool
}

// Sentinels mapped to HTTP responses by the api-gateway handler.
//
// Anti-enum: ErrAuthenticationFailed shadows pending_deletion + (in B)
// suspended-after-link statuses; user response is identical to
// "email not found". This matches Story 2.2 m-5 ruling.
var (
	ErrAccountSuspended      = errors.New("oauth: account suspended")
	ErrAuthenticationFailed  = errors.New("oauth: authentication failed")
	ErrLinkUnverified        = errors.New("oauth: cannot link to unverified email")
	ErrSubjectMismatch       = errors.New("oauth: oauth subject changed")
	ErrCrossProvider         = errors.New("oauth: email linked to different provider")
	ErrLinkingInconsistency  = errors.New("oauth: linking state inconsistent")
)

// UsersRepo is the narrow subset of `repository` that the decision uses.
// Production wires the real package functions through a thin adaptor;
// tests inject an in-memory fake. Keeping the surface narrow makes the
// 10-branch test matrix readable.
type UsersRepo interface {
	GetUserByOAuth(ctx context.Context, provider, subject string) (*repository.User, error)
	GetUserByEmail(ctx context.Context, email string) (*repository.User, error)
	LinkOAuthIdentity(ctx context.Context, userID uuid.UUID, provider, subject string) (bool, error)
	UpsertOAuthUser(ctx context.Context, p repository.UpsertOAuthUserParams) (uuid.UUID, bool, error)
	TouchUserUpdatedAt(ctx context.Context, userID uuid.UUID) error
}

// LinkingService is the decision engine. cmd/server constructs one
// instance per process with the real repository.
type LinkingService struct {
	repo UsersRepo
}

func NewLinkingService(repo UsersRepo) *LinkingService {
	return &LinkingService{repo: repo}
}

// DecideAndLink resolves the (provider, subject, email) tuple to a
// LinkOutcome. Side effects:
//   - Branch A: TouchUserUpdatedAt (no identity mutation)
//   - Branch B.1: LinkOAuthIdentity (UPDATE oauth_provider+subject+updated_at)
//   - Branch C: UpsertOAuthUser (INSERT ON CONFLICT DO NOTHING)
//   - Branch C race → re-run Branch B once
func (s *LinkingService) DecideAndLink(ctx context.Context, provider, subject, email, locale string) (LinkOutcome, error) {
	// Step 1: Branch A lookup. idx_users_oauth carries this.
	user, err := s.repo.GetUserByOAuth(ctx, provider, subject)
	if err == nil {
		return s.handleBranchA(ctx, user)
	}
	if !errors.Is(err, repository.ErrUserNotFound) {
		return LinkOutcome{}, fmt.Errorf("oauth: lookup by oauth: %w", err)
	}

	// Step 2: Branch B lookup (by email).
	user, err = s.repo.GetUserByEmail(ctx, email)
	if err == nil {
		return s.handleBranchB(ctx, user, provider, subject)
	}
	if !errors.Is(err, repository.ErrUserNotFound) {
		return LinkOutcome{}, fmt.Errorf("oauth: lookup by email: %w", err)
	}

	// Step 3: Branch C — fresh user. Upsert with race protection.
	return s.handleBranchC(ctx, provider, subject, email, locale)
}

// handleBranchA — existing OAuth user. Status check decides the response.
func (s *LinkingService) handleBranchA(ctx context.Context, u *repository.User) (LinkOutcome, error) {
	switch u.Status {
	case "active":
		// Happy path — re-login.
	case "locked":
		// BR-3.10 — bypass the lock. OAuth ≠ password; the lock exists to
		// throttle password-brute-force and should not gate provider-vouched
		// identity. Carry WasLocked=true marker so the audit publisher emits
		// bypass_lock=true and Grafana panel #8 (m-3) counts it.
	case "suspended":
		return LinkOutcome{Branch: BranchASuspended}, ErrAccountSuspended
	case "pending_deletion":
		return LinkOutcome{Branch: BranchAPendingDeletion}, ErrAuthenticationFailed
	default:
		// Unknown status — treat conservatively as auth-failed.
		return LinkOutcome{Branch: BranchAPendingDeletion}, ErrAuthenticationFailed
	}

	if err := s.repo.TouchUserUpdatedAt(ctx, u.ID); err != nil {
		return LinkOutcome{}, fmt.Errorf("oauth: touch user: %w", err)
	}
	return LinkOutcome{
		UserID:      u.ID,
		Email:       u.Email,
		Branch:      BranchARelogin,
		IsNewUser:   false,
		RequiresMFA: u.TOTPEnabled,
		WasLocked:   u.Status == "locked",
	}, nil
}

// handleBranchB — email matched. Status + linking-state-based decision.
func (s *LinkingService) handleBranchB(ctx context.Context, u *repository.User, provider, subject string) (LinkOutcome, error) {
	// Status-precedence: suspended + pending_deletion are short-circuits.
	switch u.Status {
	case "suspended":
		return LinkOutcome{Branch: BranchBSuspended}, ErrAccountSuspended
	case "pending_deletion":
		return LinkOutcome{Branch: BranchBPendingDeletion}, ErrAuthenticationFailed
	}

	// Already linked to this provider?
	if u.OAuthProvider != nil && *u.OAuthProvider == provider {
		if u.OAuthSubject != nil && *u.OAuthSubject == subject {
			// Race inconsistency — Branch A should have matched in step 1.
			// Re-touch updated_at and return BranchARelogin semantics so the
			// flow still issues a JWT (the row IS the user).
			if err := s.repo.TouchUserUpdatedAt(ctx, u.ID); err != nil {
				return LinkOutcome{}, fmt.Errorf("oauth: touch user (B.3): %w", err)
			}
			return LinkOutcome{
				UserID:      u.ID,
				Email:       u.Email,
				Branch:      BranchB3Inconsistency,
				IsNewUser:   false,
				RequiresMFA: u.TOTPEnabled,
			}, nil
		}
		// Same provider, different subject — user's provider account changed.
		return LinkOutcome{Branch: BranchB4SubjectMismatch}, ErrSubjectMismatch
	}

	if u.OAuthProvider != nil && *u.OAuthProvider != provider {
		return LinkOutcome{Branch: BranchB5CrossProvider}, ErrCrossProvider
	}

	// oauth_provider IS NULL — verified-only auto-link decision.
	if u.EmailVerifiedAt == nil {
		return LinkOutcome{Branch: BranchB2UnverifiedReject}, ErrLinkUnverified
	}

	// Branch B.1 — auto-link.
	ok, err := s.repo.LinkOAuthIdentity(ctx, u.ID, provider, subject)
	if err != nil {
		return LinkOutcome{}, fmt.Errorf("oauth: link identity: %w", err)
	}
	if !ok {
		// Lost-update race — re-run Branch A against the (now-linked) row.
		return LinkOutcome{Branch: BranchLinkingInconsistency()}, ErrLinkingInconsistency
	}
	return LinkOutcome{
		UserID:      u.ID,
		Email:       u.Email,
		Branch:      BranchB1AutoLink,
		IsNewUser:   false,
		RequiresMFA: u.TOTPEnabled,
	}, nil
}

// BranchLinkingInconsistency centralises the constant used as a sentinel
// branch label for the (rare) lost-update fallback. Returning it from a
// helper avoids leaking the underlying string-typing into call sites.
func BranchLinkingInconsistency() LinkBranch { return BranchB3Inconsistency }

// handleBranchC — fresh email; upsert + race fallback.
func (s *LinkingService) handleBranchC(ctx context.Context, provider, subject, email, locale string) (LinkOutcome, error) {
	id, created, err := s.repo.UpsertOAuthUser(ctx, repository.UpsertOAuthUserParams{
		Email:    email,
		Provider: provider,
		Subject:  subject,
		Locale:   locale,
	})
	if err != nil {
		return LinkOutcome{}, fmt.Errorf("oauth: upsert user: %w", err)
	}
	if created {
		return LinkOutcome{
			UserID:    id,
			Email:     email,
			Branch:    BranchCNewUser,
			IsNewUser: true,
		}, nil
	}
	// Race — another callback won the INSERT. Re-run the email lookup.
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		return LinkOutcome{}, fmt.Errorf("oauth: race fallback lookup: %w", err)
	}
	return s.handleBranchB(ctx, user, provider, subject)
}
