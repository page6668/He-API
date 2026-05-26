// tpm_deduct.go — Story 5.3 AC3 post-deduction hook.
//
// Invoked from the chat-completions handler after the upstream returns
// the canonical `usage.total_tokens` (Story 4.1 BR-3.3 Normaliser
// contract). For streaming, invoked on the tail-usage chunk per
// Story 4.1 BR-2.4 (`usage` populated BEFORE `data: [DONE]`).
//
// Fire-and-forget from the request's perspective: the response has
// already been written by the time TPMDeduct runs. Errors are logged
// WARN + Prometheus-counted; never propagated to the client.
//
// Streaming edge cases (Architect Q10):
//   - normal completion: deduct usage.total_tokens
//   - stream error mid-flight (Q10-ii): caller deducts prompt_tokens
//   - client disconnect (Q10-iii): caller deducts prompt_tokens
//   - missing-tail-usage 502 (Q10-iv): caller does NOT invoke TPMDeduct

package ratelimit

import (
	"context"
	"log/slog"
)

// TPMDeduct adds `tokens` to the per-key TPM counter via the atomic
// tpm_deduct.lua script (INCRBY + EXPIRE 60 NX in a single Redis
// round-trip). Strict NX semantics preserve the fixed-window contract
// (Architect H-2 / M-1).
//
// Behaviour:
//   - tokens ≤ 0 → no-op + slog WARN (defensive — BR-3.10 / BLIND-SPOT
//     UNIT-030 documents tokens=-1 as a guard against upstream
//     normaliser regressions);
//   - Redis nil (cfg.Redis == nil) → no-op + slog DEBUG;
//   - Redis error → slog WARN + metric inc; never returns the error
//     to the caller (best-effort post-deduction).
//
// PII discipline (BR-3.10 / BR-X.6): the slog record carries ONLY the
// integer token count and api_key_id. NEVER message content. NEVER
// plaintext keys.
func (m *Middleware) TPMDeduct(ctx context.Context, apiKeyID string, tokens int) {
	if tokens <= 0 {
		m.logger.WarnContext(
			ctx, "ratelimit_tpm_deduct_skipped",
			slog.String("api_key_id", apiKeyID),
			slog.Int("tokens", tokens),
			slog.String("reason", "non_positive_tokens"),
		)
		return
	}
	if apiKeyID == "" {
		m.logger.WarnContext(
			ctx, "ratelimit_tpm_deduct_skipped",
			slog.Int("tokens", tokens),
			slog.String("reason", "empty_api_key_id"),
		)
		return
	}
	if m.cfg.Redis == nil {
		m.logger.DebugContext(
			ctx, "ratelimit_tpm_deduct_no_redis",
			slog.String("api_key_id", apiKeyID),
			slog.Int("tokens", tokens),
		)
		return
	}

	key := keyPrefix + apiKeyID + ":tpm"
	if err := m.tpmDeductScript.Run(ctx, m.cfg.Redis, []string{key}, tokens, tpmTTLSeconds).Err(); err != nil {
		m.metrics.tpmDeductFailInc(ctx)
		m.logger.WarnContext(
			ctx, "ratelimit_tpm_deduct_failed",
			slog.String("api_key_id", apiKeyID),
			slog.Int("tokens", tokens),
			slog.String("error", err.Error()),
		)
		return
	}

	m.logger.DebugContext(
		ctx, "ratelimit_tpm_deducted",
		slog.String("api_key_id", apiKeyID),
		slog.Int("tokens", tokens),
	)
}
