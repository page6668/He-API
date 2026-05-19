package upstream

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 4.5-UNIT-003 (P0) cascade — Architect Round 1 OQ-4.5-6 ruling: the
// Doubao client uses stdlib http.Transport{ForceAttemptHTTP2: true} so
// ALPN can negotiate HTTP/2 when the upstream supports it AND fall back
// to HTTP/1.1 when only http/1.1 is offered.
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

// 4.5-UNIT-010 (P0) — OQ-4.5-6 status-only classifier mapping per
// L2 per-vendor errors.go replica. NEW for Story 4.5: enum includes
// ErrorKindEndpointIDNotConfigured per BR-4.6 (surfaced via ClassifyError
// when err wraps ErrUnsupportedModel — NOT via an HTTP status code).
func TestClassifyHTTPStatus_StatusOnlyMapping(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{200, ""},
		{201, ""},
		{400, ErrorKindUpstream4xx},
		{401, ErrorKindAuthRevoked},
		{403, ErrorKindAuthRevoked},
		{418, ErrorKindUpstream4xx},
		{429, ErrorKindRateLimitThrottle},
		{500, ErrorKindUpstream5xx},
		{502, ErrorKindUpstream5xx},
		{503, ErrorKindUpstream5xx},
	}
	for _, c := range cases {
		if got := ClassifyHTTPStatus(c.status); got != c.want {
			t.Errorf("ClassifyHTTPStatus(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}

// 4.5-UNIT-010 (P0) — NEW BR-4.6: ClassifyError surfaces
// ErrorKindEndpointIDNotConfigured when err wraps ErrUnsupportedModel.
func TestClassifyError_EndpointIDNotConfigured_FromErrUnsupportedModel(t *testing.T) {
	wrapped := errors.Join(errors.New("translate: build request body"), ErrUnsupportedModel)
	if got := ClassifyError(wrapped); got != ErrorKindEndpointIDNotConfigured {
		t.Fatalf("ClassifyError(wrap of ErrUnsupportedModel) = %q, want %q (BR-4.6)", got, ErrorKindEndpointIDNotConfigured)
	}
	if got := ClassifyError(ErrUnsupportedModel); got != ErrorKindEndpointIDNotConfigured {
		t.Fatalf("ClassifyError(ErrUnsupportedModel) = %q, want %q", got, ErrorKindEndpointIDNotConfigured)
	}
}
