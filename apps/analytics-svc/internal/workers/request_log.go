// Story 9.1 AC1 — RequestLogWorker consumes `request.logged` (consumer group
// `analytics-svc-request-log`) and batch-INSERTs each event into ClickHouse
// `he_api.request_logs` (the FIRST analytics ingestion path; analytics-svc was
// GDPR-export-only before 9.1). A malformed event is routed to
// `request.logged.dlq` and acked so the partition is never blocked (BR-ING-7).
// At-least-once: offsets commit only AFTER the batch INSERT lands (INT-008); a
// duplicate delivery is tolerated for the approximate dashboard (Q-DEDUP).
package workers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"

	"github.com/he-api/he-api/apps/analytics-svc/internal/clickhouse"
)

// RequestLogConsumerGroup is the Kafka consumer-group name (BR-ING-6).
const RequestLogConsumerGroup = "analytics-svc-request-log"

// RequestLogTopic / RequestLogDLQTopic mirror data-models §4.4.
const (
	RequestLogTopic    = "request.logged"
	RequestLogDLQTopic = "request.logged.dlq"
)

// flushBackoff bounds the busy-loop when ClickHouse is down (INT-008).
const flushBackoff = 2 * time.Second

// DLQPublisher publishes a poison event to request.logged.dlq.
type DLQPublisher interface {
	Publish(ctx context.Context, key, value []byte) error
}

// NopDLQ drops poison events (used when no DLQ writer is configured).
type NopDLQ struct{}

// Publish does nothing.
func (NopDLQ) Publish(context.Context, []byte, []byte) error { return nil }

// RequestLogWorker is the request.logged → ClickHouse ingestion consumer.
type RequestLogWorker struct {
	Reader        KafkaReader
	Writer        *clickhouse.BatchWriter
	DLQ           DLQPublisher
	Logger        *slog.Logger
	FlushInterval time.Duration
}

// NewRequestLogWorker validates deps and returns a ready worker.
func NewRequestLogWorker(reader KafkaReader, writer *clickhouse.BatchWriter, dlq DLQPublisher, logger *slog.Logger) *RequestLogWorker {
	if reader == nil || writer == nil {
		panic("workers: RequestLogWorker missing required dependency")
	}
	if dlq == nil {
		dlq = NopDLQ{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RequestLogWorker{
		Reader:        reader,
		Writer:        writer,
		DLQ:           dlq,
		Logger:        logger,
		FlushInterval: clickhouse.FlushInterval,
	}
}

// Run consumes until ctx is cancelled. Rows are buffered and flushed on a size
// OR time threshold; offsets for a batch commit only after the INSERT lands.
func (w *RequestLogWorker) Run(ctx context.Context) error {
	var pending []kafka.Message
	for {
		select {
		case <-ctx.Done():
			// Best-effort final flush + commit before exit.
			_ = w.flushAndCommit(context.WithoutCancel(ctx), &pending)
			return ctx.Err()
		default:
		}

		// Bound each fetch by the flush interval so an idle partition still
		// flushes buffered rows on time (BR-ING-6).
		fctx, cancel := context.WithTimeout(ctx, w.FlushInterval)
		msg, err := w.Reader.FetchMessage(fctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				_ = w.flushAndCommit(context.WithoutCancel(ctx), &pending)
				return nil
			}
			// DeadlineExceeded (idle) or transient fetch error → time-flush.
			if ferr := w.flushAndCommit(ctx, &pending); ferr != nil {
				w.backoff(ctx)
			}
			continue
		}

		ev := &analyticsv1.UsageLogEvent{}
		if perr := protojson.Unmarshal(msg.Value, ev); perr != nil {
			w.toDLQ(ctx, msg, "unmarshal_failed", perr)
			w.commitOne(ctx, msg)
			continue
		}
		row, rerr := clickhouse.EventToRow(ev, w.Logger)
		if rerr != nil {
			w.toDLQ(ctx, msg, "malformed_event", rerr)
			w.commitOne(ctx, msg)
			continue
		}

		full := w.Writer.Add(row)
		pending = append(pending, msg)
		if full {
			if ferr := w.flushAndCommit(ctx, &pending); ferr != nil {
				w.backoff(ctx)
			}
		}
	}
}

// flushAndCommit flushes the buffered rows and, on success, commits the pending
// offsets. On a ClickHouse error the rows + offsets are RETAINED (no commit) so
// a redelivery re-inserts them after recovery (INT-008 / at-least-once).
func (w *RequestLogWorker) flushAndCommit(ctx context.Context, pending *[]kafka.Message) error {
	if len(*pending) == 0 {
		return w.Writer.Flush(ctx) // no-op when buffer empty
	}
	if err := w.Writer.Flush(ctx); err != nil {
		w.Logger.WarnContext(ctx, "request_log_flush_failed",
			slog.Int("pending", len(*pending)), slog.String("error", err.Error()))
		return err
	}
	if err := w.Reader.CommitMessages(ctx, (*pending)...); err != nil {
		// Offsets uncommitted → redelivery (dup tolerated, Q-DEDUP). Rows are
		// already inserted; the dup re-insert is a ≪0.01% over-count.
		w.Logger.WarnContext(ctx, "request_log_commit_failed", slog.String("error", err.Error()))
		return err
	}
	*pending = nil
	return nil
}

func (w *RequestLogWorker) commitOne(ctx context.Context, msg kafka.Message) {
	if err := w.Reader.CommitMessages(ctx, msg); err != nil {
		w.Logger.WarnContext(ctx, "request_log_dlq_commit_failed", slog.String("error", err.Error()))
	}
}

func (w *RequestLogWorker) toDLQ(ctx context.Context, msg kafka.Message, reason string, cause error) {
	w.Logger.WarnContext(ctx, "request_log_routed_to_dlq",
		slog.String("event", "request_log_parse_failed"),
		slog.String("reason", reason),
		slog.String("error", cause.Error()))
	if err := w.DLQ.Publish(ctx, msg.Key, msg.Value); err != nil {
		w.Logger.WarnContext(ctx, "request_log_dlq_publish_failed", slog.String("error", err.Error()))
	}
}

func (w *RequestLogWorker) backoff(ctx context.Context) {
	t := time.NewTimer(flushBackoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
