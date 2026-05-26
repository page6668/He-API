// ratelimit.go — Middleware constructor + per-request decision path.
//
// Wraps an inner http.Handler with the atomic Redis Lua check across the
// 3 axes (QPS, RPM, TPM). The api_key_id is read from the request context
// (populated by Story-3.2 bearer-auth). On denial: 429 envelope + Retry-
// After. On Redis error / timeout: fail-OPEN (Architect Q6).

package ratelimit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// RedisClient is the narrow surface needed by the middleware + TPMDeduct.
// *redis.Client satisfies it; tests may substitute via miniredis.NewClient
// (also satisfies it). Defined here (not at the call site) so the
// middleware unit tests can plug a thin fake without dragging the entire
// go-redis import surface.
type RedisClient interface {
	redis.Scripter
}

//go:embed scripts/check_and_incr.lua
var checkAndIncrLuaSrc string

//go:embed scripts/tpm_deduct.lua
var tpmDeductLuaSrc string

// keyPrefix is the architecture data-models.md §4.3 Redis Key 规范 stem
// for per-key rate-limit counters. Story 5.3 EXTENDS the pre-declared
// `:qps` row with the implied `:rpm` + `:tpm` siblings (per `同上`).
const keyPrefix = "ratelimit:key:"

// axis names — single source of truth for the 3 axes. The Lua script
// returns an integer 1/2/3 which the Go side maps via axisFromIndex.
const (
	axisQPS = "qps"
	axisRPM = "rpm"
	axisTPM = "tpm"
)

// TTL constants for the 3 axes. QPS = 1s window; RPM + TPM = 60s.
const (
	qpsTTLSeconds = 1
	rpmTTLSeconds = 60
	tpmTTLSeconds = 60
)

// codeFor maps the exhausted axis to the canonical §5.1.2 envelope code.
var codeFor = map[string]string{
	axisQPS: "429_rate_limit_qps",
	axisRPM: "429_rate_limit_rpm",
	axisTPM: "429_rate_limit_tpm",
}

// messageFor maps the exhausted axis to its user-facing message.
var messageFor = map[string]string{
	axisQPS: "Rate limit exceeded: per-second request budget",
	axisRPM: "Rate limit exceeded: per-minute request budget",
	axisTPM: "Rate limit exceeded: per-minute token budget",
}

// DefaultFailOpenTimeout per Architect Q6 SM default — caps the Redis
// Lua round-trip on the request hot path. Production main.go MAY
// override via Config.FailOpenTimeout; tests typically use a longer
// value to avoid flake on slow CI.
const DefaultFailOpenTimeout = 5 * time.Millisecond

// Middleware is the per-process rate-limit middleware. Build once at
// startup and share across all bearer-protected routes.
type Middleware struct {
	cfg             Config
	logger          *slog.Logger
	checkAndIncr    *redis.Script
	tpmDeductScript *redis.Script
	metrics         *metrics
}

// New constructs a Middleware. cfg.Redis MAY be nil (middleware degrades
// to a no-op pass-through with a one-shot WARN log per cold-start); all
// other fields fall back to sensible defaults.
//
// Caller is expected to log boot-time validation failures (qpsMax < 1,
// etc.) before passing the Config — the middleware itself trusts the
// values that arrive.
func New(cfg Config, logger *slog.Logger) *Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.FailOpenTimeout <= 0 {
		cfg.FailOpenTimeout = DefaultFailOpenTimeout
	}
	if cfg.ResolveCeilings == nil {
		// MVP free-tier-only ResolveCeilings — pure constant return per
		// Architect H-1 remediation (signature kept stable for a future
		// 5.x Story to swap in a cached-claims-aware impl).
		defaults := cfg.FreeTierDefaults
		cfg.ResolveCeilings = func(context.Context, string) (Ceilings, error) {
			return defaults, nil
		}
	}
	return &Middleware{
		cfg:             cfg,
		logger:          logger,
		checkAndIncr:    redis.NewScript(checkAndIncrLuaSrc),
		tpmDeductScript: redis.NewScript(tpmDeductLuaSrc),
		metrics:         newMetrics(),
	}
}

