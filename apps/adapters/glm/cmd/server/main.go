// Command server is the GLM (Zhipu) adapter's Connect-RPC bootstrap.
// Listens on $PORT (default 8080), exposes the AdapterService.Chat RPC,
// talks upstream to Zhipu AI v4 OpenAI-compatible API via HTTPS
// (HTTP/2-preferred per Architect Round 1 OQ-4.4-4 ruling — cascade
// default from Story-4.2 OQ-4.2-5).
//
// Environment configuration:
//
//	PORT                                — listen port (default 8080)
//	GLM_UPSTREAM_BASE_URL               — default https://open.bigmodel.cn
//	GLM_UPSTREAM_API_KEY                — Bearer token (required; sourced via
//	                                       Vault per OQ3 ruling — path
//	                                       kv/data/he-api/upstream/glm/)
//	GLM_UPSTREAM_TIMEOUT_SECONDS        — per-request deadline (default 60)
//	GLM_BOUND_MODEL_IDS                 — comma-separated model id list
//	                                       (default "glm-4" per BR-1.10 N=1;
//	                                       informs slog tagging + Helm
//	                                       supportedModels ConfigMap)
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

	glminternal "github.com/he-api/he-api/apps/adapters/glm/internal"
	"github.com/he-api/he-api/apps/adapters/glm/internal/upstream"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// DefaultUpstreamTimeout mirrors upstream.DefaultUpstreamTimeout for the
// startup-default path.
const DefaultUpstreamTimeout = 60 * time.Second

// defaultBoundModelIDs is the BR-1.10 default model-id list — Story 4.4
// is the FIRST Epic-4 adapter to host a SINGLE model id (N=1 degenerate
// case of the Story-4.2/4.3 multi-model-id pattern). Future GLM sizes
// would extend this slice without code changes.
var defaultBoundModelIDs = []string{"glm-4"}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("GLM_UPSTREAM_BASE_URL", "https://open.bigmodel.cn")
	apiKey := os.Getenv("GLM_UPSTREAM_API_KEY")
	if apiKey == "" {
		logger.Error("GLM_UPSTREAM_API_KEY is not set — adapter cannot reach upstream")
		os.Exit(1)
	}
	timeout := DefaultUpstreamTimeout
	if v := os.Getenv("GLM_UPSTREAM_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	boundModelIDs := defaultBoundModelIDs
	if v := os.Getenv("GLM_BOUND_MODEL_IDS"); v != "" {
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
	svc := glminternal.NewService(client, logger, boundModelIDs)

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
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("adapter-glm listening",
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
