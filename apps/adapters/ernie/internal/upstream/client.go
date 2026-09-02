package upstream

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// DefaultUpstreamTimeout is the BR-1.8 default outbound deadline (60s).
// Operators override via ERNIE_UPSTREAM_TIMEOUT_SECONDS at deployment.
const DefaultUpstreamTimeout = 60 * time.Second

// Client is the upstream HTTPS client over the Baidu Qianfan v2 OpenAI-
// compat API.
//
// Transport policy (Architect Round 1 OQ-4.6-5 ruling — cascade default
// from Story-4.2 OQ-4.2-5): use the Go stdlib
// `http.Transport{ForceAttemptHTTP2: true}` which allows ALPN-negotiated
// HTTP/1.1 fallback if Qianfan v2 lacks HTTP/2 support. Empirical-fallback
// is safer than dogmatic-forced for SSE long-streams; documented in
// `docs/dev/logs/4.6-dev-log.md` Phase 0.
type Client struct {
	mu         sync.RWMutex
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewClient constructs a Client with the HTTP/2-preferred transport. If
// timeout is 0, DefaultUpstreamTimeout applies.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = DefaultUpstreamTimeout
	}
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			// Story 9.4 BR-TR-6 (T6.2): wrap the bespoke OQ-4.2-5 ALPN transport so
			// the adapter→vendor model call surfaces as a client span (TTFB) on the
			// request's trace. otelhttp.NewTransport takes `transport` as its base
			// RoundTripper, so the ForceAttemptHTTP2/ALPN policy is FULLY preserved —
			// the wrapper only injects `traceparent` + times the call. NEVER routed
			// through obs.NewHTTPClient() (that uses http.DefaultTransport and would
			// drop the ALPN policy).
			Transport: otelhttp.NewTransport(transport),
			Timeout:   timeout,
		},
	}
}

// SetKey 热更新 upstream API key (AD-004 运行时配置)。线程安全。
func (c *Client) SetKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.APIKey = key
}

// SetBaseURL 热更新 upstream base URL (AD-004 运行时配置)。线程安全。
func (c *Client) SetBaseURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.BaseURL = url
}

// Key 读取当前 upstream API key (受 RLock 保护)。
func (c *Client) Key() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.APIKey
}

// BaseURLSafe 读取当前 upstream base URL (受 RLock 保护)。
func (c *Client) BaseURLSafe() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.BaseURL
}

