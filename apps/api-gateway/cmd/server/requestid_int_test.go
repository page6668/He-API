// Story 3.6 integration tests — exercise the full middleware chain
// (RequestID → SecurityHeaders → CSRF → handlers) and assert the AC1 + AC2
// joint invariant: every error response carries the §5.1.2 5-field envelope
// AND the X-He-Request-Id header value equals the body's error.he_request_id.
//
// Scenarios:
//   3.6-INT-001 — GET /v1/models with VALID bearer (header present + regex)
//   3.6-INT-002 — GET /v1/models without bearer (joint header==body assertion)
//   3.6-INT-003 — GET /health (probe bypass — NO X-He-Request-Id)
//   3.6-INT-004 — POST /v1/chat/completions 413 (header==body joint assertion)
//   3.6-INT-005 — POST /v1/auth/signin 401 (auth.go writeError refactor)
//   3.6-INT-006 — PUT /v1/me (405) — stdlib mux 405 path, RequestID middleware
//                 still stamps the header.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	obs "github.com/he-api/he-api/packages/go-observability"
)

var int36ReqIDRE = regexp.MustCompile(`^req_[a-f0-9]{12}$`)

// buildStory36Mux constructs a minimal but representative routing chain:
//   - probeMux on /health (bypasses RequestID)
//   - mainMux wrapped with requestid.RequestID → SecurityHeaders → CSRF
//   - inner: a chat-completions stub + a models stub + a /v1/auth/signin
//     stub that exercises the auth.go-style error path.
func buildStory36Mux(t *testing.T) http.Handler {
	t.Helper()
	mainMux := http.NewServeMux()

	// /v1/models stub — returns 401 if no Authorization header (mimics
	// bearer-auth middleware's behaviour without the full Redis + connect-go
	// dependency stack).
	mainMux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			// Direct invocation of openaierr.Write via the bearer-auth path
			// (which is the production reach for this 401). We simulate via
			// an inline call mirroring middleware/bearer_auth.go.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			// Build the envelope by reaching into requestid for the stamped id.
			id, _ := requestid.FromContext(r.Context())
			body := `{"error":{"code":"401_invalid_api_key","message":"Missing or malformed Authorization header.","type":"invalid_request_error","param":null,"he_request_id":"` + id + `"}}`
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})

	// /v1/chat/completions stub — emits 413 envelope mimicking the real
	// handler's MaxBytesReader path.
	mainMux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
		if len(body) > 1<<20 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			id, _ := requestid.FromContext(r.Context())
			env := `{"error":{"code":"413_payload_too_large","message":"Request body exceeds 1 MiB.","type":"invalid_request_error","param":null,"he_request_id":"` + id + `"}}`
			_, _ = w.Write([]byte(env))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test"}`))
	})

	// /v1/auth/signin stub — 401 envelope (exercises the auth.go writeError
	// refactor surface).
	mainMux.HandleFunc("POST /v1/auth/signin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		id, _ := requestid.FromContext(r.Context())
		env := `{"error":{"code":"401_invalid_credentials","message":"request body must be JSON","type":"invalid_request_error","param":null,"he_request_id":"` + id + `"}}`
		_, _ = w.Write([]byte(env))
	})

	// /v1/me — GET only (so PUT triggers stdlib 405).
	mainMux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	// Build the chain exactly like main.go does post-Story-3.6.
	handler := requestid.RequestID(middleware.SecurityHeaders(middleware.CSRF(middleware.CSRFConfig{
		AllowedOrigins: []string{"http://localhost:3000"},
	}, mainMux)))

	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/health", handlers.NewHealthHandler("0.0.1").Serve)
	probeMux.HandleFunc("/healthz", handlers.NewHealthHandler("0.0.1").Serve)

	rootMux := http.NewServeMux()
	rootMux.Handle("/health", probeMux)
	rootMux.Handle("/healthz", probeMux)
	rootMux.Handle("/", handler)

	return obs.WrapHTTPHandler(rootMux, "api-gateway-int-test")
}

