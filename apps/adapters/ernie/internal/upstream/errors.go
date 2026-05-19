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
// `upstream_error_kind` attribute (Architect Round 1 OQ-4.6-6: verbatim
// REUSE of Story-4.4 OQ-4.4-6 cascade — status-only classifier; no body-
// aware classifier from Story-4.3; no Baidu-specific carve-outs
// documented at draft time; no Story-4.5 `ErrorKindEndpointIDNotConfigured`
// — endpoint-id translate not exercised under OQ-4.6-1 option (a); no
// option-(b) `ErrorKindAccessTokenRefreshFailed` — option (b) REJECTED).
//
// The user-facing envelope is always BR-1.4 (502 / 504); ErrorKind is
// internal-observability-only.
type ErrorKind string

const (
	ErrorKindAuthRevoked       ErrorKind = "auth_revoked"
	ErrorKindRateLimitThrottle ErrorKind = "rate_limit_throttle" // BR-4.4 REUSE Story-4.2
	ErrorKindUpstream5xx       ErrorKind = "upstream_5xx"
	ErrorKindUpstream4xx       ErrorKind = "upstream_4xx"
	ErrorKindUpstreamTimeout   ErrorKind = "upstream_timeout"
	ErrorKindTLS               ErrorKind = "tls"
	ErrorKindDNS               ErrorKind = "dns"
	ErrorKindConnectionRefused ErrorKind = "connection_refused"
	ErrorKindMissingUsage      ErrorKind = "missing_usage"
	ErrorKindEmptyChoices      ErrorKind = "empty_choices"
	ErrorKindMalformedChunk    ErrorKind = "malformed_chunk"
	ErrorKindUsageConstraint   ErrorKind = "usage_constraint_violation"
)

// ClassifyError turns a transport-level or HTTP-level error into an
// ErrorKind. Falls through to ErrorKindUpstream5xx for unknown failure
// modes so the gateway-side BR-1.4 mapping always has a non-empty value.
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ""
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
// Status-only — Qianfan v2 OpenAI-compat does NOT carry a body-aware
// failure shape per OQ-4.6-6 (the Story-4.3 ClassifyMoonshotErrorBody
// helper is NOT cascaded; if a future Baidu-specific failure shape
// surfaces, add a body-aware helper here per the Story-4.3 m-1
// precedent — NO Architect Round 2 needed for additive enum entries).
//
// Architect Round 1 OQ-4.6-6 cascade: 429 → ErrorKindRateLimitThrottle
// (slog disambiguation per BR-4.4); the user-facing envelope still
// maps to 502 per BR-1.4.
func ClassifyHTTPStatus(status int) ErrorKind {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusUnauthorized:
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
