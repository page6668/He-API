package upstream

import (
	"crypto/tls"
	"net/http"
	"time"
)

// DefaultUpstreamTimeout is the BR-1.8 default outbound deadline (60s).
// Operators override via GLM_UPSTREAM_TIMEOUT_SECONDS at deployment.
const DefaultUpstreamTimeout = 60 * time.Second

// Client is the upstream HTTPS client over the Zhipu v4 OpenAI-compat
// API.
//
// Transport policy (Architect Round 1 OQ-4.4-4 ruling — cascade default
// from Story-4.2 OQ-4.2-5): use the Go stdlib
// `http.Transport{ForceAttemptHTTP2: true}` which allows ALPN-negotiated
// HTTP/1.1 fallback if Zhipu lacks HTTP/2 support. Empirical-fallback is
// safer than dogmatic-forced for SSE long-streams; documented in
// `docs/dev/logs/4.4-dev-log.md` Phase 0.
type Client struct {
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
			Transport: transport,
			Timeout:   timeout,
		},
	}
}
