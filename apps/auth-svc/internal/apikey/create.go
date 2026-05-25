// Story 5.1 T1.2 — CreateApiKey RPC handler.
//
// Flow (AC1 §Scenario):
//   1. Validate name (NFC + trim + 1-100 runes + class L/M/N/P/Sc + ASCII space).
//   2. Look up users.status — reject `pending_deletion` with FailedPrecondition
//      `account_pending_deletion` (Architect Q9 ratified).
//   3. Generate plaintext + key_prefix + key_hash via apikey.Generate (BR-1.4).
//   4. INSERT api_keys row RETURNING (id, created_at).
//   5. Emit Kafka audit.event `api_key.created` best-effort (BR-3.5 +
//      Story-2.5 BR-2.9 audit-graceful-degradation pattern — failure
//      WARN-logs but does NOT roll back the PG row).
//   6. Return CreateApiKeyResponse with the plaintext ONCE (BR-1.5).
//
// Defence-in-depth (BR-1.5): plaintext NEVER appears in slog fields, OTel
// span attributes, or returned error messages. The companion
// 5.1-SECURITY-001 test grep-asserts.

package apikey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// Name validation constants (Story 5.1 BR-1.7; parity with Story-2.5
// display_name per Architect Q10 ratified — see apikey.IsAllowedKeyNameRune).
const (
	keyNameMinRunes = 1
	keyNameMaxRunes = 100
)

// Sentinel errors for name validation. Mapped at the handler edge into
// gRPC InvalidArgument with structured field reasons (the 5.1-UNIT-008
// table-driven test asserts the distinct outcomes).
var (
	errKeyNameMissing     = errors.New("key_name_missing")
	errKeyNameTooLong     = errors.New("key_name_too_long")
	errKeyNameInvalidChar = errors.New("key_name_invalid_chars")
)

// CreateApiKey is the Story-5.1 AC1 RPC handler. Inputs:
//   - req.UserId : uuid v4 string. The api-gateway extracts this from the
//     verified JWT `sub` claim (BR-1.3 IDOR defence); client-supplied values
//     MUST be discarded by the gateway BEFORE invoking this RPC.
//   - req.Name   : user-supplied label (BR-1.7 NFC + trim + 1-100 runes).
//   - req.ClientIp / req.UserAgent : audit payload metadata.
//
// Returns the plaintext key ONCE in the response (BR-1.5); subsequent
// ListApiKeys calls surface only key_prefix (BR-1.6).
func (s *Service) CreateApiKey(ctx context.Context, req *authv1.CreateApiKeyRequest) (*authv1.CreateApiKeyResponse, error) {
	ctx, span := s.startSpanNamed(ctx, "auth.CreateApiKey")
	defer span.End()

	// --- Validate user_id shape -----------------------------------------------
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		span.SetAttributes(attribute.String("apikey.create.outcome", "invalid_user_id"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_user_id"))
	}

	// --- Validate name --------------------------------------------------------
	normalizedName, validateErr := ValidateKeyName(req.GetName())
	if validateErr != nil {
		span.SetAttributes(
			attribute.String("apikey.create.outcome", "invalid_name"),
			attribute.String("apikey.create.invalid_reason", validateErr.Error()),
		)
		return nil, connect.NewError(connect.CodeInvalidArgument, validateErr)
	}

	// --- Q9: pending_deletion gate -------------------------------------------
	status, statusErr := s.Repo.GetUserStatus(ctx, userID)
	if statusErr != nil {
		if errors.Is(statusErr, repository.ErrUserNotFound) {
			// Treat unknown user as pending_deletion-equivalent — caller
			// gateway should never reach this branch (JWT verification
			// would have rejected), but defence-in-depth.
			span.SetAttributes(attribute.String("apikey.create.outcome", "user_not_found"))
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("account_pending_deletion"))
		}
		span.SetAttributes(attribute.String("apikey.create.outcome", "user_lookup_failed"))
		s.Logger.WarnContext(
			ctx, "apikey create user-status lookup failed",
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("error", statusErr.Error()),
		)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("user lookup failed: %w", statusErr))
	}
	if status == "pending_deletion" {
		span.SetAttributes(attribute.String("apikey.create.outcome", "pending_deletion"))
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("account_pending_deletion"))
	}

	// --- Generate plaintext + key_prefix + key_hash ---------------------------
	plaintext, keyPrefix, keyHash, genErr := Generate()
	if genErr != nil {
		span.SetAttributes(attribute.String("apikey.create.outcome", "generate_failed"))
		s.Logger.ErrorContext(
			ctx, "apikey_create_generate_failed",
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("error", genErr.Error()),
		)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate: %w", genErr))
	}

	// --- INSERT (RETURNING id, created_at) -----------------------------------
	apiKeyID, createdAt, insertErr := s.Repo.InsertAPIKey(ctx, userID, normalizedName, keyPrefix, keyHash)
	if insertErr != nil {
		span.SetAttributes(attribute.String("apikey.create.outcome", "insert_failed"))
		s.Logger.WarnContext(
			ctx, "apikey_create_insert_failed",
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("error", insertErr.Error()),
		)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("insert: %w", insertErr))
	}

	// --- Emit audit (best-effort; NEVER blocks the response) -----------------
	s.publishAuditCreated(ctx, audit.Event{
		EventType: audit.EventAPIKeyCreated,
		UserID:    userID.String(),
		IP:        oauth.HashClientIP(req.GetClientIp()),
		UserAgent: oauth.HashUserAgent(req.GetUserAgent()),
		Timestamp: s.now(),
		Success:   true,
		Metadata: map[string]any{
			"api_key_id": apiKeyID.String(),
			"key_prefix": keyPrefix,
			"name":       normalizedName,
		},
	})

	span.SetAttributes(
		attribute.String("apikey.create.outcome", "ok"),
		attribute.String("apikey.key_prefix", keyPrefix),
		attribute.String("apikey.api_key_id", apiKeyID.String()),
	)
	s.Logger.InfoContext(
		ctx, "apikey_created",
		slog.String("api_key_id", apiKeyID.String()),
		slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
		slog.String("key_prefix", keyPrefix),
		slog.String("client_ip_hash", oauth.HashClientIP(req.GetClientIp())),
		slog.String("user_agent_hash", oauth.HashUserAgent(req.GetUserAgent())),
		// PLAINTEXT + KEY_HASH ABSENT — 5.1-SECURITY-001 grep-asserts.
	)

	return &authv1.CreateApiKeyResponse{
		ApiKeyId:  apiKeyID.String(),
		KeyPrefix: keyPrefix,
		Name:      normalizedName,
		CreatedAt: timestamppb.New(createdAt),
		Plaintext: plaintext,
	}, nil
}

