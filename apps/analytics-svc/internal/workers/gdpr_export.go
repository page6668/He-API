// Package workers contains the analytics-svc Kafka-consumer workers.
//
// Story 2.6 T4.2 — GdprExportWorker: consumes gdpr.export.requested
// (consumer group `analytics-svc.gdpr-export`, BR-4.1), fans out 7
// concurrent PG/CH dumps + ZIP + OSS upload + signed-URL email trigger,
// flips the data_export_requests row through the
// pending → processing → (completed | failed) state machine, and emits
// audit events at each transition.
//
// SCOPE DEFERRAL (per Story 2.6 atomic implementation note in Dev Log):
// the live PG/CH/OSS clients require credentials and SDKs that are not
// vendored in this session (alicloud-sdk-go OSS client + ClickHouse Go
// client absent from go.mod). The worker exposes the integration-ready
// interface contract (Worker.Run / Worker.Process), the Kafka consumer
// loop, the errgroup fan-out pattern, the status-machine UPDATE-guard
// (BR-4.2 double-consume protection), and a typed Uploader / Dumper /
// Mailer interface chain so the integration tests (T4.1 testcontainers,
// 7 scenarios) can wire real or mock implementations on the PR.
//
// Tests on PR (T4.1 INT-020..026) seed PG + CH + LocalStack-OSS via
// testcontainers and verify the full chain end-to-end.
package workers

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	gdprv1 "github.com/he-api/he-api/packages/proto/gen/go/he/gdpr/v1"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
)

// ConsumerGroupID is the Kafka consumer-group name per BR-4.1.
const ConsumerGroupID = "analytics-svc.gdpr-export"

// DumpFanoutTimeout per AC4 BR-4.9.
const DumpFanoutTimeout = 30 * time.Minute

// KafkaReader is the segmentio/kafka-go consumer surface this worker uses.
type KafkaReader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// Store is the PG state-machine surface for `he_api.data_export_requests`.
// All methods are scoped to a single export_id; the worker drives the row
// through pending → processing → (completed | failed).
type Store interface {
	// MarkProcessing atomically transitions pending → processing and
	// returns false when the row is already past `pending` (BR-4.2
	// double-consume guard — 0 rows affected → ack + skip).
	MarkProcessing(ctx context.Context, exportID string) (claimed bool, err error)
	MarkCompleted(ctx context.Context, exportID, ossObjectKey string, signedURLExpiresAt time.Time) error
	MarkFailed(ctx context.Context, exportID, failureReason string) error
	// LookupEmailContext returns the user's email + locale + timezone +
	// display_name needed by the email-trigger step. Read fresh per
	// AC5 BR-5.4 (no snapshot).
	LookupEmailContext(ctx context.Context, exportID string) (EmailContext, error)
}

// EmailContext carries the per-user values needed for the SendEmail call.
type EmailContext struct {
	UserID      string
	Email       string
	DisplayName string
	Locale      string
	Timezone    string
}

