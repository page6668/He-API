// Story 5.2 T2.1 — the key-policy enforcement middleware.
//
// Runs AFTER bearer_auth.go resolves the key identity (extended cache shape
// per Q-A) but BEFORE the chat-completions / embeddings handler. Three
// sequential gates, short-circuiting on the first denial so zero upstream
// cost is incurred on a denied request:
//
//	AC2 IP whitelist  → 403 403_ip_not_whitelisted
//	AC3 model scope   → 403 403_model_not_in_scope
//	AC4 monthly cap   → 402 402_quota_exhausted   (fail-OPEN on Redis error, Q-F)
//
// Each gate emits a slog event (IPs hashed per the Story-2.3 /24+/64 hasher —
// SECURITY-006) + an OTel counter. Anti-enumeration parity (BR-2.8/3.8/4.9):
// the response envelope is byte-identical regardless of whitelist/scope size
// or how close the counter is to the cap.
package keypolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	"github.com/he-api/he-api/apps/api-gateway/internal/notifyclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// MonthlyCostReader reads the realtime monthly-cost counter (Q-D Redis SoT)
// for an api_key_id. Returns (currentUSD string-decimal, found bool, err).
// Wired in cmd/server/main.go to usage.ReadMonthlyCostUSD. A nil reader (or
// found=false) is treated as "counter absent" → 0 cost (BR-4.4).
type MonthlyCostReader func(ctx context.Context, apiKeyID string) (currentUSD string, found bool, err error)

// Options carries the middleware's injected dependencies.
type Options struct {
	Logger         *slog.Logger
	TrustedProxies []netip.Prefix // Q-E XFF walker source (env-sourced at startup)
	CostReader     MonthlyCostReader
	Metrics        *PolicyMetrics

	// Story 5.4 — sticky-trip breaker + threshold-crossing notifier. Both nil
	// in Story-5.2 callers (sentinel-hit fast path + email fire are skipped,
	// behaviour falls back to the pure counter-comparison gate).
	CapSentinel CapTrippedSentinel
	Notifier    CapThresholdNotifier
}

const (
	codeIPNotWhitelisted = "403_ip_not_whitelisted"
	codeModelNotInScope  = "403_model_not_in_scope"
	codeQuotaExhausted   = "402_quota_exhausted"

	msgIPNotWhitelisted = "Client IP not in API key's whitelist."
	msgModelNotInScope  = "Model not in API key's allowed scope."
	msgQuotaExhausted   = "API key monthly cost cap exhausted."
)

