// Package ratelimit — Story 5.3 — gateway-side 3-axis (QPS/RPM/TPM) rate
// limit middleware backed by atomic Redis Lua scripts.
//
// The middleware runs INSIDE the bearer-auth boundary (Story 3.2 — needs
// the resolved api_key_id) and BEFORE the chat-completions / embeddings
// handlers (Architect Q9). It MUST NOT cache rate-limit counters in
// process memory — Redis is the single source of truth (BR-X.3 stateless
// gateway invariant per high-level-architecture.md §1.2).
//
// Algorithm: fixed-window INCR+EXPIRE NX (Architect Q2 ratified MVP —
// divergence from §10.3 nominal ZSET sliding-window). Boundary artifact
// bounded ≤2× ceiling at the window edge; acceptable for the abuse-
// prevention threat model. ZSET sliding-window is reserved as a future
// precision-upgrade Story.
//
// Failure mode: fail-OPEN on Redis unavailable (Architect Q6 ratified;
// divergence from Story-2.3 OQ3 fail-CLOSED rationalised by Q6 threat-
// model analysis — /v1/chat/completions is authenticated and Story 5.4
// monthly-cap 402 is the cost backstop).
package ratelimit

import (
	"context"
	"time"
)

// Ceilings carries the per-axis budget for one rate-limit decision. MVP
// (Architect C-1 remediation): values are sourced from Helm
// `infra/helm/api-gateway/values.yaml` `ratelimit.freeTierDefaults.*` →
// env vars `RATELIMIT_FREE_TIER_{QPS,RPM,TPM}_MAX` → loaded once at boot
// by cmd/server/main.go. NO per-request resolution; NO read of
// `api_keys.scope` (Story 5.2's frozen JSONB contract does not carry
// `rate_limits`).
type Ceilings struct {
	QPSMax int `json:"qps_max"`
	RPMMax int `json:"rpm_max"`
	TPMMax int `json:"tpm_max"`
}

// Decision is the parsed result of one atomic Lua check_and_incr call.
type Decision struct {
	// Allowed is true when all 3 axes were below ceiling.
	Allowed bool
	// RetryAfterSeconds is the integer delta-seconds the client should
	// wait before retrying. 0 on allowed path; 1-60 inclusive on denied.
	RetryAfterSeconds int
	// ExhaustedAxis identifies which axis short-circuited. One of:
	// "qps", "rpm", "tpm", or "" (empty on allowed path).
	ExhaustedAxis string
}

// ResolveCeilingsFunc returns the effective ceilings for the supplied
// api_key_id. MVP impl is a pure constant return of FreeTierDefaults —
// signature kept stable so a future 5.x Story can swap in a cached-
// claims-aware impl (Architect H-1 remediation pattern) without changing
// the middleware call site.
type ResolveCeilingsFunc func(ctx context.Context, apiKeyID string) (Ceilings, error)

// Config is the dependency surface required to construct a Middleware.
// All fields are required UNLESS the doc comment says otherwise.
type Config struct {
	// Redis is the go-redis v9 client. When nil the middleware no-ops
	// (every request passes through with slog WARN once per cold-start).
	// Production wiring constructs the client via cmd/server/main.go and
	// reuses the bearer-auth Redis URL.
	Redis RedisClient

	// ResolveCeilings returns the per-key budget. MVP impl is a pure
	// constant returning FreeTierDefaults — see types.go doc comment.
	ResolveCeilings ResolveCeilingsFunc

	// FreeTierDefaults is the fallback budget when ResolveCeilings is
	// nil OR returns an error. Operator-tunable per env via Helm
	// `ratelimit.freeTierDefaults.*` (M-3 source-of-truth).
	FreeTierDefaults Ceilings

	// FailOpenTimeout caps the Redis Lua round-trip. On timeout the
	// middleware fails OPEN (Architect Q6). SM default: 5 * time.Millisecond.
	FailOpenTimeout time.Duration
}
