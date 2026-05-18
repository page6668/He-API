// Story 3.2 — APIKeyAuthenticator wires Bearer-token API-key validation on
// the api-gateway. Mirrors jwt_verify.go's structure: lazy struct +
// per-request middleware + context-key helpers + a private JSON-envelope
// error writer.
//
// Hot path (AC1 + AC4):
//
//	GET cache_key=sha256(plaintext)        →   HIT (decode JSON, populate ctx)
//	                                        →   MISS → auth-svc.ValidateApiKey
//	                                            ok=true    → SETEX cache_key TTL=300s
//	                                            ok=false   → 401 (no SETEX — positives-only)
//	                                            connect-error → 503
//	                                       →   inner handler
//
// Defence in depth:
//   - Plaintext key NEVER appears in slog fields or OTel span attributes
//     (TC-9 + BR-2.10).
//   - Authorization header is NOT mutated (BR-1.9) — downstream handlers
//     may re-read it for upstream LLM provider passthrough.
//   - Cache key is sha256(plaintext) hex — a Redis-dump compromise yields
//     no plaintext (BR-1.5).
//   - Redis is lazy-initialised via sync.Once so cold-start passes when
//     Redis is unreachable at process boot (BR-4.7 + TC-10).
//
// TODO(epic-8-or-negcache): The positives-only cache (BR-1.6) leaves
// well-formed `he-XXXXX...` probes incurring full bcrypt cost on every
// cache miss — the regex pre-filter at the auth-svc layer cannot help
// here. Two mitigations are tracked: (a) Epic-8 per-IP rate-limit, which
// SHOULD ship before public API exposure; OR (b) a bounded negative-cache
// (30s TTL, 10K-entry LRU). If Epic 8 slips past public launch, (b)
// becomes mandatory before exposing /v1/* to untrusted clients.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
)

// ----- Constants ---------------------------------------------------------

const (
	apiKeyMinLen = 10
	apiKeyMaxLen = 256

	// cacheKeyPrefix matches docs/architecture/data-models.md §4.3 (after
	// the Story 3.2 T6.5 sync). 11-char prefix + 64-char hex suffix = 75
	// chars total (well under Redis' 512MB key cap).
	cacheKeyPrefix = "auth:apikey:"

	// cacheTTL is the AC4 BR-4.1 spec — 5 minutes verbatim from
	// security.md §8.2. NOT configurable in 3.2.
	cacheTTL = 300 * time.Second
)

// CodeInvalidAPIKey + the partner codes are the OpenAI-compatible envelope
// codes for the bearer-auth gateway boundary (AC1 / TC-3). KEEP CODE STRINGS
// EXACT — they're asserted by SEC-005 / UNIT-018 / UNIT-019 / UNIT-020 /
// UNIT-021.
const (
	codeInvalidAPIKey    = "401_invalid_api_key"
	codeAuthUnavailable  = "503_auth_unavailable"
	msgMissingAuthHeader = "Missing or malformed Authorization header."
	msgInvalidAPIKey     = "Invalid API key provided."
	msgAuthUnavailable   = "Authentication service temporarily unavailable."
)

// ----- Context keys ------------------------------------------------------

// Per Story 3.2 OQ3 separation guidance — distinct package-private context
// keys per auth path. This keeps the session-auth (JWT) and API-key-auth
// call paths explicit at the handler boundary; collisions between
// jwt_verify.go's `userIDKey` and bearer_auth's `bearerUserIDKey` are
// impossible because the unexported ctxKey struct is type-distinct here.
var (
	apiKeyIDKey     = ctxKey{name: "apiKeyID"}
	bearerUserIDKey = ctxKey{name: "bearerUserID"}
	teamIDKey       = ctxKey{name: "teamID"}
	scopeKey        = ctxKey{name: "apiKeyScope"}
)

// WithAPIKeyID attaches the validated api_keys.id UUID string to ctx.
func WithAPIKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, apiKeyIDKey, id)
}

// APIKeyIDFromContext retrieves the validated api_keys.id; returns ("",
// false) if RequireAPIKey did not run.
func APIKeyIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(apiKeyIDKey).(string)
	return v, ok && v != ""
}

// BearerWithUserID attaches the api-key owner user_id UUID string to ctx.
// Named distinctly from WithUserID (the JWT-path helper) per OQ3 separation
// guidance: a handler that reads both context keys must know which auth
// path populated each.
func BearerWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, bearerUserIDKey, userID)
}

// BearerUserIDFromContext retrieves the api-key owner user_id; returns
// ("", false) if RequireAPIKey did not run.
func BearerUserIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(bearerUserIDKey).(string)
	return v, ok && v != ""
}

