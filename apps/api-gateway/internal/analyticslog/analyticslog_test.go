// Story 9.1 AC1 — gateway request.logged producer unit tests (9.1-UNIT-001..005,
// 008). The producer is the FIRST half of the 成功率 contract: it must emit ONE
// event on EVERY terminal outcome (success AND every error class), fire-and-
// forget, PII-safe, cost_usd NON-NULL.
package analyticslog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
)

// recordingEmitter captures every emitted event synchronously.
type recordingEmitter struct {
	mu     sync.Mutex
	events []*analyticsv1.UsageLogEvent
}

func (r *recordingEmitter) Emit(_ context.Context, ev *analyticsv1.UsageLogEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingEmitter) only(t *testing.T) *analyticsv1.UsageLogEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 1 {
		t.Fatalf("want exactly 1 event, got %d", len(r.events))
	}
	return r.events[0]
}

// seedIdentity attaches the request-scoped identity the middleware reads.
func seedIdentity(req *http.Request) *http.Request {
	ctx := requestid.WithRequestID(req.Context(), "req_abcdef012345")
	ctx = middleware.BearerWithUserID(ctx, "11111111-1111-1111-1111-111111111111")
	ctx = middleware.WithAPIKeyID(ctx, "22222222-2222-2222-2222-222222222222")
	return req.WithContext(ctx)
}

func serve(emitter Emitter, handler http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	Middleware(emitter)(handler).ServeHTTP(rr, req)
	return rr
}

// 9.1-UNIT-001 — terminal success (200) emits ONE event with status_code=200,
// the served model, and the token triple from the Record.
func TestSuccessEmitsOneEvent(t *testing.T) {
	rec := &recordingEmitter{}
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-He-Selected-Model", "qwen-max")
		FromContext(r.Context()).Populate("qwen-max", 100, 200, 300, false)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	serve(rec, h, seedIdentity(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	ev := rec.only(t)
	if ev.GetStatusCode() != 200 || ev.GetModel() != "qwen-max" || ev.GetSelectedByStrategy() != "qwen-max" {
		t.Fatalf("status/model mismatch: %+v", ev)
	}
	if ev.GetPromptTokens() != 100 || ev.GetCompletionTokens() != 200 || ev.GetTotalTokens() != 300 {
		t.Fatalf("token triple mismatch: %+v", ev)
	}
	if ev.GetHeRequestId() != "req_abcdef012345" || ev.GetUserId() != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("identity mismatch: %+v", ev)
	}
	if ev.GetCostUsd() != "0" {
		t.Fatalf("cost_usd should default to \"0\", got %q", ev.GetCostUsd())
	}
}

// 9.1-UNIT-002 — each error class emits ONE event with the right status_code +
// error_code, tokens=0 (the 成功率 rows usage.recorded misses). [BR-ING-2]
func TestEachErrorClassEmitsOneEvent(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusPaymentRequired, "402_balance_insufficient"},
		{http.StatusForbidden, "403_scope_forbidden"},
		{http.StatusTooManyRequests, "429_rate_limit"},
		{http.StatusBadGateway, "502_upstream_unavailable"},
		{http.StatusGatewayTimeout, "504_upstream_timeout"},
		{http.StatusBadRequest, "400_content_filter"},
		{http.StatusInternalServerError, "500_internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			rec := &recordingEmitter{}
			h := func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"code":"` + tc.code + `","message":"x"}}`))
			}
			serve(rec, h, seedIdentity(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

			ev := rec.only(t)
			if int(ev.GetStatusCode()) != tc.status {
				t.Fatalf("status = %d, want %d", ev.GetStatusCode(), tc.status)
			}
			if ev.GetErrorCode() != tc.code {
				t.Fatalf("error_code = %q, want %q", ev.GetErrorCode(), tc.code)
			}
			if ev.GetTotalTokens() != 0 || ev.GetPromptTokens() != 0 {
				t.Fatalf("error row must carry tokens=0, got %+v", ev)
			}
			if ev.GetCostUsd() != "0" {
				t.Fatalf("error row cost_usd must be \"0\", got %q", ev.GetCostUsd())
			}
		})
	}
}

// 9.1-UNIT-005 — cost_usd NON-NULL DEFAULT 0 [BOUNDARY-001]: a pre-dispatch 402
// (no spend, no Record populate) still emits cost_usd="0", never null/empty.
func TestCostUsdNonNullDefault(t *testing.T) {
	rec := &recordingEmitter{}
	h := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"402_balance_insufficient"}}`))
	}
	serve(rec, h, seedIdentity(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))
	ev := rec.only(t)
	if ev.GetCostUsd() != "0" {
		t.Fatalf("cost_usd = %q, want \"0\" (NON-NULL default)", ev.GetCostUsd())
	}
}

// 9.1-UNIT-008 — A/B request emits ONE event with model=served-leg (leg-A): the
// first emitUsage/Populate wins, later legs are ignored for request.logged.
func TestAbLegFirstWins(t *testing.T) {
	rec := &recordingEmitter{}
	h := func(w http.ResponseWriter, r *http.Request) {
		r0 := FromContext(r.Context())
		r0.Populate("qwen-max", 10, 20, 30, false)   // leg A
		r0.Populate("deepseek-v3", 99, 99, 198, false) // leg B — ignored
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	serve(rec, h, seedIdentity(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))
	ev := rec.only(t)
	if ev.GetModel() != "qwen-max" || ev.GetTotalTokens() != 30 {
		t.Fatalf("A/B must attribute leg-A (qwen-max/30), got %s/%d", ev.GetModel(), ev.GetTotalTokens())
	}
}

// 9.1-UNIT-004 — PII discipline: the marshaled event carries NO message content,
// NO completion text, NO plaintext key (structural — the proto has no such field).
func TestNoPIIInEvent(t *testing.T) {
	rec := &recordingEmitter{}
	h := func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).Populate("qwen-max", 1, 2, 3, false)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"secret reply"}}]}`))
	}
	serve(rec, h, seedIdentity(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))
	body, err := protojson.Marshal(rec.only(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(body)
	for _, banned := range []string{"content", "messages", "secret", "Bearer", "sk-"} {
		if strings.Contains(js, banned) {
			t.Fatalf("event leaked %q: %s", banned, js)
		}
	}
}

// failWriter always errors — exercises the fire-and-forget guarantee.
type failWriter struct{}

func (failWriter) WriteMessages(context.Context, ...kafka.Message) error {
	return context.DeadlineExceeded
}

// 9.1-UNIT-003 — fire-and-forget [ERROR-002]: a Kafka write failure NEVER affects
// the already-served response (it logs+drops). The response body is byte-identical
// whether the producer succeeds or fails.
func TestFireAndForgetResponseUnaffected(t *testing.T) {
	done := make(chan struct{})
	em := NewKafkaEmitter(failWriter{}, nil)
	em.onDone = func() { close(done) }

	h := func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).Populate("qwen-max", 1, 2, 3, false)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	rr := serve(em, h, seedIdentity(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	if rr.Code != http.StatusOK || rr.Body.String() != `{"ok":true}` {
		t.Fatalf("response mutated by producer: code=%d body=%q", rr.Code, rr.Body.String())
	}
	select {
	case <-done: // the detached produce ran and the failure was swallowed
	case <-time.After(2 * time.Second):
		t.Fatal("detached produce never completed")
	}
}
