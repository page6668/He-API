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

	// SentinelKeyPrefix is the Story-5.1 BR-3.8 cross-pod cache-
	// invalidation key prefix. MUST match the auth-svc redisclient
	// package constant of the same value verbatim — the two binaries
	// communicate by reading/writing this key uncoordinated except for
	// the shared string. UNIT-028 asserts parity at the test boundary.
	//
	// Full key = SentinelKeyPrefix + apiKeyID.String(). On a positive
	// cache-hit, the gateway EXISTS-checks this key; presence means the
	// auth-svc revoked the key since the cache was filled and the
	// gateway MUST purge the cached positive before serving (the
	// Story-3.2 Validate path will then return REVOKED → 401).
	SentinelKeyPrefix = "auth:apikey:revoked:"

	// ConfigUpdatedSentinelKeyPrefix is the Story-5.2 BR-1.9 config-update
	// cache-invalidation key prefix. MUST match the auth-svc redisclient
	// constant of the same value. On a positive cache hit the gateway
	// EXISTS-checks this key alongside the revoke sentinel (single MULTI
	// round-trip); presence means the auth-svc mutated the key's scope/cap
	// since the cache was filled, so the gateway purges the stale extended-
	// shape entry and falls through to Validate (which returns the new shape).
	ConfigUpdatedSentinelKeyPrefix = "auth:apikey:config_updated:"
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
	cacheValueKey   = ctxKey{name: "apiKeyCacheValue"}
)

// WithCacheValue attaches the full resolved CachedClaims to ctx (Story 5.2
// T3.4) so the keypolicy middleware (AC2/AC3/AC4) reads the structured policy
// fields without re-deriving them. Stored as a pointer; never mutated after
// attach.
func WithCacheValue(ctx context.Context, c *CachedClaims) context.Context {
	return context.WithValue(ctx, cacheValueKey, c)
}

// CacheValueFromContext retrieves the resolved CachedClaims attached by
// RequireAPIKey. Returns (nil, false) if the bearer middleware did not run.
func CacheValueFromContext(ctx context.Context) (*CachedClaims, bool) {
	v, ok := ctx.Value(cacheValueKey).(*CachedClaims)
	return v, ok && v != nil
}

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

	// Story 5.2 (Q-A) — extended policy shape for the keypolicy middleware
	// (AC2/AC3/AC4). All `omitempty` — the tags ARE the backward-compat
	// mechanism: a pre-Story-5.2 cache entry (which lacks these keys)
	// deserialises with these fields zero-valued during the 5-min rollout
	// window (Go's json.Unmarshal ignores missing fields). The legacy
	// `Scope` string is PRESERVED verbatim so deprecated WithScope/
	// ScopeFromContext callers don't regress (Architect M3/M4 fix).
	ScopeModels       []string `json:"scope_models,omitempty"`
	ScopeIPWhitelist  []string `json:"scope_ip_whitelist,omitempty"`
	MonthlyCostCapUSD *string  `json:"monthly_cost_cap_usd,omitempty"`
}

// scopeJSON is the api_keys.scope JSONB shape the gateway parses from the
// Validate response to populate the structured ScopeModels/ScopeIPWhitelist
// cache fields (Q-A). Unknown keys are ignored.
type scopeJSON struct {
	Models      []string `json:"models"`
	IPWhitelist []string `json:"ip_whitelist"`
}

