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
type Repository interface {
	LookupAPIKeysByPrefix(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error)
	TouchAPIKeyLastUsed(ctx context.Context, apiKeyID uuid.UUID) error
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

// Service implements the AC2 Validate path. Concurrency-safe; one instance
// is constructed at startup and reused across all RPC requests.
type Service struct {
	Repo   Repository
	Tracer trace.Tracer
	Logger *slog.Logger
	// Clock is injectable for deterministic tests; production wires time.Now.
	Clock func() time.Time
}

// NewService constructs a Service with sensible defaults applied to any nil
// optional field (Clock → time.Now, Logger → slog.Default, Tracer →
// no-op tracer via otel global). Repo is required.
func NewService(repo Repository, tracer trace.Tracer, logger *slog.Logger) *Service {
	s := &Service{Repo: repo, Tracer: tracer, Logger: logger, Clock: time.Now}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
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
		s.Logger.WarnContext(ctx, "api_keys lookup failed",
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
		s.Logger.WarnContext(ctx, "api_keys candidate count exceeded cap",
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
				s.Logger.WarnContext(ctx, "bcrypt compare error",
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
			s.Logger.Debug("api_keys.last_used_at fire-and-forget UPDATE failed",
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
	return &authv1.ValidateApiKeyResponse{
		Ok:        true,
		ApiKeyId:  row.ID.String(),
		UserId:    row.UserID.String(),
		TeamId:    teamID,
		Scope:     string(row.Scope),
		Reason:    authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_UNSPECIFIED,
	}
}
