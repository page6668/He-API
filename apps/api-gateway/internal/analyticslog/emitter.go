package analyticslog

import (
	"context"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/encoding/protojson"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"
)

// writeTimeout bounds the detached produce so a stuck broker cannot leak
// goroutines indefinitely (billingemit parity).
const writeTimeout = 10 * time.Second

// Topic is the request.logged Kafka topic (data-models §4.4).
const Topic = "request.logged"

// Emitter emits a request-log event. Implementations MUST be fire-and-forget —
// Emit never blocks the caller meaningfully and never surfaces an error
// (BR-ING-4: the chat/embeddings response is already served).
type Emitter interface {
	Emit(ctx context.Context, ev *analyticsv1.UsageLogEvent)
}

// Nop is the default no-op emitter (analytics disabled / tests).
type Nop struct{}

// Emit does nothing.
func (Nop) Emit(context.Context, *analyticsv1.UsageLogEvent) {}

// MessageWriter is the minimal segmentio writer surface (satisfied by
// *kafka.Writer; tests inject a fake).
type MessageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// KafkaEmitter produces request.logged events.
type KafkaEmitter struct {
	w        MessageWriter
	logger   *slog.Logger
	failures metric.Int64Counter
	onDone   func() // test hook: invoked when a detached produce attempt completes
}

// NewKafkaEmitter builds a producer over the supplied writer. logger may be nil.
func NewKafkaEmitter(w MessageWriter, logger *slog.Logger) *KafkaEmitter {
	if logger == nil {
		logger = slog.Default()
	}
	failures, _ := otel.Meter("apps/api-gateway/internal/analyticslog").Int64Counter(
		"he_analytics_emit_failures_total",
		metric.WithDescription("request.logged producer failures (response unaffected — fire-and-forget)"),
	)
	return &KafkaEmitter{w: w, logger: logger, failures: failures}
}

// Emit marshals the event (protojson) and writes it keyed by user_id (partition
// stability). The produce runs in a DETACHED goroutine with a bounded timeout so
// it never delays the connection's reuse and never affects the already-served
// response (BR-ING-4). A marshal/write error is swallowed — logged + counted.
func (e *KafkaEmitter) Emit(ctx context.Context, ev *analyticsv1.UsageLogEvent) {
	body, err := protojson.Marshal(ev)
	if err != nil {
		e.fail(ctx, "marshal", ev, err)
		return
	}
	msg := kafka.Message{
		Topic: Topic,
		Key:   []byte(ev.GetUserId()),
		Value: body,
	}
	go func() {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()
		if err := e.w.WriteMessages(wctx, msg); err != nil {
			e.fail(wctx, "write", ev, err)
		}
		e.done()
	}()
}

func (e *KafkaEmitter) done() {
	if e.onDone != nil {
		e.onDone()
	}
}

func (e *KafkaEmitter) fail(ctx context.Context, stage string, ev *analyticsv1.UsageLogEvent, err error) {
	if e.failures != nil {
		e.failures.Add(ctx, 1)
	}
	e.logger.WarnContext(ctx, "analytics_emit_failed",
		slog.String("event", "analytics_emit_failed"),
		slog.String("stage", stage),
		slog.String("he_request_id", ev.GetHeRequestId()),
		slog.String("error", err.Error()),
	)
}
