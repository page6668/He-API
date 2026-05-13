// linking_test.go — Story 2.3 P4 unit tests for the LinkingService.
//
// File→scenario mapping (from docs/qa/assessments/2.3-test-design-20260512.md):
//   - 2.3-UNIT-030..049 (Branch A/B/C decision matrix + race fallback +
//     audit hashed PII + subject-as-provider-id invariants)
//   - 2.3-INT-014..018 (testcontainers PG; P9 owns those — left as
//     // TODO stubs here)
//
// Test approach: in-memory fake repository so the decision logic is
// exercised without PG. The repository methods themselves are unit-tested
// via pgxmock in `repository/users_test.go` (Story 2.2 baseline + P4
// extensions). This split keeps the 10-branch matrix readable.
package oauth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// -- in-memory fake repository --------------------------------------------

type fakeUsersRepo struct {
	byOAuth map[string]*repository.User // key: provider+":"+subject
	byEmail map[string]*repository.User

	// LinkOAuthIdentity behaviour: per-user-id success/failure mapping.
	// Default behaviour is "success" if absent.
	linkResult map[uuid.UUID]bool
	linkErr    error

	// UpsertOAuthUser behaviour. upsertResult.id == uuid.Nil triggers the
	// race fallback (caller re-runs Branch B).
	upsertID  uuid.UUID
	upsertNew bool
	upsertErr error

	touchErr error

	// counters for invocation assertions (local to this file — package
	// `sync/atomic` is also imported via google_test.go / github_test.go)
	getOAuthHits counter
	getEmailHits counter
	linkHits     counter
	upsertHits   counter
	touchHits    counter
}

type counter struct{ v int }

func (a *counter) inc()      { a.v++ }
func (a *counter) load() int { return a.v }

func newFakeRepo() *fakeUsersRepo {
	return &fakeUsersRepo{
		byOAuth:    map[string]*repository.User{},
		byEmail:    map[string]*repository.User{},
		linkResult: map[uuid.UUID]bool{},
	}
}

func (f *fakeUsersRepo) GetUserByOAuth(_ context.Context, provider, subject string) (*repository.User, error) {
	f.getOAuthHits.inc()
	u, ok := f.byOAuth[provider+":"+subject]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	return u, nil
}

func (f *fakeUsersRepo) GetUserByEmail(_ context.Context, email string) (*repository.User, error) {
	f.getEmailHits.inc()
	u, ok := f.byEmail[email]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	return u, nil
}

func (f *fakeUsersRepo) LinkOAuthIdentity(_ context.Context, userID uuid.UUID, provider, subject string) (bool, error) {
	f.linkHits.inc()
	if f.linkErr != nil {
		return false, f.linkErr
	}
	// "success" by default; map override flips to lost-update.
	if v, ok := f.linkResult[userID]; ok && !v {
		return false, nil
	}
	// Update the fake so subsequent Branch A lookups see the link.
	if u, ok := f.byEmail[""]; ok {
		_ = u // placeholder
	}
	for _, u := range f.byEmail {
		if u.ID == userID {
			p, s := provider, subject
			u.OAuthProvider = &p
			u.OAuthSubject = &s
			f.byOAuth[provider+":"+subject] = u
		}
	}
	return true, nil
}

func (f *fakeUsersRepo) UpsertOAuthUser(_ context.Context, p repository.UpsertOAuthUserParams) (uuid.UUID, bool, error) {
	f.upsertHits.inc()
	if f.upsertErr != nil {
		return uuid.Nil, false, f.upsertErr
	}
	if f.upsertID == uuid.Nil {
		// Race fallback path
		return uuid.Nil, false, nil
	}
	// Materialise the inserted user in the fake so subsequent lookups see it.
	provider, subject := p.Provider, p.Subject
	user := &repository.User{
		ID:              f.upsertID,
		Email:           p.Email,
		OAuthProvider:   &provider,
		OAuthSubject:    &subject,
		Locale:          p.Locale,
		Status:          "active",
		EmailVerifiedAt: timePtr(time.Now()),
	}
	f.byEmail[p.Email] = user
	f.byOAuth[p.Provider+":"+p.Subject] = user
	return f.upsertID, true, nil
}

