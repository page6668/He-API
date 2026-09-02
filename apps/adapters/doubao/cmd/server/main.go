// Command server is the Doubao (Volcengine Ark v3) adapter's Connect-RPC
// bootstrap. Listens on $PORT (default 8080), exposes the
// AdapterService.Chat RPC, talks upstream to the Volcengine Ark v3
// OpenAI-compatible API via HTTPS (HTTP/2-preferred per Architect Round
// 1 OQ-4.5-6 ruling — cascade default from Story-4.2 OQ-4.2-5).
//
// Environment configuration:
//
//	PORT                                — listen port (default 8080)
//	DOUBAO_UPSTREAM_BASE_URL            — default https://ark.cn-beijing.volces.com
//	DOUBAO_UPSTREAM_API_KEY             — Bearer token (required; sourced via
//	                                       Vault per OQ3 ruling — path
//	                                       kv/data/he-api/upstream/doubao/)
//	DOUBAO_UPSTREAM_TIMEOUT_SECONDS     — per-request deadline (default 60)
//	DOUBAO_BOUND_MODEL_IDS              — comma-separated model id list
//	                                       (default "doubao-pro,doubao-lite" per
//	                                       BR-1.10 N=2; informs slog tagging
//	                                       + Helm supportedModels ConfigMap)
//	DOUBAO_PRO_ENDPOINT_ID              — Volcengine endpoint id for `doubao-pro`
//	                                       (REQUIRED for pro traffic; BR-1.12
//	                                       fail-fast if unset; sourced from
//	                                       ConfigMap `doubao-endpoint-ids` per
//	                                       OQ-4.5-4 ratification)
//	DOUBAO_LITE_ENDPOINT_ID             — Volcengine endpoint id for `doubao-lite`
//	                                       (REQUIRED for lite traffic; BR-1.12
//	                                       fail-fast if unset)
//
// Cold-start budget per Story-4.1 M5 inheritance: ≤ 2s. K8s readiness
// probe gates traffic until the Connect-RPC handler is registered.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	doubaointernal "github.com/he-api/he-api/apps/adapters/doubao/internal"
	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	obs "github.com/he-api/he-api/packages/go-observability"
	providerrefresher "github.com/he-api/he-api/packages/provider-refresher"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
	"go.opentelemetry.io/otel"
)

// DefaultUpstreamTimeout mirrors upstream.DefaultUpstreamTimeout for the
// startup-default path.
const DefaultUpstreamTimeout = 60 * time.Second

// OTel resource identity (Story 9.4 T6.1). serviceNS matches the sibling
// services ("he-api-staging") so Jaeger groups the adapter tier with the rest
// of the platform.
const (
	serviceName    = "adapter-doubao"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
)

// defaultBoundModelIDs is the BR-1.10 default model-id list — Story 4.5
// is the FIRST Epic-4 adapter to RESTORE the N=2 multi-model-id-per-service
// case after Story-4.4's N=1 detour. The list informs slog `supportedModels`
// observability + the Helm ConfigMap; per-call model dispatch reads
// `req.Model` directly (verified via 4.5-UNIT-013 `assert.Same(h_pro, h_lite)`
// invariant on the gateway side).
var defaultBoundModelIDs = []string{"doubao-pro", "doubao-lite"}

// asrModelID is the catalogue/registry id for the Doubao ASR model. Both the
// observability auto-append (when ASR is configured) and the M-2 boot gate key
// on it, so they cannot drift.
const asrModelID = "doubao-asr"

// ttsModelID is the catalogue/registry id for the Doubao TTS model (Story 9.7).
// The observability auto-append (when TTS is configured) and the conditional
// boot gate key on it, so they cannot drift.
const ttsModelID = "doubao-tts"

// asrBootGateError implements the M-2 Round-2 Architect ruling (CONDITIONAL
// boot fail-fast). An operator who EXPLICITLY binds `doubao-asr` (declares
// intent to serve ASR) but leaves the ASR upstream env unset has a deploy-time
// misconfiguration that MUST surface at boot — not at a paying customer's first
// Transcribe on a money-handling, security-sensitive endpoint. A deployment
// that does NOT bind `doubao-asr` (e.g. the chat-only default) boots normally
// and relies on the request-time connect.CodeUnavailable defence-in-depth
// (asr_transcribe.go), so the shared Doubao chat service is never regressed.
// Returns a non-nil error (naming the missing envs) only when the gate trips;
// when asrConfigured is true it always returns nil.
func asrBootGateError(boundModelIDs []string, asrConfigured bool) error {
	if asrConfigured {
		return nil
	}
	for _, id := range boundModelIDs {
		if id == asrModelID {
			return errors.New("doubao-asr is in DOUBAO_BOUND_MODEL_IDS but the ASR upstream is not configured — set DOUBAO_ASR_UPSTREAM_{API_TOKEN,APP_ID,CLUSTER}")
		}
	}
	return nil
}

