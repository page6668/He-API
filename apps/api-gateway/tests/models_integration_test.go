// Story 4.7 — Backend integration tests.
//
// Exercises the real middleware chain (RequestID → SecurityHeaders → CSRF
// → PublicCORS → mux) with both endpoints wired:
//
//   - bearer-gated /v1/models (with a stub bearer-auth shim that mirrors
//     production semantics — no auth-svc / Redis dependency stack)
//   - unauthenticated /public/models (mounted OUTSIDE the stub bearer-auth
//     chain — Story 4.7 BR-1.7 mounting discipline)
//
// Scenarios:
//
//	4.7-INT-001 — both endpoints emit byte-identical bodies
//	4.7-INT-002 — /public/models returns 200 without a bearer
//	4.7-INT-003 — /v1/models BR-1.6 defence-in-depth regression check
//	4.7-INT-007 — CORS anti-credential guard (Architect m-1, MANDATORY)
//	4.7-BLIND-BOUNDARY-002 — OPTIONS preflight CORS no-credentials
//	4.7-BLIND-BOUNDARY-003 — query string ignored on /public/models
//
// The integration suite runs WITHOUT touching auth-svc / Redis — a
// minimal in-process bearer-auth wrap mirrors Story-3.5 INT-003 pattern.
package story_4_1_skeleton_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/cors"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
)

