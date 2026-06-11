package apikey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// plaintextKeyRegex enforces BR-2.6 — `he-` prefix + base62 body in
// [10, 253] chars (matches the architecture's `he-` + base62(32 bytes)
// ≈ 46-char total format with slack). Non-matching plaintext is rejected
// BEFORE any DB query so the bcrypt-DoS-via-oversized-input attack path
// is foreclosed at the boundary.
var plaintextKeyRegex = regexp.MustCompile(`^he-[A-Za-z0-9]{10,253}$`)

// KeyPrefixLength is the size of the prefix slice used for the `key_prefix`
// indexed lookup — first 12 chars of the plaintext (`he-` + 9 chars). Matches
// the indexed VARCHAR(16) column shape from migration 0006 and the
// architecture's data-models.md §4.1 schema.
const KeyPrefixLength = 12

// fireForgetTimeout caps the fire-and-forget last_used_at UPDATE so a slow
// PG path cannot leak goroutines on a busy gateway (BR-2.5).
const fireForgetTimeout = 1 * time.Second

// Repository is the narrow surface the service needs from the repository
// package. Defined here (not at the call site) so tests can substitute a
// pgxmock-backed fake without dragging the full repository.Querier in.
//
// Story-5.1 extends this interface additively (LookupAPIKeysByPrefix +
// TouchAPIKeyLastUsed remain unchanged for the Validate hot path). The
// owner-status lookup `GetUserStatus` underpins the Q9 pending_deletion
// gate on CreateApiKey.
type Repository interface {
	// Story 3.2 — Validate hot path
	LookupAPIKeysByPrefix(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error)
	TouchAPIKeyLastUsed(ctx context.Context, apiKeyID uuid.UUID) error

	// Story 5.1 — Create / List / Revoke management surface
	InsertAPIKey(ctx context.Context, userID uuid.UUID, name, keyPrefix, keyHash string) (uuid.UUID, time.Time, error)
	ListAPIKeysByUser(ctx context.Context, userID uuid.UUID) ([]repository.ApiKeyRow, error)
	SelectAPIKeyForUpdate(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error)
	UpdateAPIKeyRevokedAt(ctx context.Context, apiKeyID uuid.UUID) (time.Time, error)
	GetUserStatus(ctx context.Context, userID uuid.UUID) (string, error)

	// Story 5.2 — UpdateApiKey config-mutation surface (Story 8.4 extends
	// UpdateAPIKeyConfig additively with the resolved strictness level).
	SelectAPIKeyConfigForUpdate(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error)
	UpdateAPIKeyConfig(ctx context.Context, apiKeyID, userID uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error)
}

// QuerierRepository adapts a repository.Querier into the Repository
// interface so cmd/server can wire a *pgxpool.Pool directly without writing
// boilerplate at each call site.
type QuerierRepository struct {
	Q repository.Querier
}

// LookupAPIKeysByPrefix delegates to the package-level repository function.
func (a QuerierRepository) LookupAPIKeysByPrefix(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
	return repository.LookupAPIKeysByPrefix(ctx, a.Q, prefix)
}

// TouchAPIKeyLastUsed delegates to the package-level repository function.
func (a QuerierRepository) TouchAPIKeyLastUsed(ctx context.Context, apiKeyID uuid.UUID) error {
	return repository.TouchAPIKeyLastUsed(ctx, a.Q, apiKeyID)
}

// InsertAPIKey delegates the Story-5.1 CreateApiKey INSERT.
func (a QuerierRepository) InsertAPIKey(ctx context.Context, userID uuid.UUID, name, keyPrefix, keyHash string) (uuid.UUID, time.Time, error) {
	return repository.InsertAPIKey(ctx, a.Q, userID, name, keyPrefix, keyHash)
}

// ListAPIKeysByUser delegates the Story-5.1 ListApiKeys SELECT.
func (a QuerierRepository) ListAPIKeysByUser(ctx context.Context, userID uuid.UUID) ([]repository.ApiKeyRow, error) {
	return repository.ListAPIKeysByUser(ctx, a.Q, userID)
}

// SelectAPIKeyForUpdate delegates the Story-5.1 RevokeApiKey SELECT (FOR
// UPDATE). Used outside an explicit tx in production — see Service.Revoke's
// comment on the lock-degradation tradeoff (Story-5.1 BR-3.x serialization
// is best-effort; idempotency makes concurrent revoke-of-same-id safe).
func (a QuerierRepository) SelectAPIKeyForUpdate(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error) {
	return repository.SelectAPIKeyForUpdate(ctx, a.Q, apiKeyID)
}

// UpdateAPIKeyRevokedAt delegates the Story-5.1 RevokeApiKey UPDATE.
func (a QuerierRepository) UpdateAPIKeyRevokedAt(ctx context.Context, apiKeyID uuid.UUID) (time.Time, error) {
	return repository.UpdateAPIKeyRevokedAt(ctx, a.Q, apiKeyID)
}

// SelectAPIKeyConfigForUpdate delegates the Story-5.2 UpdateApiKey SELECT.
func (a QuerierRepository) SelectAPIKeyConfigForUpdate(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error) {
	return repository.SelectAPIKeyConfigForUpdate(ctx, a.Q, apiKeyID)
}