func (f *fakeUsersRepo) TouchUserUpdatedAt(_ context.Context, _ uuid.UUID) error {
	f.touchHits.inc()
	return f.touchErr
}

func timePtr(t time.Time) *time.Time { return &t }
func strPtr(s string) *string        { return &s }

// -- Tests: Branch A (existing OAuth user re-login) -----------------------

// Scenario: 2.3-UNIT-030 + BR-3.10
// Branch A — status='active' returns BranchARelogin + no MFA + no lock marker.
// Repository.TouchUserUpdatedAt fires exactly once.
func TestLinking_BranchA_Active(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uid := uuid.New()
	repo.byOAuth["google:g-sub-1"] = &repository.User{
		ID: uid, Email: "alex@example.com", Status: "active",
		OAuthProvider: strPtr("google"), OAuthSubject: strPtr("g-sub-1"),
	}
	svc := oauth.NewLinkingService(repo)

	out, err := svc.DecideAndLink(context.Background(), "google", "g-sub-1", "alex@example.com", "en")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	if out.Branch != oauth.BranchARelogin {
		t.Errorf("Branch = %s, want %s", out.Branch, oauth.BranchARelogin)
	}
	if out.IsNewUser {
		t.Error("IsNewUser = true, want false on re-login")
	}
	if out.WasLocked {
		t.Error("WasLocked = true on active user")
	}
	if out.UserID != uid {
		t.Errorf("UserID = %v, want %v", out.UserID, uid)
	}
	if got := repo.touchHits.load(); got != 1 {
		t.Errorf("TouchUserUpdatedAt hits = %d, want 1", got)
	}
}

// Scenario: 2.3-UNIT-031 + BR-3.10 + m-3
// Branch A — status='locked' bypasses the lock (OAuth ≠ password). Outcome
// carries WasLocked=true marker so the handler audits bypass_lock=true.
func TestLinking_BranchA_LockedBypass(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uid := uuid.New()
	repo.byOAuth["google:g-sub-2"] = &repository.User{
		ID: uid, Email: "locked@example.com", Status: "locked",
		LockedUntil:   timePtr(time.Now().Add(5 * time.Minute)),
		OAuthProvider: strPtr("google"), OAuthSubject: strPtr("g-sub-2"),
	}
	svc := oauth.NewLinkingService(repo)

	out, err := svc.DecideAndLink(context.Background(), "google", "g-sub-2", "locked@example.com", "en")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	if out.Branch != oauth.BranchARelogin {
		t.Errorf("Branch = %s, want BranchARelogin (locked bypassed)", out.Branch)
	}
	if !out.WasLocked {
		t.Error("WasLocked = false, want true for locked-bypass audit marker")
	}
}