// Wrap returns an http.Handler that enforces the 3-axis rate limit
// before delegating to next. Use as the INNER wrap of the bearer-auth
// chain in cmd/server/main.go (Architect Q9):
//
//	mux.Handle("POST /v1/chat/completions",
//	    bearerAuth.RequireAPIKey(rateLimit.Wrap(chatHandler)))
//
// On denial the response is the canonical 5-field §5.1.2 envelope per
// openaierr.Write + `Retry-After: <seconds>` header. On Redis error /
// timeout: fail-OPEN (passes through with slog WARN; never 5xx).
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// BR-X.4 / Architect Q9 — bearer-auth MUST have populated
		// api_key_id. If missing, this is a chain-position misconfig
		// in main.go; emit defensive 500 + slog ERROR (SEC-003).
		apiKeyID, ok := middleware.APIKeyIDFromContext(ctx)
		if !ok || apiKeyID == "" {
			m.logger.ErrorContext(
				ctx, "ratelimit_middleware_misconfigured",
				slog.String("error", ErrAPIKeyIDMissing.Error()),
			)
			_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
				"500_gateway_misconfigured",
				"Gateway misconfigured: ratelimit middleware reached without resolved api_key_id",
				nil)
			return
		}

		// Resolve ceilings (MVP: constant FreeTierDefaults).
		ceilings, err := m.cfg.ResolveCeilings(ctx, apiKeyID)
		if err != nil {
			// Defensive — the constant impl cannot fail; log + use defaults.
			m.logger.WarnContext(
				ctx, "ratelimit_resolve_ceilings_failed",
				slog.String("api_key_id", apiKeyID),
				slog.String("error", err.Error()),
			)
			ceilings = m.cfg.FreeTierDefaults
		}

		decision, checkErr := m.check(ctx, apiKeyID, ceilings)
		if checkErr != nil {
			// Fail-OPEN per BR-X.2 / Architect Q6. Pass through; log
			// WARN; increment fail_open counter. Never 5xx the client.
			m.logger.WarnContext(
				ctx, "ratelimit_fail_open",
				slog.String("api_key_id", apiKeyID),
				slog.String("error", checkErr.Error()),
			)
			m.metrics.failOpenInc(ctx, "all")
			next.ServeHTTP(w, r)
			return
		}

		if !decision.Allowed {
			axis := decision.ExhaustedAxis
			m.metrics.decisionDenied(ctx, axis)
			retry := decision.RetryAfterSeconds
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			code, msg := codeFor[axis], messageFor[axis]
			m.logger.InfoContext(
				ctx, "ratelimit_decision",
				slog.String("axis", axis),
				slog.String("outcome", "denied"),
				slog.String("api_key_id", apiKeyID),
				slog.Int("retry_after_seconds", retry),
				slog.Int("qps_max", ceilings.QPSMax),
				slog.Int("rpm_max", ceilings.RPMMax),
				slog.Int("tpm_max", ceilings.TPMMax),
			)
			_ = openaierr.Write(w, ctx, http.StatusTooManyRequests, code, msg, nil)
			return
		}

		// Allowed path — one slog record per request per axis.
		m.metrics.decisionAllowed(ctx, axisQPS)
		m.metrics.decisionAllowed(ctx, axisRPM)
		// TPM allowed at pre-check stage; post-deduction lands later.
		m.logger.DebugContext(
			ctx, "ratelimit_decision",
			slog.String("outcome", "allowed"),
			slog.String("api_key_id", apiKeyID),
		)
		next.ServeHTTP(w, r)
	})
}

// check executes the atomic Lua script. Returns (decision, nil) on a
// successful round-trip OR (zero-decision, error) on Redis failure /
// timeout / malformed reply. Callers fail-OPEN on err != nil.
func (m *Middleware) check(ctx context.Context, apiKeyID string, c Ceilings) (Decision, error) {
	if m.cfg.Redis == nil {
		return Decision{}, ErrRedisUnavailable
	}

	keys := []string{
		keyPrefix + apiKeyID + ":qps",
		keyPrefix + apiKeyID + ":rpm",
		keyPrefix + apiKeyID + ":tpm",
	}

	args := []interface{}{
		c.QPSMax, c.RPMMax, c.TPMMax,
		qpsTTLSeconds, rpmTTLSeconds,
	}

	cctx, cancel := context.WithTimeout(ctx, m.cfg.FailOpenTimeout)
	defer cancel()

	started := time.Now()
	raw, err := m.checkAndIncr.Run(cctx, m.cfg.Redis, keys, args...).Result()
	m.metrics.recordDuration(ctx, time.Since(started).Seconds())
	if err != nil {
		return Decision{}, err
	}

	dec, perr := parseDecision(raw)
	if perr != nil {
		return Decision{}, perr
	}
	return dec, nil
}

// parseDecision converts the Lua array `{decision, retry_after, axis_index}`
// into our Decision struct. Defensive against malformed replies — returns
// an error which the caller treats as fail-open per BR-X.2.
func parseDecision(raw interface{}) (Decision, error) {
	arr, ok := raw.([]interface{})
	if !ok || len(arr) != 3 {
		return Decision{}, fmt.Errorf("ratelimit: malformed Lua reply: %T %v", raw, raw)
	}
	decisionI, _ := arr[0].(int64)
	retryI, _ := arr[1].(int64)
	axisIdxI, _ := arr[2].(int64)
	axis := axisFromIndex(int(axisIdxI))
	return Decision{
		Allowed:           decisionI == 1,
		RetryAfterSeconds: int(retryI),
		ExhaustedAxis:     axis,
	}, nil
}

// axisFromIndex maps the Lua-side integer axis index back to its string
// name. 0 = none (allowed path).
func axisFromIndex(i int) string {
	switch i {
	case 1:
		return axisQPS
	case 2:
		return axisRPM
	case 3:
		return axisTPM
	default:
		return ""
	}
}

// IsAPIKeyIDMissing reports whether err is the sentinel ErrAPIKeyIDMissing.
// Exposed so callers can distinguish "operator misconfigured the chain"
// from generic Redis errors when logging.
func IsAPIKeyIDMissing(err error) bool { return errors.Is(err, ErrAPIKeyIDMissing) }
