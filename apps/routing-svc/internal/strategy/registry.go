package strategy

import (
	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// DefaultStrategies is the canonical strategy-slug → implementation map that
// routing-svc registers at boot. It is the single registration point consumed
// by cmd/server/main.go and the engine tests, so the set of supported slugs
// never drifts between production wiring and tests.
//
// STRATEGY_UNSPECIFIED is intentionally absent: the engine maps it to
// STRATEGY_DEFAULT before dispatch (Q-D), so it needs no own entry.
func DefaultStrategies() map[routingv1.Strategy]engine.Strategy {
	return map[routingv1.Strategy]engine.Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: NewDefault(),
		routingv1.Strategy_STRATEGY_QUALITY: NewQuality(),
		routingv1.Strategy_STRATEGY_COST:    NewCost(),
		routingv1.Strategy_STRATEGY_LATENCY: NewLatency(),
	}
}
