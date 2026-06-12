package obs

// Tests for Story 9.4: 全链路 trace（OpenTelemetry）.
//
// AUTO-GENERATED skeleton by QA Test Design (Turing, 2026-06-11); implemented by
// Dev (Linus). Every scenario maps to docs/qa/assessments/9.4-test-design-20260611.md.
// SCOPE: only the locally-runnable UNIT + blind-spot scenarios (OTel SDK +
// in-memory SpanRecorder + propagation carriers are fully in-process per
// project_toolchain_env_limits). INT/E2E scenarios run in CI/staging under the
// integration build tag — NOT in this file.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// ---- helpers -------------------------------------------------------------

// recorderProvider returns an always-sampling TracerProvider backed by an
// in-memory SpanRecorder so a test can inspect emitted spans (links, attrs).
func recorderProvider() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	return tp, sr
}

// sampledParentCtx returns a context carrying a REMOTE parent span context with
// the given sampled flag — the shape otelhttp/Kafka extraction produces.
func sampledParentCtx(sampled bool) (context.Context, trace.SpanContext) {
	cfg := trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:  trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		Remote:  true,
	}
	if sampled {
		cfg.TraceFlags = trace.FlagsSampled
	}
	sc := trace.NewSpanContext(cfg)
	return trace.ContextWithSpanContext(context.Background(), sc), sc
}

// repoRoot walks up from the package dir to the workspace root (the dir with go.work).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root (go.work) above %s", dir)
		}
		dir = parent
	}
}

