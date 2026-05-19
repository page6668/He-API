// Command server is the DeepSeek adapter's Connect-RPC bootstrap. Listens on
// $PORT (default 8080), exposes the AdapterService.Chat RPC, talks upstream
// to DeepSeek via HTTPS (HTTP/2-forced per OQ7).
//
// Environment configuration:
//
//	PORT                                — listen port (default 8080)
//	DEEPSEEK_UPSTREAM_BASE_URL          — default https://api.deepseek.com
//	DEEPSEEK_UPSTREAM_API_KEY           — Bearer token (required; sourced via
//	                                       Vault per OQ3 ruling in prod)
//	DEEPSEEK_UPSTREAM_TIMEOUT_SECONDS   — per-request deadline (default 60)
//
// Cold-start budget per Architect Round 2 M5 ruling: ≤ 2s. K8s readiness
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
	"syscall"
	"time"

	deepseekinternal "github.com/he-api/he-api/apps/adapters/deepseek/internal"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("DEEPSEEK_UPSTREAM_BASE_URL", "https://api.deepseek.com")
	apiKey := os.Getenv("DEEPSEEK_UPSTREAM_API_KEY")
	if apiKey == "" {
		logger.Error("DEEPSEEK_UPSTREAM_API_KEY is not set — adapter cannot reach upstream")
		os.Exit(1)
	}
	timeout := DefaultUpstreamTimeout
	if v := os.Getenv("DEEPSEEK_UPSTREAM_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	client := upstream.NewClient(baseURL, apiKey, timeout)
	svc := deepseekinternal.NewService(client, logger)

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
		logger.Info("adapter-deepseek listening", slog.String("addr", srv.Addr))
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

// DefaultUpstreamTimeout mirrors upstream.DefaultUpstreamTimeout for the
// startup-default path. Kept package-local to keep main.go's import set
// minimal.
const DefaultUpstreamTimeout = 60 * time.Second

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
