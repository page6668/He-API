//go:build integration

package obs

// Story 9.4 integration scenarios (9.4-INT-*). Build-tagged `integration` so the
// default `go test` stays fast; the CI observability/integration job runs
// `go test -tags=integration ./...`. These run IN-PROCESS (two otelhttp-wrapped
// httptest servers + an in-memory SpanRecorder + an in-process Kafka carrier) —
// they need NO docker/collector/Jaeger, so they are runnable here unlike the
// full staging E2E (9.4-E2E-001), which stays a documented stub
// (project_toolchain_env_limits).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// 9.4-INT-001: a request through ≥2 services yields ONE trace_id and the
// downstream server span is a CHILD of the caller (not a fresh root).
func Test_9_4_INT_001_ServerSpanIsChildAcrossTwoServices(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	otel.SetTracerProvider(tp)
	SetupPropagation()

	// "service B" — its otelhttp server handler must EXTRACT the incoming
	// traceparent (global propagator) and open a CHILD server span.
	svcB := httptest.NewServer(WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), "svcB"))
	defer svcB.Close()

	// "service A" — on each request it calls service B via the instrumented client.
	client := NewHTTPClient()
	svcA := httptest.NewServer(WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, svcB.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		resp.Body.Close()
		w.WriteHeader(http.StatusOK)
	}), "svcA"))
	defer svcA.Close()

	resp, err := http.Get(svcA.URL)
	if err != nil {
		t.Fatalf("drive request: %v", err)
	}
	resp.Body.Close()

	// Collect the two SERVER spans (svcA root server span + svcB child server span).
	var serverSpans []sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.SpanKind() == trace.SpanKindServer {
			serverSpans = append(serverSpans, s)
		}
	}
	if len(serverSpans) < 2 {
		t.Fatalf("expected ≥2 server spans (svcA, svcB), got %d", len(serverSpans))
	}
	// All recorded spans must share ONE trace_id (the chain is connected).
	root := serverSpans[0].SpanContext().TraceID()
	for _, s := range sr.Ended() {
		if s.SpanContext().TraceID() != root {
			t.Fatalf("trace broken: span %q has trace_id %s, want %s", s.Name(), s.SpanContext().TraceID(), root)
		}
	}
	// At least one server span must be a CHILD (valid parent within the trace) —
	// proves the downstream server is no longer a fresh root.
	sawChild := false
	for _, s := range serverSpans {
		if s.Parent().IsValid() && s.Parent().TraceID() == root {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatal("no downstream server span became a CHILD — propagator/instrumentation regression")
	}
}

// 9.4-INT-004 (in-process analogue): a real produce→consume over kafka headers
// carries the producer trace_id into the consumer span's LINK (link mode).
func Test_9_4_INT_004_KafkaLinkCarriesTraceID(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	otel.SetTracerProvider(tp)
	SetupPropagation()

	// produce within a trace
	pctx, span := tp.Tracer("producer").Start(context.Background(), "request.logged produce",
		trace.WithSpanKind(trace.SpanKindProducer))
	producerTraceID := span.SpanContext().TraceID()
	msg := kafka.Message{Topic: "request.logged"}
	InjectKafkaHeaders(pctx, &msg)
	span.End()

	// consume: StartConsumerSpan must LINK back to the producer trace_id (new root)
	_, cspan := StartConsumerSpan(context.Background(), "consumer", "request.logged consume", msg)
	cspan.End()

	var consumer sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.SpanKind() == trace.SpanKindConsumer {
			consumer = s
		}
	}
	if consumer == nil {
		t.Fatal("no consumer span recorded")
	}
	if consumer.SpanContext().TraceID() == producerTraceID {
		t.Fatal("consumer shares producer trace_id (child) — must be a NEW root with a link")
	}
	links := consumer.Links()
	if len(links) != 1 || links[0].SpanContext.TraceID() != producerTraceID {
		t.Fatalf("consumer link must carry producer trace_id %s; got %+v", producerTraceID, links)
	}
}