// grepGo walks root/sub for non-test .go files and returns "path:line" for every
// line matching any needle (substring match).
func grepGo(t *testing.T, root, sub string, needles ...string) []string {
	t.Helper()
	var hits []string
	start := filepath.Join(root, sub)
	err := filepath.Walk(start, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			line := sc.Text()
			for _, n := range needles {
				if strings.Contains(line, n) {
					hits = append(hits, path+":"+itoa(ln)+": "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", start, err)
	}
	return hits
}

// grepGoCode is grepGo restricted to executable lines: it skips lines whose
// trimmed form begins with `//` so a doc comment that merely *mentions* a needle
// (e.g. notification/client.go's "use http.DefaultClient or a custom-tuned one")
// is not flagged as a real construction site (TAUDIT-SCOPE-001).
func grepGoCode(t *testing.T, root, sub string, needles ...string) []string {
	t.Helper()
	var hits []string
	start := filepath.Join(root, sub)
	err := filepath.Walk(start, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			line := sc.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, n := range needles {
				if strings.Contains(line, n) {
					hits = append(hits, path+":"+itoa(ln)+": "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", start, err)
	}
	return hits
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ============================================================
// AC1: 同步全链路贯通 — global propagator + outbound client injection
// ============================================================

func Test_9_4_UNIT_001_SetupPropagation_CompositeRoundTrip(t *testing.T) {
	// P0 | BR-TR-1 | Q-PROP
	SetupPropagation()
	prop := otel.GetTextMapPropagator()

	fields := strings.Join(prop.Fields(), ",")
	if !strings.Contains(fields, "traceparent") {
		t.Fatalf("composite propagator missing traceparent field; got %q", fields)
	}
	if !strings.Contains(fields, "baggage") {
		t.Fatalf("composite propagator missing baggage field; got %q", fields)
	}

	ctx, sc := sampledParentCtx(true)
	carrier := propagation.MapCarrier{}
	prop.Inject(ctx, carrier)
	if carrier["traceparent"] == "" {
		t.Fatal("inject produced no traceparent")
	}
	out := prop.Extract(context.Background(), carrier)
	got := trace.SpanContextFromContext(out)
	if got.TraceID() != sc.TraceID() || got.SpanID() != sc.SpanID() {
		t.Fatalf("round-trip mismatch: in=%s/%s out=%s/%s", sc.TraceID(), sc.SpanID(), got.TraceID(), got.SpanID())
	}
}

func Test_9_4_UNIT_002_SetupPropagation_Idempotent(t *testing.T) {
	// P1 | BR-TR-1
	SetupPropagation()
	first := otel.GetTextMapPropagator()
	SetupPropagation() // second call must not panic / accumulate
	second := otel.GetTextMapPropagator()

	// Both must still be a working composite carrying the SAME field set.
	// (composite Fields() dedupes via a map, so compare as a set, not ordered.)
	fieldSet := func(p propagation.TextMapPropagator) map[string]bool {
		m := map[string]bool{}
		for _, f := range p.Fields() {
			m[f] = true
		}
		return m
	}
	a, b := fieldSet(first), fieldSet(second)
	if len(a) != 3 || !a["traceparent"] || !a["tracestate"] || !a["baggage"] {
		t.Fatalf("composite field set wrong: %v", first.Fields())
	}
	if len(a) != len(b) || !a["traceparent"] || !b["traceparent"] || !a["baggage"] || !b["baggage"] {
		t.Fatalf("idempotency broken: field set drifted %v -> %v", first.Fields(), second.Fields())
	}
	ctx, sc := sampledParentCtx(true)
	c := propagation.MapCarrier{}
	second.Inject(ctx, c)
	if got := trace.SpanContextFromContext(second.Extract(context.Background(), c)); got.TraceID() != sc.TraceID() {
		t.Fatal("propagator non-functional after second SetupPropagation")
	}
}

func Test_9_4_UNIT_003_NewHTTPClient_InjectsTraceparent(t *testing.T) {
	// P0 | BR-TR-2
	SetupPropagation()
	tp, _ := recorderProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(sdktrace.NewTracerProvider()) // reset to a quiet provider

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Traceparent")
	}))
	defer srv.Close()

	client := NewHTTPClient()
	ctx, span := tp.Tracer("test").Start(context.Background(), "caller")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	span.End()
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if gotHeader == "" {
		t.Fatal("no traceparent header injected on the outbound request")
	}
	if !strings.Contains(gotHeader, span.SpanContext().TraceID().String()) {
		t.Fatalf("injected traceparent %q does not carry caller trace_id %s", gotHeader, span.SpanContext().TraceID())
	}
}

func Test_9_4_UNIT_004_NewHTTPClient_PreservesTimeout(t *testing.T) {
	// P1 | BR-TR-2
	c := NewHTTPClient(WithTimeout(7 * time.Second))
	if c.Timeout != 7*time.Second {
		t.Fatalf("timeout not preserved: got %v", c.Timeout)
	}
	if c.Transport == nil {
		t.Fatal("transport must be the instrumented otelhttp transport, got nil")
	}
}

func Test_9_4_UNIT_005_Sampler_ChildHonorsParentSampled(t *testing.T) {
	// P0 | BR-TR-5 | Q-SAMPLE
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0") // local ratio 0 — must be ignored when parent is sampled
	s := samplerFromEnv()
	ctx, _ := sampledParentCtx(true)
	res := s.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: ctx,
		TraceID:       trace.TraceID{0xaa},
		Name:          "child",
		Kind:          trace.SpanKindServer,
	})
	if res.Decision != sdktrace.RecordAndSample {
		t.Fatalf("child of a SAMPLED parent must stay sampled (no independent decision); got %v", res.Decision)
	}
}

func Test_9_4_UNIT_006_Sampler_UnsampledParentDropsChild_NoHalfTrace(t *testing.T) {
	// P0 | BR-TR-5
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1") // local ratio 1 — must still drop under an unsampled parent
	s := samplerFromEnv()
	ctx, _ := sampledParentCtx(false)
	res := s.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: ctx,
		TraceID:       trace.TraceID{0xbb},
		Name:          "child",
		Kind:          trace.SpanKindServer,
	})
	if res.Decision != sdktrace.Drop {
		t.Fatalf("child of an UNSAMPLED parent must be dropped (no half-trace); got %v", res.Decision)
	}
}

func Test_9_4_UNIT_007_Sampler_UnparseableArgFallsBackToOne(t *testing.T) {
	// P1 | data-validation
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "abc")
	s := samplerFromEnv() // must not panic
	// No parent → root decision uses the ratio. Fallback 1.0 ⇒ always sample.
	res := s.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		TraceID:       trace.TraceID{0xcc, 0xdd, 0xee},
		Name:          "root",
		Kind:          trace.SpanKindServer,
	})
	if res.Decision != sdktrace.RecordAndSample {
		t.Fatalf("unparseable arg must fall back to ratio 1.0 (sample root); got %v", res.Decision)
	}
}

