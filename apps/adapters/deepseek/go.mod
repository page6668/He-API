module github.com/he-api/he-api/apps/adapters/deepseek

go 1.22

require (
	connectrpc.com/connect v1.16.2
	github.com/he-api/he-api/packages/go-observability v0.0.0-00010101000000-000000000000
	github.com/he-api/he-api/packages/proto v0.0.0-00010101000000-000000000000
	golang.org/x/net v0.27.0
)

require (
	golang.org/x/text v0.16.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

replace github.com/he-api/he-api/packages/proto => ../../../packages/proto

replace github.com/he-api/he-api/packages/go-observability => ../../../packages/go-observability
