// Package requestid is the shared per-request identifier accessor for the
// He-API platform. It provides the context-key plumbing every Go service
// needs to extract / propagate the canonical `he_request_id` value the
// api-gateway stamps via its `RequestID` middleware (Story 3.6).
//
// Architectural placement (Architect Round 2 OQ8 ruling, Story 4.1):
// cross-service internal-import of `apps/api-gateway/internal/middleware/
// requestid` violates Go module hygiene — the accessor is fundamentally an
// observability cross-cutting concern, same category as the slog logger /
// OTel tracer / otelhttp wrap already housed under `packages/go-observability`.
// Story 4.1 lifts the context-key + accessor pair to this package so the
// `apps/adapters/deepseek/` service (and Stories 4.2-4.6 adapter siblings)
// can extract the request-id without a forbidden cross-app import.
//
// The api-gateway's `apps/api-gateway/internal/middleware/requestid` package
// re-exports the four public symbols (HeaderName, SpanAttributeKey,
// FromContext, WithRequestID) so existing gateway / openaierr / handler
// callers continue to compile unchanged. The http.Handler `RequestID(next)`
// stays in the gateway package — it is a gateway-specific wiring concern
// (OTel-span derivation + crypto/rand fallback) that has no use case in
// downstream adapter services.
//
// Story 4.1 — DeepSeek Adapter (OQ8 ratification).
package requestid