// UpdateAPIKeyConfig delegates the Story-5.2/8.4 UpdateApiKey UPDATE.
func (a QuerierRepository) UpdateAPIKeyConfig(ctx context.Context, apiKeyID, userID uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error) {
	return repository.UpdateAPIKeyConfig(ctx, a.Q, apiKeyID, userID, scope, cap, strictness)
}

// GetUserStatus returns the user's status column ('active' /
// 'pending_deletion' / etc) for the Story-5.1 Q9 gate. Returns
// repository.ErrUserNotFound on no-such-user (caller maps to gRPC
// FailedPrecondition `account_pending_deletion` per the same surface).
func (a QuerierRepository) GetUserStatus(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := repository.GetUserByID(ctx, a.Q, userID)
	if err != nil {
		return "", err
	}
	return u.Status, nil
}

// Service implements the AC2 Validate path AND the Story-5.1 Create / List
// / Revoke management surface. Concurrency-safe; one instance is constructed
// at startup and reused across all RPC requests.
//
// Audit + Sentinel are OPTIONAL — Story-3.2 deployments that ship before
// Story-5.1 wire only Repo + Tracer + Logger; Create/List/Revoke will
// surface CodeInternal if invoked without those deps wired. Production
// post-Story-5.1 wires all 5 dependencies via NewServiceWithMgmt.
type Service struct {
	Repo     Repository
	Tracer   trace.Tracer
	Logger   *slog.Logger
	Audit    AuditPublisher // Story 5.1 — nil-safe; Create/Revoke fail gracefully (WARN log) when nil
	Sentinel SentinelStore  // Story 5.1 — nil-safe; Revoke logs WARN per BR-3.13 fail-open
	// ConfigSentinel is the Story-5.2 config-update sentinel store. nil-safe;
	// UpdateApiKey logs WARN + continues when nil (BR-1.9 fail-open).
	ConfigSentinel ConfigSentinelStore
	// Clock is injectable for deterministic tests; production wires time.Now.
	Clock func() time.Time
}

// NewService constructs a Service with sensible defaults applied to any nil
// optional field (Clock → time.Now, Logger → slog.Default, Tracer →
// no-op tracer via otel global). Repo is required.
//
// Story-5.1 callers MAY set s.Audit + s.Sentinel post-construction or use
// NewServiceWithMgmt for one-shot wiring.
func NewService(repo Repository, tracer trace.Tracer, logger *slog.Logger) *Service {
	s := &Service{Repo: repo, Tracer: tracer, Logger: logger, Clock: time.Now}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	return s
}

// NewServiceWithMgmt constructs a Service wired for both Story-3.2 Validate
// AND Story-5.1 Create/List/Revoke. Production cmd/server uses this; tests
// that exercise only Validate may continue using NewService.
func NewServiceWithMgmt(repo Repository, tracer trace.Tracer, logger *slog.Logger, auditPub AuditPublisher, sentinel SentinelStore) *Service {
	s := NewService(repo, tracer, logger)
	s.Audit = auditPub
	s.Sentinel = sentinel
	return s
}