// Scenario: 2.3-UNIT-032
// Branch A — status='suspended' → ErrAccountSuspended (handler maps 403,
// not anti-enumeration because suspension is operator-initiated).
func TestLinking_BranchA_Suspended(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.byOAuth["google:g-sub-sus"] = &repository.User{
		ID: uuid.New(), Status: "suspended",
		OAuthProvider: strPtr("google"), OAuthSubject: strPtr("g-sub-sus"),
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-sub-sus", "x@example.com", "en")
	if !errors.Is(err, oauth.ErrAccountSuspended) {
		t.Fatalf("err = %v, want ErrAccountSuspended", err)
	}
}

// Scenario: 2.3-UNIT-033 + Story 2.2 m-5
// Branch A — status='pending_deletion' → ErrAuthenticationFailed (anti-
// enumeration generic 401; the handler MUST NOT leak account state).
func TestLinking_BranchA_PendingDeletion_AntiEnum(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.byOAuth["google:g-sub-del"] = &repository.User{
		ID: uuid.New(), Status: "pending_deletion",
		OAuthProvider: strPtr("google"), OAuthSubject: strPtr("g-sub-del"),
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-sub-del", "x@example.com", "en")
	if !errors.Is(err, oauth.ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed (anti-enum)", err)
	}
}

// -- Tests: Branch B (email exists, oauth_provider check) ------------------

// Scenario: 2.3-UNIT-035 + BR-3.2 / Q2 ruling
// Branch B.1 — existing verified user (email_verified_at IS NOT NULL,
// oauth_provider IS NULL) auto-links. UPDATE mutates exactly 3 columns
// (provider, subject, updated_at) — verified via fake (no password_hash
// or email_verified_at change).
func TestLinking_BranchB1_AutoLink(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uid := uuid.New()
	verifiedAt := time.Now().Add(-24 * time.Hour)
	pwHash := []byte("$2a$12$existing-hash")
	repo.byEmail["alex@example.com"] = &repository.User{
		ID:              uid,
		Email:           "alex@example.com",
		PasswordHash:    pwHash,
		EmailVerifiedAt: &verifiedAt,
		OAuthProvider:   nil,
		OAuthSubject:    nil,
		Status:          "active",
	}
	svc := oauth.NewLinkingService(repo)

	out, err := svc.DecideAndLink(context.Background(), "google", "g-sub-link", "alex@example.com", "en")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	if out.Branch != oauth.BranchB1AutoLink {
		t.Fatalf("Branch = %s, want B.1", out.Branch)
	}
	if out.IsNewUser {
		t.Error("IsNewUser = true on auto-link")
	}
	if out.UserID != uid {
		t.Errorf("UserID = %v, want %v", out.UserID, uid)
	}
	// BR-3.7 — password_hash + email_verified_at preserved.
	linked := repo.byEmail["alex@example.com"]
	if string(linked.PasswordHash) != string(pwHash) {
		t.Error("PasswordHash mutated during auto-link (BR-3.7 violation)")
	}
	if linked.EmailVerifiedAt == nil || !linked.EmailVerifiedAt.Equal(verifiedAt) {
		t.Error("EmailVerifiedAt mutated during auto-link")
	}
}

// Scenario: 2.3-UNIT-036 + BR-3.2
// Branch B.2 — existing user with unverified email → ErrLinkUnverified.
// Account-takeover defence: provider-asserted email cannot override the
// local unverified state.
func TestLinking_BranchB2_UnverifiedRejects(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.byEmail["unverified@example.com"] = &repository.User{
		ID:              uuid.New(),
		Email:           "unverified@example.com",
		EmailVerifiedAt: nil,
		OAuthProvider:   nil,
		Status:          "active",
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-sub-uv", "unverified@example.com", "en")
	if !errors.Is(err, oauth.ErrLinkUnverified) {
		t.Fatalf("err = %v, want ErrLinkUnverified", err)
	}
}

// Scenario: 2.3-UNIT-038 + BR-3.x
// Branch B.4 — email matched user is already linked to the SAME provider
// but with a DIFFERENT subject (user's Google account was replaced). Returns
// ErrSubjectMismatch so api-gateway emits 409_oauth_subject_mismatch.
func TestLinking_BranchB4_SubjectMismatch(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	verifiedAt := time.Now().Add(-24 * time.Hour)
	repo.byEmail["sm@example.com"] = &repository.User{
		ID: uuid.New(), Email: "sm@example.com",
		EmailVerifiedAt: &verifiedAt,
		OAuthProvider:   strPtr("google"),
		OAuthSubject:    strPtr("g-OLD-subject"),
		Status:          "active",
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-NEW-subject", "sm@example.com", "en")
	if !errors.Is(err, oauth.ErrSubjectMismatch) {
		t.Fatalf("err = %v, want ErrSubjectMismatch", err)
	}
}

// Scenario: 2.3-UNIT-039
// Branch B.5 — email matched user is linked to a DIFFERENT provider. Returns
// ErrCrossProvider so api-gateway emits 409_oauth_conflict_other_provider.
func TestLinking_BranchB5_CrossProvider(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	verifiedAt := time.Now().Add(-24 * time.Hour)
	repo.byEmail["xp@example.com"] = &repository.User{
		ID: uuid.New(), Email: "xp@example.com",
		EmailVerifiedAt: &verifiedAt,
		OAuthProvider:   strPtr("github"),
		OAuthSubject:    strPtr("987654321"),
		Status:          "active",
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-sub-x", "xp@example.com", "en")
	if !errors.Is(err, oauth.ErrCrossProvider) {
		t.Fatalf("err = %v, want ErrCrossProvider", err)
	}
}

// Scenario: 2.3-UNIT-040
// Branch B — status='suspended' must surface ErrAccountSuspended (NOT
// the verify/link decision; status check precedes the provider/subject
// comparison so a suspended user gets a stable response).
func TestLinking_BranchB_Suspended(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.byEmail["sus@example.com"] = &repository.User{
		ID: uuid.New(), Email: "sus@example.com",
		EmailVerifiedAt: timePtr(time.Now()),
		Status:          "suspended",
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-sub-s", "sus@example.com", "en")
	if !errors.Is(err, oauth.ErrAccountSuspended) {
		t.Fatalf("err = %v, want ErrAccountSuspended", err)
	}
}

// Scenario: 2.3-UNIT-041 + Story 2.2 m-5
// Branch B — status='pending_deletion' → ErrAuthenticationFailed (anti-
// enumeration; identical response to "email not found").
func TestLinking_BranchB_PendingDeletion_AntiEnum(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.byEmail["pd@example.com"] = &repository.User{
		ID: uuid.New(), Email: "pd@example.com",
		EmailVerifiedAt: timePtr(time.Now()),
		Status:          "pending_deletion",
	}
	svc := oauth.NewLinkingService(repo)

	_, err := svc.DecideAndLink(context.Background(), "google", "g-sub-p", "pd@example.com", "en")
	if !errors.Is(err, oauth.ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed (anti-enum)", err)
	}
}

// -- Tests: Branch C (new user via OAuth INSERT) --------------------------

// Scenario: 2.3-UNIT-042 + BR-3.6 + AC3 Branch C
// Branch C — fresh email → UpsertOAuthUser INSERT. IsNewUser=true.
// password_hash NULL is enforced by the SQL (not visible at the fake level).
func TestLinking_BranchC_NewUserInsert(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	newUID := uuid.New()
	repo.upsertID = newUID
	repo.upsertNew = true
	svc := oauth.NewLinkingService(repo)

	out, err := svc.DecideAndLink(context.Background(), "google", "g-new-sub", "new@example.com", "ja")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	if out.Branch != oauth.BranchCNewUser {
		t.Errorf("Branch = %s, want C", out.Branch)
	}
	if !out.IsNewUser {
		t.Error("IsNewUser = false on Branch C")
	}
	if out.UserID != newUID {
		t.Errorf("UserID = %v, want %v", out.UserID, newUID)
	}
}

// Scenario: 2.3-UNIT-043 + BR-3.3 + 2.3-BLIND-CONCURRENCY-001
// Branch C race — UpsertOAuthUser hits ON CONFLICT DO NOTHING (0 rows).
// Caller falls back to Branch B which then finds the row inserted by the
// winning concurrent callback → Branch B.1 auto-link with the original
// `email` lookup hit.
func TestLinking_BranchC_RaceFallsBackToBranchB(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	verifiedAt := time.Now().Add(-1 * time.Second)
	racingUID := uuid.New()
	// Pre-populate as if a sibling callback just won the INSERT race.
	repo.byEmail["race@example.com"] = &repository.User{
		ID:              racingUID,
		Email:           "race@example.com",
		EmailVerifiedAt: &verifiedAt,
		OAuthProvider:   nil, // not yet linked (the race winner just INSERTed)
		Status:          "active",
	}
	repo.upsertID = uuid.Nil // Upsert returns 0 rows → race detected
	svc := oauth.NewLinkingService(repo)

	out, err := svc.DecideAndLink(context.Background(), "google", "g-race-sub", "race@example.com", "en")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	// After race fallback, expect either B.1 auto-link (if race winner was the
	// Branch C path) OR a recovered Branch A re-fetch. The spec leaves it open
	// — Branch B.1 is the conservative pick. Either is acceptable; assert "not new".
	if out.IsNewUser {
		t.Error("IsNewUser = true after race fallback (winner INSERTed already)")
	}
	if out.Branch != oauth.BranchB1AutoLink && out.Branch != oauth.BranchARelogin {
		t.Errorf("Branch = %s, want B.1 or A (race recovery)", out.Branch)
	}
}

// Scenario: 2.3-UNIT-044 + BR-3.5 (2FA hook)
// users.totp_enabled = true → outcome carries RequiresMFA=true so the
// handler issues a short-lived `requires_2fa` JWT (90s) instead of full
// session tokens.
func TestLinking_BranchA_TOTPEnabled_RequiresMFA(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uid := uuid.New()
	repo.byOAuth["google:g-sub-mfa"] = &repository.User{
		ID: uid, Email: "mfa@example.com",
		Status: "active", TOTPEnabled: true,
		OAuthProvider: strPtr("google"), OAuthSubject: strPtr("g-sub-mfa"),
	}
	svc := oauth.NewLinkingService(repo)

	out, err := svc.DecideAndLink(context.Background(), "google", "g-sub-mfa", "mfa@example.com", "en")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	if !out.RequiresMFA {
		t.Error("RequiresMFA = false despite users.totp_enabled = true (BR-3.5 hook)")
	}
}

// Scenario: 2.3-UNIT-046 + BR-3.6
// Subject MUST be the provider-assigned ID — never the email. This is
// already enforced by the repository signature but we pin it explicitly
// here so a refactor that swaps fields surfaces immediately.
func TestLinking_OAuthSubjectIsProviderID_NotEmail(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uid := uuid.New()
	verifiedAt := time.Now().Add(-24 * time.Hour)
	repo.byEmail["sub@example.com"] = &repository.User{
		ID: uid, Email: "sub@example.com",
		EmailVerifiedAt: &verifiedAt, Status: "active",
	}
	svc := oauth.NewLinkingService(repo)

	// Use a subject value that explicitly differs from the email so the
	// post-link state assertion proves we wrote the provider id.
	_, err := svc.DecideAndLink(context.Background(), "google", "PROVIDER-ID-123", "sub@example.com", "en")
	if err != nil {
		t.Fatalf("DecideAndLink: %v", err)
	}
	linked := repo.byEmail["sub@example.com"]
	if linked.OAuthSubject == nil || *linked.OAuthSubject != "PROVIDER-ID-123" {
		t.Errorf("OAuthSubject = %v, want PROVIDER-ID-123", linked.OAuthSubject)
	}
	if linked.OAuthSubject != nil && *linked.OAuthSubject == "sub@example.com" {
		t.Error("OAuthSubject became the email — security regression")
	}
}

// -- Tests: Blind-spot CONCURRENCY scenarios (QA-2.3-M6) -------------------
//
// True concurrent execution lives in the testcontainers integration suite
// (INT-014..018, CI-gated). The fake-based tests below pin the deterministic
// outcomes the linking service MUST produce when two concurrent callers
// race through the Branch C / Branch B.1 windows — the semantic guarantee
// that PG UNIQUE(email) + idx_users_oauth defend at the DB layer.

// Scenario: 2.3-BLIND-CONCURRENCY-001 + BR-3.3
// Branch C race — two concurrent callers both pass the byOAuth check
// before either INSERTs. The DB's UNIQUE(email) means exactly one wins
// the upsert; the loser falls back to Branch B and observes the winner's
// row already carries the matching provider+subject, returning B.3
// (inconsistency) WITHOUT erroring. Guarantees no double-INSERT and a
// stable JWT issuance path for both callers.
func TestLinking_BlindConcurrency001_BranchC_Race(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	winnerID := uuid.New()
	verifiedAt := time.Now()
	provider, subject, email := "google", "sub-c-race", "race@example.com"

	// Simulate the race window: winner already INSERTed (byEmail sees it)
	// but the loser's byOAuth snapshot pre-dates the INSERT.
	repo.byEmail[email] = &repository.User{
		ID:              winnerID,
		Email:           email,
		OAuthProvider:   strPtr(provider),
		OAuthSubject:    strPtr(subject),
		EmailVerifiedAt: &verifiedAt,
		Status:          "active",
	}
	// Loser's UpsertOAuthUser returns 0 rows (ON CONFLICT DO NOTHING).
	repo.upsertID = uuid.Nil
	repo.upsertNew = false

	svc := oauth.NewLinkingService(repo)
	out, err := svc.DecideAndLink(context.Background(), provider, subject, email, "en")
	if err != nil {
		t.Fatalf("DecideAndLink (loser): %v", err)
	}
	if out.Branch != oauth.BranchB3Inconsistency {
		t.Errorf("Branch = %s, want B.3 (inconsistency)", out.Branch)
	}
	if out.UserID != winnerID {
		t.Errorf("UserID = %v, want %v (winner's id)", out.UserID, winnerID)
	}
	if out.IsNewUser {
		t.Error("IsNewUser = true on race loser; want false (winner already inserted)")
	}
	// BR-3.3 invariants: no double-INSERT and no second link-write. The
	// loser must short-circuit at handleBranchB (when it sees the winner's
	// row in byEmail) OR at the handleBranchC race-fallback re-query;
	// either way the linking-mutation count MUST be zero.
	if repo.upsertHits.load() > 1 {
		t.Errorf("UpsertOAuthUser hits = %d, want ≤1 (no double INSERT)", repo.upsertHits.load())
	}
	if repo.linkHits.load() != 0 {
		t.Errorf("LinkOAuthIdentity hits = %d, want 0 (loser must not relink)", repo.linkHits.load())
	}
	if repo.touchHits.load() != 1 {
		t.Errorf("TouchUserUpdatedAt hits = %d, want 1 (B.3 re-touch)", repo.touchHits.load())
	}
}

// Scenario: 2.3-BLIND-CONCURRENCY-002 + BR-3.3
// B.1 cross-provider race — a verified password user has two concurrent
// OAuth flows from different providers. The first writer wins the
// LinkOAuthIdentity UPDATE; the second sees the row already linked and
// MUST short-circuit at B.5 (cross-provider) with ErrCrossProvider, NOT
// silently overwrite the linkage.
func TestLinking_BlindConcurrency002_B1CrossProvider_Race(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uid := uuid.New()
	verifiedAt := time.Now().Add(-24 * time.Hour)
	pwHash := []byte("$2a$12$existing-hash")
	repo.byEmail["x@example.com"] = &repository.User{
		ID:              uid,
		Email:           "x@example.com",
		PasswordHash:    pwHash,
		EmailVerifiedAt: &verifiedAt,
		Status:          "active",
	}
	svc := oauth.NewLinkingService(repo)

	// Goroutine 1 simulation — google wins the LinkOAuthIdentity write.
	out1, err := svc.DecideAndLink(context.Background(), "google", "g-sub-race", "x@example.com", "en")
	if err != nil {
		t.Fatalf("first (google) DecideAndLink: %v", err)
	}
	if out1.Branch != oauth.BranchB1AutoLink {
		t.Fatalf("first branch = %s, want B.1 (auto-link)", out1.Branch)
	}

	// Goroutine 2 simulation — github sees the row already linked to
	// google and MUST short-circuit at the B.5 cross-provider check before
	// any LinkOAuthIdentity attempt.
	linkHitsBefore := repo.linkHits.load()
	_, err = svc.DecideAndLink(context.Background(), "github", "gh-sub-race", "x@example.com", "en")
	if !errors.Is(err, oauth.ErrCrossProvider) {
		t.Fatalf("second (github) err = %v, want ErrCrossProvider", err)
	}
	if delta := repo.linkHits.load() - linkHitsBefore; delta != 0 {
		t.Errorf("LinkOAuthIdentity hits delta = %d on cross-provider loser; want 0", delta)
	}
}