// New builds the per-route middleware. Mount on the bearer-protected chain
// AFTER bearer_auth: `bearerAuth.RequireAPIKey(keypolicy.New(opts)(handler))`.
func New(opts Options) func(http.Handler) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx := r.Context()

			claims, ok := middleware.CacheValueFromContext(ctx)
			if !ok {
				// Bearer auth did not populate the extended claims (e.g. a
				// route mounted without bearer_auth). Cannot enforce — pass
				// through rather than 500 (defensive; never reached in prod).
				logger.WarnContext(ctx, "keypolicy_no_claims_passthrough")
				next.ServeHTTP(w, r)
				return
			}

			clientIP := ResolveClientIP(r, opts.TrustedProxies)

			// --- AC2: IP whitelist ------------------------------------------
			if !CheckIPWhitelist(clientIP, claims.ScopeIPWhitelist) {
				opts.Metrics.check(ctx, "ip_whitelist", "denied")
				opts.Metrics.denial(ctx, "ip_whitelist", codeIPNotWhitelisted)
				logger.WarnContext(ctx, "apikey_ip_whitelist_denied",
					slog.String("api_key_id", claims.APIKeyID),
					slog.String("user_id", claims.UserID),
					slog.String("client_ip_hash", hashClientIP(clientIP)),
					slog.Int("whitelist_size", len(claims.ScopeIPWhitelist)))
				opts.Metrics.duration(ctx, time.Since(start).Seconds())
				_ = openaierr.Write(w, ctx, http.StatusForbidden, codeIPNotWhitelisted, msgIPNotWhitelisted, nil)
				return
			}
			opts.Metrics.check(ctx, "ip_whitelist", "allowed")

			// --- AC3: model scope -------------------------------------------
			model, _ := PeekModel(r)
			if !CheckModelScope(model, claims.ScopeModels) {
				opts.Metrics.check(ctx, "model_scope", "denied")
				opts.Metrics.denial(ctx, "model_scope", codeModelNotInScope)
				logger.WarnContext(ctx, "apikey_model_scope_denied",
					slog.String("api_key_id", claims.APIKeyID),
					slog.String("user_id", claims.UserID),
					slog.String("requested_model", model),
					slog.Int("scope_size", len(claims.ScopeModels)))
				opts.Metrics.duration(ctx, time.Since(start).Seconds())
				param := "model"
				_ = openaierr.Write(w, ctx, http.StatusForbidden, codeModelNotInScope, msgModelNotInScope, &param)
				return
			}
			opts.Metrics.check(ctx, "model_scope", "allowed")

			// --- AC4 + Story 5.4: monthly cap (fail-OPEN on Redis error, Q-F) -
			if cap := claims.MonthlyCostCapUSD; cap != nil && *cap != "" {
				reqID, _ := requestid.FromContext(ctx)

				// Story 5.4 AC1 BR-1.1 — sticky-trip sentinel EXISTS fast-path,
				// BEFORE the counter GET. A tripped key skips the counter
				// round-trip and re-emits 402 without re-firing the email
				// (BR-1.11).
				if opts.CapSentinel != nil {
					tripped, serr := opts.CapSentinel.Exists(ctx, claims.APIKeyID)
					switch {
					case serr != nil:
						// BR-1.10 fail-OPEN: fall through to the counter slow path.
						logger.WarnContext(ctx, "apikey_cap_tripped_sentinel_exists_failed",
							slog.String("api_key_id", claims.APIKeyID),
							slog.String("error", serr.Error()))
					case tripped:
						opts.Metrics.check(ctx, "monthly_cap", "denied")
						opts.Metrics.denial(ctx, "monthly_cap", codeQuotaExhausted)
						opts.Metrics.sentinelHit(ctx)
						logger.WarnContext(ctx, "apikey_cap_tripped_sentinel_hit",
							slog.String("api_key_id", claims.APIKeyID),
							slog.String("he_request_id", reqID))
						opts.Metrics.duration(ctx, time.Since(start).Seconds())
						_ = openaierr.Write(w, ctx, http.StatusPaymentRequired, codeQuotaExhausted, msgQuotaExhausted, nil)
						return
					}
				}

				if opts.CostReader == nil {
					// No counter wired — cannot enforce; fail-open + WARN.
					opts.Metrics.check(ctx, "monthly_cap", "redis_error")
					logger.WarnContext(ctx, "apikey_cap_check_reader_unwired",
						slog.String("api_key_id", claims.APIKeyID))
				} else {
					current, _, err := opts.CostReader(ctx, claims.APIKeyID)
					switch {
					case err != nil:
						opts.Metrics.check(ctx, "monthly_cap", "redis_error")
						logger.WarnContext(ctx, "apikey_cap_check_redis_failed",
							slog.String("api_key_id", claims.APIKeyID),
							slog.String("error", err.Error()))
					case !CheckMonthlyCap(current, *cap):
						// current >= cap — AC1 slow-path first-crossing.
						opts.fireCapTripped(ctx, logger, claims, current, *cap, reqID)
						opts.Metrics.check(ctx, "monthly_cap", "denied")
						opts.Metrics.denial(ctx, "monthly_cap", codeQuotaExhausted)
						logger.WarnContext(ctx, "apikey_monthly_cap_denied",
							slog.String("api_key_id", claims.APIKeyID),
							slog.String("user_id", claims.UserID),
							slog.String("current_cost_usd", current),
							slog.String("cap_usd", *cap))
						opts.Metrics.duration(ctx, time.Since(start).Seconds())
						_ = openaierr.Write(w, ctx, http.StatusPaymentRequired, codeQuotaExhausted, msgQuotaExhausted, nil)
						return
					default:
						// under cap — AC2 threshold detector (80% warning).
						if opts.Notifier != nil && crossedWarning(current, *cap) {
							opts.Notifier.NotifyCapThresholdAsync(ctx, claims.APIKeyID, notifyclient.ThresholdWarning80)
							opts.Metrics.notify(ctx, "warning_80", "fired")
						}
						opts.Metrics.check(ctx, "monthly_cap", "allowed")
					}
				}
			}

			opts.Metrics.duration(ctx, time.Since(start).Seconds())
			next.ServeHTTP(w, r)
		})
	}
}

