// Story 3.4 — Streaming-dispatch bridge from ChatCompletionsHandler.ServeHTTP
// to the internal/streaming package. Story 4.1 augments this with the
// adapter-dispatch branch (BR-2.1) + emit-before-flush boundary (BR-2.5) +
// mid-stream-failure SSE error frame (BR-2.6).
//
// serveStream owns:
//   - Writer construction (streaming.NewWriter wraps the ResponseWriter)
//   - Story 4.1 BR-2.1 dispatch fork: if the adapter registry resolves
//     req.Model, build an AdapterChunker against the adapter Connect-RPC
//     server-stream; otherwise fall through to the Story-3.4 MockChunker.
//   - MockChunker construction with primitives (BR-1.4 id + BR-1.5 created +
//     BR-1.6 model echo + handlers.MockContent supplied at this call site —
//     the streaming package does NOT import handlers per Architect Round 1
//     C1 fix)
//   - BR-1.8 structured-log emission at stream completion (success OR
//     disconnect), with PII redaction discipline inherited from Story 3.3.
//   - Story 4.1 BR-2.5: when the chunker errors WITHOUT having flushed
//     headers, gateway emits the OpenAI §5.1.2 JSON envelope (NOT an SSE
//     error frame). BR-2.6: when the chunker errors AFTER flushing,
//     gateway emits an inline `data: {"error":...}\n\n` + `data: [DONE]\n\n`
//     terminal sequence (status remains 200 — already committed).
//
// All headers + body bytes are written THROUGH the streaming.Writer — no
// direct w.Header().Set or w.WriteHeader call lives here.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/he-api/he-api/apps/api-gateway/internal/streaming"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// serveStream is invoked from ServeHTTP when validateChatRequest succeeds
// AND req.Stream == true. The caller has already enforced bearer-auth +
// body-size + JSON parse + role validation.
func (h *ChatCompletionsHandler) serveStream(w http.ResponseWriter, r *http.Request, req *ChatRequest, apiKeyID, selected string, failoverChain []string, strategy string) {
	ctx := r.Context()
	// Story 4.1 BR-2.1 — dispatch fork BEFORE the writer/chunker is built.
	// Registry hit (on the Story-6.2 ROUTED model) → real adapter Connect-RPC
	// server-stream; miss → fall through to the Story-3.4 MockChunker.
	// Story 6.3 AC3 — the adapter stream path runs the PRE-FLUSH failover loop
	// (dispatchStreamWithFailover); the mock path below has no failover.
	if h.adapterRegistry != nil {
		if _, ok := h.adapterRegistry.Resolve(selected); ok {
			h.dispatchStreamWithFailover(w, r, req, apiKeyID, selected, failoverChain, strategy)
			return
		}
	}

	// realStart anchors TTFB + total_ms to wall-clock — these are observability
	// dimensions tracking actual elapsed time, NOT logical-completion identity.
	// h.now() (which tests may stub) drives only the `created` field per BR-1.5
	// so the chunk timestamps stay deterministic in golden tests.
	realStart := time.Now()
	id := h.newID()
	created := h.now().UTC().Unix()

	// BR1-2 — X-He-Selected-Model on the mock STREAM success path too (Story
	// 6.2: was unset). MUST precede the first WriteEvent (the streaming.Writer
	// flushes headers lazily on first event — FLOW-001).
	w.Header().Set("X-He-Selected-Model", selected)
	writer := streaming.NewWriter(w)
	chunker := streaming.NewMockChunker(selected, MockContent, id, created)

	chunksEmitted, firstFlushAt, streamErr := chunker.Stream(ctx, writer)
	_ = writer.Close() // BR-4.4 Close is a no-op for this Story; future Writer impls may hold resources.

	var ttfbMs int64
	if !firstFlushAt.IsZero() {
		ttfbMs = firstFlushAt.Sub(realStart).Milliseconds()
	}
	totalMs := time.Since(realStart).Milliseconds()

	clientDisconnected := streamErr != nil &&
		(errors.Is(streamErr, context.Canceled) ||
			errors.Is(ctx.Err(), context.Canceled) ||
			errors.Is(streamErr, streaming.ErrFlushUnsupported) ||
			isWriteOrFlushFailure(streamErr))

	attrs := []slog.Attr{
		slog.String("event", "chat_completions_stream"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("chunks_emitted", chunksEmitted),
		slog.Int64("ttfb_ms", ttfbMs),
		slog.Int64("total_ms", totalMs),
		slog.Bool("client_disconnected", clientDisconnected),
	}
	if clientDisconnected && streamErr != nil {
		attrs = append(attrs, slog.String("flush_error", streamErr.Error()))
	}
	h.logger.LogAttrs(ctx, slog.LevelInfo, "chat_completions_stream", attrs...)
}

// isWriteOrFlushFailure treats any non-cancellation chunker error as a
// disconnect / mid-stream write failure. The mock chunker has no upstream
// to fail against; the only errors that surface are from the Writer
// (WriteEvent / Flush / WriteDone on a closed connection, or
// ErrFlushUnsupported in the defensive non-flushable-writer case).
func isWriteOrFlushFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// dispatchStreamWithFailover is the Story-6.3 streaming PRE-FLUSH failover loop
// (AC3). It iterates the scope-filtered candidate chain, dispatching each
// resolvable hop via attemptAdapterStream. Failover is possible ONLY while no
// SSE byte has been committed (writer.HeadersFlushed()==false — Q-E HTTP
// constraint): a pre-flush 502/504 (Q-C) advances to the next model (same
// 3-attempt / 30s budget as AC2); once a chunk flushes (or a post-flush error is
// handled inline per BR-2.6), the response is committed and the loop STOPS. On
// pre-flush exhaustion it writes the BR-2.5 JSON envelope (status not yet
// committed).
func (h *ChatCompletionsHandler) dispatchStreamWithFailover(w http.ResponseWriter, r *http.Request, req *ChatRequest, apiKeyID, selected string, failoverChain []string, strategy string) {
	ctx, cancel := context.WithTimeout(r.Context(), h.failoverBudget) // Q-G
	defer cancel()

	chain := append([]string{selected}, failoverChain...)

	attempts := 0
	var lastErr error
	var prevModel, prevReason string

	for _, model := range chain {
		if ctx.Err() != nil {
			break // budget exhausted / client disconnected (Q-B/Q-G)
		}
		handle, ok := h.adapterRegistry.Resolve(model)
		if !ok {
			h.logger.LogAttrs(ctx, slog.LevelWarn, "chat_completions_stream_failover_skip_unresolved",
				slog.String("event", "chat_completions_stream_failover_skip_unresolved"),
				slog.String("model", model),
			)
			continue // BR2-3 — not an upstream attempt
		}
		if prevModel != "" {
			h.recordFailover(ctx, prevModel, model, prevReason, strategy, attempts+1)
		}
		attempts++

		committed, preErr := h.attemptAdapterStream(ctx, w, req, apiKeyID, model, handle)
		if committed {
			// Stream succeeded OR a post-flush error was handled inline (BR-2.6).
			// The response is committed — NO failover past the flush frontier.
			h.router.RecordFailoverAttempts(ctx, attempts)
			if attempts > 1 {
				h.logFailoverServed(ctx, chain[0], model, attempts) // 6.3-UNIT-048
			}
			return
		}

		// Pre-flush failure — nothing committed; the loop MAY advance.
		code, retriable := isRetriableUpstream(preErr)
		if !retriable {
			h.router.RecordFailoverAttempts(ctx, attempts)
			h.writeStreamPreflushTerminal(w, ctx, req, apiKeyID, preErr)
			return
		}
		lastErr = preErr
		prevModel, prevReason = model, failoverReason(code)
		if attempts >= MaxFailoverAttempts {
			break // 3-attempt cap (Q-B/BR2-2)
		}
	}

	// Pre-flush exhaustion — chain end, attempt cap, or budget. Emit the BR-2.5
	// JSON envelope (status not yet committed).
	h.router.RecordFailoverAttempts(ctx, attempts)
	if lastErr == nil {
		lastErr = errUpstreamInvalidResponse
	}
	h.writeStreamPreflushTerminal(w, ctx, req, apiKeyID, lastErr)
}

// attemptAdapterStream performs ONE streaming upstream attempt against handle
// (Story 6.3 m-1 — the per-hop pre-flush dispatch primitive). It returns:
//
//   - (true, nil)  → the stream SUCCEEDED, or a POST-flush error was handled
//     inline (BR-2.6 SSE error frame + [DONE]); the response is COMMITTED and
//     the failover loop STOPS (Q-E — committed bytes are unrecoverable).
//   - (false, err) → a PRE-flush failure (adapter Chat() error, or a chunker
//     error with HeadersFlushed()==false): NO byte was committed; the loop may
//     failover. err carries the cause for classification + the terminal envelope.
//
// The failed upstream stream/connection is Closed on return (defer) before the
// next hop re-opens a fresh one (BLIND-RESOURCE-001).
func (h *ChatCompletionsHandler) attemptAdapterStream(ctx context.Context, w http.ResponseWriter, req *ChatRequest, apiKeyID, servedModel string, handle adapterclient.ClientHandle) (committed bool, preFlushErr error) {
	realStart := time.Now()
	heRequestID, _ := requestid.FromContext(ctx)

	adapterReq := buildAdapterRequest(req, servedModel, heRequestID)
	adapterReq.Stream = true // BR-2.1 streaming branch
	headers := http.Header{}
	if heRequestID != "" {
		headers.Set("X-He-Request-Id", heRequestID) // BR-1.5
	}

	stream, callErr := handle.Chat(ctx, adapterReq, headers)
	if callErr != nil {
		// BR-2.5 — failure BEFORE any writer/byte exists → pre-flush (failoverable).
		return false, callErr
	}
	defer func() { _ = stream.Close() }()

	writer := streaming.NewWriter(w)
	chunker := streaming.NewAdapterChunker(stream, servedModel)

	// BR1-2 / Q-F — X-He-Selected-Model = the model SERVING this hop. Set BEFORE
	// the first chunk emit (the writer flushes headers lazily on first WriteEvent
	// — FLOW-001); pre-flush it is overwritable, so the FINAL served model wins.
	w.Header().Set("X-He-Selected-Model", servedModel)

	chunksEmitted, firstFlushAt, streamErr := chunker.Stream(ctx, writer)
	_ = writer.Close()

	if streamErr != nil && !writer.HeadersFlushed() {
		// BR-2.5 pre-flush boundary — the chunker errored before any SSE byte
		// reached the wire → failoverable. The deferred stream.Close() releases
		// the upstream connection before the next hop (BLIND-RESOURCE-001).
		return false, streamErr
	}

	// Committed: the response is either a success or a post-flush error.
	var ttfbMs int64
	if !firstFlushAt.IsZero() {
		ttfbMs = firstFlushAt.Sub(realStart).Milliseconds()
	}
	totalMs := time.Since(realStart).Milliseconds()

	clientDisconnected := streamErr != nil &&
		(errors.Is(streamErr, context.Canceled) ||
			errors.Is(ctx.Err(), context.Canceled))

	if streamErr != nil {
		// BR-2.6 post-flush boundary — emit inline SSE error frame + terminal
		// [DONE]. Status is already 200 (headers flushed); NO failover (BR3-2).
		if !clientDisconnected {
			h.writeSSEErrorFrame(w, ctx, writer, streamErr, req.Model, apiKeyID, chunksEmitted)
		}
	}

	// Story 5.3 ISSUE-001 / Architect Q10 — streaming TPM post-deduction (Q-H:
	// fires once, on the committed stream only).
	h.maybeStreamTPMDeduct(ctx, apiKeyID, req.Model, chunker.TailUsage(), streamErr, clientDisconnected)

	// Story 7.1 — usage.recorded emit AFTER the final chunk with the final token
	// totals (Decision 8B (j) — never pre-flush). Emitted ONLY on a clean
	// completion (streamErr==nil, not client-disconnected, tail usage present);
	// a failed/partial stream emits nothing (BR-D-7).
	if streamErr == nil && !clientDisconnected {
		if tu := chunker.TailUsage(); tu != nil {
			h.emitUsage(ctx, apiKeyID, servedModel, ChatUsage{
				PromptTokens:     int(tu.GetPromptTokens()),
				CompletionTokens: int(tu.GetCompletionTokens()),
				TotalTokens:      int(tu.GetTotalTokens()),
			}, true, false, 0)
		}
	}

	attrs := []slog.Attr{
		slog.String("event", "chat_completions_stream"),
		slog.String("model", req.Model),
		slog.String("served_model", servedModel), // Story 6.3 Q-F (non-PII)
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("chunks_emitted", chunksEmitted),
		slog.Int64("ttfb_ms", ttfbMs),
		slog.Int64("total_ms", totalMs),
		slog.Bool("client_disconnected", clientDisconnected),
		slog.Bool("adapter_path", true),
	}
	if streamErr != nil && !clientDisconnected {
		attrs = append(attrs, slog.String("stream_error", streamErr.Error()))
	} else if clientDisconnected && streamErr != nil {
		attrs = append(attrs, slog.String("flush_error", streamErr.Error()))
	}
	h.logger.LogAttrs(ctx, slog.LevelInfo, "chat_completions_stream", attrs...)
	return true, nil
}

// writeStreamPreflushTerminal emits the BR-2.5 pre-flush JSON envelope at the
// end of the streaming failover loop (status not yet committed). For 502/504 it
// is byte-identical to the pre-6.3 pre-flush write (classifyFailoverError ==
// classifyAdapterError for those codes).
func (h *ChatCompletionsHandler) writeStreamPreflushTerminal(w http.ResponseWriter, ctx context.Context, req *ChatRequest, apiKeyID string, err error) {
	status, code, message, _ := classifyFailoverError(err)
	h.logger.LogAttrs(ctx, slog.LevelWarn, "chat_completions_stream_adapter_preflush_error",
		slog.String("event", "chat_completions_stream_adapter_preflush_error"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
		slog.String("error_code", code),
		slog.String("error", adapterErrString(err)),
	)
	_ = openaierr.Write(w, ctx, status, code, message, nil)
}

// writeSSEErrorFrame emits the BR-2.6 inline error envelope as a single
// `data: {"error":{...}}\n\n` event followed by the literal
// `data: [DONE]\n\n` terminator. The streaming.Writer is reused so the
// frame goes through the same path as content chunks (consistent flush
// semantics + middleware-wrap survival).
func (h *ChatCompletionsHandler) writeSSEErrorFrame(_ http.ResponseWriter, ctx context.Context, writer streaming.Writer, cause error, model, apiKeyID string, chunksEmittedBefore int) {
	_, code, message := classifyAdapterError(cause)
	heRequestID, _ := requestid.FromContext(ctx)
	body := map[string]any{
		"error": map[string]any{
			"code":          code,
			"message":       "Upstream model service interruption during streaming.",
			"type":          openaierrTypeFor(code),
			"param":         nil,
			"he_request_id": heRequestID,
		},
	}
	_ = message // message intentionally collapsed into the streaming-specific BR-2.6 wording above
	payload, _ := json.Marshal(body)
	_ = writer.WriteEvent("", payload)
	_ = writer.WriteDone()

	h.logger.LogAttrs(ctx, slog.LevelWarn, "chat_completions_stream_adapter_postflush_error",
		slog.String("event", "chat_completions_stream_adapter_postflush_error"),
		slog.String("model", model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("chunks_emitted_before_failure", chunksEmittedBefore),
		slog.String("error_code", code),
		slog.String("error", adapterErrString(cause)),
	)
}

// maybeStreamTPMDeduct implements Architect Q10 for the streaming path.
// Called once per streamed request after Stream() returns. Fire-and-forget:
// errors land in slog WARN via the deducter; the response is already on
// the wire.
//
// Cases:
//
//	streamErr == nil + tailUsage != nil  → (i)  TPMDeduct(total_tokens)
//	streamErr != nil + tailUsage != nil  → (ii/iii) TPMDeduct(prompt_tokens)
//	tailUsage == nil                     → (iv) NO deduct + WARN slog
//
// The clientDisconnected flag disambiguates Q10 (ii) (server-side mid-flight
// error) from (iii) (client disconnect) for slog-only purposes; the
// deduction amount is identical in both partial cases.
func (h *ChatCompletionsHandler) maybeStreamTPMDeduct(ctx context.Context, apiKeyID, model string, tailUsage *adapterv1.Usage, streamErr error, clientDisconnected bool) {
	if tailUsage == nil {
		// Q10 case iv — missing-tail-usage. NO deduct; slog WARN so SREs
		// can correlate with upstream regressions.
		h.logger.LogAttrs(ctx, slog.LevelWarn, "ratelimit_tpm_failed_no_deduction",
			slog.String("event", "ratelimit_tpm_failed_no_deduction"),
			slog.String("model", model),
			slog.String("api_key_id", apiKeyID),
			slog.Bool("stream_error", streamErr != nil),
			slog.Bool("client_disconnected", clientDisconnected),
		)
		return
	}
	if streamErr == nil {
		// Q10 case i — normal completion → total_tokens.
		h.tokenDeducter.TPMDeduct(ctx, apiKeyID, int(tailUsage.GetTotalTokens()))
		return
	}
	// Q10 case ii (mid-flight error) OR case iii (client disconnect) —
	// partial deduction of prompt_tokens only (work the upstream
	// actually billed us for).
	partial := int(tailUsage.GetPromptTokens())
	reason := "stream_error"
	if clientDisconnected {
		reason = "client_disconnect"
	}
	h.logger.LogAttrs(ctx, slog.LevelWarn, "ratelimit_tpm_partial_deduction",
		slog.String("event", "ratelimit_tpm_partial_deduction"),
		slog.String("model", model),
		slog.String("api_key_id", apiKeyID),
		slog.String("reason", reason),
		slog.Int("prompt_tokens", partial),
	)
	h.tokenDeducter.TPMDeduct(ctx, apiKeyID, partial)
}

// openaierrTypeFor mirrors the openaierr package's code→type mapping for
// the SSE-inline error envelope (BR-2.6). The package-private mapping in
// openaierr is not exported so this small switch covers the codes the
// gateway emits from Story 4.1.
func openaierrTypeFor(code string) string {
	switch code {
	case "504_upstream_timeout", "502_upstream_unavailable":
		return "server_error"
	case "400_invalid_request":
		return "invalid_request_error"
	default:
		return "server_error"
	}
}
