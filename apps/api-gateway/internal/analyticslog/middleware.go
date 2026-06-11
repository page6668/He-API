package analyticslog

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
)

// errBodyCap bounds how much of an error response body we buffer to extract the
// envelope error_code. Error envelopes are tiny; success bodies are never
// buffered (the completion text must NEVER reach the analytics event — BR-ING-5).
const errBodyCap = 8 << 10 // 8 KiB

// nowFunc is overridable for deterministic tests.
type nowFunc func() time.Time

// Middleware wraps a terminal /v1/chat/completions or /v1/embeddings handler and
// emits ONE request.logged event per outcome — success AND every error envelope
// (the 成功率 rows usage.recorded misses). The emit is fire-and-forget and runs
// AFTER the response is fully written, so it can never delay or mutate it
// (BR-ING-4). Mount INSIDE bearer auth so user_id/api_key_id are in context.
func Middleware(e Emitter) func(http.Handler) http.Handler {
	return middlewareWithClock(e, time.Now)
}

func middlewareWithClock(e Emitter, now nowFunc) func(http.Handler) http.Handler {
	if e == nil {
		e = Nop{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, rec := NewContext(r.Context())
			r = r.WithContext(ctx)

			cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
			start := now()
			next.ServeHTTP(cw, r)
			elapsed := now().Sub(start)

			ev := buildEvent(r, cw, rec, elapsed)
			// Emit on the request ctx; the Emitter detaches internally.
			e.Emit(r.Context(), ev)
		})
	}
}

// buildEvent assembles the UsageLogEvent from the request context identity, the
// captured response (status + error_code), the served Record, and the measured
// latency. PII-safe: only request-scoped IDs + status/tokens/cost — NEVER
// message content or the plaintext key (BR-ING-5).
func buildEvent(r *http.Request, cw *captureWriter, rec *Record, elapsed time.Duration) *analyticsv1.UsageLogEvent {
	heRequestID, _ := requestid.FromContext(r.Context())
	userID, _ := middleware.BearerUserIDFromContext(r.Context())
	apiKeyID, _ := middleware.APIKeyIDFromContext(r.Context())

	// X-He-Selected-Model is the strategy-selected upstream model (set by the
	// handler on every routed path; absent on the A/B path, BR2-7). It closes the
	// 6.2 selected_by_strategy deferral (H-3).
	selected := cw.Header().Get("X-He-Selected-Model")
	model := selected
	if rec != nil && rec.Model != "" {
		model = rec.Model
	}

	ev := &analyticsv1.UsageLogEvent{
		HeRequestId:        heRequestID,
		UserId:             userID,
		ApiKeyId:           apiKeyID,
		Model:              model,
		UpstreamModel:      selected,
		RoutingStrategy:    r.Header.Get("X-He-Routing-Strategy"),
		SelectedByStrategy: selected,
		StatusCode:         clampStatus(cw.status),
		CostUsd:            "0", // NON-NULL default (Q-COST / H-1)
		LatencyMsTotal:     durMs(elapsed),
		ClientIp:           clientIP(r),
		ErrorCode:          cw.errorCode(),
		Ts:                 nowTS(r),
	}
	if rec != nil {
		ev.PromptTokens = rec.PromptTokens
		ev.CompletionTokens = rec.CompletionTokens
		ev.TotalTokens = rec.TotalTokens
		ev.TtfbMs = rec.TtfbMs
		ev.IsStreaming = rec.IsStreaming
		if rec.CostUsd != "" {
			ev.CostUsd = rec.CostUsd
		}
	}
	return ev
}

func nowTS(r *http.Request) string {
	// ts is informational; use the wall clock at build time (RFC3339, UTC).
	return time.Now().UTC().Format(time.RFC3339)
}

func clampStatus(s int) uint32 {
	if s < 100 || s > 599 {
		return 0
	}
	return uint32(s)
}

func durMs(d time.Duration) uint32 {
	ms := d.Milliseconds()
	if ms < 0 {
		return 0
	}
	if ms > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(ms)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// captureWriter records the terminal status code and (only for error responses)
// a bounded copy of the body so the envelope error_code can be extracted without
// touching any openaierr.Write call site. It is Flusher-transparent so the SSE
// streaming path keeps working.
type captureWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	errBuf      []byte
	bufferBody  bool
}

func (c *captureWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = status
	// Only error envelopes are small JSON we want to inspect for error_code.
	c.bufferBody = status >= 400
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.bufferBody && len(c.errBuf) < errBodyCap {
		remaining := errBodyCap - len(c.errBuf)
		if remaining > len(b) {
			remaining = len(b)
		}
		c.errBuf = append(c.errBuf, b[:remaining]...)
	}
	return c.ResponseWriter.Write(b)
}

// Flush proxies to the underlying writer (SSE streaming path needs it).
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// errorCode extracts the OpenAI-style envelope code ({"error":{"code":"..."}})
// from the buffered error body. Returns "" for success (no body buffered) or an
// unparseable body.
func (c *captureWriter) errorCode() string {
	if len(c.errBuf) == 0 {
		return ""
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(c.errBuf, &env); err != nil {
		return ""
	}
	return env.Error.Code
}
