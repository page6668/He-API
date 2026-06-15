// Story 10.6 — POST /v1/me/playground/chat (rest-api-spec §5.1.3, additive).
//
// A JWT-cookie-authed sibling of /v1/chat/completions for the console Playground
// (OQ-10.6-1 ruling (b)). The browser NEVER holds a plaintext key; identity is
// the JWT `sub` (same per-user IDOR fence as 9.1/9.2/9.3). Billing / scope /
// rate-limit attribution REUSES the existing per-key machinery: the request body
// carries the caller's own `api_key_id` (UUID) → the handler verifies ownership
// (WHERE user_id=$JWT.sub) → resolves it to the existing per-key policy → injects
// the bearer-style context → delegates to the SAME internal chat pipeline
// (routing 6.2 / adapters / content-safety 8.2-8.3 / A/B 6.4 / TPMDeduct 5.3 /
// scope.models). No new proto, no new gRPC RPC, no migration, no new per-user
// billing path. SSE + A/B + the §5.1.2 error envelope all flow through unchanged.
//
// Defence-in-depth wire invariants (BR-10.6.2):
//   - JWT `sub` only — a forged body `user_id` is rejected by strict-decode (the
//     chat ChatRequest tolerates unknown fields, so the strictness lives HERE).
//   - api_key_id ownership is the IDOR fence: a foreign / unknown / revoked id
//     → 403_api_key_not_owned (QA 10.6-INT-002 fixes the contract at 403).
//   - single-model scope.models is gated here (the keyPolicy middleware that
//     normally does this on the bearer path is not in this JWT chain); A/B leg
//     scope is gated downstream inside dispatchAB (6.4 Q-K).
//   - Cache-Control: no-store on every response.

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
)

// ErrKeyNotOwned is returned by a PlaygroundKeyResolver when the supplied
// api_key_id does not exist in the authenticated user's key set, or is revoked.
// It maps to 403_api_key_not_owned (the IDOR fence). A distinct sentinel (vs a
// generic error) keeps the upstream-unavailable path (→ 502) separable.
var ErrKeyNotOwned = errors.New("playground: api_key_id not owned by the authenticated user")

// ResolvedKeyPolicy is the subset of an api key's per-key policy the Playground
// proxy needs to attribute a chat call (billing/scope/strictness). It mirrors the
// bearer-path CachedClaims fields the reused chat pipeline reads from context.
type ResolvedKeyPolicy struct {
	APIKeyID                string
	TeamID                  string
	Scope                   string // the JSONB scope string, verbatim (legacy WithScope consumers)
	ScopeModels             []string
	ContentSafetyStrictness string
}

// PlaygroundKeyResolver resolves a body-carried api_key_id to its per-key policy,
// IDOR-fenced to userID (the JWT `sub`). It MUST return ErrKeyNotOwned when the
// key does not belong to userID (or is revoked); any other error is treated as an
// upstream/resolver failure (→ 502).
type PlaygroundKeyResolver interface {
	Resolve(ctx context.Context, userID, apiKeyID string) (*ResolvedKeyPolicy, error)
}

// maxPlaygroundBodyBytes caps the Playground request body at 1 MiB — the text
// chat path (BR-1.2). The Playground is a text debugger; vision multipart is out
// of its UI scope, so the 9.5 two-stage 8 MiB cap is not needed here.
const maxPlaygroundBodyBytes int64 = 1 << 20

