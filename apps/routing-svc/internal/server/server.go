// Package server assembles routing-svc's HTTP surface: the RoutingService
// Connect-RPC handler plus the /livez + /ready probes, wrapped with the shared
// observability middleware (which also serves /metrics). It is importable by
// both cmd/server (production boot) and the integration tests so the wiring is
// defined once.
package server

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/handler"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1/routingv1connect"
)

// Options configures a Server.
type Options struct {
	// Catalogue is the boot-loaded model catalogue. An empty catalogue is a
	// fatal boot error (BR1-2) — New returns an error, never a served-empty
	// server.
	Catalogue modelscatalogue.Catalogue
	// Strategies is the slug -> implementation registry. A nil implementation
	// for any registered slug is a fatal boot error (AC1).
	Strategies map[routingv1.Strategy]engine.Strategy
	// Logger may be nil (falls back to slog.Default()).
	Logger *slog.Logger
	// ServiceName labels the observability middleware (default "routing-svc").
	ServiceName string
}

// Server holds the assembled HTTP handler and the readiness gate.
type Server struct {
	Handler http.Handler
	engine  *engine.Engine
	ready   atomic.Bool
}

// New constructs the engine (fail-fast on empty catalogue / nil-impl slug,
// AC1) and wires the HTTP handler. Readiness starts FALSE: /ready returns 503
// until SetReady(true) is called once the listener is up (FLOW-001 — the
// engine is fully constructed before readiness can flip, so any request
// accepted after readiness is guaranteed a constructed engine).
func New(opts Options) (*Server, error) {
	e, err := engine.NewEngine(opts.Catalogue, opts.Strategies)
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	name := opts.ServiceName
	if name == "" {
		name = "routing-svc"
	}

	s := &Server{engine: e}

	mux := http.NewServeMux()
	path, h := routingv1connect.NewRoutingServiceHandler(handler.NewRoutingServer(e, logger))
	mux.Handle(path, h)

	// Liveness: 200 unconditionally once the process is up (INT-002).
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Readiness: 503 until the snapshot is loaded + engine constructed, then
	// 200 (INT-003, BR1-2).
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})

	// obs.WrapHTTPHandler adds OTel tracing + serves /metrics via promhttp
	// (REUSE, Story 1.5 / 2.2) — no bespoke observability code (INT-004).
	s.Handler = obs.WrapHTTPHandler(mux, name)
	return s, nil
}

// SetReady flips the readiness gate. Call SetReady(true) after the listener is
// accepting connections.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Ready reports the current readiness state.
func (s *Server) Ready() bool { return s.ready.Load() }
