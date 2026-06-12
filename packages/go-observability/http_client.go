package obs

// Story 9.4 root-cause #2 fix: outbound client instrumentation. Before 9.4 every
// downstream connect-RPC client was built with a bare &http.Client{} (no
// otelhttp.NewTransport), so even with the global propagator installed the client
// never INJECTED `traceparent` into the outgoing HTTP/2 headers and the chain
// broke at every service edge. NewHTTPClient makes "instrumented by default" the
// path of least resistance; the T-AUDIT grep invariant (zero bare clients on an
// RPC path) keeps it that way.

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WrapTransport wraps an http.RoundTripper in otelhttp.NewTransport so every
// outbound request issued through it injects the W3C `traceparent` of the active
// span (BR-TR-2). A nil base falls back to http.DefaultTransport (preserving the
// default keep-alive connection pool — RESOURCE-001).
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}

type clientOptions struct {
	timeout   time.Duration
	transport http.RoundTripper
}

// ClientOption configures NewHTTPClient.
type ClientOption func(*clientOptions)

// WithTimeout sets the returned client's Timeout (callers MUST pass through their
// existing per-call-site Timeout so swapping a bare client is behaviour-preserving).
func WithTimeout(d time.Duration) ClientOption {
	return func(o *clientOptions) { o.timeout = d }
}

// WithBaseTransport overrides the wrapped base RoundTripper (defaults to
// http.DefaultTransport). Used when a call site needs custom TLS/dial settings.
func WithBaseTransport(rt http.RoundTripper) ClientOption {
	return func(o *clientOptions) { o.transport = rt }
}

// NewHTTPClient returns an *http.Client whose Transport is otelhttp.NewTransport
// (so it injects `traceparent` on every request) and whose Timeout is the
// caller-supplied one (BR-TR-2). It is the mandatory replacement for a bare
// &http.Client{} on any downstream-RPC construction path.
func NewHTTPClient(opts ...ClientOption) *http.Client {
	o := &clientOptions{transport: http.DefaultTransport}
	for _, opt := range opts {
		opt(o)
	}
	return &http.Client{
		Transport: otelhttp.NewTransport(o.transport),
		Timeout:   o.timeout,
	}
}
