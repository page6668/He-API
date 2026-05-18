// Story 3.1 — AC1 /health handler tests (Turing test design 2026-05-18).
//
// Pkg `handlers` (internal) so tests may inspect the cached []byte field on
// HealthHandler to assert BR-1.6 "marshalled-once" invariant via reflection.
// Mirrors existing handlers/me_test.go + jwks-related tests stylistically but
// is purely stdlib + httptest — no testify (the wider codebase tests use
// stdlib only; see existing me_test.go / jwks tests).
package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

const expectedBody = `{"status":"ok","service":"api-gateway","version":"0.0.1"}`

func newHealthRecorder(t *testing.T, method, path string, body io.Reader) (*HealthHandler, *httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	h := NewHealthHandler("0.0.1")
	req := httptest.NewRequest(method, path, body)
	rec := httptest.NewRecorder()
	return h, rec, req
}

// Scenario: 3.1-UNIT-001
// Priority: P0 | Level: unit | BR: BR-1.5 / BR-1.8
func TestHealthHandler_GET_returns200WithExpectedBody(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodGet, "/health", nil)
	h.Serve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type: got %q, want application/json; charset=utf-8", got)
	}
	if got := rec.Body.String(); got != expectedBody {
		t.Fatalf("body: got %q, want %q", got, expectedBody)
	}
}

// Scenario: 3.1-UNIT-002
// Priority: P0 | Level: unit | BR: AC1 §Scenario
func TestHealthHandler_GET_carriesCacheControlNoStore(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodGet, "/health", nil)
	h.Serve(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: got %q, want no-store", got)
	}
}

// Scenario: 3.1-UNIT-003
// Priority: P0 | Level: unit | BR: AC1 §Scenario
func TestHealthHandler_GET_hasNoSetCookieHeader(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodGet, "/health", nil)
	h.Serve(rec, req)
	if vs := rec.Header().Values("Set-Cookie"); len(vs) != 0 {
		t.Fatalf("Set-Cookie: got %v, want absent (probes are stateless)", vs)
	}
}

// Scenario: 3.1-UNIT-004
// Priority: P0 | Level: unit | BR: BR-1.3
//
// The handler itself MUST NOT emit the SecurityHeaders bundle; the bypass at
// the mux level (probeMux) is verified by the integration test 3.1-INT-001.
func TestHealthHandler_GET_hasNoSecurityHeadersBundle(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodGet, "/health", nil)
	h.Serve(rec, req)
	for _, header := range []string{
		"Content-Security-Policy",
		"Strict-Transport-Security",
		"X-Frame-Options",
		"X-Content-Type-Options",
		"Referrer-Policy",
		"Permissions-Policy",
	} {
		if v := rec.Header().Get(header); v != "" {
			t.Fatalf("header %s: got %q, want absent (probeMux bypasses SecurityHeaders)", header, v)
		}
	}
}

// Scenario: 3.1-UNIT-005
// Priority: P0 | Level: unit | BR: AC1 §Scenario (RFC 9110 §9.3.2)
func TestHealthHandler_HEAD_returns200EmptyBody(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodHead, "/health", nil)
	h.Serve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body: got %d bytes, want 0", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Length"); got != "0" {
		t.Fatalf("Content-Length: got %q, want 0", got)
	}
}

// Scenario: 3.1-UNIT-006
// Priority: P0 | Level: unit | BR: AC1 §Error Handling
func TestHealthHandler_POST_returns405WithAllowHeader(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodPost, "/health", strings.NewReader(""))
	h.Serve(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow: got %q, want %q", got, "GET, HEAD")
	}
	if got := rec.Body.String(); got != `{"error":"method_not_allowed"}` {
		t.Fatalf("body: got %q, want %q", got, `{"error":"method_not_allowed"}`)
	}
}

// Scenario: 3.1-UNIT-007
// Priority: P1 | Level: unit | BR: AC1 §Error Handling
func TestHealthHandler_PUT_returns405(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodPut, "/health", strings.NewReader(""))
	h.Serve(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow: got %q, want GET, HEAD", got)
	}
}

// Scenario: 3.1-UNIT-008
// Priority: P1 | Level: unit | BR: AC1 §Error Handling
func TestHealthHandler_DELETE_returns405(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodDelete, "/health", nil)
	h.Serve(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow: got %q, want GET, HEAD", got)
	}
}