// playgroundBody is the strict-decode surface: the OpenAI chat fields the
// Playground UI sends, PLUS api_key_id. DisallowUnknownFields rejects anything
// else (incl. a forged `user_id` — 10.6-INT-007). The chat fields are kept as
// RawMessage: they are validated + re-marshalled byte-faithfully for forwarding
// and parsed by the reused chat handler, NOT here.
type playgroundBody struct {
	APIKeyID       string          `json:"api_key_id,omitempty"`
	Model          json.RawMessage `json:"model,omitempty"`
	Messages       json.RawMessage `json:"messages,omitempty"`
	Stream         json.RawMessage `json:"stream,omitempty"`
	Temperature    json.RawMessage `json:"temperature,omitempty"`
	MaxTokens      json.RawMessage `json:"max_tokens,omitempty"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
}

// PlaygroundChatHandler is the JWT-authed proxy. Construct once at startup and
// reuse. `chat` is the SAME chat pipeline mounted on /v1/chat/completions (wrapped
// with the 7.1 billingGate so the pre-flight 402 balance gate is reused); `keys`
// resolves api_key_id ownership.
type PlaygroundChatHandler struct {
	chat   http.Handler
	keys   PlaygroundKeyResolver
	logger *slog.Logger
}

// NewPlaygroundChatHandler builds the handler. logger nil → slog.Default().
func NewPlaygroundChatHandler(chat http.Handler, keys PlaygroundKeyResolver, logger *slog.Logger) *PlaygroundChatHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PlaygroundChatHandler{chat: chat, keys: keys, logger: logger}
}

// ServeHTTP implements http.Handler. Production mounts it under
// jwtVerifier.RequireJWT; the userID check below is defence-in-depth (mirrors
// me_keys.go) so a wiring regression fails closed instead of serving anonymously.
func (h *PlaygroundChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// no-store on every response (PII-adjacent; §5.1.3). Set before any write so
	// it survives the non-stream JSON write; the SSE path overrides Cache-Control
	// with no-cache,no-transform, which is acceptable (no-store not required there).
	w.Header().Set("Cache-Control", "no-store")

	// 1. JWT fence — identity is the `sub` claim ONLY (10.6-INT-001).
	userID, ok := middleware.UserIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusUnauthorized, "401_unauthenticated", "missing access token", nil)
		return
	}

	// 2. Read body (1 MiB text cap).
	r.Body = http.MaxBytesReader(w, r.Body, maxPlaygroundBodyBytes)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			_ = openaierr.Write(w, ctx, http.StatusRequestEntityTooLarge, "413_payload_too_large",
				"Request body exceeds 1 MiB.", nil)
			return
		}
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Request body is not valid JSON.", nil)
		return
	}

	// 3. Strict decode — reject unknown fields incl. a forged user_id (10.6-INT-007).
	dec := json.NewDecoder(bytes.NewReader(rawBody))
	dec.DisallowUnknownFields()
	var body playgroundBody
	if err := dec.Decode(&body); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Request body contains unknown or malformed fields.", nil)
		return
	}
	if dec.More() {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Request body must contain a single JSON object.", nil)
		return
	}

	// 4. api_key_id required + UUID-shaped (uuidV4Re from me_keys.go).
	if !uuidV4Re.MatchString(body.APIKeyID) {
		field := "api_key_id"
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Field 'api_key_id' is required and must be a valid key id.", &field)
		return
	}

	// 5. Ownership / IDOR fence (WHERE user_id=$JWT.sub).
	policy, err := h.keys.Resolve(ctx, userID, body.APIKeyID)
	if err != nil {
		if errors.Is(err, ErrKeyNotOwned) {
			_ = openaierr.Write(w, ctx, http.StatusForbidden, "403_api_key_not_owned",
				"The provided api_key_id is not available for this account.", nil)
			return
		}
		// Resolver/upstream fault — fail closed with the auth-svc envelope (parity
		// with me_keys.translateUpstreamError default).
		_ = openaierr.Write(w, ctx, http.StatusBadGateway, "502_auth_svc_unavailable",
			"Authentication service temporarily unavailable.", nil)
		return
	}

	// 6. Single-model scope.models gate (the bearer-path keyPolicy middleware is
	//    absent from this JWT chain). Skipped on the A/B path — there the legs come
	//    from the X-He-AB-Models header and are gated inside dispatchAB (6.4 Q-K).
	//    An empty scope means "all models allowed" (CheckModelScope returns true).
	if !routingclient.ABModelsPresent(r.Header) {
		var model string
		_ = json.Unmarshal(body.Model, &model)
		if model != "" && !keypolicy.CheckModelScope(model, policy.ScopeModels) {
			_ = openaierr.Write(w, ctx, http.StatusForbidden, "403_model_not_in_scope",
				"The selected model is not available for this key.", nil)
			return
		}
	}

	// 7. Inject the bearer-style context the reused chat pipeline reads. This is
	//    what makes billing/scope/strictness attribute to the resolved key WITHOUT
	//    inventing a per-user path.
	claims := &middleware.CachedClaims{
		APIKeyID:                body.APIKeyID,
		UserID:                  userID,
		TeamID:                  policy.TeamID,
		Scope:                   policy.Scope,
		ScopeModels:             policy.ScopeModels,
		ContentSafetyStrictness: policy.ContentSafetyStrictness,
	}
	ctx = middleware.WithAPIKeyID(ctx, body.APIKeyID)
	ctx = middleware.BearerWithUserID(ctx, userID)
	ctx = middleware.WithTeamID(ctx, policy.TeamID)
	ctx = middleware.WithScope(ctx, policy.Scope)
	ctx = middleware.WithCacheValue(ctx, claims)

	// 8. Forward the body WITHOUT api_key_id so the chat pipeline (and adapters)
	//    never see it. Re-marshal is byte-faithful for the chat fields (RawMessage).
	body.APIKeyID = ""
	fwd, mErr := json.Marshal(body)
	if mErr != nil {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "Internal error.", nil)
		return
	}
	r2 := r.WithContext(ctx)
	r2.Body = io.NopCloser(bytes.NewReader(fwd))
	r2.ContentLength = int64(len(fwd))

	h.chat.ServeHTTP(w, r2)
}

// ----- production resolver (reuses the auth-svc ListApiKeys RPC) -------------

// MeKeysPolicyResolver resolves api_key_id ownership + per-key policy by calling
// the SAME auth-svc ListApiKeys RPC the /v1/me/keys surface uses (Story 5.1). The
// RPC already user-fences the list (WHERE user_id=$1 in auth-svc), so a foreign
// api_key_id is simply ABSENT from the result — the IDOR fence for free, with NO
// new RPC / proto / migration (OQ-10.6-1: internal reuse only).
type MeKeysPolicyResolver struct {
	upstream authv1connect.AuthServiceClient
}

// NewMeKeysPolicyResolver constructs the resolver with the auth-svc client.
func NewMeKeysPolicyResolver(upstream authv1connect.AuthServiceClient) *MeKeysPolicyResolver {
	return &MeKeysPolicyResolver{upstream: upstream}
}

// scopeModelsJSON is the api_keys.scope JSONB projection the resolver needs (just
// the models allow-list). Mirrors bearer_auth.scopeJSON (unexported there).
type scopeModelsJSON struct {
	Models []string `json:"models"`
}

// Resolve implements PlaygroundKeyResolver. An upstream/transport error is
// returned verbatim (→ 502); a key absent from the user's list, or revoked, is
// ErrKeyNotOwned (→ 403).
func (r *MeKeysPolicyResolver) Resolve(ctx context.Context, userID, apiKeyID string) (*ResolvedKeyPolicy, error) {
	resp, err := r.upstream.ListApiKeys(ctx, connect.NewRequest(&authv1.ListApiKeysRequest{UserId: userID}))
	if err != nil {
		return nil, err
	}
	for _, k := range resp.Msg.GetKeys() {
		if k.GetApiKeyId() != apiKeyID {
			continue
		}
		// A revoked key cannot be used to bill a chat call.
		if k.RevokedAt != nil {
			return nil, ErrKeyNotOwned
		}
		pol := &ResolvedKeyPolicy{
			APIKeyID:                apiKeyID,
			Scope:                   k.GetScope(),
			ContentSafetyStrictness: k.GetContentSafetyStrictness(),
		}
		if s := k.GetScope(); s != "" {
			var sj scopeModelsJSON
			if json.Unmarshal([]byte(s), &sj) == nil {
				pol.ScopeModels = sj.Models
			}
		}
		return pol, nil
	}
	// api_key_id is not in this user's key set → not owned (IDOR fence).
	return nil, ErrKeyNotOwned
}