// WithTeamID attaches the (possibly empty) team_id string to ctx.
func WithTeamID(ctx context.Context, teamID string) context.Context {
	return context.WithValue(ctx, teamIDKey, teamID)
}

// TeamIDFromContext retrieves the team_id; the second return is false only
// when RequireAPIKey did not run (empty-string team_id is normal for users
// without a team).
func TeamIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(teamIDKey).(string)
	return v, ok
}

// WithScope attaches the JSON-encoded scope string to ctx.
func WithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey, scope)
}

// ScopeFromContext retrieves the JSON-encoded scope string.
func ScopeFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(scopeKey).(string)
	return v, ok
}

// ----- Authenticator -----------------------------------------------------

// CachedClaims is the JSON shape persisted to Redis on cache hits. Storing
// as JSON (not protobuf) is non-negotiable per BR-4.2 — `redis-cli GET
// auth:apikey:...` MUST yield human-readable output for SRE 2 AM debugging.
type CachedClaims struct {
	APIKeyID string `json:"api_key_id"`
	UserID   string `json:"user_id"`
	TeamID   string `json:"team_id"`
	Scope    string `json:"scope"`
}

// APIKeyAuthenticator is the concrete middleware. Construct once at startup
// and reuse across all bearer-protected routes (currently only POST
// /v1/chat/completions; Stories 3.3-3.6 add more).
type APIKeyAuthenticator struct {
	upstream  authv1connect.AuthServiceClient
	redisFunc func() *redis.Client
	logger    *slog.Logger
	// once + cachedRedis materialize the BR-4.7 lazy-init pattern. Redis is
	// constructed on the first cache GET; a Redis-misconfigured environment
	// does NOT prevent cold-start. Mirrors the auth-svc startup pattern but
	// pushes the failure mode to the request path where degradation is
	// observable + recoverable.
	once        sync.Once
	cachedRedis *redis.Client
}

// NewAPIKeyAuthenticator builds an authenticator with the supplied
// upstream and a Redis-client factory. `redisFunc` MUST return a
// ready-to-use *redis.Client (or nil if Redis is intentionally disabled).
// The factory is invoked at most once (sync.Once) on the first cache GET.
func NewAPIKeyAuthenticator(
	upstream authv1connect.AuthServiceClient,
	redisFunc func() *redis.Client,
	logger *slog.Logger,
) *APIKeyAuthenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &APIKeyAuthenticator{upstream: upstream, redisFunc: redisFunc, logger: logger}
}

// redisClient lazy-loads the Redis client. Returns nil when redisFunc is
// nil (test environments may bypass Redis entirely) — the request path
// then degrades to "every request hits auth-svc" (AC4 outage scenario).
func (a *APIKeyAuthenticator) redisClient() *redis.Client {
	a.once.Do(func() {
		if a.redisFunc != nil {
			a.cachedRedis = a.redisFunc()
		}
	})
	return a.cachedRedis
}

// RequireAPIKey is the per-route middleware factory. Wrap the inner
// handler exactly as `mux.Handle("POST /v1/chat/completions",
// bearerAuth.RequireAPIKey(http.HandlerFunc(chatPlaceholder)))`.
func (a *APIKeyAuthenticator) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plaintext := StripBearer(r.Header.Get("Authorization"))
		if plaintext == "" {
			_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, codeInvalidAPIKey, msgMissingAuthHeader, nil)
			return
		}
		// BR-1 Data Validation — token byte length gate. Generic RFC-6750
		// DoS defence; AC2 regex narrows to `he-` shape inside auth-svc.
		if len(plaintext) < apiKeyMinLen || len(plaintext) > apiKeyMaxLen {
			_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, codeInvalidAPIKey, msgInvalidAPIKey, nil)
			return
		}

		ctx := r.Context()
		cacheKey := buildCacheKey(plaintext)

		// Cache GET path (positives-only, BR-1.6 / BR-4.1).
		claims, hit, getErr := a.cacheGet(ctx, cacheKey)
		if getErr != nil {
			// Logged once at WARN; fall through to the RPC path. Redis is a
			// perf optimisation, not a correctness gate.
			a.logger.WarnContext(ctx, "bearer_auth redis GET error — falling through",
				slog.String("error", getErr.Error()),
			)
		}
		if hit {
			a.applyContext(r, claims)
			next.ServeHTTP(w, r.WithContext(r.Context()))
			return
		}

		// Cache miss → upstream.
		resp, err := a.upstream.ValidateApiKey(ctx, connect.NewRequest(&authv1.ValidateApiKeyRequest{
			PlaintextKey: plaintext,
			ClientIp:     r.Header.Get("X-Forwarded-For"),
			UserAgent:    r.UserAgent(),
		}))
		if err != nil {
			a.handleUpstreamError(w, r, err)
			return
		}
		if !resp.Msg.GetOk() {
			// Anti-enumeration: same 401 envelope for NOT_FOUND and REVOKED.
			_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, codeInvalidAPIKey, msgInvalidAPIKey, nil)
			return
		}

		// SETEX positive result. Failure is logged at WARN but MUST NOT
		// fail the request.
		newClaims := CachedClaims{
			APIKeyID: resp.Msg.GetApiKeyId(),
			UserID:   resp.Msg.GetUserId(),
			TeamID:   resp.Msg.GetTeamId(),
			Scope:    resp.Msg.GetScope(),
		}
		if setErr := a.cacheSet(ctx, cacheKey, &newClaims); setErr != nil {
			a.logger.WarnContext(ctx, "bearer_auth redis SETEX failed",
				slog.String("error", setErr.Error()),
			)
		}

		a.applyContext(r, &newClaims)
		next.ServeHTTP(w, r.WithContext(r.Context()))
	})
}