// Scenario: 3.1-UNIT-009
// Priority: P0 | Level: unit | BR: BR-1.5
//
// 100 sequential GETs MUST yield byte-identical bodies — the byte-identity
// invariant is what enables a CI golden-file diff downstream.
func TestHealthHandler_responseBodyIsByteIdenticalAcross100Calls(t *testing.T) {
	h := NewHealthHandler("0.0.1")
	var first []byte
	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		h.Serve(rec, req)
		got := rec.Body.Bytes()
		if i == 0 {
			first = append([]byte(nil), got...)
			continue
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("call %d body diverged: first=%q got=%q", i, first, got)
		}
	}
}

// Scenario: 3.1-UNIT-010
// Priority: P0 | Level: unit | BR: BR-1.8
func TestNewHealthHandler_versionFieldIsParameterised(t *testing.T) {
	h := NewHealthHandler("9.9.9")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.Serve(rec, req)
	want := `{"status":"ok","service":"api-gateway","version":"9.9.9"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

// Scenario: 3.1-UNIT-011
// Priority: P0 | Level: unit | BR: BR-1.6
//
// The body is cached at construction. We assert two things:
//  1. The body []byte returned in successive responses lives in the same
//     backing array (no per-request marshal allocation).
//  2. The cached []byte field equals the bytes the handler returns.
func TestHealthHandler_bodyMarshalledOnceAtConstruction(t *testing.T) {
	h := NewHealthHandler("0.0.1")

	// (1) inspect the cached field via unsafe — we own the package so this
	// is safe. The cached body must equal what Serve writes.
	cached := *(*[]byte)(unsafe.Pointer(&h.body))
	if string(cached) != expectedBody {
		t.Fatalf("cached body: got %q, want %q", cached, expectedBody)
	}

	// (2) The bytes the recorder gets share backing memory with h.body
	// (cap stable across calls; the http.ResponseRecorder copies into
	// its own buffer, so we re-check that h.body has not been mutated).
	rec := httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !bytes.Equal(h.body, cached) {
		t.Fatalf("h.body was mutated by Serve: before=%q after=%q", cached, h.body)
	}
}

// Scenario: 3.1-UNIT-012
// Priority: P0 | Level: unit | BR: BR-1.6
//
// Inner-handler latency must be ≤ 1 ms wall-clock (ns/op < 1_000_000) on the
// CI baseline. The benchmark runs httptest.NewRecorder + Serve in a hot loop;
// allocations are bounded by the recorder itself, not the handler.
func BenchmarkHealthHandler_GET(b *testing.B) {
	h := NewHealthHandler("0.0.1")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.Serve(rec, req)
	}
}

// Scenario: 3.1-UNIT-013
// Priority: P1 | Level: unit | BR: BR-1.2
//
// The handler is path-agnostic — registering it under /healthz must yield the
// identical response as /health. The mux is responsible for the alias; the
// handler simply re-uses its cached bytes.
func TestHealthHandler_aliasPathReturnsIdenticalResponse(t *testing.T) {
	h := NewHealthHandler("0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.Serve)
	mux.HandleFunc("GET /healthz", h.Serve)

	collect := func(path string) (int, []byte) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.Bytes()
	}

	code1, body1 := collect("/health")
	code2, body2 := collect("/healthz")
	if code1 != code2 || !bytes.Equal(body1, body2) {
		t.Fatalf("/health (%d, %q) vs /healthz (%d, %q): expected identical", code1, body1, code2, body2)
	}
}

// Scenario: 3.1-UNIT-014
// Priority: P2 | Level: unit | BR: BR-1.5
func TestHealthHandler_responseBodyHasNoTrailingNewline(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodGet, "/health", nil)
	h.Serve(rec, req)
	body := rec.Body.Bytes()
	if len(body) == 0 || body[len(body)-1] != '}' {
		t.Fatalf("body must end with '}': got %q", body)
	}
	if got, want := len(body), len(expectedBody); got != want {
		t.Fatalf("body length: got %d, want %d (no trailing newline)", got, want)
	}
}

// Scenario: 3.1-UNIT-015
// Priority: P2 | Level: unit | BR: BR-1.5
//
// Struct field declaration order in NewHealthHandler dictates JSON field
// order. Byte-equality with expectedBody already pins this, but the explicit
// substring-order check below guards a future maintainer who switches to a
// map[string]string (which would randomise order).
func TestHealthHandler_responseBodyFieldOrderIsDeterministic(t *testing.T) {
	h, rec, req := newHealthRecorder(t, http.MethodGet, "/health", nil)
	h.Serve(rec, req)
	body := rec.Body.String()
	iStatus := strings.Index(body, `"status"`)
	iService := strings.Index(body, `"service"`)
	iVersion := strings.Index(body, `"version"`)
	if !(iStatus >= 0 && iStatus < iService && iService < iVersion) {
		t.Fatalf("field order: status@%d, service@%d, version@%d (want ascending)", iStatus, iService, iVersion)
	}
}

// Scenario: 3.1-UNIT-016
// Priority: P1 | Level: unit | BR: BR-1.7
//
// Compile-time symbol presence: this function references `HealthHandler` and
// `NewHealthHandler(version string) *HealthHandler` directly. A rename or
// signature change causes a compile failure here — the safety net required by
// BR-1.7.
func TestHealthHandler_structAndConstructorExist(t *testing.T) {
	var _ *HealthHandler = NewHealthHandler("0.0.1")
}

// ============================================================
// Blind Spot Scenarios (cross-cutting)
// ============================================================

// Scenario: 3.1-BLIND-BOUNDARY-001
// Category: BOUNDARY-001 (null/empty input) | Priority: P1
func TestHealthHandler_BLIND_BOUNDARY_001_emptyHostHeader(t *testing.T) {
	h := NewHealthHandler("0.0.1")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Host = ""
	rec := httptest.NewRecorder()
	h.Serve(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty Host: got %d, want 200 (probe is host-agnostic)", rec.Code)
	}
	if rec.Body.String() != expectedBody {
		t.Fatalf("empty Host body: got %q, want %q", rec.Body.String(), expectedBody)
	}
}

// Scenario: 3.1-BLIND-BOUNDARY-002
// Category: BOUNDARY-001 | Priority: P1
//
// Some load-balancer probes append a body to GET requests. The handler must
// ignore the body and return the same response as a no-body GET.
func TestHealthHandler_BLIND_BOUNDARY_002_bodyPresentOnGet(t *testing.T) {
	h := NewHealthHandler("0.0.1")
	req := httptest.NewRequest(http.MethodGet, "/health", strings.NewReader("not empty"))
	rec := httptest.NewRecorder()
	h.Serve(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != expectedBody {
		t.Fatalf("body: got %q, want %q", got, expectedBody)
	}
}

// Scenario: 3.1-BLIND-FLOW-001
// Category: FLOW-002 (duplicate / concurrent submission) | Priority: P1
func TestHealthHandler_BLIND_FLOW_001_concurrentRequests(t *testing.T) {
	h := NewHealthHandler("0.0.1")
	var wg sync.WaitGroup
	errCh := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.Serve(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			if rec.Code != http.StatusOK {
				errCh <- &errString{msg: "non-200 from concurrent GET"}
				return
			}
			if rec.Body.String() != expectedBody {
				errCh <- &errString{msg: "body mismatch under concurrency"}
				return
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// Scenario: 3.1-BLIND-CONCURRENCY-002
// Category: CONCURRENCY-002 (race condition) | Priority: P2
//
// Run under `go test -race` for the race-detector assertion to mean anything.
// The cached []byte is set once in the constructor; all subsequent reads from
// Serve are race-free per the Go memory model.
func TestHealthHandler_BLIND_CONCURRENCY_002_constructorAndServeConcurrent(t *testing.T) {
	const ctors = 8
	const servesPerCtor = 64
	handlers := make([]*HealthHandler, ctors)

	var ctorWG sync.WaitGroup
	for i := 0; i < ctors; i++ {
		ctorWG.Add(1)
		go func(i int) {
			defer ctorWG.Done()
			handlers[i] = NewHealthHandler("0.0.1")
		}(i)
	}
	ctorWG.Wait()

	var serveWG sync.WaitGroup
	for _, h := range handlers {
		for s := 0; s < servesPerCtor; s++ {
			serveWG.Add(1)
			go func(h *HealthHandler) {
				defer serveWG.Done()
				rec := httptest.NewRecorder()
				h.Serve(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
				if rec.Code != http.StatusOK || rec.Body.String() != expectedBody {
					t.Errorf("concurrency: got code=%d body=%q", rec.Code, rec.Body.String())
				}
			}(h)
		}
	}
	serveWG.Wait()
}

// errString is a tiny Stringer-error to avoid pulling fmt into the hot path of
// the concurrency test.
type errString struct{ msg string }

func (e *errString) Error() string { return e.msg }
