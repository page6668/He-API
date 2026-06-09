// Package server assembles billing-svc's HTTP surface: the BillingService
// Connect-RPC handler (CheckBalance) plus /livez + /ready probes, wrapped with
// the shared observability middleware (which serves /metrics). Importable by
// cmd/server (production boot) and integration tests so the wiring is defined
// once. Mirrors apps/routing-svc/internal/server.
package server

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
)

// Options configures a Server.
type Options struct {
	// BillingHandler implements the BillingService Connect handler (CheckBalance).
	BillingHandler billingv1connect.BillingServiceHandler
	// Logger may be nil (slog.Default()).
	Logger *slog.Logger
	// ServiceName labels the observability middleware (default "billing-svc").
	ServiceName string
}

// Server holds the assembled HTTP handler and the readiness gate.
type Server struct {
	Handler http.Handler
	ready   atomic.Bool
}

// New wires the HTTP handler. Readiness starts FALSE: /ready returns 503 until
// SetReady(true) once the listener + consumer are up.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	name := opts.ServiceName
	if name == "" {
		name = "billing-svc"
	}
	handler := opts.BillingHandler
	if handler == nil {
		handler = billingv1connect.UnimplementedBillingServiceHandler{}
	}

	s := &Server{}

	mux := http.NewServeMux()
	path, h := billingv1connect.NewBillingServiceHandler(handler)
	mux.Handle(path, h)

	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})

	s.Handler = obs.WrapHTTPHandler(mux, name)
	return s
}

// SetReady flips the readiness gate.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Ready reports the current readiness state.
func (s *Server) Ready() bool { return s.ready.Load() }
