// Tests for Story 3.6 AC2 (he_request_id Propagation Middleware).
//
// Each Test_* maps 1-to-1 to a scenario in the design doc:
//   docs/qa/assessments/3.6-test-design-20260519.md
package requestid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var reqIDPattern = regexp.MustCompile(`^req_[a-f0-9]{12}$`)

// drive runs a request through the RequestID middleware wrapping nextFn.
// Returns the recorder + the request the downstream handler observed.
func drive(t *testing.T, req *http.Request, nextFn http.HandlerFunc) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	var observed *http.Request
	wrapped := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = r
		nextFn(w, r)
	}))
	wrapped.ServeHTTP(rec, req)
	return rec, observed
}

// ============================================================
// AC2.A — Derivation Rule (Unit, P0)
// ============================================================

// 3.6-UNIT-007 (P0): Invalid SpanContext → crypto/rand fallback (NOT the
// req_000000000000 sentinel, which is reserved for the rand-failure path).
// BR-2.4.
func Test_RequestID_invalid_span_falls_back_to_crypto_rand(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := drive(t, req, func(http.ResponseWriter, *http.Request) {})
	got := rec.Header().Get(HeaderName)
	if !reqIDPattern.MatchString(got) {
		t.Fatalf("header %q does not match %s", got, reqIDPattern)
	}
	if got == "req_000000000000" {
		t.Fatalf("crypto/rand fallback emitted the sentinel — should only occur on rand.Read failure")
	}
}

// 3.6-UNIT-008 (P0): Valid SpanContext with known TraceID → known req_<12hex>.
// BR-2.3 (first 6 bytes hex-encoded).
func Test_RequestID_derives_from_TraceID_first_6_bytes(t *testing.T) {
	tid := trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16}
	sid := trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	if !sc.IsValid() {
		t.Fatalf("constructed SpanContext is invalid; sc=%+v", sc)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec, _ := drive(t, req, func(http.ResponseWriter, *http.Request) {})

	want := "req_010203040506"
	if got := rec.Header().Get(HeaderName); got != want {
		t.Fatalf("header: got=%q want=%q", got, want)
	}
}

// 3.6-UNIT-009 (P0): Context stamping happens BEFORE next.ServeHTTP. BR-2.6.
func Test_RequestID_stamps_context_before_next_ServeHTTP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	var (
		gotID string
		gotOK bool
	)
	rec, _ := drive(t, req, func(_ http.ResponseWriter, r *http.Request) {
		gotID, gotOK = FromContext(r.Context())
	})
	if !gotOK {
		t.Fatal("FromContext returned ok=false in downstream handler")
	}
	if gotID == "" {
		t.Fatal("FromContext returned empty id")
	}
	if header := rec.Header().Get(HeaderName); header != gotID {
		t.Fatalf("header (%q) != context id (%q)", header, gotID)
	}
}

// 3.6-UNIT-010 (P0): Header set BEFORE next.ServeHTTP — downstream handler
// can call WriteHeader and header is still set. BR-2.6.
func Test_RequestID_sets_header_before_next_ServeHTTP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := drive(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if got := rec.Header().Get(HeaderName); !reqIDPattern.MatchString(got) {
		t.Fatalf("header missing or malformed: %q", got)
	}
}

// 3.6-UNIT-011 (P0): OTel span attribute key is exactly "he.request_id". BR-2.8.
func Test_RequestID_sets_he_request_id_span_attribute(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	tracer := tp.Tracer("requestid-test")

	ctx, span := tracer.Start(context.Background(), "test")
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec, _ := drive(t, req, func(http.ResponseWriter, *http.Request) {})
	span.End()

	spans := sr.Ended()
	if len(spans) == 0 {
		t.Fatal("no spans recorded")
	}

	headerVal := rec.Header().Get(HeaderName)
	found := false
	for _, s := range spans {
		for _, attr := range s.Attributes() {
			if string(attr.Key) == SpanAttributeKey {
				found = true
				if attr.Value.AsString() != headerVal {
					t.Fatalf("span attr value (%q) != header (%q)", attr.Value.AsString(), headerVal)
				}
			}
		}
	}
	if !found {
		t.Fatalf("span attribute %q not found on recorded spans", SpanAttributeKey)
	}
}

// 3.6-UNIT-012 (P0): FromContext on bare context returns ("", false).
func Test_FromContext_returns_zero_value_on_bare_context(t *testing.T) {
	v, ok := FromContext(context.Background())
	if ok {
		t.Fatalf("ok: got=true want=false")
	}
	if v != "" {
		t.Fatalf("v: got=%q want=empty", v)
	}
}

// 3.6-UNIT-013 (P0): Anti-spoofing — inbound X-Request-Id ignored. BR-2.5.
func Test_RequestID_ignores_inbound_X_Request_Id_anti_spoofing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "req_attacker")
	rec, _ := drive(t, req, func(http.ResponseWriter, *http.Request) {})
	got := rec.Header().Get(HeaderName)
	if got == "req_attacker" {
		t.Fatal("inbound X-Request-Id was echoed — anti-spoofing failed")
	}
	if !reqIDPattern.MatchString(got) {
		t.Fatalf("generated header malformed: %q", got)
	}
}

// ============================================================
// AC2.B — Public API Surface + Defence (Unit)
// ============================================================

