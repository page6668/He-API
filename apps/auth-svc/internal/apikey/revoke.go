// Story 5.1 T3.1 — RevokeApiKey RPC handler + sentinel write.
//
// Flow (AC3 §Scenario):
//   1. Validate user_id + api_key_id (UUID v4 shape).
//   2. SELECT ... FOR UPDATE (serializes concurrent revokes on same id).
//      - 0 rows → NotFound (BR-3.2 anti-enumeration).
//      - row.user_id != req.user_id → NotFound (same envelope; BR-3.2 collapse).
//      - row.revoked_at != NULL → idempotent path: return historical
//        revoked_at + was_already_revoked=true (BR-3.3; NO new Kafka emit
//        per BR-3.4; sentinel re-SET defensively per T3.1 line 544).
//   3. UPDATE api_keys SET revoked_at=NOW() RETURNING revoked_at.
//   4. Emit Kafka audit.event `api_key.revoked` (best-effort; BR-3.6).
//   5. Write Redis sentinel `auth:apikey:revoked:{api_key_id}` EX 300
//      (BR-3.8; gateway-side bearer_auth.go reads this on cache hit).
//      Sentinel failure → fail-open (WARN-log + continue per BR-3.13).
//   6. Return RevokeApiKeyResponse{api_key_id, revoked_at, was_already_revoked}.

package apikey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// RevokeApiKey is the Story-5.1 AC3 RPC handler.
func (s *Service) RevokeApiKey(ctx context.Context, req *authv1.RevokeApiKeyRequest) (*authv1.RevokeApiKeyResponse, error) {
	ctx, span := s.startSpanNamed(ctx, "auth.RevokeApiKey")
	defer span.End()

	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		span.SetAttributes(attribute.String("apikey.revoke.outcome", "invalid_user_id"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_user_id"))
	}
	apiKeyID, err := uuid.Parse(req.GetApiKeyId())
	if err != nil {
		span.SetAttributes(attribute.String("apikey.revoke.outcome", "invalid_api_key_id"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_api_key_id"))
	}

	// --- SELECT FOR UPDATE ---------------------------------------------------
	row, selErr := s.Repo.SelectAPIKeyForUpdate(ctx, apiKeyID)
	if selErr != nil {
		if errors.Is(selErr, repository.ErrAPIKeyNotFound) {
			span.SetAttributes(attribute.String("apikey.revoke.outcome", "not_found"))
			return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
		}
		span.SetAttributes(attribute.String("apikey.revoke.outcome", "select_failed"))
		s.Logger.WarnContext(
			ctx, "apikey_revoke_select_failed",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("error", selErr.Error()),
		)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("select: %w", selErr))
	}

	// --- BR-3.2 cross-user anti-enumeration collapse -------------------------
	if row.UserID != userID {
		// Log the IDOR attempt with HASHED ids for forensics; the response
		// envelope is BYTE-IDENTICAL to the not-found path so an attacker
		// cannot enumerate the id space via differential responses.
		s.Logger.WarnContext(
			ctx, "apikey_revoke_idor_attempt",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("attempted_user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("actual_user_id_hash", oauth.HashClientIP(row.UserID.String())),
		)
		span.SetAttributes(attribute.String("apikey.revoke.outcome", "not_found_owner_mismatch"))
		return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
	}

	// --- BR-3.3 idempotent re-revoke -----------------------------------------
	if row.RevokedAt.Valid {
		span.SetAttributes(
			attribute.String("apikey.revoke.outcome", "already_revoked"),
			attribute.String("apikey.api_key_id", apiKeyID.String()),
		)
		// Defensive sentinel re-SET (per T3.1 line 544 — safe; refreshes TTL).
		s.writeSentinel(ctx, apiKeyID)
		s.Logger.InfoContext(
			ctx, "apikey_revoked",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.Bool("was_already_revoked", true),
		)
		return &authv1.RevokeApiKeyResponse{
			ApiKeyId:          apiKeyID.String(),
			RevokedAt:         timestamppb.New(row.RevokedAt.Time),
			WasAlreadyRevoked: true,
		}, nil
	}

	// --- UPDATE SET revoked_at=NOW() RETURNING revoked_at --------------------
	revokedAt, updErr := s.Repo.UpdateAPIKeyRevokedAt(ctx, apiKeyID)
	if updErr != nil {
		span.SetAttributes(attribute.String("apikey.revoke.outcome", "update_failed"))
		s.Logger.WarnContext(
			ctx, "apikey_revoke_update_failed",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("error", updErr.Error()),
		)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update: %w", updErr))
	}

	// --- Audit (best-effort) -------------------------------------------------
	s.publishAuditRevoked(ctx, audit.Event{
		EventType: audit.EventAPIKeyRevoked,
		UserID:    userID.String(),
		IP:        oauth.HashClientIP(req.GetClientIp()),
		UserAgent: oauth.HashUserAgent(req.GetUserAgent()),
		Timestamp: s.now(),
		Success:   true,
		Metadata: map[string]any{
			"api_key_id": apiKeyID.String(),
			"key_prefix": row.KeyPrefix,
			"name":       row.Name,
			"revoked_at": revokedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		},
	})

	// --- Sentinel SET (BR-3.8; fail-open per BR-3.13) ------------------------
	s.writeSentinel(ctx, apiKeyID)

	span.SetAttributes(
		attribute.String("apikey.revoke.outcome", "ok"),
		attribute.String("apikey.api_key_id", apiKeyID.String()),
	)
	s.Logger.InfoContext(
		ctx, "apikey_revoked",
		slog.String("api_key_id", apiKeyID.String()),
		slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
		slog.String("client_ip_hash", oauth.HashClientIP(req.GetClientIp())),
		slog.String("user_agent_hash", oauth.HashUserAgent(req.GetUserAgent())),
		slog.Bool("was_already_revoked", false),
	)

	return &authv1.RevokeApiKeyResponse{
		ApiKeyId:          apiKeyID.String(),
		RevokedAt:         timestamppb.New(revokedAt),
		WasAlreadyRevoked: false,
	}, nil
}

// publishAuditRevoked invokes the audit publisher; on nil receiver or
// publisher error logs WARN + continues (Architect Q-Spec-3 ratified
// graceful-degradation pattern; PG row is the durable source of truth).
func (s *Service) publishAuditRevoked(ctx context.Context, ev audit.Event) {
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

// writeSentinel invokes the Redis sentinel store; on nil receiver or
// failure logs WARN + continues (BR-3.13 fail-open default; the api-
// gateway positive-cache will natural-expire at TTL 300s — degraded mode
// matches the security.md §8.2.1 pre-Story-5.1 contract).
func (s *Service) writeSentinel(ctx context.Context, apiKeyID uuid.UUID) {
	if s.Sentinel == nil {
		s.Logger.WarnContext(
			ctx, "apikey_revoke_sentinel_skipped (store unwired)",
			slog.String("api_key_id", apiKeyID.String()),
		)
		return
	}
	if err := s.Sentinel.SetRevokedSentinel(ctx, apiKeyID); err != nil {
		s.Logger.WarnContext(
			ctx, "apikey_revoke_sentinel_failed",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("error", err.Error()),
		)
	}
}
