package upstream

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 4.3-UNIT-004 (P0) — Architect Round 1 OQ-4.3-3 ruling (cascade-default
// from Story-4.2 OQ-4.2-5): the Kimi client uses stdlib
// http.Transport{ForceAttemptHTTP2: true} so ALPN can negotiate HTTP/2
// when the upstream supports it AND fall back to HTTP/1.1 when only
// http/1.1 is offered. Dev MUST record empirical
// `curl --http2 -v https://api.moonshot.cn/v1/chat/completions` results
// in docs/dev/logs/4.3-dev-log.md per T6.6.
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

// 4.3-UNIT-classifyhttpstatus-429 — Story-4.2 OQ-4.2-4 cascade anchor.
func TestClassifyHTTPStatus_429_IsRateLimitThrottle(t *testing.T) {
	if got := ClassifyHTTPStatus(429); got != ErrorKindRateLimitThrottle {
		t.Fatalf("ClassifyHTTPStatus(429) = %q, want %q", got, ErrorKindRateLimitThrottle)
	}
}

// 4.3-UNIT-classifyhttpstatus-other-mappings.
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

// 4.3-UNIT-007 (P0) — Architect Round 1 m-1 ruling: ClassifyMoonshotErrorBody
// returns ErrorKindContextLengthExceeded for Moonshot 400 responses whose
// JSON body matches `{"error":{"type":"invalid_request_error",
// "message":"...maximum context length..."}}`. Falls through to
// ClassifyHTTPStatus for other 400 bodies and non-400 statuses.
func TestClassifyMoonshotErrorBody_ContextLength_400_Detected(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"Your request exceeded the maximum context length of 8192 tokens for moonshot-v1-8k."}}`)
	if got := ClassifyMoonshotErrorBody(400, body); got != ErrorKindContextLengthExceeded {
		t.Fatalf("ClassifyMoonshotErrorBody(400, ctx-length-body) = %q, want %q", got, ErrorKindContextLengthExceeded)
	}
}

func TestClassifyMoonshotErrorBody_ContextLength_Underscore_Variant(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"context_length exceeded"}}`)
	if got := ClassifyMoonshotErrorBody(400, body); got != ErrorKindContextLengthExceeded {
		t.Fatalf("ClassifyMoonshotErrorBody underscore variant = %q, want %q", got, ErrorKindContextLengthExceeded)
	}
}

func TestClassifyMoonshotErrorBody_Generic400_FallsThrough(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"unsupported response_format"}}`)
	if got := ClassifyMoonshotErrorBody(400, body); got != ErrorKindUpstream4xx {
		t.Fatalf("ClassifyMoonshotErrorBody(generic 400) = %q, want %q", got, ErrorKindUpstream4xx)
	}
}

func TestClassifyMoonshotErrorBody_NonInvalidRequestError_FallsThrough(t *testing.T) {
	// Body has a context-length substring but the error.type is wrong;
	// MUST NOT promote to context_length_exceeded.
	body := []byte(`{"error":{"type":"server_error","message":"maximum context length boom"}}`)
	if got := ClassifyMoonshotErrorBody(400, body); got != ErrorKindUpstream4xx {
		t.Fatalf("ClassifyMoonshotErrorBody(server_error type) = %q, want %q", got, ErrorKindUpstream4xx)
	}
}

func TestClassifyMoonshotErrorBody_Non400_FallsThrough(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"maximum context length"}}`)
	if got := ClassifyMoonshotErrorBody(429, body); got != ErrorKindRateLimitThrottle {
		t.Fatalf("ClassifyMoonshotErrorBody(429) = %q, want %q (status-only fallthrough)", got, ErrorKindRateLimitThrottle)
	}
	if got := ClassifyMoonshotErrorBody(503, body); got != ErrorKindUpstream5xx {
		t.Fatalf("ClassifyMoonshotErrorBody(503) = %q, want %q", got, ErrorKindUpstream5xx)
	}
}

func TestClassifyMoonshotErrorBody_EmptyBody_FallsThrough(t *testing.T) {
	if got := ClassifyMoonshotErrorBody(400, nil); got != ErrorKindUpstream4xx {
		t.Fatalf("ClassifyMoonshotErrorBody(400, nil) = %q, want %q", got, ErrorKindUpstream4xx)
	}
}
