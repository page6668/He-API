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

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("DOUBAO_UPSTREAM_BASE_URL", "https://ark.cn-beijing.volces.com")
	apiKey := os.Getenv("DOUBAO_UPSTREAM_API_KEY")
	if apiKey == "" {
		logger.Error("DOUBAO_UPSTREAM_API_KEY is not set — adapter cannot reach upstream")
		os.Exit(1)
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
	svc := doubaointernal.NewService(client, endpointMap, logger, boundModelIDs)

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
