// usage_log_export.go — Story 9.3 AC2 UsageLogExportWorker.
//
// Consumes usage.log.export.requested (consumer group
// `analytics-svc.usage-log-export`, BR-EX-10), dumps the user's request_logs
// over [range_start, range_end] (PII+cost-excluded, format-aware), uploads to
// OSS, signs a 24h URL, drives the data_export_requests row through
// pending → processing → (completed | failed), and triggers the
// `usage_log_export_ready` email + audit.
//
// A SEPARATE worker from GdprExportWorker (BR-EX-10): one format-aware dumper,
// NOT the 7-dumper GDPR bundle (which panics unless exactly 7). Reuses the same
// Store / Uploader / AuditEmitter / EmailContext / KafkaReader contracts.
//
// SCOPE / ENV (BR-EX-17, parity with the 2.6 GDPR worker): the live OSS / Kafka
// / SendGrid clients are not vendored locally ([[project_toolchain_env_limits]]).
// The worker ships the integration-ready interface chain + the consumer loop +
// the status-machine guard; main.go gates it OFF by default
// (HE_API_ANALYTICS_USAGE_LOG_EXPORT_WORKER_ENABLED) and wires NoOp deps until
// CI/PR testcontainers provide real impls. The ClickHouse dump itself IS real
// (client vendored in 9.1).
package workers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"

	obs "github.com/he-api/he-api/packages/go-observability"
	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
)

// usageLogExportTracerName names the usage-log-export consumer's tracer.
const usageLogExportTracerName = "apps/analytics-svc/internal/workers/usage_log_export"

// UsageLogExportConsumerGroup is the Kafka consumer-group name per BR-EX-10.
const UsageLogExportConsumerGroup = "analytics-svc.usage-log-export"

// UsageLogExportTopic is the Kafka topic produced by notification-svc (BR-EX-9).
// Duplicated as a literal here (analytics-svc and notification-svc are separate
// Go modules — the notification-svc events.TopicUsageLogExportRequested const
// cannot be imported across the module boundary).
const UsageLogExportTopic = "usage.log.export.requested"

// UsageLogDumpTimeout bounds a single dump.
const UsageLogDumpTimeout = 15 * time.Minute

// UsageLogEmailTrigger invokes notification-svc.SendEmail with the
// `usage_log_export_ready` template. Unlike the GDPR EmailTrigger it carries
// `format` + `rowCount` (BR-EX-15 / Architect Low-2 — the trigger is template-
// parameterized, not gdpr-bound). The signed URL is passed for the email body
// ONLY; it is NEVER logged or persisted (TS-CONS-008).
type UsageLogEmailTrigger interface {
	Send(ctx context.Context, exportID string, ec EmailContext, signedURL string, expiresAt time.Time, format string, rowCount int64) error
}

// UsageLogExportWorker drives one usage-log export per message.
type UsageLogExportWorker struct {
	Reader   KafkaReader
	Store    Store
	Uploader Uploader
	Dumper   *dumps.RequestLogsExportDumper
	Email    UsageLogEmailTrigger
	Audit    AuditEmitter
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewUsageLogExportWorker validates required deps and returns a ready worker.
func NewUsageLogExportWorker(
	reader KafkaReader, store Store, uploader Uploader, dumper *dumps.RequestLogsExportDumper,
	email UsageLogEmailTrigger, audit AuditEmitter, logger *slog.Logger,
) *UsageLogExportWorker {
	if reader == nil || store == nil || uploader == nil || dumper == nil || email == nil || audit == nil {
		panic("workers: UsageLogExportWorker missing required dependency")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &UsageLogExportWorker{
		Reader: reader, Store: store, Uploader: uploader, Dumper: dumper,
		Email: email, Audit: audit, Logger: logger, Now: time.Now,
	}
}

// Run consumes until ctx is cancelled; offsets are committed AFTER the row's
// terminal PG state is durable (at-least-once, BR-EX-13).
func (w *UsageLogExportWorker) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		msg, err := w.Reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			w.Logger.WarnContext(ctx, "kafka fetch failed", slog.String("error", err.Error()))
			continue
		}
		// Story 9.4 BR-TR-9 — consumer span LINKED to the request that asked for
		// this export (link, not child — Q-KAFKA); legacy messages root anew.
		hctx, span := obs.StartConsumerSpan(ctx, usageLogExportTracerName, UsageLogExportTopic+" consume", msg)
		if err := w.Process(hctx, msg); err != nil {
			w.Logger.ErrorContext(hctx, "usage-log export job failed",
				slog.String("kafka_offset", fmt.Sprintf("%d", msg.Offset)),
				slog.String("error", err.Error()),
			)
		}
		span.End()
		if err := w.Reader.CommitMessages(ctx, msg); err != nil {
			w.Logger.WarnContext(ctx, "kafka commit failed", slog.String("error", err.Error()))
		}
	}
}

