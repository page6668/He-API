// Story 5.1 T5.1 — REST handlers for /v1/me/keys.
//
//	POST   /v1/me/keys                   — HandleCreate (AC1)
//	GET    /v1/me/keys                   — HandleList   (AC2)
//	DELETE /v1/me/keys/{api_key_id}      — HandleRevoke (AC3)
//
// All routes are JWT-only (wrapped by middleware.JWTVerifier.RequireJWT at
// cmd/server/main.go). NO bearer-API-key middleware (BR-1.1 — key-can-
// create-keys lateral movement explicitly rejected). user_id is extracted
// from the JWT `sub` claim via middleware.UserIDFromContext — the request
// body / path / query string MUST NOT carry user_id (BR-1.3 IDOR defence).
//
// Defence-in-depth invariants honoured at the wire boundary:
//   - Plaintext (BR-1.5) returned in response body of CREATE ONLY; never
//     surfaced on LIST or any subsequent path.
//   - key_hash NEVER reaches the gateway (BR-2.5 enforced at SQL boundary
//     in auth-svc/internal/repository/api_keys.go listAPIKeysByUserSQL).
//   - Cache-Control: no-store on all 3 endpoints (PII-adjacent defence).

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// uuidV4Re anchors the BR-3.x path-parameter validation (DELETE only).
// RFC 4122 v4: 8-4-4-4-12 hex with version nibble 4 + variant 10xx.
var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// MeKeysHandler hosts the three handler methods. Wired at cmd/server with
// the production auth-svc client + a lazy Redis client factory (for the
// rate-limit helper).
type MeKeysHandler struct {
	upstream  authv1connect.AuthServiceClient
	redisFunc func() *redis.Client
	logger    *slog.Logger

	// once + cachedRedis materialise the Story-3.2 lazy-init pattern so a
	// misconfigured HE_API_REDIS_URL does NOT prevent cold-start. Redis
	// is only used by the rate-limit helper; failure degrades to fail-
	// open per BR-1.10.
	once        sync.Once
	cachedRedis *redis.Client
}

// NewMeKeysHandler constructs the handler with the supplied upstream auth-
// svc client + a Redis-client factory + logger. logger nil → slog.Default.
func NewMeKeysHandler(upstream authv1connect.AuthServiceClient, redisFunc func() *redis.Client, logger *slog.Logger) *MeKeysHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MeKeysHandler{upstream: upstream, redisFunc: redisFunc, logger: logger}
}

// redisClient lazy-loads (same pattern as bearer_auth.go) so cold-start
// passes when Redis is unreachable at process boot.
func (h *MeKeysHandler) redisClient() *redis.Client {
	h.once.Do(func() {
		if h.redisFunc != nil {
			h.cachedRedis = h.redisFunc()
		}
	})
	return h.cachedRedis
}

