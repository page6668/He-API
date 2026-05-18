// Story 3.4 — Streaming-dispatch bridge from ChatCompletionsHandler.ServeHTTP
// to the internal/streaming package.
//
// serveStream owns:
//   - Writer construction (streaming.NewWriter wraps the ResponseWriter)
//   - MockChunker construction with primitives (BR-1.4 id + BR-1.5 created +
//     BR-1.6 model echo + handlers.MockContent supplied at this call site —
//     the streaming package does NOT import handlers per Architect Round 1
//     C1 fix)
//   - BR-1.8 structured-log emission at stream completion (success OR
//     disconnect), with PII redaction discipline inherited from Story 3.3.
//
// All headers + body bytes are written THROUGH the streaming.Writer — no
// direct w.Header().Set or w.WriteHeader call lives here.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/streaming"
)

// serveStream is invoked from ServeHTTP when validateChatRequest succeeds
// AND req.Stream == true. The caller has already enforced bearer-auth +
// body-size + JSON parse + role validation.
func (h *ChatCompletionsHandler) serveStream(w http.ResponseWriter, r *http.Request, req *ChatRequest, apiKeyID string) {
	ctx := r.Context()
	// realStart anchors TTFB + total_ms to wall-clock — these are observability
	// dimensions tracking actual elapsed time, NOT logical-completion identity.
	// h.now() (which tests may stub) drives only the `created` field per BR-1.5
	// so the chunk timestamps stay deterministic in golden tests.
	realStart := time.Now()
	id := h.newID()
	created := h.now().UTC().Unix()

	writer := streaming.NewWriter(w)
	chunker := streaming.NewMockChunker(req.Model, MockContent, id, created)

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
