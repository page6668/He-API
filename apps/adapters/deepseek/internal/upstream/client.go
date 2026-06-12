package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/http2"
)

// DefaultUpstreamTimeout is the BR-1.8 default outbound deadline (60s).
// Operators override via DEEPSEEK_UPSTREAM_TIMEOUT_SECONDS at deployment.
const DefaultUpstreamTimeout = 60 * time.Second

// ErrorKind classifies upstream-failure modes for the slog
// `upstream_error_kind` attribute (M2 disambiguation). Stays internal-
// observability-only — the user-facing envelope is always BR-1.4 (502 / 504).
type ErrorKind string

const (
	ErrorKindAuthRevoked       ErrorKind = "auth_revoked"
	ErrorKindQuotaExhausted    ErrorKind = "quota_exhausted"
	ErrorKindUpstream5xx       ErrorKind = "upstream_5xx"
	ErrorKindUpstream4xx       ErrorKind = "upstream_4xx"
	ErrorKindUpstreamTimeout   ErrorKind = "upstream_timeout"
	ErrorKindTLS               ErrorKind = "tls"
	ErrorKindDNS               ErrorKind = "dns"
	ErrorKindConnectionRefused ErrorKind = "connection_refused"
	ErrorKindMissingUsage      ErrorKind = "missing_usage"
	ErrorKindEmptyChoices      ErrorKind = "empty_choices"
	ErrorKindMalformedChunk    ErrorKind = "malformed_chunk"
	ErrorKindUsageConstraint   ErrorKind = "usage_constraint_violation"
)

// Client is the upstream HTTPS client over the DeepSeek API. HTTP/2 is
// forced explicitly per OQ7 (Architect Round 2 ruling) — DeepSeek supports
// HTTP/2 and the wire-protocol decision is in-code + test-coverage rather
// than incidental ALPN behaviour.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewClient constructs a Client with the HTTP/2-forced transport. timeout
// applies per-request (each outbound request's context inherits this
// deadline); pass 0 to use DefaultUpstreamTimeout.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = DefaultUpstreamTimeout
	}
	transport := &http2.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		// AllowHTTP=false keeps us strictly on HTTPS — DeepSeek's API is
		// HTTPS-only; localhost/loopback test fakes wire HTTP/2 via TLS
		// (httptest.NewTLSServer + EnableHTTP2 below) so the transport
		// stays unified across prod + test.
		AllowHTTP: false,
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			// Story 9.4 BR-TR-6 (T6.2): wrap the OQ7 forced-HTTP/2 transport so the
			// adapter→vendor model call surfaces as a client span (TTFB) on the
			// request's trace. *http2.Transport implements http.RoundTripper, so
			// otelhttp.NewTransport wraps it as its base and the forced-HTTP/2 policy
			// is FULLY preserved — the wrapper only injects `traceparent` + times the
			// call. NEVER routed through obs.NewHTTPClient() (http.DefaultTransport
			// would drop the forced-HTTP/2 policy).
			Transport: otelhttp.NewTransport(transport),
			Timeout:   timeout,
		},
	}
}

// ClassifyError turns a transport-level or HTTP-level error into an
// ErrorKind. Falls through to ErrorKindUpstream5xx for unknown failure
// modes so the gateway-side BR-1.4 mapping always has a non-empty value
// to log.
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	// Context cancellation / deadline takes precedence — http.Client.Do
	// wraps both in *url.Error, but the wrapping varies by Go version
	// (1.22 vs 1.23) and HTTP/2 transport path. errors.Is sees through.
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorKindUpstreamTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrorKindUpstreamTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrorKindDNS
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") {
		return ErrorKindConnectionRefused
	}
	if strings.Contains(msg, "tls") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate") {
		return ErrorKindTLS
	}
	// Generic timeout — http.Client.Timeout surfaces as an err with
	// Timeout()==true.
	type timeoutAware interface{ Timeout() bool }
	var tw timeoutAware
	if errors.As(err, &tw) && tw.Timeout() {
		return ErrorKindUpstreamTimeout
	}
	return ErrorKindUpstream5xx
}

// ClassifyHTTPStatus maps an upstream HTTP status to an ErrorKind. 401 →
// auth_revoked (M2 — our key was rotated / revoked); 429 → quota_exhausted
// (M2 — our account hit the upstream rate-limit); 4xx (other) →
// upstream_4xx; 5xx → upstream_5xx; 2xx → "" (no error).
func ClassifyHTTPStatus(status int) ErrorKind {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusUnauthorized:
		return ErrorKindAuthRevoked
	case status == http.StatusTooManyRequests:
		return ErrorKindQuotaExhausted
	case status >= 400 && status < 500:
		return ErrorKindUpstream4xx
	default:
		return ErrorKindUpstream5xx
	}
}

// UpstreamError carries the classification + HTTP status (when applicable)
// alongside the error so the adapter Chat handler can build the right
// Connect-RPC Code + slog attributes.
type UpstreamError struct {
	Kind   ErrorKind
	Status int
	Cause  error
}

func (e *UpstreamError) Error() string {
	if e == nil || e.Cause == nil {
		return fmt.Sprintf("upstream error: kind=%s status=%d", e.Kind, e.Status)
	}
	return fmt.Sprintf("upstream error: kind=%s status=%d: %v", e.Kind, e.Status, e.Cause)
}

func (e *UpstreamError) Unwrap() error { return e.Cause }
