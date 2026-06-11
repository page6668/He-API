// analytics-svc — Kafka-driven workers for cross-cutting analytics + GDPR
// data-export jobs (Story 2.6 AC4 first concrete workload, per service-
// topology §3.1).
//
// Story 2.6 boot path: subscribe to `gdpr.export.requested`, fan-out 7
// dumps, ZIP, upload to OSS, sign URL 24h, trigger SendEmail.
//
// SCOPE DEFERRAL (Story 2.6 atomic-impl note): live OSS / ClickHouse
// clients are not wired here — the alicloud-sdk-go OSS module and the
// ClickHouse Go client are absent from go.mod (vendoring is operator-
// owned per coding-standards §12.7). The boot path constructs the
// Kafka reader, a NoOp Uploader / EmailTrigger / AuditEmitter, and the
// 7-dump set so the binary compiles, the K8s deployment template lands,
// and the worker can be wired with real clients in a follow-up commit
// once the SDKs are vendored. The integration tests (T4.1) target the
// worker package directly — they wire real testcontainers-backed
// implementations on the PR.
//
// QA Round 1 ISSUE-1 remediation (Option C, 2026-05-18): while the NoOp
// implementations are wired, the binary refuses to subscribe to
// `gdpr.export.requested` unless the operator explicitly opts in by
// setting HE_API_ANALYTICS_GDPR_WORKER_ENABLED=true. The default of false
// prevents the user-visible failure mode where a real Kafka message would
// be ACK'd by the NoOp store and the user's export row would stay at
// `pending` for the entire 24h idempotency window. The flag MUST remain
// false in production until the OSS + ClickHouse SDKs are vendored and
// real Store / Uploader / Email / Audit / Dumpers are wired.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/analytics-svc/internal/clickhouse"
	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
	"github.com/he-api/he-api/apps/analytics-svc/internal/workers"

	"go.opentelemetry.io/otel"
)

const (
	serviceName    = "analytics-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
	listenAddr     = ":8080" // for /healthz + /metrics scrape; main work is the Kafka loop
)

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tp, err := obs.NewTracerProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		logger.Error("tracer provider init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	otel.SetTracerProvider(tp)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = tp.Shutdown(sctx)
	}()

	kafkaBrokers := strings.Split(strings.TrimSpace(os.Getenv("HE_API_KAFKA_BROKERS")), ",")
	if len(kafkaBrokers) == 1 && kafkaBrokers[0] == "" {
		logger.Warn("HE_API_KAFKA_BROKERS unset — analytics-svc will not consume any messages")
	}
	bucketName := envOr("HE_API_OSS_GDPR_EXPORTS_BUCKET", "he-api-gdpr-exports")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	healthSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(mux, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("analytics-svc health listener", slog.String("addr", listenAddr))
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health http server error", slog.String("error", err.Error()))
		}
	}()

	gdprWorkerEnabled := envBool("HE_API_ANALYTICS_GDPR_WORKER_ENABLED", false)
	switch {
	case len(kafkaBrokers) == 0 || kafkaBrokers[0] == "":
		// already warned above
	case !gdprWorkerEnabled:
		// QA Round 1 ISSUE-1 gating: the production binary refuses to
		// claim gdpr.export.requested while NoOp implementations are
		// wired. Real Kafka messages would otherwise be ACK'd silently
		// and the user's export row would stay at `pending` forever.
		logger.Warn("gdpr-export worker disabled — set HE_API_ANALYTICS_GDPR_WORKER_ENABLED=true after vendoring OSS + ClickHouse SDKs and wiring real Store/Uploader/Email/Audit/Dumpers",
			slog.String("topic", "gdpr.export.requested"),
			slog.String("group", workers.ConsumerGroupID),
		)
	default:
		reader := kafka.NewReader(kafka.ReaderConfig{
			Brokers:        kafkaBrokers,
			GroupID:        workers.ConsumerGroupID,
			Topic:          "gdpr.export.requested",
			MinBytes:       1,
			MaxBytes:       10 * 1024 * 1024,
			CommitInterval: 0, // sync commit per BR-4.1 at-least-once
		})
		defer reader.Close()

		// SCOPE DEFERRAL: real PG / OSS / Email / Audit clients live in
		// follow-up commits; the worker accepts our interfaces here so
		// the binary boots in environments where the SDKs aren't yet
		// configured. The integration tests (T4.1) directly exercise
		// `workers.GdprExportWorker.Process` with real testcontainers
		// implementations on the PR.
		store := &noopStore{}
		uploader := &noopUploader{}
		email := &noopEmail{}
		audit := &noopAudit{logger: logger}
		var dumpers []dumps.Dumper
		for _, name := range []string{
			"users.json", "api_keys.json", "subscriptions.json",
			"balances.json", "recharge_orders.json",
			"content_safety_logs.json", "request_logs.json",
		} {
			dumpers = append(dumpers, &emptyDumper{name: name})
		}
		worker := workers.NewGdprExportWorker(reader, store, uploader, dumpers, email, audit, bucketName, logger)
		go func() {
			logger.Warn("gdpr-export worker running with NoOp wiring — exports will be ACK'd without delivery; ensure real SDKs are vendored before production use",
				slog.String("topic", "gdpr.export.requested"),
				slog.String("group", workers.ConsumerGroupID),
			)
			if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("gdpr export worker exited with error", slog.String("error", err.Error()))
			}
		}()
	}

	// Story 9.1 AC1 — request.logged → ClickHouse ingestion worker. Env-gated
	// (default false, GDPR-worker opt-in parity): the operator MUST set
	// HE_API_ANALYTICS_REQUEST_LOG_WORKER_ENABLED=true AND provide
	// HE_API_CLICKHOUSE_DSN before this consumes. With the ClickHouse client now
	// vendored + a real batched writer, "enabled" means real ingestion (unlike
	// the GDPR NoOp wiring above).
	requestLogEnabled := envBool("HE_API_ANALYTICS_REQUEST_LOG_WORKER_ENABLED", false)
	chDSN := strings.TrimSpace(os.Getenv("HE_API_CLICKHOUSE_DSN"))
	switch {
	case len(kafkaBrokers) == 0 || kafkaBrokers[0] == "":
		// already warned above
	case !requestLogEnabled:
		logger.Warn("request.logged worker disabled — set HE_API_ANALYTICS_REQUEST_LOG_WORKER_ENABLED=true (+ HE_API_CLICKHOUSE_DSN)",
			slog.String("topic", workers.RequestLogTopic),
			slog.String("group", workers.RequestLogConsumerGroup),
		)
	case chDSN == "":
		logger.Warn("request.logged worker enabled but HE_API_CLICKHOUSE_DSN unset — refusing to ACK without a ClickHouse sink",
			slog.String("topic", workers.RequestLogTopic))
	default:
		sink, closeSink, serr := clickhouse.Open(ctx, chDSN)
		if serr != nil {
			logger.Error("clickhouse open failed — request.logged worker not started", slog.String("error", serr.Error()))
		} else {
			defer func() { _ = closeSink() }()
			rlReader := kafka.NewReader(kafka.ReaderConfig{
				Brokers:        kafkaBrokers,
				GroupID:        workers.RequestLogConsumerGroup,
				Topic:          workers.RequestLogTopic,
				MinBytes:       1,
				MaxBytes:       10 * 1024 * 1024,
				CommitInterval: 0, // sync commit (at-least-once; offsets after INSERT)
			})
			defer rlReader.Close()

			dlqWriter := &kafka.Writer{
				Addr:         kafka.TCP(kafkaBrokers...),
				Topic:        workers.RequestLogDLQTopic,
				Balancer:     &kafka.Hash{},
				RequiredAcks: kafka.RequireAll,
			}
			defer func() { _ = dlqWriter.Close() }()

			writer := clickhouse.NewBatchWriter(sink, clickhouse.DefaultBatchSize)
			worker := workers.NewRequestLogWorker(rlReader, writer, &kafkaDLQ{w: dlqWriter}, logger)
			go func() {
				logger.Info("request.logged worker running",
					slog.String("topic", workers.RequestLogTopic),
					slog.String("group", workers.RequestLogConsumerGroup),
				)
				if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Error("request.logged worker exited with error", slog.String("error", err.Error()))
				}
			}()
		}
	}

	<-ctx.Done()
	logger.Info("signal received, shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("health http shutdown failed", slog.String("error", err.Error()))
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envBool parses 1/true/TRUE/True/t/T as true; anything else (including
// unset) as the supplied fallback. Used to gate the GDPR worker.
func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "true", "t", "yes", "y":
		return true
	case "0", "false", "f", "no", "n":
		return false
	default:
		return fallback
	}
}

