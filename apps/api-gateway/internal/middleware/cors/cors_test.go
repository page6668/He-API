// Story 4.7 — PublicCORS middleware tests.
//
// Covers OQ-4.7-7 hard constraints + Architect m-1 anti-credential guard.
package cors

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// sentinelHandler asserts whether ServeHTTP was reached.
type sentinelHandler struct{ called bool }

func (s *sentinelHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.called = true
	w.WriteHeader(http.StatusOK)
}

func Test_PublicCORS_OPTIONS_short_circuits_with_204_and_wildcard(t *testing.T) {
	t.Parallel()
	inner := &sentinelHandler{}
	h := PublicCORS(inner)
	req := httptest.NewRequest(http.MethodOptions, "/public/models", nil)
	req.Header.Set("Origin", "https://thirdparty.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, Accept")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if inner.called {
		t.Error("inner handler reached on OPTIONS preflight — must short-circuit")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want \"*\"", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "GET, OPTIONS" {
		t.Errorf("Access-Control-Allow-Methods = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Accept" {
		t.Errorf("Access-Control-Allow-Headers = %q (should echo request)", got)
	}
	// Architect m-1 anti-credential guard — MUST NOT be set.
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials MUST NOT be set on /public/* (m-1); got %q", got)
	}
}

func Test_PublicCORS_GET_passes_through_with_wildcard_and_no_credentials(t *testing.T) {
	t.Parallel()
	inner := &sentinelHandler{}
	h := PublicCORS(inner)
	req := httptest.NewRequest(http.MethodGet, "/public/models", nil)
	req.Header.Set("Origin", "https://thirdparty.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !inner.called {
		t.Error("inner handler NOT reached on GET — should pass through")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials MUST NOT be set; got %q", got)
	}
}

func Test_PublicCORS_non_public_paths_pass_through_unchanged(t *testing.T) {
	t.Parallel()
	inner := &sentinelHandler{}
	h := PublicCORS(inner)

	// /v1/models — bearer-gated endpoint MUST NOT receive wildcard CORS.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !inner.called {
		t.Error("inner handler NOT reached on non-public path")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin MUST NOT be set on /v1/*; got %q (OQ-4.7-7 hard constraint #3)", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Access-Control-Allow-Methods MUST NOT be set on /v1/*; got %q", got)
	}
}

func Test_PublicCORS_handles_all_public_subpaths(t *testing.T) {
	t.Parallel()
	inner := &sentinelHandler{}
	h := PublicCORS(inner)

	for _, path := range []string{"/public/models", "/public/benchmark", "/public/compliance"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want \"*\"", path, got)
		}
	}
}