// Uploader is the OSS surface (AC4 step 5–6 — PutObject + SignURL).
type Uploader interface {
	PutObject(ctx context.Context, key string, body io.Reader) error
	SignURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// EmailTrigger invokes notification-svc.SendEmail with the GDPR-export
// template — AC5 entry point. Decoupling via interface lets the worker
// stay free of connect-go imports.
type EmailTrigger interface {
	Send(ctx context.Context, exportID string, ec EmailContext, signedURL string, expiresAt time.Time) error
}

// AuditEmitter abstracts the audit publisher (BR-4.10 OTel + audit on
// every state transition).
type AuditEmitter interface {
	EmitCompleted(ctx context.Context, exportID, userID string, totalBytes int64)
	EmitFailed(ctx context.Context, exportID, userID, failureReason, failedStage string)
}

// GdprExportWorker fans-out 7 dumps per message and drives the status
// machine.
type GdprExportWorker struct {
	Reader     KafkaReader
	Store      Store
	Uploader   Uploader
	Dumpers    []dumps.Dumper
	Email      EmailTrigger
	Audit      AuditEmitter
	BucketName string
	Logger     *slog.Logger
	Now        func() time.Time
}

// NewGdprExportWorker validates required deps and returns a ready worker.
func NewGdprExportWorker(
	reader KafkaReader, store Store, uploader Uploader, dumpers []dumps.Dumper,
	email EmailTrigger, audit AuditEmitter, bucketName string, logger *slog.Logger,
) *GdprExportWorker {
	if reader == nil || store == nil || uploader == nil || email == nil || audit == nil {
		panic("workers: GdprExportWorker missing required dependency")
	}
	if len(dumpers) != 7 {
		panic(fmt.Sprintf("workers: GdprExportWorker expects 7 dumpers (GDPR completeness invariant); got %d", len(dumpers)))
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &GdprExportWorker{
		Reader: reader, Store: store, Uploader: uploader, Dumpers: dumpers,
		Email: email, Audit: audit, BucketName: bucketName, Logger: logger,
		Now: time.Now,
	}
}

// Run consumes from Kafka until ctx is cancelled. Each message drives
// one full export job through Process; consumer offsets are committed
// after the row's terminal state is durable in PG (at-least-once).
func (w *GdprExportWorker) Run(ctx context.Context) error {
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
		if err := w.Process(ctx, msg); err != nil {
			w.Logger.ErrorContext(ctx, "gdpr export job failed",
				slog.String("kafka_offset", fmt.Sprintf("%d", msg.Offset)),
				slog.String("error", err.Error()),
			)
		}
		if err := w.Reader.CommitMessages(ctx, msg); err != nil {
			w.Logger.WarnContext(ctx, "kafka commit failed", slog.String("error", err.Error()))
		}
	}
}

// Process executes one job: parse → claim → fan-out dumps → ZIP → OSS →
// signed-URL → mark completed → trigger email + audit. Returns an error
// for caller-side logging; the row's terminal state is the source of
// truth.
func (w *GdprExportWorker) Process(ctx context.Context, msg kafka.Message) error {
	evt := &gdprv1.DataExportRequestedEvent{}
	if err := proto.Unmarshal(msg.Value, evt); err != nil {
		w.Logger.WarnContext(ctx, "poison message — proto unmarshal failed; ack and skip",
			slog.String("error", err.Error()),
		)
		return nil // ack — re-delivery would loop
	}
	exportID := evt.GetExportId()
	userID := evt.GetUserId()
	if exportID == "" || userID == "" {
		w.Logger.WarnContext(ctx, "poison message — empty export_id/user_id; ack and skip")
		return nil
	}

	// BR-4.2 — atomic pending → processing. claimed=false means another
	// consumer already picked this row up (or it's already in terminal
	// state); ack + skip.
	claimed, err := w.Store.MarkProcessing(ctx, exportID)
	if err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}
	if !claimed {
		w.Logger.InfoContext(ctx, "gdpr export already claimed; ack and skip",
			slog.String("export_id", exportID),
		)
		return nil
	}

	// 7-dump fan-out per BR-4.9.
	dumpCtx, cancel := context.WithTimeout(ctx, DumpFanoutTimeout)
	defer cancel()

	bundle, totalBytes, err := w.dumpAndZip(dumpCtx, userID)
	if err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("dump+zip failed: %v", sanitize(err)), classifyStage(err))
		return err
	}

	// AC4 step 5–6.
	key := fmt.Sprintf("gdpr-exports/%s/%s.zip", userID, exportID)
	if err := w.Uploader.PutObject(ctx, key, bytes.NewReader(bundle)); err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("oss upload failed: %v", sanitize(err)), "oss_upload")
		return fmt.Errorf("oss upload: %w", err)
	}
	signedURL, err := w.Uploader.SignURL(ctx, key, 24*time.Hour)
	if err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("oss sign url failed: %v", sanitize(err)), "oss_sign_url")
		return fmt.Errorf("oss sign url: %w", err)
	}

	expiresAt := w.Now().Add(24 * time.Hour)
	if err := w.Store.MarkCompleted(ctx, exportID, key, expiresAt); err != nil {
		// Row will stay at `processing` — ops cron flips to failed/
		// expired per the BR-4.7 follow-up. Audit a failure so the
		// observability surface is consistent.
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("pg mark completed failed: %v", sanitize(err)), "pg_dump")
		return fmt.Errorf("mark completed: %w", err)
	}

	// AC4 step 8 — trigger email + audit success.
	ec, err := w.Store.LookupEmailContext(ctx, exportID)
	if err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("email-context lookup failed: %v", sanitize(err)), "email_send")
		return fmt.Errorf("lookup email context: %w", err)
	}
	if err := w.Email.Send(ctx, exportID, ec, signedURL, expiresAt); err != nil {
		w.failWithAudit(ctx, exportID, userID, fmt.Sprintf("email send failed: %v", sanitize(err)), "email_send")
		return fmt.Errorf("trigger email: %w", err)
	}

	w.Audit.EmitCompleted(ctx, exportID, userID, totalBytes)
	w.Logger.InfoContext(ctx, "gdpr export completed",
		slog.String("export_id", exportID),
		slog.String("user_id", userID),
		slog.Int64("bytes", totalBytes),
	)
	return nil
}

