package requestid

import "context"

// HeaderName is the canonical response header carrying the per-request id.
// Documented in docs/architecture/rest-api-spec.md §5.1.1 ("always present").
const HeaderName = "X-He-Request-Id"

// SpanAttributeKey is the OTel span attribute name for the request-id. The
// `he.` prefix is reserved as the project's span-attribute namespace; OTel
// semantic conventions own the un-prefixed namespace.
const SpanAttributeKey = "he.request_id"

// requestIDKey is the unexported context key. Empty struct → no string-key
// collision risk. Defined here (not in the gateway middleware package) so
// every Go service in the platform — gateway + adapters + future microservices
// — reads from the SAME context-value slot. A parallel key in another package
// would silently fail FromContext lookups across the package boundary.
type requestIDKey struct{}

// FromContext returns the stamped he_request_id from ctx. Returns ("", false)
// if no id has been stamped (probe routes, tests without the middleware
// wired, RPC handlers called without ContextWith). Comma-ok per Go idiom.
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(requestIDKey{}).(string)
	return v, ok
}

// WithRequestID stamps id onto ctx as the per-request he_request_id. Used by
// the gateway's RequestID middleware (single-writer per BR-2.1) and by tests
// + adapter Connect-RPC server interceptors that materialise the request-id
// from the inbound `X-He-Request-Id` header.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// ContextWith is a synonym for WithRequestID matching the OQ8 ruling text
// ("`ContextWith(ctx, id) context.Context`"). Kept alongside WithRequestID
// so callers can use whichever name reads more naturally at the call site.
func ContextWith(ctx context.Context, id string) context.Context {
	return WithRequestID(ctx, id)
}
