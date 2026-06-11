// Story 9.1 AC1 — batched writer unit tests (9.1-UNIT-009 / RESOURCE-001: buffer
// + flush on size AND time; retain-on-error so offsets stay uncommitted).
package clickhouse

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeSink struct {
	mu       sync.Mutex
	batches  [][]Row
	failNext bool
}

func (f *fakeSink) Insert(_ context.Context, rows []Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		return errors.New("clickhouse down")
	}
	cp := make([]Row, len(rows))
	copy(cp, rows)
	f.batches = append(f.batches, cp)
	return nil
}

func (f *fakeSink) inserted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func TestBatchWriterFlushOnSize(t *testing.T) {
	sink := &fakeSink{}
	w := NewBatchWriter(sink, 3)

	if w.Add(Row{HeRequestID: "a"}) {
		t.Fatal("not full after 1")
	}
	if w.Add(Row{HeRequestID: "b"}) {
		t.Fatal("not full after 2")
	}
	if !w.Add(Row{HeRequestID: "c"}) {
		t.Fatal("should be full at batchSize=3")
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.inserted() != 3 || w.Len() != 0 {
		t.Fatalf("after flush: inserted=%d len=%d", sink.inserted(), w.Len())
	}
}

func TestBatchWriterFlushPartialOnTime(t *testing.T) {
	sink := &fakeSink{}
	w := NewBatchWriter(sink, 100) // large size → only the time-flush triggers

	w.Add(Row{HeRequestID: "a"})
	w.Add(Row{HeRequestID: "b"})
	if w.Len() != 2 {
		t.Fatalf("buffered=%d want 2", w.Len())
	}
	// Simulate the worker's time-based flush tick.
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.inserted() != 2 || w.Len() != 0 {
		t.Fatalf("partial time-flush: inserted=%d len=%d", sink.inserted(), w.Len())
	}
}

func TestBatchWriterRetainsOnError(t *testing.T) {
	sink := &fakeSink{failNext: true}
	w := NewBatchWriter(sink, 10)
	w.Add(Row{HeRequestID: "a"})
	w.Add(Row{HeRequestID: "b"})

	if err := w.Flush(context.Background()); err == nil {
		t.Fatal("flush should surface the sink error")
	}
	if w.Len() != 2 {
		t.Fatalf("buffer must be retained on error (offsets uncommitted), got len=%d", w.Len())
	}

	// Recovery: the next flush succeeds and clears.
	sink.failNext = false
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("recovery flush: %v", err)
	}
	if sink.inserted() != 2 || w.Len() != 0 {
		t.Fatalf("after recovery: inserted=%d len=%d", sink.inserted(), w.Len())
	}
}

func TestBatchWriterEmptyFlushNoop(t *testing.T) {
	sink := &fakeSink{}
	w := NewBatchWriter(sink, 10)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush: %v", err)
	}
	if sink.inserted() != 0 {
		t.Fatal("empty flush must not insert")
	}
}
