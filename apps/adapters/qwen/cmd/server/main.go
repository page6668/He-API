// Command server is the Qwen adapter's Connect-RPC bootstrap. Listens on
// $PORT (default 8080), exposes the AdapterService.Chat RPC, talks
// upstream to DashScope compat-mode via HTTPS (HTTP/2-preferred per
// Architect Round 1 OQ-4.2-5 ruling).
//
// Environment configuration:
//
//	PORT                                — listen port (default 8080)
//	QWEN_UPSTREAM_BASE_URL              — default https://dashscope.aliyuncs.com
//	QWEN_UPSTREAM_API_KEY               — Bearer token (required; sourced via
//	                                       Vault per OQ3 ruling — path
//	                                       kv/data/he-api/upstream/qwen/)
//	QWEN_UPSTREAM_TIMEOUT_SECONDS       — per-request deadline (default 60)
//	QWEN_BOUND_MODEL_IDS                — comma-separated model id list
//	                                       (default "qwen-max,qwen-plus" per
//	                                       BR-1.10; informs slog tagging +
//	                                       Helm supportedModels ConfigMap)
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

	qweninternal "github.com/he-api/he-api/apps/adapters/qwen/internal"
	"github.com/he-api/he-api/apps/adapters/qwen/internal/upstream"
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
	serviceName    = "adapter-qwen"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
)

// defaultBoundModelIDs is the BR-1.10 default model-id list — qwen-max,
// qwen-plus AND (Story 9.5) the Vision model qwen-vl-max all dispatch to this
// single service (N=3, multi-model-id-per-service).
var defaultBoundModelIDs = []string{"qwen-max", "qwen-plus", "qwen-vl-max"}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("QWEN_UPSTREAM_BASE_URL", "https://dashscope.aliyuncs.com")
	apiKey := os.Getenv("QWEN_UPSTREAM_API_KEY")
	if apiKey == "" {
		// AD-004: credentials may arrive at runtime from the gateway's
		// /internal/providers/active loopback endpoint. Don't hard-fail at
		// startup; the refresher goroutine below will populate them.
		logger.Warn("QWEN_UPSTREAM_API_KEY is not set — waiting for runtime config from gateway")
	}
	timeout := DefaultUpstreamTimeout
	if v := os.Getenv("QWEN_UPSTREAM_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	boundModelIDs := defaultBoundModelIDs
	if v := os.Getenv("QWEN_BOUND_MODEL_IDS"); v != "" {
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

	client := upstream.NewClient(baseURL, apiKey, timeout)

	// AD-004: hot-reload upstream credentials from the gateway's loopback-only
	// /internal/providers/active endpoint. Runs for the process lifetime.
	gwURL := envOr("HE_API_PROVIDER_REFRESH_URL", "http://127.0.0.1:8080")
	refresher := providerrefresher.New(gwURL, "qwen", client, logger)
	go refresher.Start(context.Background())
	svc := qweninternal.NewService(client, logger, boundModelIDs)

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
		logger.Info("adapter-qwen listening",
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
