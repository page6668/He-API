package upstream

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 4.2-UNIT-004 (P0) — Architect Round 1 OQ-4.2-5 ruling: the Qwen client
// uses stdlib http.Transport{ForceAttemptHTTP2: true} so ALPN can
// negotiate HTTP/2 when the upstream supports it AND fall back to
// HTTP/1.1 when only http/1.1 is offered. This is the Qwen-specific
// divergence from Story-4.1 DeepSeek (which forces HTTP/2).
//
// Setup A: TLS server offering only "http/1.1" → client must negotiate
// HTTP/1.1 successfully (no error); response.Proto == "HTTP/1.1".
func TestClient_ALPN_NegotiatesHTTP11_WhenServerOffersOnlyHTTP11(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	defer srv.Close()

	c := NewClient(srv.URL, "k", 0)
	c.HTTPClient.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}},
		ForceAttemptHTTP2: true,
	}
	resp, err := c.HTTPClient.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("Get err = %v (HTTP/1.1 ALPN fallback should succeed)", err)
	}
	defer resp.Body.Close()
	if resp.Proto != "HTTP/1.1" {
		t.Fatalf("Proto = %q, want HTTP/1.1 (server only offered http/1.1)", resp.Proto)
	}
}

// Setup B: TLS server offering both "h2" and "http/1.1" → client
// negotiates HTTP/2; response.Proto == "HTTP/2.0".
func TestClient_ALPN_NegotiatesHTTP2_WhenServerOffersH2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	c := NewClient(srv.URL, "k", 0)
	c.HTTPClient.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}},
		ForceAttemptHTTP2: true,
	}
	resp, err := c.HTTPClient.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	defer resp.Body.Close()
	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("Proto = %q, want HTTP/2.0 (server offered h2)", resp.Proto)
	}
}

// 4.2-UNIT-classifyhttpstatus-429 — OQ-4.2-4 ratification anchor.
func TestClassifyHTTPStatus_429_IsRateLimitThrottle(t *testing.T) {
	if got := ClassifyHTTPStatus(429); got != ErrorKindRateLimitThrottle {
		t.Fatalf("ClassifyHTTPStatus(429) = %q, want %q", got, ErrorKindRateLimitThrottle)
	}
}

// 4.2-UNIT-classifyhttpstatus-other-mappings
func TestClassifyHTTPStatus_OtherStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{200, ""},
		{201, ""},
		{401, ErrorKindAuthRevoked},
		{403, ErrorKindUpstream4xx},
		{418, ErrorKindUpstream4xx},
		{500, ErrorKindUpstream5xx},
		{503, ErrorKindUpstream5xx},
	}
	for _, c := range cases {
		if got := ClassifyHTTPStatus(c.status); got != c.want {
			t.Errorf("ClassifyHTTPStatus(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}