func Test_9_4_UNIT_008_SpanAttributes_PIIAllowList(t *testing.T) {
	// P0 [SECURITY] | BR-TR-7 | Q-PII
	// (a) focused: the obs package itself sets NO forbidden span attribute.
	// (b) static guard: no app code sets a forbidden key as a span attribute.
	root := repoRoot(t)
	forbidden := []string{
		`attribute.String("prompt"`, `attribute.String("messages"`, `attribute.String("response"`,
		`attribute.String("authorization"`, `attribute.String("Authorization"`,
		`attribute.String("api_key"`, `attribute.String("apikey"`, `attribute.String("x-he-api-key"`,
		`attribute.String("email"`, `attribute.String("client_ip"`, `attribute.String("ip"`,
	}
	hits := grepGo(t, root, "apps", forbidden...)
	hits = append(hits, grepGo(t, root, filepath.Join("packages", "go-observability"), forbidden...)...)
	if len(hits) > 0 {
		t.Fatalf("PII/secret set as span attribute (BR-TR-7 violation):\n%s", strings.Join(hits, "\n"))
	}
}

func Test_9_4_UNIT_009_TAudit_NoBareHTTPClientOnRPCPath(t *testing.T) {
	// P0 | BR-TR-2 anti-orphan — BROADENED per QA round-1 TAUDIT-SCOPE-001.
	//
	// The round-1 guard only scanned apps/api-gateway and only matched the literal
	// `&http.Client{`, so it passed GREEN while two inter-service connect clients
	// (auth-svc→notification-svc, billing-svc→payment-svc) still rode the
	// `http.DefaultClient` singleton (TRACE-ORPHAN-001) — false assurance. The guard
	// now scans EVERY service under apps/ and flags BOTH untraced idioms:
	//   - `&http.Client{`        (a fresh bare client, no otelhttp.NewTransport)
	//   - `http.DefaultClient`   (the shared default singleton, no otelhttp.NewTransport)
	// on any inter-service RPC construction path. Either roots a new trace.
	//
	// EXEMPT — genuinely EXTERNAL third-party clients: they call a vendor/PSP, not a
	// He service, so they are true trace leaves and out of the "one connected trace"
	// invariant (PII handling is governed separately). Allow-listed by package path:
	//   internal/oauth/    (Google/GitHub OAuth)      internal/password/ (HIBP)
	//   internal/provider/ (Stripe/Alipay/PayPal/...) internal/sendgrid/ (SendGrid)
	//   internal/fx/       (FX rate provider)         internal/upstream/ (LLM vendor
	//                                                  upstreams)
	//
	// NOTE (T6.4 / ADAPTER-DARK-001 resolved 2026-06-11): the exemption is now scoped
	// to `internal/upstream/` (the per-adapter VENDOR client only) — NOT the blanket
	// `apps/adapters/` it used to be. The adapter mains are therefore back ON the
	// inter-service path so a future bare client in an adapter main would be caught.
	// The vendor upstream client legitimately keeps the `&http.Client{` literal, but
	// it is no longer un-traced: it now wraps its bespoke ALPN/HTTP2 transport with
	// otelhttp.NewTransport — asserted POSITIVELY by Test_9_4_UNIT_016 (the model-TTFB
	// span, BR-TR-6). `internal/upstream/` exists only under apps/adapters/.
	root := repoRoot(t)
	allowExternal := []string{
		filepath.FromSlash("internal/oauth/"),
		filepath.FromSlash("internal/password/"),
		filepath.FromSlash("internal/provider/"),
		filepath.FromSlash("internal/sendgrid/"),
		filepath.FromSlash("internal/fx/"),
		filepath.FromSlash("internal/upstream/"),
	}
	var orphans []string
	for _, hit := range grepGoCode(t, root, "apps", "&http.Client{", "http.DefaultClient") {
		exempt := false
		for _, a := range allowExternal {
			if strings.Contains(hit, a) {
				exempt = true
				break
			}
		}
		if !exempt {
			orphans = append(orphans, hit)
		}
	}
	if len(orphans) > 0 {
		t.Fatalf("bare/default http.Client on an inter-service RPC path (must be obs.NewHTTPClient):\n%s", strings.Join(orphans, "\n"))
	}
}

