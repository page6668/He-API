package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ErrorKind classifies upstream-failure modes for the slog
// `upstream_error_kind` attribute (Architect Round 1 BR-4.4 cascade —
// REUSE Story-4.2 set + ADDS ErrorKindContextLengthExceeded per Story-4.3
// BR-4.5 for Kimi-specific 400 context-window-exceeded disambiguation).
//
// The user-facing envelope is always BR-1.4 (502 / 504); ErrorKind is
// internal-observability-only.
type ErrorKind string

const (
	ErrorKindAuthRevoked           ErrorKind = "auth_revoked"
	ErrorKindRateLimitThrottle     ErrorKind = "rate_limit_throttle"     // BR-4.4 REUSE Story-4.2
	ErrorKindContextLengthExceeded ErrorKind = "context_length_exceeded" // BR-4.5 NEW Story-4.3 (Kimi 8k/32k/128k budgets)
	ErrorKindUpstream5xx           ErrorKind = "upstream_5xx"
	ErrorKindUpstream4xx           ErrorKind = "upstream_4xx"
	ErrorKindUpstreamTimeout       ErrorKind = "upstream_timeout"
	ErrorKindTLS                   ErrorKind = "tls"
	ErrorKindDNS                   ErrorKind = "dns"
	ErrorKindConnectionRefused     ErrorKind = "connection_refused"
	ErrorKindMissingUsage          ErrorKind = "missing_usage"
	ErrorKindEmptyChoices          ErrorKind = "empty_choices"
	ErrorKindMalformedChunk        ErrorKind = "malformed_chunk"
	ErrorKindUsageConstraint       ErrorKind = "usage_constraint_violation"
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
// Status-only signature preserved from Story-4.2 qwen for non-Moonshot
// failure modes (5xx, timeout, auth-revoked, generic 4xx). For Kimi-
// specific 400-context-length-exceeded disambiguation use
// ClassifyMoonshotErrorBody — the status code alone cannot distinguish
// context-length errors from generic 400s.
//
// Architect Round 1 OQ-4.2-4 cascade: 429 → ErrorKindRateLimitThrottle
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

// ClassifyMoonshotErrorBody extends ClassifyHTTPStatus with body
// inspection so 400-context-length-exceeded responses can be tagged with
// the Kimi-specific ErrorKindContextLengthExceeded slog kind (BR-4.5).
//
// Architect Round 1 m-1 (Story 4.3): Moonshot returns context-window-
// exceeded errors as HTTP 400 with body shape
//
//	{"error":{"type":"invalid_request_error",
//	          "message":"...maximum context length..."}}
//
// The status-only classifier cannot distinguish these from generic 400s
// (e.g., malformed JSON, unsupported fields), so this body-aware helper
// short-circuits on the canonical Moonshot context-length shape. Falls
// through to ClassifyHTTPStatus on any mismatch.
//
// PII safety: only the parsed `error.type` and `error.message` strings
// are inspected; the body is not logged or echoed by callers — adapter
// slog records carry the kind, not the raw body, per BR-1.9.
func ClassifyMoonshotErrorBody(status int, body []byte) ErrorKind {
	if status == http.StatusBadRequest && len(body) > 0 {
		var parsed struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &parsed); err == nil {
			if parsed.Error.Type == "invalid_request_error" {
				lower := strings.ToLower(parsed.Error.Message)
				if strings.Contains(lower, "context length") || strings.Contains(lower, "context_length") {
					return ErrorKindContextLengthExceeded
				}
			}
		}
	}
	return ClassifyHTTPStatus(status)
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
