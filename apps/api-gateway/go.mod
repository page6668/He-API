module github.com/he-api/he-api/apps/api-gateway

go 1.25.0

require (
	connectrpc.com/connect v1.16.2
	github.com/ClickHouse/clickhouse-go/v2 v2.30.0
	github.com/alicebob/miniredis/v2 v2.37.0
	github.com/go-pdf/fpdf v0.9.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/he-api/he-api/packages/go-observability v0.0.0-00010101000000-000000000000
	github.com/he-api/he-api/packages/proto v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.6.0
	github.com/pashagolub/pgxmock/v3 v3.3.0
	github.com/redis/go-redis/v9 v9.6.1
	github.com/segmentio/kafka-go v0.4.47
	github.com/shopspring/decimal v1.4.0
	go.opentelemetry.io/otel v1.26.0
	go.opentelemetry.io/otel/sdk v1.26.0
	go.uber.org/goleak v1.3.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/ClickHouse/ch-go v0.61.5 // indirect
	github.com/andybalholm/brotli v1.1.1 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/klauspost/compress v1.17.7 // indirect
	github.com/paulmach/orb v0.11.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/segmentio/asm v1.2.0 // indirect
	golang.org/x/crypto v0.28.0 // indirect
	golang.org/x/sync v0.12.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.20.0 // indirect
	github.com/he-api/he-api/packages/models-catalogue v0.0.0-00010101000000-000000000000
	github.com/he-api/he-api/packages/plan-catalogue v0.0.0-00010101000000-000000000000
	github.com/he-api/he-api/packages/safety-lexicon v0.0.0-00010101000000-000000000000
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.19.1 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.51.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.26.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.26.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.48.0 // indirect
	go.opentelemetry.io/otel/metric v1.26.0
	go.opentelemetry.io/otel/sdk/metric v1.26.0
	go.opentelemetry.io/otel/trace v1.26.0
	go.opentelemetry.io/proto/otlp v1.2.0 // indirect
	golang.org/x/net v0.30.0 // indirect
	golang.org/x/sys v0.26.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/grpc v1.64.0 // indirect
)

replace github.com/he-api/he-api/packages/go-observability => ../../packages/go-observability

replace github.com/he-api/he-api/packages/proto => ../../packages/proto

replace github.com/he-api/he-api/packages/models-catalogue => ../../packages/models-catalogue

replace github.com/he-api/he-api/packages/plan-catalogue => ../../packages/plan-catalogue

replace github.com/he-api/he-api/packages/safety-lexicon => ../../packages/safety-lexicon
