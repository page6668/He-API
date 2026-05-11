package obs

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WrapHTTPHandler returns an http.Handler that:
//   - serves the caller's handler at every path EXCEPT /metrics
//   - serves Prometheus /metrics via promhttp.Handler()
//   - wraps the whole composite in otelhttp.NewHandler so every request opens
//     a server span (the OTel SDK emits the
//     http_server_request_duration_seconds_* histogram from these spans).
//
// The name parameter feeds otelhttp's span name and the OTel resource link;
// services pass their service.name verbatim (e.g. "sample-grpc-app").
func WrapHTTPHandler(h http.Handler, name string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/", h)
	return otelhttp.NewHandler(mux, name)
}