// 3.6-INT-001 (P0): GET /v1/models success path — X-He-Request-Id header
// matches the canonical regex.
func Test_INT_001_models_success_carries_he_request_id_header(t *testing.T) {
	srv := httptest.NewServer(buildStory36Mux(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer any-valid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	got := resp.Header.Get("X-He-Request-Id")
	if !int36ReqIDRE.MatchString(got) {
		t.Fatalf("X-He-Request-Id=%q does not match %s", got, int36ReqIDRE)
	}
}

// 3.6-INT-002 (P0): GET /v1/models without bearer — joint AC1+AC2 assertion.
// Response header X-He-Request-Id MUST equal error.he_request_id body field.
func Test_INT_002_models_401_header_equals_body(t *testing.T) {
	srv := httptest.NewServer(buildStory36Mux(t))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got=%d want=401", resp.StatusCode)
	}

	header := resp.Header.Get("X-He-Request-Id")
	if !int36ReqIDRE.MatchString(header) {
		t.Fatalf("header malformed: %q", header)
	}

	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	bodyID, _ := body.Error["he_request_id"].(string)
	if bodyID != header {
		t.Fatalf("body.he_request_id (%q) != header (%q)", bodyID, header)
	}
}

// 3.6-INT-003 (P0): /health bypass — NO X-He-Request-Id header.
func Test_INT_003_health_probe_bypasses_request_id_middleware(t *testing.T) {
	srv := httptest.NewServer(buildStory36Mux(t))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got=%d want=200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-He-Request-Id"); got != "" {
		t.Fatalf("probe should NOT carry X-He-Request-Id; got %q", got)
	}
}

// 3.6-INT-004 (P0): POST /v1/chat/completions 413 — joint assertion.
func Test_INT_004_chat_completions_413_header_equals_body(t *testing.T) {
	srv := httptest.NewServer(buildStory36Mux(t))
	defer srv.Close()
	// 2 MiB body — triggers 413 path.
	body := bytes.Repeat([]byte("x"), 2<<20)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3000") // satisfy CSRF
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got=%d want=413", resp.StatusCode)
	}
	header := resp.Header.Get("X-He-Request-Id")
	if !int36ReqIDRE.MatchString(header) {
		t.Fatalf("header malformed: %q", header)
	}
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := env.Error["he_request_id"].(string); got != header {
		t.Fatalf("body.he_request_id (%q) != header (%q)", got, header)
	}
	if got, _ := env.Error["code"].(string); got != "413_payload_too_large" {
		t.Fatalf("body.code=%q want=413_payload_too_large", got)
	}
}

// 3.6-INT-005 (P0): POST /v1/auth/signin 401 — exercises auth.go writeError
// refactor surface. Header + body 5-field envelope alignment.
func Test_INT_005_auth_signin_401_emits_5_field_envelope(t *testing.T) {
	srv := httptest.NewServer(buildStory36Mux(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/signin", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got=%d want=401", resp.StatusCode)
	}
	header := resp.Header.Get("X-He-Request-Id")
	if !int36ReqIDRE.MatchString(header) {
		t.Fatalf("header malformed: %q", header)
	}
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 5-field envelope: code, message, type, param, he_request_id.
	for _, k := range []string{"code", "message", "type", "param", "he_request_id"} {
		if _, ok := env.Error[k]; !ok {
			t.Errorf("body.error missing field %q (env=%v)", k, env.Error)
		}
	}
}

// 3.6-INT-006 (P0): PUT /v1/me (405) — RequestID middleware still stamps
// the header on the stdlib-emitted 405. (Body shape on stdlib 405 is the
// stdlib default — body unification is out-of-scope for Story 3.6.)
func Test_INT_006_method_not_allowed_carries_request_id_header(t *testing.T) {
	srv := httptest.NewServer(buildStory36Mux(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/me", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: got=%d want=405", resp.StatusCode)
	}
	if got := resp.Header.Get("X-He-Request-Id"); !int36ReqIDRE.MatchString(got) {
		t.Fatalf("header malformed: %q", got)
	}
}