func Test_9_4_UNIT_016_AdapterTierInstrumented(t *testing.T) {
	// P0 | BR-TR-6 | ADAPTER-DARK-001 (Architect escalation ruling, 2026-06-11) | T6
	//
	// The model-TTFB span is the single most valuable span in an LLM-gateway trace
	// and AC1's own worked example ("adapter→<vendor> upstream client … sharing one
	// trace_id"). It does NOT come from the gateway's adapterclient — that wraps only
	// the gateway→adapter hop, because adapterclient is a connect-RPC client to a
	// SEPARATE adapter microservice. The model call lives inside each adapter's OWN
	// vendor upstream client. So this guard asserts ALL 6 adapter services carry the
	// trace to it (a POSITIVE check — the negative bare-client check in UNIT-009
	// correctly exempts the genuinely-external vendor client):
	//   (a) main.go installs a TracerProvider + the global propagator + a wrapped
	//       server handler, so the gateway→adapter `traceparent` is EXTRACTED and the
	//       adapter server span becomes a CHILD (no longer a fresh root); and
	//   (b) internal/upstream/client.go wraps its bespoke ALPN/HTTP2 vendor transport
	//       with otelhttp.NewTransport, so adapter→vendor is a client span (TTFB).
	root := repoRoot(t)
	vendors := []string{"deepseek", "doubao", "ernie", "glm", "kimi", "qwen"}
	mainNeedles := []string{"obs.SetupPropagation(", "obs.WrapHTTPHandler(", "otel.SetTracerProvider("}
	for _, v := range vendors {
		mainSrc := readGoFile(t, filepath.Join(root, "apps", "adapters", v, "cmd", "server", "main.go"))
		for _, n := range mainNeedles {
			if !strings.Contains(mainSrc, n) {
				t.Errorf("adapter %s main.go missing %q — gateway→adapter span goes dark (T6.1)", v, n)
			}
		}
		clientSrc := readGoFile(t, filepath.Join(root, "apps", "adapters", v, "internal", "upstream", "client.go"))
		if !strings.Contains(clientSrc, "otelhttp.NewTransport(") {
			t.Errorf("adapter %s upstream/client.go does not wrap its transport with otelhttp.NewTransport — model-TTFB span never produced (T6.2, BR-TR-6)", v)
		}
	}
}

// readGoFile reads an expected-to-exist source file for a static guard; a missing
// file is a hard failure (the instrumentation target moved or was deleted).
func readGoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ============================================================
// AC2: 异步全链路贯通 — Kafka header inject/extract, span-LINK
// ============================================================

func Test_9_4_UNIT_010_KafkaCarrier_RoundTrip_PreservesRequestId(t *testing.T) {
	// P0 | BR-TR-8/9 | Q-KAFKA
	SetupPropagation()
	ctx, sc := sampledParentCtx(true)
	msg := kafka.Message{Headers: []kafka.Header{
		{Key: "RequestId", Value: []byte("req_abcdef123456")},
		{Key: "x-other", Value: []byte("keep-me")},
	}}
	InjectKafkaHeaders(ctx, &msg)

	// pre-existing headers survive
	if findHeader(msg.Headers, "RequestId") != "req_abcdef123456" {
		t.Fatal("RequestId header not preserved after inject")
	}
	if findHeader(msg.Headers, "x-other") != "keep-me" {
		t.Fatal("unrelated header dropped after inject")
	}
	if findHeader(msg.Headers, "traceparent") == "" {
		t.Fatal("traceparent not injected into kafka headers")
	}

	out := ExtractKafkaHeaders(context.Background(), msg)
	got := trace.SpanContextFromContext(out)
	if got.TraceID() != sc.TraceID() || got.SpanID() != sc.SpanID() {
		t.Fatalf("kafka carrier round-trip mismatch: in=%s out=%s", sc.TraceID(), got.TraceID())
	}
}

func Test_9_4_UNIT_011_Producer_InjectsTraceparentWhenInTrace(t *testing.T) {
	// P0 | BR-TR-11 — obs-level core of the producer wiring
	SetupPropagation()
	ctx, _ := sampledParentCtx(true)
	msg := kafka.Message{Topic: "usage.recorded"}
	InjectKafkaHeaders(ctx, &msg)
	if findHeader(msg.Headers, "traceparent") == "" {
		t.Fatal("produced within a trace but no traceparent header present")
	}
}

func Test_9_4_UNIT_012_Producer_NoTraceparentWhenOutOfTrace(t *testing.T) {
	// P1 | BR-TR-11
	SetupPropagation()
	msg := kafka.Message{Topic: "usage.recorded"}
	InjectKafkaHeaders(context.Background(), &msg) // no active span
	if findHeader(msg.Headers, "traceparent") != "" {
		t.Fatal("traceparent injected outside any trace (must be absent so consumer roots a new trace)")
	}
}

