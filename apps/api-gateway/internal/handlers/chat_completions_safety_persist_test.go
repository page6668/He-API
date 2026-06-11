// Story 8.5 — handler-level persist integration + the consolidated HARD gates
// (no-regression byte-identity, fire-and-forget not-blocked, sub-threshold zero
// rows, output persist). The persisting Recorder is a PURE side-effect: it
// changes ONLY the DB write, never the 8.2/8.3/8.4 response path. Scenario IDs
// trace to docs/qa/assessments/8.5-test-design-20260611.md.
package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/safetylog"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

const insertRe = "INSERT INTO he_api.content_safety_logs"

// anyArgs returns n pgxmock.AnyArg matchers (the persisted row's exact values
// depend on the request-context identity, which is not the subject under test).
func anyArgs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = pgxmock.AnyArg()
	}
	return out
}

// waitMet polls until the async worker has satisfied the mock (the INSERT landed).
func waitMet(t *testing.T, mock pgxmock.PgxPoolIface, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if mock.ExpectationsWereMet() == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("INSERT did not land within %s: %v", d, mock.ExpectationsWereMet())
}

// 8.5-INT-001 — input block end-to-end through the real handler + the real
// PersistingRecorder (pgxmock): exactly ONE content_safety_logs row is written.
func Test8_5_INT001_InputBlockPersistsOneRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec(insertRe).WithArgs(anyArgs(8)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := safetylog.NewPersistingRecorder(ctx, mock, discardLogger())

	rr := doRequest(t, safetyHandler(rec), hitUserBody)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	waitMet(t, mock, 2*time.Second) // the async INSERT landed exactly once
}

// 8.5-UNIT-030 (HARD) — NO-REGRESSION: with the persisting Recorder wired (and
// even when its DB write ERRORS), the 400 envelope is identical to the NopRecorder
// baseline — the Recorder is a pure side-effect on the response path.
func Test8_5_UNIT030_NoRegressionResponsePath(t *testing.T) {
	base := doRequest(t, safetyHandler(contentsafety.NopRecorder{}), hitUserBody)

	okMock, _ := pgxmock.NewPool()
	defer okMock.Close()
	okMock.ExpectExec(insertRe).WithArgs(anyArgs(8)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	okResp := doRequest(t, safetyHandler(safetylog.NewPersistingRecorder(ctx1, okMock, discardLogger())), hitUserBody)

	errMock, _ := pgxmock.NewPool()
	defer errMock.Close()
	errMock.ExpectExec(insertRe).WithArgs(anyArgs(8)...).WillReturnError(errors.New("connection refused"))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	errResp := doRequest(t, safetyHandler(safetylog.NewPersistingRecorder(ctx2, errMock, discardLogger())), hitUserBody)

	assertSameEnvelope(t, base, okResp)
	assertSameEnvelope(t, base, errResp)
}

// 8.5-UNIT-031 (HARD) — FIRE-AND-FORGET: the handler NEVER waits on the DB. With a
// Recorder whose underlying Exec blocks indefinitely, the 400 still returns
// promptly (the worker, not the handler, owns the write).
func Test8_5_UNIT031_FireAndForgetHandlerNotBlocked(t *testing.T) {
	be := &blockingExecer{release: make(chan struct{}), reached: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	rec := safetylog.NewPersistingRecorder(ctx, be, discardLogger())

	start := time.Now()
	rr := doRequest(t, safetyHandler(rec), hitUserBody)
	elapsed := time.Since(start)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("handler blocked %s on the DB write (fire-and-forget violated)", elapsed)
	}
	// The write was genuinely attempted asynchronously (worker reached Exec),
	// proving the event was enqueued, not silently skipped.
	select {
	case <-be.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never reached the DB write")
	}
	close(be.release) // let the worker finish
	cancel()
}

// 8.5-UNIT-004 — sub-threshold (loose key, only a LOW term, NOT acted on) →
// ZERO rows: recordSafetyBlock is never called, so the Recorder sees no event.
func Test8_5_UNIT004_SubThresholdWritesZeroRows(t *testing.T) {
	scanner := seededScanner(enSeed("lowinword", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 8830))
	rec := &captureRecorder{}
	h := handlers.NewChatCompletionsHandler(discardLogger(),
		handlers.WithSafetyScanner(scanner),
		handlers.WithSafetyRecorder(rec),
	)
	body := `{"model":"qwen-max","messages":[{"role":"user","content":"only lowinword here"}]}`
	rr := doRequestStrictness(t, h, body, "loose")
	if rr.Code != http.StatusOK {
		t.Fatalf("loose: a low-only request must dispatch, got %d body=%s", rr.Code, rr.Body.String())
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("sub-threshold must record ZERO events, got %d", n)
	}
}

// 8.5-INT-002 — output redact end-to-end through the real handler + the real
// PersistingRecorder: a sensitive non-stream completion persists an output row.
func Test8_5_INT002_OutputRedactPersistsRow(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec(insertRe).WithArgs(anyArgs(8)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := safetylog.NewPersistingRecorder(ctx, mock, discardLogger())

	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": newSingleChunkHandle(sensitiveAdapterChunk()),
	})
	h := failoverHandler(t, rc, reg, &countingDeducter{},
		handlers.WithOutputSafetyScanner(defaultOutputScanner()),
		handlers.WithSafetyRecorder(rec),
	)
	rr := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	waitMet(t, mock, 2*time.Second) // an output row landed
}

// --- helpers ---

// blockingExecer is a safetylog.Querier whose Exec blocks until released — to
// prove the handler's fire-and-forget enqueue never waits on the DB.
type blockingExecer struct {
	release chan struct{}
	reached chan struct{}
}

func (b *blockingExecer) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	select {
	case b.reached <- struct{}{}:
	default:
	}
	<-b.release
	return pgconn.CommandTag{}, nil
}

func assertSameEnvelope(t *testing.T, want, got *httptest.ResponseRecorder) {
	t.Helper()
	if want.Code != got.Code {
		t.Fatalf("status diverged: %d vs %d", want.Code, got.Code)
	}
	wm := decodeErr(t, want)
	gm := decodeErr(t, got)
	if !reflect.DeepEqual(wm, gm) {
		t.Fatalf("response envelope diverged with persisting recorder:\n base=%v\n got =%v", wm, gm)
	}
}

// decodeErr returns the error object minus the per-request he_request_id.
func decodeErr(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	m := decodeBody(t, rr)
	if e, ok := m["error"].(map[string]any); ok {
		delete(e, "he_request_id")
	}
	return m
}
