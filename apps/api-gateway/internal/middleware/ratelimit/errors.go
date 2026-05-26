package ratelimit

import "errors"

// ErrAPIKeyIDMissing is returned by the middleware when the bearer-auth
// middleware did NOT populate api_key_id in the request context. This
// indicates a misconfiguration in cmd/server/main.go: the ratelimit
// middleware MUST run AFTER bearer-auth (Architect Q9). Surfaces as 500
// `500_gateway_misconfigured` to the client.
var ErrAPIKeyIDMissing = errors.New("ratelimit: api_key_id missing from context")

// ErrRedisUnavailable signals the fail-open path. The middleware logs
// WARN + increments the fail_open Prometheus counter + passes through.
// Never reaches the client.
var ErrRedisUnavailable = errors.New("ratelimit: redis unavailable (fail-open path triggered)")
