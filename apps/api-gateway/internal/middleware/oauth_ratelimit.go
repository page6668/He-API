// oauth_ratelimit.go — Story 2.3 BR-4.1 / BR-4.2.
//
// Two-rail ratelimit on the OAuth surface:
//
//   /v1/auth/oauth/{provider}/initiate  → 30 req/min/IP
//   /v1/auth/oauth/{provider}/callback  → 30 req/min/IP
//
// Redis-backed via INCR + EXPIRE (the Story 2.2 ratelimit pkg uses the
// same shape). When Redis is unavailable the middleware fails CLOSED —
// returns 503 — so a Redis outage cannot bypass the limit (BR-4.2).
//
// The middleware is path-scoped: cmd/server mounts it onto the
// /v1/auth/oauth/* prefix so the password-login and signup rate limits
// (already configured in auth-svc) stay independent.
package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Defaults match BR-4.1 (30/min/IP) and BR-4.2 (60s window).
const (
	OAuthRatelimitDefaultLimit  = 30
	OAuthRatelimitDefaultWindow = 60 * time.Second
)

// OAuthRatelimit is the configurable middleware. Limit + Window override
// the BR-4.1 defaults (useful for testing). KeyPrefix lets tests isolate
// counters per case. ClientIPFn extracts the trusted client IP; default
// uses the X-Forwarded-For first hop (same convention as Story 2.2
// signin ratelimit).
type OAuthRatelimit struct {
	Redis      redis.Cmdable
	Limit      int
	Window     time.Duration
	KeyPrefix  string
	ClientIPFn func(*http.Request) string
}

// NewOAuthRatelimit returns a middleware with BR-4.1 defaults applied to
// the supplied Redis client.
func NewOAuthRatelimit(rdb redis.Cmdable) *OAuthRatelimit {
	return &OAuthRatelimit{
		Redis:     rdb,
		Limit:     OAuthRatelimitDefaultLimit,
		Window:    OAuthRatelimitDefaultWindow,
		KeyPrefix: "ratelimit:oauth:",
	}
}

// Wrap returns an http.Handler that ratelimits inbound requests by client
// IP. The endpoint label (e.g. "initiate" / "callback") is encoded in the
// Redis key so initiate and callback maintain independent counters.
func (m *OAuthRatelimit) Wrap(endpoint string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := m.clientIP(r)
		key := m.KeyPrefix + endpoint + ":ip:" + ip
		count, ttl, err := m.incrCount(r.Context(), key)
		if err != nil {
			// Fail-closed — Redis unreachable.
			http.Error(w, `{"error":{"code":"503_service_unavailable"}}`, http.StatusServiceUnavailable)
			return
		}
		if count > int64(m.Limit) {
			// Retry-After in seconds (round up).
			ra := int(ttl.Seconds()) + 1
			w.Header().Set("Retry-After", fmt.Sprintf("%d", ra))
			http.Error(w, `{"error":{"code":"429_rate_limit_oauth"}}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// incrCount runs INCR + EXPIRE (only when the key is fresh) and returns
// the post-INCR count + remaining TTL.
func (m *OAuthRatelimit) incrCount(ctx context.Context, key string) (int64, time.Duration, error) {
	count, err := m.Redis.Incr(ctx, key).Result()
	if err != nil {
		return 0, 0, err
	}
	// Set EXPIRE on first increment only; subsequent INCRs reuse the TTL.
	if count == 1 {
		if err := m.Redis.Expire(ctx, key, m.Window).Err(); err != nil {
			return count, m.Window, err
		}
		return count, m.Window, nil
	}
	ttl, err := m.Redis.TTL(ctx, key).Result()
	if err != nil {
		return count, m.Window, err
	}
	// Negative TTL means "no expiry" (-1) or "key missing" (-2); both surface
	// as a fresh-window-equivalent.
	if ttl < 0 {
		return count, m.Window, nil
	}
	return count, ttl, nil
}

// clientIP returns the X-Forwarded-For first hop, falling back to r.RemoteAddr.
// Identical convention to Story 2.2 signin ratelimit (TS-CONS-013).
func (m *OAuthRatelimit) clientIP(r *http.Request) string {
	if m.ClientIPFn != nil {
		return m.ClientIPFn(r)
	}
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		// First hop is the closest hop to the client (per common convention).
		if i := strings.Index(h, ","); i >= 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	// strip port from RemoteAddr if present
	if i := strings.LastIndex(r.RemoteAddr, ":"); i >= 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}

// Convenience errors (kept for symmetry with ratelimit.ErrRateLimited).
var (
	ErrOAuthRateLimitExceeded = errors.New("oauth ratelimit exceeded")
	ErrOAuthRedisUnavailable  = errors.New("oauth ratelimit redis unavailable")
)