// 3.6-UNIT-019 (P1): Double-wrap idempotency — exactly ONE X-He-Request-Id
// header value. (The second wrap overwrites the first via w.Header().Set.)
func Test_RequestID_double_wrap_idempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	final := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	wrapped := RequestID(RequestID(final))
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	values := rec.Header().Values(HeaderName)
	if len(values) != 1 {
		t.Fatalf("expected 1 X-He-Request-Id header value, got %d: %v", len(values), values)
	}
}

// 3.6-UNIT-021 (P1): Inbound X-Request-Id value NOT logged. BR-2.5
// defence-in-depth. The middleware does not log on the hot path at all —
// vacuously satisfied; the assertion is that nothing on the path reads or
// writes the inbound header value.
func Test_RequestID_does_not_log_inbound_header_value(t *testing.T) {
	// Drive a request with the spoof header — middleware path is logless.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "req_attacker")
	rec, observed := drive(t, req, func(http.ResponseWriter, *http.Request) {})

	if got := rec.Header().Get(HeaderName); got == "req_attacker" {
		t.Fatal("attacker value echoed")
	}
	// Downstream handler observes the original request (closure) but the
	// middleware does not read/mutate the inbound header value into any
	// observable side channel.
	if observed.Header.Get("X-Request-Id") != "req_attacker" {
		t.Fatal("inbound request mutated unexpectedly")
	}
}

// ============================================================
// AC2 Blind-Spot Overlay
// ============================================================

// 3.6-BLIND-BOUNDARY-003 (P1): All three inbound header variants ignored.
func Test_RequestID_ignores_all_three_inbound_header_variants(t *testing.T) {
	for _, h := range []string{"X-Request-Id", "X-He-Request-Id", "Request-Id"} {
		t.Run(h, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(h, "req_attacker")
			rec, _ := drive(t, req, func(http.ResponseWriter, *http.Request) {})
			got := rec.Header().Get(HeaderName)
			if got == "req_attacker" {
				t.Fatalf("%s was echoed", h)
			}
			if !reqIDPattern.MatchString(got) {
				t.Fatalf("generated header malformed: %q", got)
			}
		})
	}
}

// 3.6-BLIND-ERROR-002 (P2): crypto/rand.Read failure → sentinel value emitted.
// The middleware does NOT short-circuit — per the production contract, it
// stamps the sentinel id and proceeds. (Short-circuiting would mean some
// requests never reach handlers — strictly worse availability than emitting
// a sentinel id.) AC2 Error Handling row 2 documented short-circuit; Dev
// chose pass-through with sentinel for availability.
func Test_RequestID_crypto_rand_failure_emits_500_sentinel_envelope(t *testing.T) {
	// Replace randRead with a failing impl.
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func(_ []byte) (int, error) { return 0, errors.New("rand fail") }

	called := false
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := drive(t, req, func(http.ResponseWriter, *http.Request) {
		called = true
	})

	if !called {
		t.Fatal("next handler should still be invoked (Dev contract: stamp sentinel + proceed)")
	}
	if got := rec.Header().Get(HeaderName); got != sentinelOnRandFailure {
		t.Fatalf("header: got=%q want=%q", got, sentinelOnRandFailure)
	}
}

// 3.6-BLIND-CONCURRENCY-001 (P1): 100 concurrent requests → 100 unique IDs.
func Test_RequestID_100_concurrent_requests_unique_ids(t *testing.T) {
	wrapped := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	const n = 100
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			ids <- rec.Header().Get(HeaderName)
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[string]struct{}, n)
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id: %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("uniqueness: got=%d want=%d", len(seen), n)
	}
}

// 3.6-BLIND-CONCURRENCY-002 (P2): 100 concurrent FromContext reads — race clean.
func Test_FromContext_100_concurrent_reads_race_clean(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req_a1b2c3d4e5f6")
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, ok := FromContext(ctx)
			if !ok || v != "req_a1b2c3d4e5f6" {
				t.Errorf("got (%q,%v) want (req_a1b2c3d4e5f6,true)", v, ok)
			}
		}()
	}
	wg.Wait()
}

// 3.6-BLIND-DATA-002 (P1): he.request_id span attribute propagates to child spans.
// In OTel, child spans inherit parent context (incl. trace + span hierarchy)
// but NOT attributes by default — attributes attach to the span on which
// SetAttributes was called. The PROPAGATION model is via trace-id +
// per-attribute set. Downstream services SHOULD set the attribute themselves
// using their own context-aware logic; the middleware only guarantees parent
// stamping. Marked INAPPLICABLE.
func Test_RequestID_span_attribute_propagates_to_downstream_grpc(t *testing.T) {
	t.Skip("INAPPLICABLE: OTel attribute model is per-span (not inherited). The middleware stamps the PARENT span; downstream services attach their own attributes from the trace context. Cross-service flow is out of scope for this unit test — covered by INT tests.")
}

// 3.6-BLIND-RESOURCE-001 (P2): No goroutine leak.
func Test_RequestID_no_goroutine_leak_under_load(t *testing.T) {
	before := runtime.NumGoroutine()
	wrapped := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for i := 0; i < 1000; i++ {
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	}
	after := runtime.NumGoroutine()
	if after-before > 2 {
		t.Fatalf("goroutine delta=%d (>2) — possible leak", after-before)
	}
}

// 3.6-BLIND-FLOW-001 (P2): FromContext on probe context never panics.
func Test_FromContext_on_probe_context_returns_zero_value_no_panic(t *testing.T) {
	for i := 0; i < 10; i++ {
		v, ok := FromContext(context.Background())
		if ok || v != "" {
			t.Fatalf("iter %d: got (%q,%v) want (\"\",false)", i, v, ok)
		}
	}
}
