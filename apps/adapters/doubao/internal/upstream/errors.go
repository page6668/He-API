package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ErrorKind classifies upstream-failure modes for the slog
// `upstream_error_kind` attribute. Architect Round 1 OQ-4.5-6 ratifies
// verbatim REUSE of Story-4.2 BR-4.4 + Story-4.1 BR-1.4 mapping — NO
// Volcengine-specific carve-outs documented at draft time; the
// Story-4.3 body-aware ErrorKindContextLengthExceeded is NOT cascaded
// (per OQ-4.4-6 → OQ-4.5-6 cascade-locked).
//
// NEW for Story 4.5: ErrorKindEndpointIDNotConfigured (BR-4.6) — surfaced
// from the internal endpoint_map.Lookup returning ErrUnsupportedModel
// when DOUBAO_PRO_ENDPOINT_ID / DOUBAO_LITE_ENDPOINT_ID env vars are
// unset/empty at process startup. The error kind does NOT come from an
// HTTP status code (the upstream HTTPS call is NEVER initiated per BR-1.12
// fail-fast); the adapter short-circuits Service.Chat before issuing the
// HTTPS call.
//
// The user-facing envelope is always BR-1.4 (502 / 504); ErrorKind is
// internal-observability-only.
type ErrorKind string

const (
	ErrorKindAuthRevoked             ErrorKind = "auth_revoked"
	ErrorKindRateLimitThrottle       ErrorKind = "rate_limit_throttle" // BR-4.4 REUSE Story-4.2
	ErrorKindUpstream5xx             ErrorKind = "upstream_5xx"
	ErrorKindUpstream4xx             ErrorKind = "upstream_4xx"
	ErrorKindUpstreamTimeout         ErrorKind = "upstream_timeout"
	ErrorKindTLS                     ErrorKind = "tls"
	ErrorKindDNS                     ErrorKind = "dns"
	ErrorKindConnectionRefused       ErrorKind = "connection_refused"
	ErrorKindMissingUsage            ErrorKind = "missing_usage"
	ErrorKindEmptyChoices            ErrorKind = "empty_choices"
	ErrorKindMalformedChunk          ErrorKind = "malformed_chunk"
	ErrorKindUsageConstraint         ErrorKind = "usage_constraint_violation"
	ErrorKindEndpointIDNotConfigured ErrorKind = "endpoint_id_not_configured" // NEW Story-4.5 BR-4.6
	ErrorKindOutputTooLarge          ErrorKind = "output_too_large"           // NEW Story-9.7 BR-4.3 (synthesized audio over the cap)
)

// ClassifyError turns a transport-level or HTTP-level error into an
// ErrorKind. Falls through to ErrorKindUpstream5xx for unknown failure
// modes so the gateway-side BR-1.4 mapping always has a non-empty value.
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrUnsupportedModel) {
		return ErrorKindEndpointIDNotConfigured
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorKindUpstreamTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrorKindUpstreamTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrorKindDNS
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") {
		return ErrorKindConnectionRefused
	}
	if strings.Contains(msg, "tls") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate") {
		return ErrorKindTLS
	}
	type timeoutAware interface{ Timeout() bool }
	var tw timeoutAware
	if errors.As(err, &tw) && tw.Timeout() {
		return ErrorKindUpstreamTimeout
	}
	return ErrorKindUpstream5xx
}

// ClassifyHTTPStatus maps an upstream HTTP status to an ErrorKind.
// Status-only — Volcengine Ark v3 OpenAI-compat does NOT carry a
// body-aware failure shape per OQ-4.5-6 (the Story-4.3
// ClassifyMoonshotErrorBody helper is NOT cascaded; if a future
// Volcengine-specific failure shape surfaces, append a body-aware
// helper here per the Story-4.3 m-1 precedent — NO Architect Round 2
// needed for additive enum entries).
//
// 429 → ErrorKindRateLimitThrottle (BR-4.4 cascade REUSE Story-4.2);
// 401/403 → ErrorKindAuthRevoked; 4xx → ErrorKindUpstream4xx;
// 5xx → ErrorKindUpstream5xx; 2xx → "" (no error).
func ClassifyHTTPStatus(status int) ErrorKind {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusUnauthorized:
		return ErrorKindAuthRevoked
	case status == http.StatusForbidden:
		return ErrorKindAuthRevoked
	case status == http.StatusTooManyRequests:
		return ErrorKindRateLimitThrottle
	case status >= 400 && status < 500:
		return ErrorKindUpstream4xx
	default:
		return ErrorKindUpstream5xx
	}
}

// UpstreamError carries the classification + HTTP status alongside the
// cause so the adapter Chat handler can build the right Connect-RPC Code
// + slog attributes.
type UpstreamError struct {
	Kind   ErrorKind
	Status int
	Cause  error
}

func (e *UpstreamError) Error() string {
	if e == nil || e.Cause == nil {
		return fmt.Sprintf("upstream error: kind=%s status=%d", e.Kind, e.Status)
	}
	return fmt.Sprintf("upstream error: kind=%s status=%d: %v", e.Kind, e.Status, e.Cause)
}

func (e *UpstreamError) Unwrap() error { return e.Cause }
