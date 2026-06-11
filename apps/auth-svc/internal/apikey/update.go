// Story 5.2 T1.1 — UpdateApiKey RPC handler (configure scope + cost cap).
//
// Partial-update of api_keys.scope (models[] + ip_whitelist[]) and
// monthly_cost_cap_usd. Only fields PRESENT in the patch mutate (BR-1.7).
// The gateway is the authoritative validator (model-registry lookup + CIDR +
// cap range + strict-field); auth-svc re-validates CIDR shape + cap range
// (stdlib only — no model registry here) as defence-in-depth and owns the
// merge + persistence + sentinel + audit.
//
// Flow:
//  1. Parse user_id + api_key_id (InvalidArgument on bad UUID).
//  2. users.status='pending_deletion' → FailedPrecondition (Q-I).
//  3. SELECT ... FOR UPDATE the full row; cross-user / revoked / missing all
//     collapse to NotFound (BR-1.8 anti-enumeration, Story-5.1 BR-3.2 cascade).
//  4. Merge scope JSONB (preserving unknown keys); validate CIDR + cap.
//  5. UPDATE ... WHERE id AND user_id AND revoked_at IS NULL RETURNING row
//     (the WHERE re-assertion defeats a concurrent-revoke TOCTOU).
//  6. SET config-updated sentinel (BR-1.9; fail-open).
//  7. Emit Kafka api_key.config_updated with changed_fields[] (BR-1.11).
package apikey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// Cap range bounds per Q-J (inclusive). The NUMERIC(10,2) column upper-bounds
// at 99999999.99 but the product contract caps at 999999.99; the lower bound
// 0.01 avoids an accidental 0-cap that would deny every call.
const (
	capMinUSD = 0.01
	capMaxUSD = 999999.99
)

// Validation sentinels surfaced as gRPC InvalidArgument. The gateway maps
// these to 400 envelopes; they are defence-in-depth (the gateway already
// validated the same inputs before the RPC).
var (
	errEmptyPatch        = errors.New("empty_patch")
	errInvalidCIDR       = errors.New("invalid_cidr")
	errCapRange          = errors.New("cap_out_of_range")
	errCapFormat         = errors.New("cap_invalid_format")
	errInvalidScope      = errors.New("invalid_scope_json")
	errInvalidStrictness = errors.New("invalid_content_safety_strictness")
)

// validStrictness is the closed Story-8.4 level enum {strict, default, loose}.
// auth-svc re-validates it as defence-in-depth (the gateway already validated the
// same token BEFORE the RPC, and the DB CHECK is a third layer) — mirroring the
// cap-range / CIDR re-validation posture.
var validStrictness = map[string]bool{"strict": true, "default": true, "loose": true}

// scopeShape is the canonical api_keys.scope JSONB shape. Marshalled via a
// map[string]json.RawMessage merge so any future keys are preserved verbatim
// (BR-1.7) — this struct documents the two keys Story 5.2 owns.
type scopeShape struct {
	Models      []string `json:"models,omitempty"`
	IPWhitelist []string `json:"ip_whitelist,omitempty"`
}