// handleUpstreamError maps connect-go transport / connection failures to
// 503 envelopes while logging at WARN per BR-1.3. The api-gateway must
// distinguish AUTH failure (401) from INFRA failure (503) so clients
// route retries correctly.
func (a *APIKeyAuthenticator) handleUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		switch cerr.Code() {
		case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
			a.logger.WarnContext(r.Context(), "auth-svc unavailable",
				slog.String("connect_code", cerr.Code().String()),
				slog.String("error", err.Error()),
			)
			_ = openaierr.Write(w, r.Context(), http.StatusServiceUnavailable, codeAuthUnavailable, msgAuthUnavailable, nil)
			return
		}
	}
	// Catch-all — any non-OK / non-mapped error becomes 503 as well; the
	// only "ok" path is `err == nil && resp.Msg.Ok` (handled above).
	a.logger.WarnContext(r.Context(), "auth-svc validate error",
		slog.String("error", err.Error()),
	)
	_ = openaierr.Write(w, r.Context(), http.StatusServiceUnavailable, codeAuthUnavailable, msgAuthUnavailable, nil)
}

// applyContext is the single point where the validated claims hit the
// request context. Updates r.Context() in place so the next.ServeHTTP
// callers do not need to thread the new context manually.
func (a *APIKeyAuthenticator) applyContext(r *http.Request, c *CachedClaims) {
	ctx := r.Context()
	ctx = WithAPIKeyID(ctx, c.APIKeyID)
	ctx = BearerWithUserID(ctx, c.UserID)
	ctx = WithTeamID(ctx, c.TeamID)
	ctx = WithScope(ctx, c.Scope)
	*r = *r.WithContext(ctx)
}

// cacheGet returns (claims, true, nil) on hit, (nil, false, nil) on miss
// (including Redis-unconfigured), (nil, false, err) on Redis transport
// errors. JSON-parse errors are treated as miss + logged WARN so the
// corrupt entry gets overwritten on the next SETEX.
func (a *APIKeyAuthenticator) cacheGet(ctx context.Context, key string) (*CachedClaims, bool, error) {
	rdb := a.redisClient()
	if rdb == nil {
		return nil, false, nil
	}
	raw, err := rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var c CachedClaims
	if jsonErr := json.Unmarshal([]byte(raw), &c); jsonErr != nil {
		a.logger.WarnContext(ctx, "bearer_auth redis cache JSON parse failed",
			slog.String("error", jsonErr.Error()),
		)
		return nil, false, nil
	}
	return &c, true, nil
}

// cacheSet serializes + SETEXs the positive validation. Failure is
// non-fatal; the caller logs at WARN and proceeds.
func (a *APIKeyAuthenticator) cacheSet(ctx context.Context, key string, c *CachedClaims) error {
	rdb := a.redisClient()
	if rdb == nil {
		return nil
	}
	body, err := json.Marshal(c)
	if err != nil {
		// Marshal of a 4-string struct cannot fail in practice — defensive.
		return fmt.Errorf("marshal cached claims: %w", err)
	}
	return rdb.SetEx(ctx, key, body, cacheTTL).Err()
}

// buildCacheKey returns `auth:apikey:<hex(sha256(plaintext))>` (BR-1.5 /
// BR-4.5). SHA-256 chosen over bcrypt — sub-microsecond derivation; the
// one-way property satisfies the Redis-dump defence in depth.
func buildCacheKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return cacheKeyPrefix + hex.EncodeToString(sum[:])
}

// Story 3.6: writeAPIKeyError + mapErrorType have been deleted; all error
// emissions route through openaierr.Write, which derives error.type from
// the codeMetadata table and stamps he_request_id from requestid.FromContext.