// publishAuditCreated invokes the audit publisher; on nil receiver or
// publisher error logs WARN + continues. The PG row has already committed
// — audit failure does NOT roll it back (BR audit-graceful-degradation /
// Story-2.5 BR-2.9 pattern; Architect Q-Spec-3 ratified).
func (s *Service) publishAuditCreated(ctx context.Context, ev audit.Event) {
	if s.Audit == nil {
		s.Logger.WarnContext(
			ctx, "audit_emit_skipped (publisher unwired)",
			slog.String("event_type", string(ev.EventType)),
		)
		return
	}
	if err := s.Audit.Publish(ctx, ev); err != nil {
		s.Logger.WarnContext(
			ctx, "audit_emit_failed",
			slog.String("event_type", string(ev.EventType)),
			slog.String("error", err.Error()),
		)
	}
}

// now returns the current wall-clock via Service.Clock (injectable for
// deterministic tests). Defaults to time.Now when Clock is nil.
func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// startSpanNamed mirrors startSpan but lets a method override the span name.
// On nil-tracer path returns the context's existing span (typically a no-op
// span) so callers can SetAttributes / End uniformly.
func (s *Service) startSpanNamed(ctx context.Context, name string) (context.Context, trace.Span) {
	if s.Tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return s.Tracer.Start(ctx, name)
}

// ValidateKeyName implements Story 5.1 BR-1.7 (parity with Story-2.5
// display_name per Architect Q10 ratified — Letter/Mark/Number/Punctuation/
// Symbol-Currency + ASCII space; reject control/format/private-use/other-
// symbol). Differs from Story-2.5 in ONE place: empty-after-trim is
// REJECTED (Story 5.1 names are REQUIRED), whereas Story 2.5 normalizes
// empty to NULL for the BR-2.4 clear case.
//
// Returns the NFC-normalized + trimmed string on success.
func ValidateKeyName(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errKeyNameMissing
	}
	normalized := norm.NFC.String(trimmed)
	if utf8.RuneCountInString(normalized) > keyNameMaxRunes {
		return "", errKeyNameTooLong
	}
	if utf8.RuneCountInString(normalized) < keyNameMinRunes {
		return "", errKeyNameMissing
	}
	for _, r := range normalized {
		if !IsAllowedKeyNameRune(r) {
			return "", errKeyNameInvalidChar
		}
	}
	return normalized, nil
}

// IsAllowedKeyNameRune returns true for code-points permitted in an API
// key `name`. EXACTLY mirrors Story-2.5 isAllowedDisplayNameRune (Architect
// Q10 + L-1 ratified share-by-import default; L-1 also requires a 5.1-UNIT
// cell asserting equivalence). Exported so 5.1-UNIT-008 can range over a
// shared test input matrix.
func IsAllowedKeyNameRune(r rune) bool {
	if r == ' ' {
		return true
	}
	switch {
	case unicode.IsLetter(r): // L
		return true
	case unicode.IsMark(r): // M
		return true
	case unicode.IsNumber(r): // N
		return true
	case unicode.IsPunct(r): // P
		return true
	case unicode.In(r, unicode.Sc): // Currency symbol
		return true
	}
	return false
}