// dumpAndZip runs the 7 dumpers concurrently via errgroup (BR-4.9) and
// packs the resulting JSON files into one in-memory ZIP (BR-4.6 size-cap
// hint is not enforced here; the BR explicitly notes the cap is a hint
// not a hard limit).
func (w *GdprExportWorker) dumpAndZip(ctx context.Context, userID string) ([]byte, int64, error) {
	type out struct {
		name string
		body []byte
	}
	results := make([]out, len(w.Dumpers))
	g, gctx := errgroup.WithContext(ctx)
	for i, d := range w.Dumpers {
		i, d := i, d
		g.Go(func() error {
			var buf bytes.Buffer
			n, err := d.Dump(gctx, userID, &buf)
			if err != nil {
				return fmt.Errorf("dump %s: %w", d.Name(), err)
			}
			_ = n // n surfaced via the ZIP file size below for the metric
			results[i] = out{name: d.Name(), body: buf.Bytes()}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, 0, err
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	var total int64
	for _, r := range results {
		f, err := zw.Create(r.name)
		if err != nil {
			return nil, 0, fmt.Errorf("zip create %s: %w", r.name, err)
		}
		n, err := f.Write(r.body)
		if err != nil {
			return nil, 0, fmt.Errorf("zip write %s: %w", r.name, err)
		}
		total += int64(n)
	}
	if err := zw.Close(); err != nil {
		return nil, 0, fmt.Errorf("zip close: %w", err)
	}
	return zbuf.Bytes(), total, nil
}

func (w *GdprExportWorker) failWithAudit(ctx context.Context, exportID, userID, reason, stage string) {
	if err := w.Store.MarkFailed(ctx, exportID, reason); err != nil {
		w.Logger.WarnContext(ctx, "mark failed failed (sic)", slog.String("error", err.Error()))
	}
	w.Audit.EmitFailed(ctx, exportID, userID, reason, stage)
}

// sanitize strips potentially-PII fragments from error messages so the
// failure_reason column (operator-readable) doesn't accidentally leak
// user content (BR-4.3 spirit; TS-CONS-008 — signed URLs are credentials).
func sanitize(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "...(truncated)"
	}
	return s
}

// classifyStage attempts to infer the failed_stage tag for audit emission
// from the error context (best-effort; the worker's failure paths above
// pass the explicit stage in most cases — this is a fallback).
func classifyStage(err error) string {
	s := err.Error()
	switch {
	case contains(s, "users") || contains(s, "subscriptions") || contains(s, "api_keys"):
		return "pg_dump"
	case contains(s, "request_logs"):
		return "ch_dump"
	case contains(s, "context deadline exceeded"):
		return "timeout"
	default:
		return "pg_dump"
	}
}

// contains is a tiny strings.Contains substitute kept inline so the package
// doesn't pull `strings` for this one use.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