// fireCapTripped runs the AC1 slow-path first-crossing side effects: claim the
// sticky-trip sentinel (BR-1.2 fire-and-forget, fail-OPEN) and fire the
// threshold notifications. The TRIPPED email fire is gated on the SETNX win
// (BR-1.9 single-source dedup); a straight-to-over-cap jump also fires
// WARNING_80 (notification-svc dedupes per BR-2 both-crossings).
func (opts Options) fireCapTripped(ctx context.Context, logger *slog.Logger, claims *middleware.CachedClaims, current, capUSD, reqID string) {
	acquired := true
	if opts.CapSentinel != nil {
		a, serr := opts.CapSentinel.SetNX(ctx, claims.APIKeyID)
		if serr != nil {
			// BR-1.2 fail-OPEN: the 402 is emitted regardless; skip the fire so
			// a Redis outage does not duplicate-email on every request.
			acquired = false
			logger.WarnContext(ctx, "apikey_cap_tripped_sentinel_write_failed",
				slog.String("api_key_id", claims.APIKeyID),
				slog.String("error", serr.Error()))
		} else {
			acquired = a
		}
	}
	// BR-1.8/1.9: only the SETNX winner fires — concurrent losers stay silent
	// so a tripped key produces exactly one tripped email. The winner also
	// fires WARNING_80 for the straight-to-over-cap jump (BR-2 both-crossings);
	// notification-svc dedupes the warning independently, so a no-op when the
	// 80% email already went out earlier in the month.
	if !acquired || opts.Notifier == nil {
		return
	}
	logger.InfoContext(ctx, "apikey_cap_tripped_sentinel_set",
		slog.String("api_key_id", claims.APIKeyID),
		slog.String("current_cost_usd", current),
		slog.String("monthly_cost_cap_usd", capUSD),
		slog.String("he_request_id", reqID))
	if crossedWarning(current, capUSD) {
		opts.Notifier.NotifyCapThresholdAsync(ctx, claims.APIKeyID, notifyclient.ThresholdWarning80)
		opts.Metrics.notify(ctx, "warning_80", "fired")
	}
	opts.Notifier.NotifyCapThresholdAsync(ctx, claims.APIKeyID, notifyclient.ThresholdTripped)
	opts.Metrics.notify(ctx, "tripped", "fired")
}

// hashClientIP masks the address to its /24 (IPv4) or /64 (IPv6) network and
// returns the hex SHA-256 — the Story-2.3 m-4 PII-discipline hasher. Raw IPs
// MUST NEVER reach slog output (SECURITY-006 grep-asserts zero raw IPs). The
// zero Addr (unparseable RemoteAddr) hashes the empty prefix deterministically.
func hashClientIP(addr netip.Addr) string {
	if !addr.IsValid() {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:])
	}
	bits := 24
	if addr.Is6() && !addr.Is4In6() {
		bits = 64
	}
	masked := netip.PrefixFrom(addr, bits).Masked().Addr()
	b, _ := masked.MarshalBinary()
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