// HandleCreate implements AC1 — POST /v1/me/keys.
func (h *MeKeysHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	// Body size cap (defensive — name ≤ 100 runes ≈ ≤ 400 bytes; 4 KiB
	// matches the Story 2.5 BR Data Validation row).
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	bodyBytes, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusRequestEntityTooLarge, "413_payload_too_large", "Request body too large.", nil)
		return
	}

	// Strict-field-validation per BR-1.2 — DisallowUnknownFields.
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.DisallowUnknownFields()
	var req CreateKeyRequest
	if err := dec.Decode(&req); err != nil {
		// Distinguish "unknown field" from generic parse failure for the
		// observability surface — both surface as 400_unknown_field per
		// Story 2.5 BR-2.2 cascade (the canonical envelope code only
		// distinguishes via message).
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", err.Error(), nil)
		return
	}
	if dec.More() {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", "request body must contain a single JSON object", nil)
		return
	}

	// Rate-limit BR-1.10 — applied AFTER body parsing so strict-field-
	// validation failures don't consume the budget (5.1-INT-006 carve-out).
	decision := CheckCreateKeyRateLimit(r.Context(), h.redisClient(), h.logger, userID)
	if !decision.Allowed {
		w.Header().Set("Retry-After", FormatRetryAfter(decision.RetryAfter))
		_ = openaierr.Write(w, r.Context(), http.StatusTooManyRequests, "429_rate_limit_key_create",
			"Too many key creations. Please wait and try again.", nil)
		return
	}

	// auth-svc CreateApiKey RPC. user_id from JWT context — req body NEVER
	// carries user_id (BR-1.3).
	resp, err := h.upstream.CreateApiKey(r.Context(), connect.NewRequest(&authv1.CreateApiKeyRequest{
		UserId:    userID,
		Name:      req.Name,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		h.translateUpstreamError(w, r, err)
		return
	}
	msg := resp.Msg

	// Wire-shape per BR-1.13 — field order locked by struct declaration.
	out := CreateKeyResponse{
		APIKeyID:  msg.GetApiKeyId(),
		KeyPrefix: msg.GetKeyPrefix(),
		Name:      msg.GetName(),
		Plaintext: msg.GetPlaintext(),
		CreatedAt: msg.GetCreatedAt().AsTime().UTC().Format(time.RFC3339),
		Warning:   PlaintextOneTimeWarning,
	}
	// PII page — never cached by intermediate proxies. Defence-in-depth
	// even though TLS already excludes off-path actors.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusCreated, out)
}

// HandleList implements AC2 — GET /v1/me/keys.
func (h *MeKeysHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	// BR-2.1 strict-reject — query string MUST be empty. Most importantly
	// rejects `?user_id=<other>` so IDOR attempts are observable.
	if r.URL.RawQuery != "" {
		// First offending key for the openaierr param surface.
		q := r.URL.Query()
		var first string
		for k := range q {
			first = k
			break
		}
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", "unexpected query parameter: "+first, &first)
		return
	}

	resp, err := h.upstream.ListApiKeys(r.Context(), connect.NewRequest(&authv1.ListApiKeysRequest{
		UserId: userID,
	}))
	if err != nil {
		h.translateUpstreamError(w, r, err)
		return
	}
	msg := resp.Msg

	entries := make([]KeyEntry, 0, len(msg.GetKeys()))
	for _, k := range msg.GetKeys() {
		entries = append(entries, protoEntryToJSON(k))
	}
	out := ListKeysResponse{Object: "list", Data: entries}
	// PII-adjacent (key_prefix) — never cache.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// HandleRevoke implements AC3 — DELETE /v1/me/keys/{api_key_id}.
func (h *MeKeysHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	apiKeyID := r.PathValue("api_key_id")
	if !uuidV4Re.MatchString(apiKeyID) {
		field := "api_key_id"
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "Invalid request parameters.", &field)
		return
	}

	resp, err := h.upstream.RevokeApiKey(r.Context(), connect.NewRequest(&authv1.RevokeApiKeyRequest{
		UserId:    userID,
		ApiKeyId:  apiKeyID,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		h.translateUpstreamError(w, r, err)
		return
	}
	msg := resp.Msg
	out := RevokeKeyResponse{
		APIKeyID:          msg.GetApiKeyId(),
		RevokedAt:         msg.GetRevokedAt().AsTime().UTC().Format(time.RFC3339),
		WasAlreadyRevoked: msg.GetWasAlreadyRevoked(),
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// translateUpstreamError maps connect.* error codes from auth-svc into
// the canonical §5.1.2 envelopes. Distinct from `translateConnectError`
// (auth.go) because the auth-svc messages on this surface are bare
// reason strings (e.g., "account_pending_deletion") rather than the
// "NNN_code" prefix convention.
//
// Mapping table:
//
//	NotFound          → 404_api_key_not_found (BR-3.2 anti-enumeration)
//	InvalidArgument   → 400_invalid_request (defensive; gateway already
//	                    validates client input)
//	FailedPrecondition (msg=account_pending_deletion) → 403
//	FailedPrecondition (msg=invalid_name reason) → 400_invalid_key_name
//	Unavailable / DeadlineExceeded → 502_auth_svc_unavailable
//	default → 500_internal_error
func (h *MeKeysHandler) translateUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		_ = openaierr.Write(w, r.Context(), http.StatusBadGateway, "502_auth_svc_unavailable", err.Error(), nil)
		return
	}
	switch cerr.Code() {
	case connect.CodeNotFound:
		_ = openaierr.Write(w, r.Context(), http.StatusNotFound, "404_api_key_not_found", "API key not found.", nil)
	case connect.CodeFailedPrecondition:
		// auth-svc messages on this path: "account_pending_deletion" |
		// validation reasons (key_name_*). Default to pending_deletion.
		msg := cerr.Message()
		switch {
		case bytes.Contains([]byte(msg), []byte("account_pending_deletion")):
			_ = openaierr.Write(w, r.Context(), http.StatusForbidden, "403_account_pending_deletion",
				"This account is scheduled for deletion.", nil)
		default:
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", msg, nil)
		}
	case connect.CodeInvalidArgument:
		// Maps validation reasons surfaced from the auth-svc handler. The
		// name-validation sentinels (key_name_missing / key_name_too_long /
		// key_name_invalid_chars) all collapse to 400_invalid_key_name with
		// the i18n key surfaced via the message slot.
		msg := cerr.Message()
		field := "name"
		if bytes.Contains([]byte(msg), []byte("key_name_")) {
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_key_name", msg, &field)
			return
		}
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", msg, nil)
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, r.Context(), http.StatusBadGateway, "502_auth_svc_unavailable",
			"Authentication service temporarily unavailable.", nil)
	case connect.CodeInternal:
		_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError, "500_internal_error",
			"Something went wrong. Please try again.", nil)
	default:
		_ = openaierr.Write(w, r.Context(), http.StatusBadGateway, "502_auth_svc_unavailable",
			cerr.Message(), nil)
	}
}

// protoEntryToJSON projects the proto ApiKeyEntry message onto the JSON
// wire shape. Nullable timestamps render as RFC 3339 UTC strings (or nil
// → JSON `null`). The Scope field carries the JSONB body verbatim.
func protoEntryToJSON(p *authv1.ApiKeyEntry) KeyEntry {
	out := KeyEntry{
		APIKeyID:                p.GetApiKeyId(),
		Name:                    p.GetName(),
		KeyPrefix:               p.GetKeyPrefix(),
		Scope:                   json.RawMessage(stringOrEmpty(p.GetScope(), `{}`)),
		CurrentMonthCostUSD:     p.GetCurrentMonthCostUsd(),
		CreatedAt:               p.GetCreatedAt().AsTime().UTC().Format(time.RFC3339),
		ContentSafetyStrictness: p.GetContentSafetyStrictness(), // Story 8.4 read-back
	}
	if p.MonthlyCostCapUsd != nil {
		v := *p.MonthlyCostCapUsd
		out.MonthlyCostCapUSD = &v
	}
	if p.LastUsedAt != nil {
		s := p.LastUsedAt.AsTime().UTC().Format(time.RFC3339)
		out.LastUsedAt = &s
	}
	if p.RevokedAt != nil {
		s := p.RevokedAt.AsTime().UTC().Format(time.RFC3339)
		out.RevokedAt = &s
	}
	return out
}

// stringOrEmpty returns the supplied string when non-empty, otherwise the
// fallback. Used to ensure scope JSONB renders as `{}` rather than empty
// string when the auth-svc proto field is unset.
func stringOrEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
