// Story 5.2 T5.2 — monthly-cost counter WRITE contract (STUB).
//
// The production write path (INCRBYFLOAT on usage:apikey:{id}:month_cost_usd
// per usage event) is owned by billing-svc (Epic 6+). This stub gives
// upstream gateway code a stable signature to call TODAY so the wiring lights
// up automatically when billing-svc lands — no call-site change needed.
package usage

import (
	"context"
	"log/slog"
)

// PublishCostIncrement is the forward-compat hook for billing-svc. Currently
// a no-op that logs at WARN so an accidental call in production is observable
// (it MUST NOT silently appear to work). Returns nil — callers treat the
// publish as best-effort.
func PublishCostIncrement(ctx context.Context, logger *slog.Logger, apiKeyID, costUSD string) error {
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, "apikey_usage_counter_publish_not_implemented",
		slog.String("api_key_id", apiKeyID),
		slog.String("cost_usd", costUSD))
	return nil
}