// UpdateApiKey is the Story-5.2 AC1 RPC handler.
func (s *Service) UpdateApiKey(ctx context.Context, req *authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error) {
	ctx, span := s.startSpanNamed(ctx, "auth.UpdateApiKey")
	defer span.End()

	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		span.SetAttributes(attribute.String("apikey.update.outcome", "invalid_user_id"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_user_id"))
	}
	apiKeyID, err := uuid.Parse(req.GetApiKeyId())
	if err != nil {
		span.SetAttributes(attribute.String("apikey.update.outcome", "invalid_api_key_id"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_api_key_id"))
	}

	// BR-1.3 — at least one field must be present (defence-in-depth; the
	// gateway already rejects the empty `{}` body).
	scopePatch := req.GetScope()
	scopePresent := scopePatch != nil && (scopePatch.GetModelsPresent() || scopePatch.GetIpWhitelistPresent())
	capPresent := req.MonthlyCostCapUsd != nil || req.GetClearMonthlyCap()
	strictnessPresent := req.ContentSafetyStrictness != nil // Story 8.4 — proto3-optional presence
	if !scopePresent && !capPresent && !strictnessPresent {
		span.SetAttributes(attribute.String("apikey.update.outcome", "empty_patch"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errEmptyPatch)
	}

	// Story 8.4 — validate the strictness enum (defence-in-depth) BEFORE the
	// SELECT/UPDATE so a bad token never hits the DB CHECK. Present & invalid →
	// InvalidArgument (the gateway maps to 400).
	if strictnessPresent && !validStrictness[req.GetContentSafetyStrictness()] {
		span.SetAttributes(attribute.String("apikey.update.outcome", "invalid_strictness"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidStrictness)
	}

	// --- Q-I pending_deletion gate -------------------------------------------
	status, statusErr := s.Repo.GetUserStatus(ctx, userID)
	if statusErr != nil {
		if errors.Is(statusErr, repository.ErrUserNotFound) {
			// No such user — collapse to NotFound (anti-enumeration parity).
			span.SetAttributes(attribute.String("apikey.update.outcome", "user_not_found"))
			return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
		}
		span.SetAttributes(attribute.String("apikey.update.outcome", "status_lookup_failed"))
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("status: %w", statusErr))
	}
	if status == "pending_deletion" {
		span.SetAttributes(attribute.String("apikey.update.outcome", "pending_deletion"))
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("account_pending_deletion"))
	}

	// --- SELECT FOR UPDATE (full row) ----------------------------------------
	row, selErr := s.Repo.SelectAPIKeyConfigForUpdate(ctx, apiKeyID)
	if selErr != nil {
		if errors.Is(selErr, repository.ErrAPIKeyNotFound) {
			span.SetAttributes(attribute.String("apikey.update.outcome", "not_found"))
			return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
		}
		span.SetAttributes(attribute.String("apikey.update.outcome", "select_failed"))
		s.Logger.WarnContext(ctx, "apikey_update_select_failed",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("error", selErr.Error()))
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("select: %w", selErr))
	}

	// BR-1.8 anti-enumeration: cross-user OR revoked collapse to the SAME
	// NotFound envelope as "doesn't exist". Log the IDOR attempt with hashed
	// ids for forensics (byte-identical response prevents id-space probing).
	if row.UserID != userID {
		s.Logger.WarnContext(ctx, "apikey_update_idor_attempt",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("attempted_user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("actual_user_id_hash", oauth.HashClientIP(row.UserID.String())))
		span.SetAttributes(attribute.String("apikey.update.outcome", "not_found_owner_mismatch"))
		return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
	}
	if row.RevokedAt.Valid {
		span.SetAttributes(attribute.String("apikey.update.outcome", "not_found_revoked"))
		return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
	}

	// --- Merge scope (BR-1.7) ------------------------------------------------
	mergedScope, changedScope, mergeErr := mergeScopeJSON(row.Scope, scopePatch)
	if mergeErr != nil {
		span.SetAttributes(attribute.String("apikey.update.outcome", "invalid_scope"))
		return nil, connect.NewError(connect.CodeInvalidArgument, mergeErr)
	}

	// --- Cap (BR-1.6 / Q-J) --------------------------------------------------
	capVal := row.MonthlyCostCapUSD // default: preserve existing
	capChanged := false
	switch {
	case req.GetClearMonthlyCap():
		capVal = pgtype.Numeric{} // Valid=false → SQL NULL
		capChanged = true
	case req.MonthlyCostCapUsd != nil:
		parsed, capErr := parseCapToNumeric(req.GetMonthlyCostCapUsd())
		if capErr != nil {
			span.SetAttributes(attribute.String("apikey.update.outcome", "invalid_cap"))
			return nil, connect.NewError(connect.CodeInvalidArgument, capErr)
		}
		capVal = parsed
		capChanged = true
	}

	// --- Strictness (BR-1.2 / Story 8.4) -------------------------------------
	// Preserve the existing level when the patch omits it (present-only mutation,
	// mirroring the cap/scope arms); the column is NOT NULL so row.* is never "".
	strictnessVal := row.ContentSafetyStrictness
	strictnessChanged := false
	if strictnessPresent {
		strictnessVal = req.GetContentSafetyStrictness() // already enum-validated above
		strictnessChanged = true
	}

	changedFields := append([]string{}, changedScope...)
	if capChanged {
		changedFields = append(changedFields, "monthly_cost_cap_usd")
	}
	if strictnessChanged {
		changedFields = append(changedFields, "content_safety_strictness")
	}

	// --- UPDATE (TOCTOU-guarded) ---------------------------------------------
	updated, updErr := s.Repo.UpdateAPIKeyConfig(ctx, apiKeyID, userID, mergedScope, capVal, strictnessVal)
	if updErr != nil {
		if errors.Is(updErr, repository.ErrAPIKeyNotFound) {
			// Concurrent revoke landed between SELECT and UPDATE.
			span.SetAttributes(attribute.String("apikey.update.outcome", "not_found_toctou"))
			return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
		}
		span.SetAttributes(attribute.String("apikey.update.outcome", "update_failed"))
		s.Logger.WarnContext(ctx, "apikey_update_failed",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("error", updErr.Error()))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update: %w", updErr))
	}

	// --- Sentinel SET (BR-1.9; fail-open) ------------------------------------
	s.writeConfigSentinel(ctx, apiKeyID)

	// --- Audit (best-effort; BR-1.11) ----------------------------------------
	s.publishAuditConfigUpdated(ctx, audit.Event{
		EventType: audit.EventAPIKeyConfigUpdated,
		UserID:    userID.String(),
		IP:        oauth.HashClientIP(req.GetClientIp()),
		UserAgent: oauth.HashUserAgent(req.GetUserAgent()),
		Timestamp: s.now(),
		Success:   true,
		Metadata: map[string]any{
			"api_key_id":     apiKeyID.String(),
			"key_prefix":     updated.KeyPrefix,
			"name":           updated.Name,
			"changed_fields": changedFields,
		},
	})

	span.SetAttributes(
		attribute.String("apikey.update.outcome", "ok"),
		attribute.String("apikey.api_key_id", apiKeyID.String()),
		attribute.Int("apikey.update.changed_field_count", len(changedFields)),
	)
	s.Logger.InfoContext(ctx, "apikey_config_updated",
		slog.String("api_key_id", apiKeyID.String()),
		slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
		slog.String("client_ip_hash", oauth.HashClientIP(req.GetClientIp())),
		slog.Any("changed_fields", changedFields))

	return rowToUpdateResponse(&updated), nil
}

// mergeScopeJSON merges the patch onto the existing scope JSONB, preserving
// unknown keys (BR-1.7). Returns the marshalled scope, the list of changed
// scope paths ("scope.models" / "scope.ip_whitelist"), and validates CIDR
// entries when ip_whitelist is present. The output map is key-sorted by
// encoding/json so the persisted JSONB is deterministic.
func mergeScopeJSON(existing []byte, patch *authv1.ScopePatch) ([]byte, []string, error) {
	scopeMap := map[string]json.RawMessage{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &scopeMap); err != nil {
			return nil, nil, errInvalidScope
		}
	}
	changed := []string{}
	if patch == nil {
		out, _ := json.Marshal(scopeMap)
		return out, changed, nil
	}

	if patch.GetModelsPresent() {
		models := patch.GetModels()
		if models == nil {
			models = []string{}
		}
		raw, _ := json.Marshal(models)
		scopeMap["models"] = raw
		changed = append(changed, "scope.models")
	}
	if patch.GetIpWhitelistPresent() {
		wl := patch.GetIpWhitelist()
		if wl == nil {
			wl = []string{}
		}
		if err := validateCIDREntries(wl); err != nil {
			return nil, nil, err
		}
		raw, _ := json.Marshal(wl)
		scopeMap["ip_whitelist"] = raw
		changed = append(changed, "scope.ip_whitelist")
	}

	out, err := json.Marshal(scopeMap)
	if err != nil {
		return nil, nil, errInvalidScope
	}
	return out, changed, nil
}

// validateCIDREntries rejects malformed IP/CIDR entries + the degenerate
// `0.0.0.0/0` / `::/0` (BR-1.5). Defence-in-depth — the gateway validates the
// same set with net/netip before the RPC.
func validateCIDREntries(entries []string) error {
	for _, e := range entries {
		if strings.ContainsRune(e, '/') {
			pfx, err := netip.ParsePrefix(e)
			if err != nil {
				return fmt.Errorf("%w: %s", errInvalidCIDR, e)
			}
			if pfx.Bits() == 0 {
				// 0.0.0.0/0 or ::/0 — equivalent to empty list with side effects.
				return fmt.Errorf("%w: %s", errInvalidCIDR, e)
			}
		} else if _, err := netip.ParseAddr(e); err != nil {
			return fmt.Errorf("%w: %s", errInvalidCIDR, e)
		}
	}
	return nil
}

// parseCapToNumeric parses a string-decimal cap into a pgtype.Numeric for
// exact NUMERIC(10,2) storage and validates the [0.01, 999999.99] range
// (Q-J). The coarse float bound-check is defence-in-depth; storage preserves
// the exact decimal via pgtype.Numeric.Scan.
func parseCapToNumeric(s string) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return n, fmt.Errorf("%w: %s", errCapFormat, s)
	}
	if f < capMinUSD || f > capMaxUSD {
		return n, fmt.Errorf("%w: %s", errCapRange, s)
	}
	if scanErr := n.Scan(s); scanErr != nil {
		return n, fmt.Errorf("%w: %s", errCapFormat, s)
	}
	return n, nil
}

// rowToUpdateResponse projects the updated repository row onto the proto
// response. Mirrors rowToProtoEntry (list.go) but targets UpdateApiKeyResponse
// (whose field set equals ApiKeyEntry minus key_hash). revoked_at is always
// nil on a successful UPDATE (the WHERE clause excludes revoked rows).
func rowToUpdateResponse(r *repository.ApiKeyRow) *authv1.UpdateApiKeyResponse {
	out := &authv1.UpdateApiKeyResponse{
		ApiKeyId:  r.ID.String(),
		Name:      r.Name,
		KeyPrefix: r.KeyPrefix,
		Scope:     string(r.Scope),
		CreatedAt: timestamppb.New(r.CreatedAt),
		// Story 8.4 — echo the persisted level on the read-back so the owner sees
		// what they set (NOT NULL → always populated).
		ContentSafetyStrictness: r.ContentSafetyStrictness,
	}
	if r.CurrentMonthCostUSD.Valid {
		out.CurrentMonthCostUsd = numericToDecimalString(r.CurrentMonthCostUSD)
	} else {
		out.CurrentMonthCostUsd = "0"
	}
	if r.MonthlyCostCapUSD.Valid {
		v := numericToDecimalString(r.MonthlyCostCapUSD)
		out.MonthlyCostCapUsd = &v
	}
	if r.LastUsedAt.Valid {
		out.LastUsedAt = timestamppb.New(r.LastUsedAt.Time)
	}
	if r.RevokedAt.Valid {
		out.RevokedAt = timestamppb.New(r.RevokedAt.Time)
	}
	return out
}

// writeConfigSentinel SETs the BR-1.9 config-updated sentinel. nil-safe +
// fail-open (logs WARN; lag falls back to the 5-minute positive-cache expiry).
func (s *Service) writeConfigSentinel(ctx context.Context, apiKeyID uuid.UUID) {
	if s.ConfigSentinel == nil {
		s.Logger.WarnContext(ctx, "apikey_config_update_sentinel_skipped (store unwired)",
			slog.String("api_key_id", apiKeyID.String()))
		return
	}
	if err := s.ConfigSentinel.SetConfigUpdatedSentinel(ctx, apiKeyID); err != nil {
		s.Logger.WarnContext(ctx, "apikey_config_update_sentinel_failed",
			slog.String("api_key_id", apiKeyID.String()),
			slog.String("error", err.Error()))
	}
}

// publishAuditConfigUpdated invokes the audit publisher; nil-safe + best-
// effort (WARN on nil receiver or publish error per BR-1.11 cascade).
func (s *Service) publishAuditConfigUpdated(ctx context.Context, ev audit.Event) {
	if s.Audit == nil {
		s.Logger.WarnContext(ctx, "audit_emit_skipped (publisher unwired)",
			slog.String("event_type", string(ev.EventType)))
		return
	}
	if err := s.Audit.Publish(ctx, ev); err != nil {
		s.Logger.WarnContext(ctx, "audit_emit_failed",
			slog.String("event_type", string(ev.EventType)),
			slog.String("error", err.Error()))
	}
}