// kafkaDLQ adapts a *kafka.Writer to the workers.DLQPublisher surface (Story 9.1
// — poison request.logged events route to request.logged.dlq, BR-ING-7).
type kafkaDLQ struct{ w *kafka.Writer }

func (d *kafkaDLQ) Publish(ctx context.Context, key, value []byte) error {
	return d.w.WriteMessages(ctx, kafka.Message{Key: key, Value: value})
}

// ---- NoOp implementations — bootable placeholders. Replaced on the PR
// that vendors the real PG / OSS / SendGrid clients. ----

type noopStore struct{}

func (*noopStore) MarkProcessing(_ context.Context, _ string) (bool, error)     { return false, nil }
func (*noopStore) MarkCompleted(_ context.Context, _, _ string, _ time.Time) error { return nil }
func (*noopStore) MarkFailed(_ context.Context, _, _ string) error              { return nil }
func (*noopStore) LookupEmailContext(_ context.Context, _ string) (workers.EmailContext, error) {
	return workers.EmailContext{}, nil
}

type noopUploader struct{}

func (*noopUploader) PutObject(_ context.Context, _ string, _ io.Reader) error { return nil }
func (*noopUploader) SignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://placeholder/" + key, nil
}

type noopEmail struct{}

func (*noopEmail) Send(_ context.Context, _ string, _ workers.EmailContext, _ string, _ time.Time) error {
	return nil
}

type noopAudit struct{ logger *slog.Logger }

func (a *noopAudit) EmitCompleted(_ context.Context, exportID, userID string, totalBytes int64) {
	a.logger.Info("gdpr export completed (audit noop)",
		slog.String("export_id", exportID),
		slog.String("user_id", userID),
		slog.Int64("bytes", totalBytes),
	)
}
func (a *noopAudit) EmitFailed(_ context.Context, exportID, userID, reason, stage string) {
	a.logger.Warn("gdpr export failed (audit noop)",
		slog.String("export_id", exportID),
		slog.String("user_id", userID),
		slog.String("reason", reason),
		slog.String("stage", stage),
	)
}

type emptyDumper struct{ name string }

func (e *emptyDumper) Name() string { return e.name }
func (e *emptyDumper) Dump(_ context.Context, _ string, w io.Writer) (int64, error) {
	// The boot-time placeholder writes `[]` — a valid empty JSON array —
	// so the ZIP always has 7 well-formed files while the real PG / CH
	// queries are being wired. The integration tests (T4.1) substitute
	// real Dumpers seeded by testcontainers; they don't go through this
	// placeholder.
	n, err := w.Write([]byte("[]"))
	return int64(n), err
}