// Validate executes the AC2 lookup + bcrypt-compare flow. The plaintext key
// is sourced from `req.PlaintextKey` and NEVER copied into log fields, span
// attributes, or returned errors (TC-9 / BR-2.10 — defence in depth).
//
// Per BR-2.4 the response shape is identical for REASON_NOT_FOUND and
// REASON_REVOKED — the api-gateway maps both to a single 401 envelope.
// `reason` is internal-only (SRE alerting + observability).
func (s *Service) Validate(ctx context.Context, req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	ctx, span := s.startSpan(ctx)
	defer span.End()

	plaintext := req.GetPlaintextKey()

	// BR-2.6 — syntactic regex BEFORE any DB query. Rejected payloads emit
	// a REASON_NOT_FOUND with auth.outcome=not_found_syntax; bcrypt is NOT
	// invoked (proves the DoS-via-oversized-input mitigation).
	if !plaintextKeyRegex.MatchString(plaintext) {
		span.SetAttributes(
			attribute.Int("auth.candidates_count", 0),
			attribute.String("auth.outcome", "not_found_syntax"),
			attribute.Bool("auth.matched", false),
		)
		return notFoundResponse(), nil
	}

	prefix := plaintext[:KeyPrefixLength]
	span.SetAttributes(attribute.String("auth.key_prefix", prefix))

	rows, err := s.Repo.LookupAPIKeysByPrefix(ctx, prefix)
	if err != nil {
		// Database errors surface as connect.CodeUnavailable so the gateway
		// can distinguish AUTH failure (401) from INFRA failure (503).
		span.SetAttributes(
			attribute.String("auth.outcome", "unavailable"),
			attribute.Bool("auth.matched", false),
		)
		s.Logger.WarnContext(
			ctx, "api_keys lookup failed",
			slog.String("key_prefix", prefix),
			slog.String("error", err.Error()),
		)
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("api_keys lookup failed: %w", err))
	}

	span.SetAttributes(attribute.Int("auth.candidates_count", len(rows)))

	// BR-2.3 candidate-count cap. SELECT LIMIT was 101 so len(rows) > 100
	// signals the overflow branch without an extra COUNT(*).
	if len(rows) > repository.MaxAPIKeyCandidates {
		s.Logger.WarnContext(
			ctx, "api_keys candidate count exceeded cap",
			slog.String("key_prefix", prefix),
			slog.Int("count", len(rows)),
		)
		span.SetAttributes(
			attribute.String("auth.outcome", "not_found_cap_exceeded"),
			attribute.Bool("auth.matched", false),
		)
		return notFoundResponse(), nil
	}

	if len(rows) == 0 {
		span.SetAttributes(
			attribute.String("auth.outcome", "not_found_no_prefix"),
			attribute.Bool("auth.matched", false),
		)
		return notFoundResponse(), nil
	}

	// Iterate candidates in id-ascending order (LookupAPIKeysByPrefix ORDER
	// BY id ASC); first bcrypt-match wins (BR-2.1 + BR-2.3).
	plaintextBytes := []byte(plaintext)
	for i := range rows {
		row := &rows[i]
		if err := bcrypt.CompareHashAndPassword([]byte(row.KeyHash), plaintextBytes); err != nil {
			if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
				s.Logger.WarnContext(
					ctx, "bcrypt compare error",
					slog.String("key_prefix", prefix),
					slog.String("error", err.Error()),
				)
			}
			continue
		}
		// Match found. Revoked match → REVOKED response (NO last_used_at
		// touch — revoked keys aren't "used" in the audit sense).
		if row.RevokedAt.Valid {
			span.SetAttributes(
				attribute.String("auth.outcome", "revoked"),
				attribute.Bool("auth.matched", true),
			)
			return revokedResponse(), nil
		}
		// BR-2.5 — fire-and-forget last_used_at UPDATE. Decouple from the
		// request context so cancellation of the caller's context does NOT
		// kill the bookkeeping write; the goroutine carries its own timeout.
		s.fireAndForgetTouch(row.ID)
		span.SetAttributes(
			attribute.String("auth.outcome", "ok"),
			attribute.Bool("auth.matched", true),
		)
		return okResponse(row), nil
	}

	span.SetAttributes(
		attribute.String("auth.outcome", "not_found_no_bcrypt"),
		attribute.Bool("auth.matched", false),
	)
	return notFoundResponse(), nil
}

func (s *Service) fireAndForgetTouch(apiKeyID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fireForgetTimeout)
		defer cancel()
		if err := s.Repo.TouchAPIKeyLastUsed(ctx, apiKeyID); err != nil {
			s.Logger.Debug(
				"api_keys.last_used_at fire-and-forget UPDATE failed",
				slog.String("api_key_id", apiKeyID.String()),
				slog.String("error", err.Error()),
			)
		}
	}()
}

func (s *Service) startSpan(ctx context.Context) (context.Context, trace.Span) {
	if s.Tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return s.Tracer.Start(ctx, "auth.ValidateApiKey")
}

// notFoundResponse is the canonical REASON_NOT_FOUND envelope. Byte-identical
// to revokedResponse() except for the `reason` enum (BR-2.4 anti-enum).
func notFoundResponse() *authv1.ValidateApiKeyResponse {
	return &authv1.ValidateApiKeyResponse{
		Ok:     false,
		Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND,
	}
}

// revokedResponse is the canonical REASON_REVOKED envelope. Identical shape
// to notFoundResponse() per BR-2.4.
func revokedResponse() *authv1.ValidateApiKeyResponse {
	return &authv1.ValidateApiKeyResponse{
		Ok:     false,
		Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_REVOKED,
	}
}

// okResponse populates the success envelope. team_id surfaces as empty
// string when the DB column is NULL (matches the proto contract — gateway
// stores "" in context, downstream Stories know to treat empty as "no team").
func okResponse(row *repository.ApiKeyRow) *authv1.ValidateApiKeyResponse {
	teamID := ""
	if row.TeamID.Valid {
		teamID = uuid.UUID(row.TeamID.Bytes).String()
	}
	resp := &authv1.ValidateApiKeyResponse{
		Ok:       true,
		ApiKeyId: row.ID.String(),
		UserId:   row.UserID.String(),
		TeamId:   teamID,
		Scope:    string(row.Scope),
		Reason:   authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_UNSPECIFIED,
		// Story 8.4 — carry the per-Key 内容安全 level on the Validate hot path so the
		// gateway gates the bidirectional content-safety filter from the bearer
		// cache. Column is NOT NULL so this is always populated (never "").
		ContentSafetyStrictness: row.ContentSafetyStrictness,
	}
	// Story 5.2 — carry the monthly cost cap on the hot path so the gateway
	// keypolicy middleware (AC4) enforces it from cache. NULL column → field
	// absent ("no cap" per BR-4.1).
	if row.MonthlyCostCapUSD.Valid {
		cap := numericToDecimalString(row.MonthlyCostCapUSD)
		resp.MonthlyCostCapUsd = &cap
	}
	return resp
}
