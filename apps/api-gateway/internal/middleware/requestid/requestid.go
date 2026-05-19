package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	sharedrid "github.com/he-api/he-api/packages/go-observability/requestid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HeaderName is re-exported from the shared accessor package
// (packages/go-observability/requestid) per Story 4.1 OQ8 Architect Round 2
// ruling. Existing gateway / openaierr / handler callers continue to
// reference `requestid.HeaderName` unchanged.
const HeaderName = sharedrid.HeaderName

// SpanAttributeKey is re-exported from the shared accessor package per OQ8.
const SpanAttributeKey = sharedrid.SpanAttributeKey

// sentinelOnRandFailure is the value stamped if crypto/rand.Read fails — unreachable
// on Linux + Darwin (`/dev/urandom` always succeeds) but the path is defensively
// closed off so the middleware never short-circuits without an envelope.
const sentinelOnRandFailure = "req_000000000000"

// randRead is a package-level seam for monkey-patching crypto/rand in tests.
// Production always uses crypto/rand.Read.
var randRead = rand.Read

// RequestID is the per-request he_request_id stamping middleware.
//
// It runs as the OUTERMOST wrap on the user-traffic chain (per BR-2.7) so that
// every response — success AND error, regardless of which inner middleware
// emits it — carries the X-He-Request-Id header (BR-2.6). The probeMux on
// /health and /healthz bypasses this middleware (Story 3.1 BR-1.3).
//
// Derivation rule (per BR-2.3 + Architect Round 1 OQ1 RATIFIED):
//   - If the OTel SpanContext is VALID, use req_ + hex(TraceID[0:6]).
//     The truncation is one-way → an attacker observing he_request_id cannot
//     reconstruct the full TraceID.
//   - If INVALID (defensive — production has obs.WrapHTTPHandler ALWAYS
//     wrapping the chain), fall back to crypto/rand (BR-2.4).
//
// Anti-spoofing (BR-2.5): inbound X-Request-Id / X-He-Request-Id / Request-Id
// headers are SILENTLY IGNORED.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		reqID := deriveRequestID(ctx)

		w.Header().Set(HeaderName, reqID)
		ctx = sharedrid.WithRequestID(ctx, reqID)

		if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
			span.SetAttributes(attribute.String(SpanAttributeKey, reqID))
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// FromContext is re-exported from the shared accessor package per OQ8.
// The variable indirection keeps the public symbol resolved at the same
// call site (`requestid.FromContext(ctx)`) without forcing every caller
// to rewrite its import path.
var FromContext = sharedrid.FromContext

// WithRequestID is re-exported from the shared accessor package per OQ8.
var WithRequestID = sharedrid.WithRequestID

// deriveRequestID computes the per-request he_request_id from the OTel span
// context or, defensively, from crypto/rand. See BR-2.3 + BR-2.4.
func deriveRequestID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		tid := sc.TraceID()
		return "req_" + hex.EncodeToString(tid[0:6])
	}
	var buf [6]byte
	if _, err := randRead(buf[:]); err != nil {
		return sentinelOnRandFailure
	}
	return "req_" + hex.EncodeToString(buf[:])
}
