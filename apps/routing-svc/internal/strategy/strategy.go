// Package strategy holds the 4 routing-strategy implementations registered by
// routing-svc: quality, cost, latency (Story 6.2 REAL scoring) and default
// (passthrough). Each satisfies engine.Strategy (BR2-1); the three named
// strategies also satisfy engine.SourcedStrategy (Story 6.2 score_source).
//
// Story 6.2 replaced the Story-6.1 first-alphabetical STUBS with real sources:
//   - cost    — ranks over he_api.model_pricing (input+output ascending), via
//     the pricing.Snapshot seam (candidates.go: cheapest).
//   - quality — ranks benchmark_results.quality_score desc when the Scorer has
//     data, else DEGRADES to the cost ordering (scored.go: scoredOrDegrade).
//   - latency — ranks request_logs_hourly_agg.p95_latency_ms asc when the
//     Scorer has data, else DEGRADES to the cost ordering.
//
// quality/latency degrade deterministically until Epic 9 populates their
// ClickHouse backing stores (Q-A Option A); the scoring.Scorer seam makes the
// real binding a thin Epic-9 drop-in (BR3-1).
//
// Every named-strategy path EXCLUDES the he-router-* virtual entries from
// candidacy (candidates.go: concreteCandidates) — a he-router-* id returned as
// selected_model would miss adapterRegistry.Resolve on the gateway hot path
// (Q-D / BR2-5 correctness gate).
package strategy
