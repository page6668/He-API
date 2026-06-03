package routingclient

import (
	"net/http"
	"strings"

	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// MetaModelPrefix marks the he-router-{quality,cost,latency} virtual meta-models
// — a routing directive in the OpenAI `model` field, not a routable id.
const MetaModelPrefix = "he-router-"

// RoutingStrategyHeader is the request header carrying an explicit strategy.
const RoutingStrategyHeader = "X-He-Routing-Strategy"

// ParseStrategy resolves the routing strategy from the request per the
// cascade-locked Q-I precedence:
//
//	he-router-{quality,cost,latency} meta-model in `model`  WINS
//	  → else X-He-Routing-Strategy header
//	  → else STRATEGY_DEFAULT (passthrough of the concrete `model`)
//
// Returns:
//   - strategy        — the resolved Strategy enum;
//   - requestedModel  — the requested_model to send to routing-svc (empty for a
//     meta-model directive; the concrete `model` otherwise);
//   - isMeta          — true when a he-router-* meta-model drove the decision
//     (governs Q-G fail-closed and Q-H NotFound→502 mapping);
//   - conflict        — true when BOTH a meta-model and a header were present
//     (meta wins; the caller logs WARN strategy_conflict).
//
// An unknown/empty header value resolves to the default path (UNSPECIFIED ≡
// DEFAULT), never an error (BLIND-BOUNDARY-005); an unknown he-router-xyz suffix
// is NOT a valid meta-model and falls through to the default path as a concrete
// (unresolvable) model id.
func ParseStrategy(model string, header http.Header) (strategy routingv1.Strategy, requestedModel string, isMeta, conflict bool) {
	hdrPresent := strings.TrimSpace(header.Get(RoutingStrategyHeader)) != ""

	if s, ok := metaStrategy(model); ok {
		// Meta-model wins; requested_model is cleared — it is a directive, not a
		// routable target. Header (if any) is ignored → conflict flag.
		return s, "", true, hdrPresent
	}

	if hdrPresent {
		if s, ok := headerStrategy(header.Get(RoutingStrategyHeader)); ok {
			return s, model, false, false
		}
		// Unknown header value → default path (not an error).
	}

	return routingv1.Strategy_STRATEGY_DEFAULT, model, false, false
}

// metaStrategy maps a he-router-{quality,cost,latency} model id to its strategy.
// A he-router-* with any other suffix is not a valid meta-model (ok=false).
func metaStrategy(model string) (routingv1.Strategy, bool) {
	if !strings.HasPrefix(model, MetaModelPrefix) {
		return routingv1.Strategy_STRATEGY_UNSPECIFIED, false
	}
	return strategyFromSuffix(strings.TrimPrefix(model, MetaModelPrefix))
}

// headerStrategy maps an X-He-Routing-Strategy header value to its strategy
// (case-insensitive). Unknown values → ok=false (default path).
func headerStrategy(v string) (routingv1.Strategy, bool) {
	return strategyFromSuffix(strings.ToLower(strings.TrimSpace(v)))
}

func strategyFromSuffix(s string) (routingv1.Strategy, bool) {
	switch s {
	case "quality":
		return routingv1.Strategy_STRATEGY_QUALITY, true
	case "cost":
		return routingv1.Strategy_STRATEGY_COST, true
	case "latency":
		return routingv1.Strategy_STRATEGY_LATENCY, true
	default:
		return routingv1.Strategy_STRATEGY_UNSPECIFIED, false
	}
}

// strategyLabel is the bounded metric/slog label for a Strategy enum.
func strategyLabel(s routingv1.Strategy) string {
	switch s {
	case routingv1.Strategy_STRATEGY_DEFAULT:
		return "default"
	case routingv1.Strategy_STRATEGY_QUALITY:
		return "quality"
	case routingv1.Strategy_STRATEGY_COST:
		return "cost"
	case routingv1.Strategy_STRATEGY_LATENCY:
		return "latency"
	default:
		return "unspecified"
	}
}
