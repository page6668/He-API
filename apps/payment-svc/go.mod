module github.com/he-api/he-api/apps/payment-svc

go 1.25.0

// Story 7.3 — FIRST realisation of payment-svc (service-topology §3.2). Deps are
// pinned to the workspace versions (esp. go.opentelemetry.io/otel v1.26.0 +
// otel/metric v1.26.0) — do NOT `go mod tidy` (auto-bumps sdk/metric and breaks
// the workspace pin, [[project_otel_version_pin_gotcha]]). go.sum is inherited
// from billing-svc (a superset). Provider integrations (Stripe HMAC webhook
// verification + checkout, PayPal verify-webhook-signature + checkout) are
// implemented over the stdlib (crypto/hmac, net/http) behind the PaymentProvider
// seam — no third-party SDK, so the build is self-contained + offline-testable;
// the seam isolates a future stripe-go swap (Q-SDK).
require (
	connectrpc.com/connect v1.16.2
	github.com/he-api/he-api/packages/go-observability v0.0.0-00010101000000-000000000000
	github.com/he-api/he-api/packages/proto v0.0.0-00010101000000-000000000000
	github.com/segmentio/kafka-go v0.4.51
	github.com/shopspring/decimal v1.4.0
	go.opentelemetry.io/otel v1.26.0
	go.opentelemetry.io/otel/metric v1.26.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.20.0 // indirect
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
	github.com/prometheus/client_golang v1.19.1 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.51.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.26.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.26.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.48.0 // indirect
	go.opentelemetry.io/otel/sdk v1.26.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.26.0 // indirect
	go.opentelemetry.io/otel/trace v1.26.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/sync v0.12.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/grpc v1.64.0 // indirect
)

replace github.com/he-api/he-api/packages/go-observability => ../../packages/go-observability

replace github.com/he-api/he-api/packages/proto => ../../packages/proto