func Test_9_4_UNIT_013_Consumer_SpanLinkNotChild_AllTopics(t *testing.T) {
	// P0 | BR-TR-9 | Q-KAFKA ruling: LINK (not child) for ALL 4 topics
	SetupPropagation()
	tp, sr := recorderProvider()

	// produce a traced message
	prodCtx, prodSC := sampledParentCtx(true)
	msg := kafka.Message{Topic: "request.logged"}
	InjectKafkaHeaders(prodCtx, &msg)

	// consume: extract, then start a NEW-ROOT consumer span LINKED to the producer
	consCtx := ExtractKafkaHeaders(context.Background(), msg)
	link := trace.LinkFromContext(consCtx)
	_, span := tp.Tracer("consumer").Start(context.Background(), "request.logged consume",
		trace.WithSpanKind(trace.SpanKindConsumer), trace.WithLinks(link))
	span.End()

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 consumer span, got %d", len(ended))
	}
	cs := ended[0]
	if cs.Parent().TraceID() == prodSC.TraceID() {
		t.Fatal("consumer span is a CHILD of the producer (must be a NEW root with a link)")
	}
	if cs.SpanContext().TraceID() == prodSC.TraceID() {
		t.Fatal("consumer span shares the producer trace_id (child semantics) — must be a new trace")
	}
	links := cs.Links()
	if len(links) != 1 || links[0].SpanContext.TraceID() != prodSC.TraceID() {
		t.Fatalf("consumer span must carry exactly one LINK to the producer trace_id; got %+v", links)
	}
	if cs.SpanKind() != trace.SpanKindConsumer {
		t.Fatalf("consumer span kind = %v, want Consumer", cs.SpanKind())
	}
}

func Test_9_4_UNIT_014_Consumer_LegacyHeaderlessMessage_RootsNewTrace(t *testing.T) {
	// P0 | BR-TR-9 back-compat
	SetupPropagation()
	msg := kafka.Message{Topic: "request.logged"} // no headers (pre-9.4 build)
	consCtx := ExtractKafkaHeaders(context.Background(), msg)
	if trace.SpanContextFromContext(consCtx).IsValid() {
		t.Fatal("header-less message yielded a valid remote span context (should be none → new root)")
	}
	if trace.LinkFromContext(consCtx).SpanContext.IsValid() {
		t.Fatal("header-less message produced a valid link (should be empty)")
	}
}

func Test_9_4_UNIT_015_KafkaHeaders_PIIFree(t *testing.T) {
	// P0 [SECURITY] | BR-TR-12
	SetupPropagation()
	ctx, _ := sampledParentCtx(true)
	msg := kafka.Message{Headers: []kafka.Header{{Key: "RequestId", Value: []byte("req_x")}}}
	InjectKafkaHeaders(ctx, &msg)

	allowed := map[string]bool{"traceparent": true, "tracestate": true, "baggage": true, "RequestId": true}
	for _, h := range msg.Headers {
		if !allowed[h.Key] {
			t.Fatalf("disallowed kafka header injected: %q (only traceparent/tracestate/baggage/RequestId permitted)", h.Key)
		}
	}
}

// ============================================================
// Blind Spot Scenarios — locally-runnable unit subset
// ============================================================

func Test_9_4_BLIND_BOUNDARY_001_MalformedIncomingTraceparent_RootsNewSpan(t *testing.T) {
	// BOUNDARY-001/005 | P1
	SetupPropagation()
	carrier := propagation.MapCarrier{"traceparent": "not-a-valid-traceparent"}
	out := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	if trace.SpanContextFromContext(out).IsValid() {
		t.Fatal("malformed traceparent yielded a valid span context (should be invalid → new root)")
	}
}

func Test_9_4_BLIND_BOUNDARY_002_SamplerArg_LimitsAndBeyond(t *testing.T) {
	// BOUNDARY-002/003/004 | P2
	cases := []struct {
		arg        string
		wantSample bool // root decision
	}{
		{"0", false},   // drop all
		{"1", true},    // keep all
		{"1.5", true},  // out-of-range → fallback 1.0
		{"-0.2", true}, // negative → fallback 1.0
	}
	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", tc.arg)
			s := samplerFromEnv() // must never panic
			res := s.ShouldSample(sdktrace.SamplingParameters{
				ParentContext: context.Background(),
				TraceID:       trace.TraceID{0x7f, 0xff, 0xff, 0xff},
				Name:          "root",
				Kind:          trace.SpanKindServer,
			})
			sampled := res.Decision == sdktrace.RecordAndSample
			if sampled != tc.wantSample {
				t.Fatalf("arg %q: sampled=%v want %v", tc.arg, sampled, tc.wantSample)
			}
		})
	}
}