// applyExtendedScope parses the scope JSON string + cap into the structured
// Story-5.2 cache fields. A malformed scope JSON leaves the slices nil
// (semantically "no enforcement" — empty whitelist + empty scope both mean
// "allow"); the legacy Scope string is still stored verbatim by the caller.
func applyExtendedScope(c *CachedClaims, scope string, cap *string) {
	if scope != "" {
		var s scopeJSON
		if err := json.Unmarshal([]byte(scope), &s); err == nil {
			c.ScopeModels = s.Models
			c.ScopeIPWhitelist = s.IPWhitelist
		}
	}
	if cap != nil {
		v := *cap
		c.MonthlyCostCapUSD = &v
	}
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
			a.logger.WarnContext(
				ctx, "bearer_auth redis GET error — falling through",
				slog.String("error", getErr.Error()),
			)
		}
		if hit {
			// Story 5.1 BR-3.8 — additionally check the per-api_key_id
			// sentinel before serving the cached positive. If the
			// sentinel exists, auth-svc has marked the key revoked
			// within the last 300s; purge the stale positive entry
			// and fall through to the RPC (which will return REVOKED
			// → 401 from the Story-3.2 envelope path).
			//
			// Fail-open on Redis error: if EXISTS returns an error we
			// serve the cached positive (the auth-svc PG row is the
			// durable source of truth; the cache will natural-expire
			// at TTL 300s — degraded mode matches the security.md
			// §8.2.1 pre-Story-5.1 contract). Logged at WARN.
			revoked, sentErr := a.sentinelExists(ctx, claims.APIKeyID)
			if sentErr != nil {
				a.logger.WarnContext(
					ctx, "bearer_auth sentinel EXISTS error — serving from cache",
					slog.String("error", sentErr.Error()),
				)
				a.applyContext(r, claims)
				next.ServeHTTP(w, r.WithContext(r.Context()))
				return
			}
			if !revoked {
				a.applyContext(r, claims)
				next.ServeHTTP(w, r.WithContext(r.Context()))
				return
			}
			// Sentinel present — purge the stale positive entry.
			// Failure to purge is non-fatal (entry expires at TTL);
			// log + continue to RPC.
			if delErr := a.cacheDelete(ctx, cacheKey); delErr != nil {
				a.logger.WarnContext(
					ctx, "bearer_auth cache DEL after sentinel hit failed",
					slog.String("error", delErr.Error()),
				)
			}
			a.logger.InfoContext(
				ctx, "apikey_cache_purged_by_sentinel",
				slog.String("api_key_id", claims.APIKeyID),
			)
			// Fall through to upstream RPC.
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
		// Story 5.2 (Q-A) — derive the structured policy fields for the
		// keypolicy middleware + carry the monthly cap onto the hot path.
		applyExtendedScope(&newClaims, resp.Msg.GetScope(), resp.Msg.MonthlyCostCapUsd)
		if setErr := a.cacheSet(ctx, cacheKey, &newClaims); setErr != nil {
			a.logger.WarnContext(
				ctx, "bearer_auth redis SETEX failed",
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
			a.logger.WarnContext(
				r.Context(), "auth-svc unavailable",
				slog.String("connect_code", cerr.Code().String()),
				slog.String("error", err.Error()),
			)
			_ = openaierr.Write(w, r.Context(), http.StatusServiceUnavailable, codeAuthUnavailable, msgAuthUnavailable, nil)
			return
		}
	}
	// Catch-all — any non-OK / non-mapped error becomes 503 as well; the
	// only "ok" path is `err == nil && resp.Msg.Ok` (handled above).
	a.logger.WarnContext(
		r.Context(), "auth-svc validate error",
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
	ctx = WithCacheValue(ctx, c) // Story 5.2 — keypolicy reads the policy shape
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
		a.logger.WarnContext(
			ctx, "bearer_auth redis cache JSON parse failed",
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

// cacheDelete purges a cache entry. Story 5.1 BR-3.8 invokes this when
// the sentinel-EXISTS check fires on a positive cache-hit (the entry is
// stale; the next request should fall through to the auth-svc Validate
// path which will return REVOKED). Failure is non-fatal — the entry will
// natural-expire at TTL 300s; subsequent requests fall through anyway.
func (a *APIKeyAuthenticator) cacheDelete(ctx context.Context, key string) error {
	rdb := a.redisClient()
	if rdb == nil {
		return nil
	}
	return rdb.Del(ctx, key).Err()
}

// sentinelExists implements the Story 5.1 BR-3.8 revoke-sentinel check AND
// the Story 5.2 BR-1.9 config-updated-sentinel check in a SINGLE Redis
// EXISTS round-trip (the hot path is one op total). Returns (true, nil) when
// EITHER `auth:apikey:revoked:{id}` OR `auth:apikey:config_updated:{id}` is
// present (the cached positive is stale → purge + fall through to Validate);
// (false, nil) when both absent; (false, err) on Redis transport error
// (caller fail-open serves the cache).
func (a *APIKeyAuthenticator) sentinelExists(ctx context.Context, apiKeyID string) (bool, error) {
	rdb := a.redisClient()
	if rdb == nil {
		// Sentinel store is unreachable; fail-open per BR-3.13.
		return false, nil
	}
	if apiKeyID == "" {
		// Defensive — empty api_key_id would EXISTS-check the bare
		// prefix key. Treat as "no sentinel" rather than a false match.
		return false, nil
	}
	n, err := rdb.Exists(
		ctx,
		SentinelKeyPrefix+apiKeyID,
		ConfigUpdatedSentinelKeyPrefix+apiKeyID,
	).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
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