// Process executes one job: parse → claim → dump → OSS → signed-URL →
// mark completed → email + audit. The row's terminal state is the source of
// truth; the returned error is for caller-side logging only.
func (w *UsageLogExportWorker) Process(ctx context.Context, msg kafka.Message) error {
	evt := &usagelogv1.UsageLogExportRequestedEvent{}
	if err := proto.Unmarshal(msg.Value, evt); err != nil {
		w.Logger.WarnContext(ctx, "poison message — proto unmarshal failed; ack and skip", slog.String("error", err.Error()))
		return nil
	}
	exportID := evt.GetExportId()
	userID := evt.GetUserId()
	format := evt.GetFormat()
	if exportID == "" || userID == "" {
		w.Logger.WarnContext(ctx, "poison message — empty export_id/user_id; ack and skip")
		return nil
	}
	if format != "json" && format != "csv" {
		// Unknown format → mark failed (operator-visible); do NOT guess (BR-EX-11).
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("unknown format %q", format), "validate")
		return nil
	}

	// BR-EX-13 — atomic pending → processing (double-consume guard).
	claimed, err := w.Store.MarkProcessing(ctx, exportID)
	if err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}
	if !claimed {
		w.Logger.InfoContext(ctx, "usage-log export already claimed; ack and skip", slog.String("export_id", exportID))
		return nil
	}

	rangeStart := evt.GetRangeStart().AsTime()
	rangeEnd := evt.GetRangeEnd().AsTime()

	dumpCtx, cancel := context.WithTimeout(ctx, UsageLogDumpTimeout)
	defer cancel()

	var buf bytes.Buffer
	rowCount, err := w.Dumper.Dump(dumpCtx, userID, format, rangeStart, rangeEnd, &buf)
	if err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("ch dump failed: %v", usageLogSanitize(err)), "ch_dump")
		return err
	}

	key := fmt.Sprintf("usage-log-exports/%s/%s.%s", userID, exportID, format)
	if err := w.Uploader.PutObject(ctx, key, bytes.NewReader(buf.Bytes())); err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("oss upload failed: %v", usageLogSanitize(err)), "oss_upload")
		return fmt.Errorf("oss upload: %w", err)
	}
	signedURL, err := w.Uploader.SignURL(ctx, key, 24*time.Hour)
	if err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("oss sign url failed: %v", usageLogSanitize(err)), "oss_sign_url")
		return fmt.Errorf("oss sign url: %w", err)
	}

	expiresAt := w.Now().Add(24 * time.Hour)
	if err := w.Store.MarkCompleted(ctx, exportID, key, expiresAt); err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("pg mark completed failed: %v", usageLogSanitize(err)), "pg_complete")
		return fmt.Errorf("mark completed: %w", err)
	}

	ec, err := w.Store.LookupEmailContext(ctx, exportID)
	if err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("email-context lookup failed: %v", usageLogSanitize(err)), "email_send")
		return fmt.Errorf("lookup email context: %w", err)
	}
	if err := w.Email.Send(ctx, exportID, ec, signedURL, expiresAt, format, rowCount); err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("email send failed: %v", usageLogSanitize(err)), "email_send")
		return fmt.Errorf("trigger email: %w", err)
	}

	w.Audit.EmitCompleted(ctx, exportID, userID, int64(buf.Len()))
	w.Logger.InfoContext(ctx, "usage-log export completed",
		slog.String("export_id", exportID),
		slog.String("user_id", userID),
		slog.String("format", format),
		slog.Int64("rows", rowCount),
		slog.Int("bytes", buf.Len()),
	)
	return nil
}

func (w *UsageLogExportWorker) failWithAudit(ctx context.Context, exportID, userID, reason, stage string) {
	if err := w.Store.MarkFailed(ctx, exportID, reason); err != nil {
		w.Logger.WarnContext(ctx, "mark failed failed (sic)", slog.String("error", err.Error()))
	}
	w.Audit.EmitFailed(ctx, exportID, userID, reason, stage)
}

// usageLogSanitize bounds + truncates an error string for the operator-readable
// failure_reason. The caller NEVER passes the signed URL into failure_reason
// (TS-CONS-008) — the failure paths above use only the stage error, never the
// URL, so a signed URL can never reach the row.
func usageLogSanitize(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "...(truncated)"
	}
	return s
}
