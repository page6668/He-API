// Story 9.1 AC1 — request.logged consumer unit tests with fakes (no live
// Kafka/ClickHouse): malformed → DLQ + ack (9.1-INT-006 decision at unit level);
// valid → INSERT + commit. The full testcontainers integration (9.1-INT-001/002/
// 007/008) lives in request_log_integration_test.go (build-tagged).
package workers

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"

	"github.com/he-api/he-api/apps/analytics-svc/internal/clickhouse"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// queueReader serves a fixed message queue, then returns the fctx error (idle
// DeadlineExceeded → flush; parent Canceled → exit).
type queueReader struct {
	mu        sync.Mutex
	msgs      []kafka.Message
	committed []kafka.Message
}

func (r *queueReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	r.mu.Lock()
	if len(r.msgs) > 0 {
		m := r.msgs[0]
		r.msgs = r.msgs[1:]
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done() // idle: block until the per-fetch deadline or parent cancel
	return kafka.Message{}, ctx.Err()
}

func (r *queueReader) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.committed = append(r.committed, msgs...)
	return nil
}

func (r *queueReader) Close() error { return nil }

func (r *queueReader) commitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.committed)
}

type recordingDLQ struct {
	mu     sync.Mutex
	values [][]byte
}

func (d *recordingDLQ) Publish(_ context.Context, _, value []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.values = append(d.values, value)
	return nil
}

func (d *recordingDLQ) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.values)
}

type countingSink struct {
	mu       sync.Mutex
	inserted int
}

func (s *countingSink) Insert(_ context.Context, rows []clickhouse.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserted += len(rows)
	return nil
}

func (s *countingSink) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inserted
}

func mustEvent(t *testing.T, ev *analyticsv1.UsageLogEvent) kafka.Message {
	t.Helper()
	b, err := protojson.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return kafka.Message{Key: []byte(ev.GetUserId()), Value: b}
}

func runWorkerBriefly(t *testing.T, w *RequestLogWorker) {
	t.Helper()
	w.FlushInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	// Let the queue drain + at least one idle time-flush fire.
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestWorkerIngestsValidEvent(t *testing.T) {
	good := &analyticsv1.UsageLogEvent{
		HeRequestId: "req_abcdef012345",
		UserId:      "11111111-1111-1111-1111-111111111111",
		Model:       "qwen-max",
		StatusCode:  200,
		TotalTokens: 300,
		CostUsd:     "0.5",
		Ts:          "2026-06-11T12:34:56Z",
	}
	reader := &queueReader{msgs: []kafka.Message{mustEvent(t, good)}}
	sink := &countingSink{}
	dlq := &recordingDLQ{}
	w := NewRequestLogWorker(reader, clickhouse.NewBatchWriter(sink, 100), dlq, discardLogger())

	runWorkerBriefly(t, w)

	if sink.total() != 1 {
		t.Fatalf("expected 1 row inserted, got %d", sink.total())
	}
	if reader.commitCount() != 1 {
		t.Fatalf("expected 1 committed offset, got %d", reader.commitCount())
	}
	if dlq.count() != 0 {
		t.Fatalf("valid event must not hit DLQ, got %d", dlq.count())
	}
}

// 9.1-INT-006 (unit) — a malformed event routes to the DLQ and is acked so the
// partition is NOT blocked; it never reaches ClickHouse.
func TestWorkerRoutesMalformedToDLQ(t *testing.T) {
	poison := kafka.Message{Key: []byte("k"), Value: []byte(`{not valid json`)}
	badID := mustEvent(t, &analyticsv1.UsageLogEvent{
		HeRequestId: "nope", UserId: "u", Ts: "2026-06-11T00:00:00Z",
	})
	reader := &queueReader{msgs: []kafka.Message{poison, badID}}
	sink := &countingSink{}
	dlq := &recordingDLQ{}
	w := NewRequestLogWorker(reader, clickhouse.NewBatchWriter(sink, 100), dlq, discardLogger())

	runWorkerBriefly(t, w)

	if dlq.count() != 2 {
		t.Fatalf("both poison events must hit DLQ, got %d", dlq.count())
	}
	if sink.total() != 0 {
		t.Fatalf("poison events must NOT reach ClickHouse, got %d", sink.total())
	}
	if reader.commitCount() != 2 {
		t.Fatalf("poison events must be acked (partition unblocked), got %d", reader.commitCount())
	}
}