// ttsBootGateError is the Story-9.7 mirror of asrBootGateError (the same M-2
// CONDITIONAL boot fail-fast): if the operator EXPLICITLY binds `doubao-tts` but
// leaves the TTS upstream env unset, that deploy-time misconfiguration MUST
// surface at boot — never at a paying customer's first Synthesize on a
// money-handling endpoint. A deployment that does NOT bind `doubao-tts` boots
// normally and relies on the request-time CodeUnavailable defence-in-depth.
func ttsBootGateError(boundModelIDs []string, ttsConfigured bool) error {
	if ttsConfigured {
		return nil
	}
	for _, id := range boundModelIDs {
		if id == ttsModelID {
			return errors.New("doubao-tts is in DOUBAO_BOUND_MODEL_IDS but the TTS upstream is not configured — set DOUBAO_TTS_UPSTREAM_{API_TOKEN,APP_ID,CLUSTER}")
		}
	}
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("DOUBAO_UPSTREAM_BASE_URL", "https://ark.cn-beijing.volces.com")
	apiKey := os.Getenv("DOUBAO_UPSTREAM_API_KEY")
	if apiKey == "" {
		// AD-004: credentials may arrive at runtime from the gateway's
		// /internal/providers/active loopback endpoint. Don't hard-fail at
		// startup; the refresher goroutine below will populate them.
		logger.Warn("DOUBAO_UPSTREAM_API_KEY is not set — waiting for runtime config from gateway")
	}
	timeout := DefaultUpstreamTimeout
	if v := os.Getenv("DOUBAO_UPSTREAM_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	boundModelIDs := defaultBoundModelIDs
	if v := os.Getenv("DOUBAO_BOUND_MODEL_IDS"); v != "" {
		parts := strings.Split(v, ",")
		bound := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				bound = append(bound, p)
			}
		}
		if len(bound) > 0 {
			boundModelIDs = bound
		}
	}

	// Startup-validation log per T0.3: warn (not fatal) if either endpoint
	// id is unset. BR-1.12 fail-fast at request time covers correctness;
	// this log helps operators catch ConfigMap-edit-without-pod-restart
	// drift (per Architect Round 1 OQ-4.5-4 rollover behaviour note).
	endpointMap := upstream.NewFromOS()
	for _, mid := range boundModelIDs {
		if _, err := endpointMap.Lookup(mid); err != nil {
			logger.Warn("endpoint id not configured at startup",
				slog.String("event", "adapter_startup_validation"),
				slog.String("model", mid),
				slog.String("error", err.Error()),
			)
		}
	}

	client := upstream.NewClient(baseURL, apiKey, timeout)

	// AD-004: hot-reload upstream credentials from the gateway's loopback-only
	// /internal/providers/active endpoint. Runs for the process lifetime.
	gwURL := envOr("HE_API_PROVIDER_REFRESH_URL", "http://127.0.0.1:8080")
	refresher := providerrefresher.New(gwURL, "doubao", client, logger)
	go refresher.Start(context.Background())
	svc := doubaointernal.NewService(client, endpointMap, logger, boundModelIDs)

	// Story 9.6 — Volcano (豆包语音) ASR upstream. DISTINCT config from the Ark
	// chat client (different product/endpoint). If unset, the adapter still serves
	// Chat normally and Transcribe fail-fasts with CodeUnavailable at request time
	// (the 4.5 BR-1.12 precedent — a chat-only deployment is unaffected). When the
	// ASR envs are present, doubao-asr is served.
	asrCfg := upstream.ASRConfig{
		BaseURL: envOr("DOUBAO_ASR_UPSTREAM_BASE_URL", "https://openspeech.bytedance.com"),
		Path:    envOr("DOUBAO_ASR_UPSTREAM_PATH", upstream.DefaultASRPath),
		Token:   os.Getenv("DOUBAO_ASR_UPSTREAM_API_TOKEN"),
		AppID:   os.Getenv("DOUBAO_ASR_UPSTREAM_APP_ID"),
		Cluster: os.Getenv("DOUBAO_ASR_UPSTREAM_CLUSTER"),
	}
	if asrCfg.Configured() {
		svc = svc.WithASRClient(upstream.NewASRClient(asrCfg, timeout))
		boundModelIDs = append(append([]string{}, boundModelIDs...), asrModelID) // observability tag
		logger.Info("doubao ASR upstream configured", slog.String("event", "asr_startup"), slog.String("base_url", asrCfg.BaseURL))
	} else {
		// M-2 (Round-2 ruling): conditional boot fail-fast. boundModelIDs here is
		// the operator's EXPLICIT bound set (the auto-append above did not run on
		// this branch), so it is the correct signal of declared ASR intent.
		if err := asrBootGateError(boundModelIDs, false); err != nil {
			logger.Error("ASR boot gate failed — refusing to start",
				slog.String("event", "asr_startup_validation"),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}
		logger.Warn("doubao ASR upstream not configured — Transcribe will fail-fast (set DOUBAO_ASR_UPSTREAM_{API_TOKEN,APP_ID,CLUSTER})",
			slog.String("event", "asr_startup_validation"))
	}

	// Story 9.7 — Volcano (火山引擎) TTS upstream. DISTINCT config from the Ark chat
	// + the 9.6 ASR clients (a different product/endpoint). If unset, the adapter
	// still serves Chat + Transcribe normally and Synthesize fail-fasts with
	// CodeUnavailable at request time (the 4.5 BR-1.12 precedent — a chat/ASR-only
	// deployment is unaffected). When the TTS envs are present, doubao-tts is served.
	ttsCfg := upstream.TTSConfig{
		BaseURL:   envOr("DOUBAO_TTS_UPSTREAM_BASE_URL", "https://openspeech.bytedance.com"),
		Path:      envOr("DOUBAO_TTS_UPSTREAM_PATH", upstream.DefaultTTSPath),
		Token:     os.Getenv("DOUBAO_TTS_UPSTREAM_API_TOKEN"),
		AppID:     os.Getenv("DOUBAO_TTS_UPSTREAM_APP_ID"),
		Cluster:   os.Getenv("DOUBAO_TTS_UPSTREAM_CLUSTER"),
		VoiceType: os.Getenv("DOUBAO_TTS_UPSTREAM_VOICE_TYPE"),
	}
	if ttsCfg.Configured() {
		svc = svc.WithTTSClient(upstream.NewTTSClient(ttsCfg, timeout))
		boundModelIDs = append(append([]string{}, boundModelIDs...), ttsModelID) // observability tag
		logger.Info("doubao TTS upstream configured", slog.String("event", "tts_startup"), slog.String("base_url", ttsCfg.BaseURL))
	} else {
		// Conditional boot fail-fast (M-2 ruling, repeated for TTS). boundModelIDs
		// here is the operator's EXPLICIT bound set (the ASR auto-append above may
		// have added asrModelID, which never matches the doubao-tts gate key).
		if err := ttsBootGateError(boundModelIDs, false); err != nil {
			logger.Error("TTS boot gate failed — refusing to start",
				slog.String("event", "tts_startup_validation"),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}
		logger.Warn("doubao TTS upstream not configured — Synthesize will fail-fast (set DOUBAO_TTS_UPSTREAM_{API_TOKEN,APP_ID,CLUSTER})",
			slog.String("event", "tts_startup_validation"))
	}

	// Story 9.4 (T6.1, BR-TR-6): TracerProvider + global W3C propagator BEFORE the
	// handler is built, so the gateway→adapter `traceparent` is EXTRACTED (the
	// adapter server span joins the request trace instead of rooting a new one) and
	// the adapter→vendor model call (upstream/client.go) emits its TTFB client span.
	// Degraded-mode preserved: empty OTEL_EXPORTER_OTLP_ENDPOINT → propagation still
	// installed, spans simply not exported.
	tp, err := obs.NewTracerProvider(context.Background(), serviceName, serviceNS, serviceVersion)
	if err != nil {
		logger.Error("tracer provider init failed", slog.String("err", err.Error()))
		os.Exit(1)
	}
	otel.SetTracerProvider(tp)
	obs.SetupPropagation()
	defer func() {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()
		_ = tp.Shutdown(flushCtx)
	}()

	mux := http.NewServeMux()
	path, handler := adapterv1connect.NewAdapterServiceHandler(svc)
	mux.Handle(path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	port := envOr("PORT", "8080")
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           obs.WrapHTTPHandler(mux, serviceName),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("adapter-doubao listening",
			slog.String("addr", srv.Addr),
			slog.Any("bound_model_ids", boundModelIDs),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen error", slog.String("err", err.Error()))
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
