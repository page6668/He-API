package clickhouse

import (
	"context"
	"sync"
	"time"
)

// Sink performs the actual ClickHouse INSERT of a batch of rows. The real
// implementation (chSink) binds clickhouse-go/v2; tests inject a fake so the
// buffering/flush logic is exercised without a live ClickHouse.
type Sink interface {
	Insert(ctx context.Context, rows []Row) error
}

// BatchWriter buffers rows and flushes on a size OR time threshold (BR-ING-6 —
// batched INSERT for throughput; 9.1-UNIT-009 / RESOURCE-001). It is safe for
// concurrent Add/Flush from a single consumer goroutine + a flush timer.
type BatchWriter struct {
	sink      Sink
	batchSize int
	mu        sync.Mutex
	buf       []Row
}

// NewBatchWriter builds a writer that flushes when the buffer reaches batchSize.
// The caller drives the time-based flush via the flush-interval ticker in the
// worker (Flush is called both on size-trip and on tick).
func NewBatchWriter(sink Sink, batchSize int) *BatchWriter {
	if batchSize <= 0 {
		batchSize = 500
	}
	return &BatchWriter{sink: sink, batchSize: batchSize, buf: make([]Row, 0, batchSize)}
}

// Add appends a row and returns true when the buffer has reached the size
// threshold (the caller should then Flush). The row is held until the next
// successful Flush (at-least-once: nothing is acked until the INSERT lands).
func (w *BatchWriter) Add(r Row) (full bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, r)
	return len(w.buf) >= w.batchSize
}

// Len returns the current buffered row count.
func (w *BatchWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buf)
}

// Flush INSERTs the buffered rows. On success the buffer is cleared. On error
// the buffer is RETAINED (the caller must NOT commit Kafka offsets — INT-008:
// retry/backoff, offset not committed → redelivery on recovery). A flush of an
// empty buffer is a no-op.
func (w *BatchWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buf) == 0 {
		w.mu.Unlock()
		return nil
	}
	pending := make([]Row, len(w.buf))
	copy(pending, w.buf)
	w.mu.Unlock()

	if err := w.sink.Insert(ctx, pending); err != nil {
		return err // buffer retained — offsets stay uncommitted
	}

	w.mu.Lock()
	// Drop only the rows we flushed (more may have been Added concurrently,
	// though the single-consumer worker does not do that today).
	w.buf = w.buf[len(pending):]
	w.mu.Unlock()
	return nil
}

// FlushInterval is the default time-based flush cadence (BR-ING-6). Bounds the
// "实时/今日" staleness contribution (Q-RT ≤ ~10s SLA — Kafka lag + this).
const FlushInterval = 2 * time.Second

// DefaultBatchSize is the default size-based flush threshold.
const DefaultBatchSize = 500