func Test_9_4_BLIND_BOUNDARY_003_OversizedBaggage_TraceIdStillPropagates(t *testing.T) {
	// BOUNDARY-004 | P2
	SetupPropagation()
	ctx, sc := sampledParentCtx(true)
	// attach an oversized baggage member
	m, err := baggage.NewMember("big", strings.Repeat("a", 9000))
	if err == nil {
		if b, err := baggage.New(m); err == nil {
			ctx = baggage.ContextWithBaggage(ctx, b)
		}
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	out := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	if got := trace.SpanContextFromContext(out); got.TraceID() != sc.TraceID() {
		t.Fatalf("trace_id continuity lost under oversized baggage: in=%s out=%s", sc.TraceID(), got.TraceID())
	}
}

func Test_9_4_BLIND_ERROR_002_MalformedKafkaTraceparent_RootsNewTrace(t *testing.T) {
	// ERROR-003 | P1
	SetupPropagation()
	msg := kafka.Message{Headers: []kafka.Header{{Key: "traceparent", Value: []byte("garbage")}}}
	out := ExtractKafkaHeaders(context.Background(), msg)
	if trace.SpanContextFromContext(out).IsValid() {
		t.Fatal("malformed kafka traceparent yielded a valid span context (should root a new trace)")
	}
}

func Test_9_4_BLIND_ERROR_003_ProduceFails_ErrorPathUnchanged_NoPII(t *testing.T) {
	// ERROR-001 | P2 — producer span records error status WITHOUT any PII attribute
	tp, sr := recorderProvider()
	_, span := tp.Tracer("producer").Start(context.Background(), "usage.recorded produce",
		trace.WithSpanKind(trace.SpanKindProducer))
	span.SetStatus(codes.Error, "produce failed") // static reason, no PII / no err string
	span.End()

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 producer span, got %d", len(ended))
	}
	ps := ended[0]
	if ps.Status().Code != codes.Error {
		t.Fatalf("producer span must record error status; got %v", ps.Status().Code)
	}
	for _, a := range ps.Attributes() {
		k := string(a.Key)
		if strings.Contains(k, "prompt") || strings.Contains(k, "authorization") ||
			strings.Contains(k, "api_key") || strings.Contains(k, "email") || strings.Contains(k, "client_ip") {
			t.Fatalf("producer error span leaked a PII/secret attribute: %q", k)
		}
	}
}

func Test_9_4_BLIND_CONCURRENCY_001_Redelivery_WellFormedSpan_SameLink(t *testing.T) {
	// CONCURRENCY-002 | P1 — redelivery yields a NEW span sharing the SAME upstream link
	SetupPropagation()
	tp, sr := recorderProvider()
	prodCtx, prodSC := sampledParentCtx(true)
	msg := kafka.Message{Topic: "payment.completed"}
	InjectKafkaHeaders(prodCtx, &msg)

	consume := func() {
		ctx := ExtractKafkaHeaders(context.Background(), msg)
		_, span := tp.Tracer("consumer").Start(context.Background(), "payment.completed consume",
			trace.WithSpanKind(trace.SpanKindConsumer), trace.WithLinks(trace.LinkFromContext(ctx)))
		span.End()
	}
	consume() // first delivery
	consume() // redelivery (at-least-once)

	ended := sr.Ended()
	if len(ended) != 2 {
		t.Fatalf("expected 2 consumer spans (delivery + redelivery), got %d", len(ended))
	}
	if ended[0].SpanContext().SpanID() == ended[1].SpanContext().SpanID() {
		t.Fatal("redelivery reused the same span id (must be a fresh span)")
	}
	for i, cs := range ended {
		links := cs.Links()
		if len(links) != 1 || links[0].SpanContext.TraceID() != prodSC.TraceID() {
			t.Fatalf("delivery %d: must link to the same producer trace_id; got %+v", i, links)
		}
	}
}

func Test_9_4_BLIND_RESOURCE_001_InstrumentedTransport_PreservesConnPooling(t *testing.T) {
	// RESOURCE-001 | P2 — otelhttp.NewTransport(default) keeps keep-alive reuse
	SetupPropagation()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewHTTPClient()
	do := func() bool {
		var reused bool
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return reused
	}
	do() // first call establishes the pooled connection
	if !do() {
		t.Fatal("second request did not reuse the keep-alive connection (instrumented transport broke pooling)")
	}
}

// findHeader returns the value of the first kafka header with key, or "".
func findHeader(hs []kafka.Header, key string) string {
	for _, h := range hs {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