// stubBearerAuth simulates middleware.APIKeyAuthenticator.RequireAPIKey
// without the auth-svc / Redis dependency stack. Accepts any non-empty
// `Authorization: Bearer …` and injects a canonical APIKeyID into the
// context; rejects empty headers with the §5.1.2 401 envelope.
func stubBearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || len(auth) <= len("Bearer ") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"401_invalid_api_key","message":"Missing or invalid Authorization header.","type":"invalid_request_error","param":null,"he_request_id":"req_000000000000"}}`))
			return
		}
		ctx := middleware.WithAPIKeyID(r.Context(), "11111111-1111-1111-1111-111111111111")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// buildStory47Server wires both /v1/models and /public/models behind the
// Story-3.6 / 4.7 middleware chain.
func buildStory47Server(t *testing.T) (string, func()) {
	t.Helper()
	bearer := handlers.NewModelsHandler(nil)
	snapshot := handlers.BuildPublicModelsSnapshot(bearer.StartedAt())
	publicH := handlers.NewPublicModelsHandler(nil, snapshot)

	mux := http.NewServeMux()
	mux.Handle("GET /v1/models", stubBearerAuth(bearer))
	mux.Handle("/public/models", publicH)

	handler := requestid.RequestID(middleware.SecurityHeaders(middleware.CSRF(middleware.CSRFConfig{
		AllowedOrigins: []string{"http://localhost:3000"},
	}, cors.PublicCORS(mux))))

	srv := httptest.NewServer(handler)
	return srv.URL, srv.Close
}

// Scenario: 4.7-INT-001
// Priority: P0
//
// /v1/models (with bearer) and /public/models (no bearer) emit byte-
// identical bodies for the catalogue payload. The single source of truth
// is the BuildPublicModelsSnapshot output shared via constructor injection
// (OQ-4.7-5).
func Test_4_7_INT_001_v1_and_public_byte_identical(t *testing.T) {
	t.Parallel()
	url, stop := buildStory47Server(t)
	defer stop()

	bearerReq, _ := http.NewRequest(http.MethodGet, url+"/v1/models", nil)
	bearerReq.Header.Set("Authorization", "Bearer any-test-key")
	bearerResp, err := http.DefaultClient.Do(bearerReq)
	if err != nil {
		t.Fatalf("bearer GET: %v", err)
	}
	defer bearerResp.Body.Close()
	bearerBody, _ := io.ReadAll(bearerResp.Body)
	if bearerResp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/models status = %d, body=%s", bearerResp.StatusCode, bearerBody)
	}

	publicResp, err := http.DefaultClient.Get(url + "/public/models")
	if err != nil {
		t.Fatalf("public GET: %v", err)
	}
	defer publicResp.Body.Close()
	publicBody, _ := io.ReadAll(publicResp.Body)
	if publicResp.StatusCode != http.StatusOK {
		t.Fatalf("/public/models status = %d, body=%s", publicResp.StatusCode, publicBody)
	}

	if !bytes.Equal(bearerBody, publicBody) {
		t.Fatalf("INT-001 byte-identity FAILED\n  /v1/models:    %s\n  /public/models:%s",
			bearerBody, publicBody)
	}

	// Sanity — assert the body actually carries 11 entries each with a
	// capabilities sub-struct, not an empty success body.
	var resp handlers.ModelsResponse
	if err := json.Unmarshal(bearerBody, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 11 {
		t.Errorf("len(data) = %d, want 11", len(resp.Data))
	}
	zero := handlers.ModelCapabilities{}
	for _, e := range resp.Data {
		if e.Capabilities == zero {
			t.Errorf("id=%s carries zero-value capabilities (BR-1.1 violated)", e.ID)
		}
	}
}

// Scenario: 4.7-INT-002
// Priority: P0
//
// /public/models returns 200 WITHOUT any Authorization header. The route
// is mounted OUTSIDE the stub bearer-auth wrap, so a missing bearer MUST
// NOT trigger the 401 path that /v1/models exhibits.
func Test_4_7_INT_002_public_models_no_bearer_200(t *testing.T) {
	t.Parallel()
	url, stop := buildStory47Server(t)
	defer stop()

	resp, err := http.DefaultClient.Get(url + "/public/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s (route must be OUTSIDE bearer chain)", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed handlers.ModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, body)
	}
	if len(parsed.Data) != 11 {
		t.Errorf("len(data) = %d, want 11", len(parsed.Data))
	}
}

// Scenario: 4.7-INT-003
// Priority: P0
//
// Story-3.5 BR-1.7 defence-in-depth regression check after the Capabilities
// extension. A request with a bearer header bypasses the auth-stub but
// arrives without `APIKeyID` in context (simulated by a deliberately
// broken wrap) — the inner handler MUST still emit the 500_gateway_misconfigured
// envelope, proving the defence-in-depth check was preserved.
func Test_4_7_INT_003_v1_defence_in_depth_500_when_apikeyid_missing(t *testing.T) {
	t.Parallel()
	bearer := handlers.NewModelsHandler(nil)

	// Faulty wrap — passes the request through without injecting APIKeyID.
	faulty := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// NOTE: deliberately NOT calling middleware.WithAPIKeyID here.
		bearer.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	mux.Handle("GET /v1/models", faulty)
	handler := requestid.RequestID(mux)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.DefaultClient.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (BR-1.7 defence-in-depth)\nbody=%s", resp.StatusCode, body)
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, body)
	}
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "500_gateway_misconfigured" {
		t.Errorf("error.code = %q, want \"500_gateway_misconfigured\"", got)
	}
}

// Scenario: 4.7-INT-007
// Priority: P0  ·  MANDATORY per Architect m-1
//
// /public/models CORS anti-credential guard. The wildcard Origin policy
// MUST NOT pair with the credentials header on ANY method/response — the
// combination is a standard CORS attack vector.
func Test_4_7_INT_007_public_models_anti_credential_CORS(t *testing.T) {
	t.Parallel()
	url, stop := buildStory47Server(t)
	defer stop()

	cases := []struct {
		name   string
		method string
	}{
		{name: "OPTIONS preflight", method: http.MethodOptions},
		{name: "GET response", method: http.MethodGet},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, url+"/public/models", nil)
			req.Header.Set("Origin", "https://thirdparty.example")
			if c.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "GET")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", c.method, err)
			}
			defer resp.Body.Close()

			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("Access-Control-Allow-Origin = %q, want \"*\"", got)
			}
			// MANDATORY anti-credential guard — header MUST be absent OR explicitly false.
			cred := resp.Header.Get("Access-Control-Allow-Credentials")
			if cred != "" && cred != "false" {
				t.Errorf("Access-Control-Allow-Credentials = %q; MUST be absent or \"false\" (m-1)", cred)
			}
		})
	}

	// And — /v1/models retains its tight allow-list (no wildcard echo).
	req, _ := http.NewRequest(http.MethodGet, url+"/v1/models", nil)
	req.Header.Set("Origin", "https://thirdparty.example")
	req.Header.Set("Authorization", "Bearer any-test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("/v1/models GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got == "*" {
		t.Errorf("/v1/models Access-Control-Allow-Origin = %q; bearer-protected routes MUST NOT widen (OQ-4.7-7 hard constraint #3)", got)
	}
}

// Scenario: 4.7-BLIND-BOUNDARY-002
// Priority: P0
//
// OPTIONS preflight from a third-party Origin returns 204 with the CORS
// headers AND NO credentials header. Complementary to INT-007.
func Test_4_7_BLIND_BOUNDARY_002_options_preflight_no_credentials(t *testing.T) {
	t.Parallel()
	url, stop := buildStory47Server(t)
	defer stop()

	req, _ := http.NewRequest(http.MethodOptions, url+"/public/models", nil)
	req.Header.Set("Origin", "https://thirdparty.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Errorf("OPTIONS status = %d, want 204 or 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	methods := resp.Header.Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, "GET") {
		t.Errorf("Access-Control-Allow-Methods = %q, want to include GET", methods)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" && got != "false" {
		t.Errorf("Access-Control-Allow-Credentials = %q; MUST NOT be true", got)
	}
}

// Scenario: 4.7-BLIND-BOUNDARY-003
// Priority: P2
//
// /public/models?foo=bar&baz=qux returns the same body as the bare
// endpoint (query string ignored on a parameterless endpoint).
func Test_4_7_BLIND_BOUNDARY_003_public_models_ignores_query_string(t *testing.T) {
	t.Parallel()
	url, stop := buildStory47Server(t)
	defer stop()

	bareResp, err := http.DefaultClient.Get(url + "/public/models")
	if err != nil {
		t.Fatalf("bare GET: %v", err)
	}
	defer bareResp.Body.Close()
	bareBody, _ := io.ReadAll(bareResp.Body)

	qResp, err := http.DefaultClient.Get(url + "/public/models?foo=bar&baz=qux")
	if err != nil {
		t.Fatalf("query GET: %v", err)
	}
	defer qResp.Body.Close()
	qBody, _ := io.ReadAll(qResp.Body)

	if bareResp.StatusCode != http.StatusOK || qResp.StatusCode != http.StatusOK {
		t.Fatalf("statuses bare=%d query=%d", bareResp.StatusCode, qResp.StatusCode)
	}
	if !bytes.Equal(bareBody, qBody) {
		t.Errorf("query-string body differs from bare body\n  bare:  %s\n  query: %s",
			bareBody, qBody)
	}
}

// Misc: sanity that the integration server boots and the byte-identity
// claim holds under modest concurrent load. Mirrors BLIND-CONCURRENCY-002
// but exercises through the HTTP server boundary (not the bare handler).
func Test_4_7_integration_concurrent_byte_identity(t *testing.T) {
	t.Parallel()
	url, stop := buildStory47Server(t)
	defer stop()

	const N = 20
	bodies := make([][]byte, N)
	var wg sync.WaitGroup
	wg.Add(N)
	client := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/public/models", nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("g%d: %v", idx, err)
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			bodies[idx] = b
		}(i)
	}
	wg.Wait()
	for i := 1; i < N; i++ {
		if bodies[i] != nil && !bytes.Equal(bodies[0], bodies[i]) {
			t.Errorf("body[%d] diverges from body[0]", i)
		}
	}
}
