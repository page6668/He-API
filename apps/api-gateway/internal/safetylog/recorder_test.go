package safetylog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureLogger returns a logger writing to buf (for WARN-message assertions).
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func inputEvent() contentsafety.SafetyEvent {
	return contentsafety.SafetyEvent{
		Direction:   contentsafety.DirectionInput,
		MatchedRule: "term_alpha",
		Category:    "political",
		Severity:    "high",
		Action:      contentsafety.ActionBlocked,
		UserID:      "u-1",
		APIKeyID:    "k-1",
		HeRequestID: "he-1",
		Strictness:  "strict",
	}
}

func outputEvent() contentsafety.SafetyEvent {
	ev := inputEvent()
	ev.Direction = contentsafety.DirectionOutput
	ev.Severity = "medium"
	ev.Strictness = "default"
	return ev
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// 8.5-UNIT-001 — input block → exactly ONE row {direction:input, action:blocked,
// matched_rule=canonical, strictness, excerpt 脱敏}.
func Test8_5_UNIT001_InputBlockWritesOneRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	ev := inputEvent()
	excerpt := maskedMatchExcerpt(ev.MatchedRule)
	mock.ExpectExec("INSERT INTO he_api.content_safety_logs").
		WithArgs("u-1", "k-1", "he-1", "input", "term_alpha", "blocked", "strict", excerpt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	r := newRecorder(mock, discardLogger())
	r.insert(ev)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if r.inserted.Load() != 1 {
		t.Fatalf("inserted=%d want 1", r.inserted.Load())
	}
}

// 8.5-UNIT-002 — two output (per-choice) events → TWO rows {direction:output}.
func Test8_5_UNIT002_OutputRedactWritesPerChoiceRows(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	ev := outputEvent()
	excerpt := maskedMatchExcerpt(ev.MatchedRule)
	for i := 0; i < 2; i++ {
		mock.ExpectExec("INSERT INTO he_api.content_safety_logs").
			WithArgs("u-1", "k-1", "he-1", "output", "term_alpha", "blocked", "default", excerpt).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	r := newRecorder(mock, discardLogger())
	r.queue <- ev
	r.queue <- ev
	r.drain() // deterministic synchronous flush

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if r.inserted.Load() != 2 {
		t.Fatalf("inserted=%d want 2", r.inserted.Load())
	}
}

// 8.5-UNIT-003 — stream terminate → ONE output row (a single emit).
func Test8_5_UNIT003_StreamTerminateWritesOneRow(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	ev := outputEvent()
	mock.ExpectExec("INSERT INTO he_api.content_safety_logs").
		WithArgs("u-1", "k-1", "he-1", "output", "term_alpha", "blocked", "default", maskedMatchExcerpt(ev.MatchedRule)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	r := newRecorder(mock, discardLogger())
	r.insert(ev)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 8.5-UNIT-005 / UNIT-007 — pure field projection incl. strictness attribution.
func Test8_5_UNIT005_RowProjection(t *testing.T) {
	ev := inputEvent()
	args := rowArgs(ev)
	want := []any{"u-1", "k-1", "he-1", "input", "term_alpha", "blocked", "strict", maskedMatchExcerpt("term_alpha")}
	if len(args) != len(want) {
		t.Fatalf("arg count %d want %d", len(args), len(want))
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg[%d]=%v want %v", i, args[i], want[i])
		}
	}
	// optional empties → SQL NULL
	ev2 := ev
	ev2.APIKeyID = ""
	ev2.MatchedRule = ""
	a2 := rowArgs(ev2)
	if a2[1] != nil || a2[4] != nil {
		t.Fatalf("empty optionals must be nil (NULL): api_key_id=%v matched_rule=%v", a2[1], a2[4])
	}
	if a2[7] != nil { // empty canonical → empty excerpt → NULL
		t.Fatalf("empty canonical excerpt must be nil (NULL): %v", a2[7])
	}
}

// 8.5-BLIND-ERROR-001 (HARD) — FIRE-AND-FORGET on DB error: insert errors →
// failed counter ++, WARN logged, NEVER panics/surfaces. (Handler byte-identity
// is asserted at the INT/handler layer.)
func Test8_5_BLIND_ERROR001_FireAndForgetOnDBError(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("INSERT INTO he_api.content_safety_logs").
		WillReturnError(errors.New("connection refused"))
	var buf bytes.Buffer
	r := newRecorder(mock, captureLogger(&buf))
	r.insert(inputEvent())

	if r.failed.Load() != 1 || r.inserted.Load() != 0 {
		t.Fatalf("failed=%d inserted=%d want 1/0", r.failed.Load(), r.inserted.Load())
	}
	if !bytes.Contains(buf.Bytes(), []byte("safety_log: persist failed")) {
		t.Fatalf("expected WARN 'safety_log: persist failed', got: %s", buf.String())
	}
}

// 8.5-BLIND-ERROR-002 (HARD) — FIRE-AND-FORGET on a full buffer: Record never
// blocks; the overflow event is dropped + counted; the (would-be) block proceeds.
func Test8_5_BLIND_ERROR002_FireAndForgetOnFullBuffer(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	var buf bytes.Buffer
	r := newRecorder(mock, captureLogger(&buf)) // NO worker → queue never drains
	ev := inputEvent()
	for i := 0; i < queueCap; i++ {
		r.Record(context.Background(), ev) // fills the buffer exactly
	}
	if r.DroppedTotal() != 0 {
		t.Fatalf("no drop expected while filling to cap, got %d", r.DroppedTotal())
	}
	done := make(chan struct{})
	go func() { r.Record(context.Background(), ev); close(done) }() // overflow
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Record blocked on a full buffer (fire-and-forget violated)")
	}
	if r.DroppedTotal() != 1 {
		t.Fatalf("dropped=%d want 1", r.DroppedTotal())
	}
	if !bytes.Contains(buf.Bytes(), []byte("buffer full, event dropped")) {
		t.Fatalf("expected WARN 'buffer full', got: %s", buf.String())
	}
}

// 8.5-BLIND-RESOURCE-001 / UNIT-032 (HARD) — GRACEFUL DRAIN: ctx cancel → the
// worker flushes in-flight events then exits (done closed) within the bound; no
// silent loss on a normal redeploy (OQ-8.5-1).
func Test8_5_BLIND_RESOURCE001_GracefulDrainOnCancel(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	const n = 8
	ev := inputEvent()
	for i := 0; i < n; i++ {
		mock.ExpectExec("INSERT INTO he_api.content_safety_logs").
			WithArgs(rowArgs(ev)...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	r := newRecorder(mock, discardLogger())
	for i := 0; i < n; i++ {
		r.queue <- ev // buffered before the worker starts
	}
	ctx, cancel := context.WithCancel(context.Background())
	go r.run(ctx)
	cancel() // trigger drain-then-exit

	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after ctx cancel (drain/leak)")
	}
	if r.inserted.Load() != n {
		t.Fatalf("inserted=%d want %d (events lost on drain)", r.inserted.Load(), n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 8.5-BLIND-CONCURRENCY-001 / RESOURCE-002 — N concurrent Record → all N rows
// (assert the SET/count, not insert order); worker is the only new concurrency
// surface. Run under -race.
func Test8_5_BLIND_CONCURRENCY001_AllRowsWritten(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	const n = 100
	ev := inputEvent()
	for i := 0; i < n; i++ {
		mock.ExpectExec("INSERT INTO he_api.content_safety_logs").
			WithArgs(rowArgs(ev)...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRecorder(mock, discardLogger())
	go r.run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Record(ctx, ev) }()
	}
	wg.Wait()
	waitFor(t, func() bool { return r.inserted.Load()+r.dropped.Load() == n }, 3*time.Second)
	if r.dropped.Load() != 0 {
		t.Fatalf("unexpected drops=%d (n=%d < cap=%d)", r.dropped.Load(), n, queueCap)
	}
	if r.inserted.Load() != n {
		t.Fatalf("inserted=%d want %d", r.inserted.Load(), n)
	}
}

// 8.5-UNIT-033 — nil pool → NewPersistingRecorder returns nil so
// WithSafetyRecorder keeps the NopRecorder default (degraded; mirrors main.go:488).
func Test8_5_UNIT033_NilPoolReturnsNil(t *testing.T) {
	var buf bytes.Buffer
	got := NewPersistingRecorder(context.Background(), nil, captureLogger(&buf))
	if got != nil {
		t.Fatalf("nil pool must return nil Recorder, got %T", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("no pool")) {
		t.Fatalf("expected boot WARN about no pool, got: %s", buf.String())
	}
}
