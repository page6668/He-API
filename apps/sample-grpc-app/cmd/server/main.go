// sample-grpc-app — Epic 1+ gRPC service (scaffolded by scripts/scaffold-svc.sh, Story 1.5).
//
// Single connect-go handler serves all three protocols on the same HTTP/2 path
// (Story 1.5 Q3 ruling): Connect/JSON (curl-debuggable), gRPC binary
// (production-efficient), gRPC-Web (browser BFF). Observability wired in three
// calls via the shared packages/go-observability package (Story 1.5 Q5 ruling).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	obs "github.com/he-api/he-api/packages/go-observability"
	samplev1 "github.com/he-api/he-api/packages/proto/gen/go/he/sample/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/sample/v1/samplev1connect"

	"go.opentelemetry.io/otel"
)

const (
	serviceName    = "sample-grpc-app"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
	listenAddr     = ":8080"
)

type SampleServer struct{}

func (s *SampleServer) Ping(_ context.Context, req *connect.Request[samplev1.PingRequest]) (*connect.Response[samplev1.PingResponse], error) {
	return connect.NewResponse(&samplev1.PingResponse{Pong: req.Msg.GetPing()}), nil
}

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
	// Flush in-flight spans on shutdown (Story 1.4 1.4-BLIND-RESOURCE contract).
	defer func() { sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second); defer scancel(); _ = tp.Shutdown(sctx) }()

	mux := http.NewServeMux()
	mux.Handle(samplev1connect.NewSampleServiceHandler(&SampleServer{}))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(mux, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("sample-grpc-app listening", slog.String("addr", listenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serverErr:
		logger.Error("http server error", slog.String("error", err.Error()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", slog.String("error", err.Error()))
	}
}

