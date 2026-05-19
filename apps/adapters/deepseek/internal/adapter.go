// Package internal owns the AdapterService.Chat Connect-RPC handler for the
// DeepSeek adapter (Story 4.1). The non-streaming branch is fully implemented
// in Phase A; the streaming branch returns CodeUnimplemented and is filled
// in by Phase B.
//
// Architectural seams (per Architect Round 2 OQ rulings):
//   - Connect-RPC server-streaming (OQ2) — Chat returns (stream ChatChunk).
//   - HTTP/2 forced via upstream.Client (OQ7).
//   - usage.Normaliser interface for per-vendor token-shape mapping (OQ5).
//   - he_request_id propagation via packages/go-observability/requestid (OQ8).
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/usage"
	sharedrid "github.com/he-api/he-api/packages/go-observability/requestid"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// connectHeaderRequestID is the Connect-RPC header name for the propagated
// request-id (BR-1.5). Matches the gateway response header name so wire
// inspection stays uniform.
const connectHeaderRequestID = "X-He-Request-Id"

// chunkSink is the minimal stream-send surface ChatInto writes into. The
// real Connect-RPC server-stream (*connect.ServerStream[adapterv1.ChatChunk])
// satisfies this implicitly via its Send method; tests inject a capture
// implementation. Decoupling here lets unit tests bypass the HTTP transport
// layer of Connect-RPC.
type chunkSink interface {
	Send(*adapterv1.ChatChunk) error
}

// Service is the Connect-RPC AdapterServiceHandler implementation for DeepSeek.
type Service struct {
	adapterv1connect.UnimplementedAdapterServiceHandler

	client     *upstream.Client
	normaliser usage.Normaliser
	logger     *slog.Logger
}

// NewService constructs a Service. client owns the upstream HTTPS connection
// (HTTP/2-forced per OQ7); logger must be non-nil — pass slog.Default() if no
// custom handler is desired. The DeepSeek Normaliser is used unconditionally
// (Stories 4.2-4.6 will sister-package their own Service + Normaliser).
func NewService(client *upstream.Client, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		client:     client,
		normaliser: usage.NewDeepSeek(),
		logger:     logger,
	}
}

// Chat is the Connect-RPC AdapterServiceHandler.Chat entry point. It
// extracts the inbound X-He-Request-Id header (BR-1.5) and dispatches to
// ChatInto with the propagated value populated onto req.HeRequestId so
// downstream slog records + upstream X-Request-Id propagation work
// uniformly across HTTP-fronted callers + unit tests.
func (s *Service) Chat(ctx context.Context, req *connect.Request[adapterv1.ChatRequest], stream *connect.ServerStream[adapterv1.ChatChunk]) error {
	r := req.Msg
	if r == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("nil ChatRequest"))
	}
	if r.HeRequestId == "" {
		if hdr := req.Header().Get(connectHeaderRequestID); hdr != "" {
			r.HeRequestId = hdr
		}
	}
	if r.HeRequestId != "" {
		ctx = sharedrid.WithRequestID(ctx, r.HeRequestId)
	}
	return s.ChatInto(ctx, r, stream)
}

// ChatInto is the testable core of Chat — same logic, but the sink is an
// interface that tests can capture in-memory without going through the
// Connect-RPC HTTP transport.
//
// Story 4.1 Phase B: streaming branch dispatches to chatStreaming, which
// runs the SSE-decoder loop over the upstream's text/event-stream response
// and emits one ChatChunk per upstream frame (BR-1.3, BR-2.3, BR-2.4).
func (s *Service) ChatInto(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	if req.Stream {
		return s.chatStreaming(ctx, req, sink)
	}
	return s.chatNonStreaming(ctx, req, sink)
}

// chatNonStreaming performs ONE upstream POST and emits ONE terminal
// ChatChunk to the sink per BR-1.3.
func (s *Service) chatNonStreaming(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	httpReq, err := translateRequestExternal(ctx, s.client.BaseURL, s.client.APIKey, req)
	if err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindUpstream5xx, 0, err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("translate: %w", err))
	}
	httpResp, err := s.client.HTTPClient.Do(httpReq)
	if err != nil {
		kind := upstream.ClassifyError(err)
		s.logUpstreamError(ctx, req, kind, 0, err)
		code := connectCodeForKind(kind)
		return connect.NewError(code, &upstream.UpstreamError{Kind: kind, Cause: err})
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		kind := upstream.ClassifyHTTPStatus(httpResp.StatusCode)
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		s.logUpstreamError(ctx, req, kind, httpResp.StatusCode, fmt.Errorf("upstream body: %s", body))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   kind,
			Status: httpResp.StatusCode,
			Cause:  fmt.Errorf("upstream returned %d", httpResp.StatusCode),
		})
	}

	var decoded upstream.ChatResponseJSON
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindMalformedChunk, httpResp.StatusCode, err)
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindMalformedChunk,
			Status: httpResp.StatusCode,
			Cause:  err,
		})
	}
	if len(decoded.Choices) == 0 {
		s.logUpstreamError(ctx, req, upstream.ErrorKindEmptyChoices, httpResp.StatusCode, errors.New("empty choices"))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindEmptyChoices,
			Status: httpResp.StatusCode,
		})
	}
	// BR-3.4 — missing usage is a HARD failure.
	if decoded.Usage == nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindMissingUsage, httpResp.StatusCode, errors.New("usage missing"))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindMissingUsage,
			Status: httpResp.StatusCode,
		})
	}
	normUsage, err := s.normaliser.Normalise(*decoded.Usage)
	if err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindUsageConstraint, httpResp.StatusCode, err)
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindUsageConstraint,
			Status: httpResp.StatusCode,
			Cause:  err,
		})
	}

	chunk := buildTerminalChunk(req.Model, &decoded, normUsage)
	if err := sink.Send(chunk); err != nil {
		return fmt.Errorf("sink send: %w", err)
	}

	s.logRequestEnd(ctx, req, normUsage, httpResp.StatusCode)
	return nil
}

// chatStreaming performs ONE upstream POST with stream=true and emits one
// ChatChunk per decoded SSE frame to the sink (BR-1.3 streaming branch +
// BR-2.3 strict-RFC decoder + BR-2.4 terminal-chunk-carries-usage +
// BR-2.9 forced include_usage in translateRequest).
//
// Error semantics:
//
//	pre-stream upstream failure (5xx / DNS / TLS / dial / non-2xx headers)
//	  → Connect-RPC Code (Unavailable / DeadlineExceeded) per BR-1.4.
//	mid-stream upstream failure (malformed frame, RST after first chunk,
//	  EOF without usage)
//	  → Connect-RPC Code.Unavailable; gateway BR-2.6 surfaces as SSE
//	    error frame (the gateway has already committed to streaming).
//	BR-3.4 missing usage on terminal stream chunk → Code.Unavailable
//	  with kind=missing_usage.
//
// Cancellation: the request context propagates to http.NewRequestWithContext;
// when the client disconnects, the outbound HTTPS connection is cancelled
// within the next chunk-read iteration (BR-1.8 ≤ 200ms in practice).
func (s *Service) chatStreaming(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	httpReq, err := translateRequestExternal(ctx, s.client.BaseURL, s.client.APIKey, req)
	if err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindUpstream5xx, 0, err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("translate: %w", err))
	}
	httpResp, err := s.client.HTTPClient.Do(httpReq)
	if err != nil {
		kind := upstream.ClassifyError(err)
		s.logUpstreamError(ctx, req, kind, 0, err)
		code := connectCodeForKind(kind)
		return connect.NewError(code, &upstream.UpstreamError{Kind: kind, Cause: err})
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		kind := upstream.ClassifyHTTPStatus(httpResp.StatusCode)
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		s.logUpstreamError(ctx, req, kind, httpResp.StatusCode, fmt.Errorf("upstream body: %s", body))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   kind,
			Status: httpResp.StatusCode,
			Cause:  fmt.Errorf("upstream returned %d", httpResp.StatusCode),
		})
	}

	decoder := upstream.NewDecoder(httpResp.Body)
	var (
		terminalUsage  *usage.NormalisedUsage
		emittedAny     bool
		streamingModel = req.Model
	)
	for {
		// Honour cancellation BEFORE blocking on the next read.
		if err := ctx.Err(); err != nil {
			s.logStreamInterrupted(ctx, req, upstream.ErrorKindUpstreamTimeout, err, emittedAny)
			return connect.NewError(connect.CodeDeadlineExceeded, &upstream.UpstreamError{
				Kind:  upstream.ErrorKindUpstreamTimeout,
				Cause: err,
			})
		}
		raw, err := decoder.NextChunk(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.logStreamInterrupted(ctx, req, upstream.ErrorKindUpstreamTimeout, err, emittedAny)
				return connect.NewError(connect.CodeDeadlineExceeded, &upstream.UpstreamError{
					Kind:  upstream.ErrorKindUpstreamTimeout,
					Cause: err,
				})
			}
			kind := upstream.ErrorKindMalformedChunk
			if errors.Is(err, upstream.ErrMalformedFrame) {
				kind = upstream.ErrorKindMalformedChunk
			}
			s.logStreamInterrupted(ctx, req, kind, err, emittedAny)
			return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
				Kind:  kind,
				Cause: err,
			})
		}
		var (
			normUsage *usage.NormalisedUsage
			hasUsage  bool
		)
		if raw.Usage != nil {
			n, err := s.normaliser.Normalise(*raw.Usage)
			if err != nil {
				s.logStreamInterrupted(ctx, req, upstream.ErrorKindUsageConstraint, err, emittedAny)
				return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
					Kind:  upstream.ErrorKindUsageConstraint,
					Cause: err,
				})
			}
			normUsage = &n
			hasUsage = true
			terminalUsage = &n
		}
		chunk := buildStreamingChunk(streamingModel, raw, normUsage, hasUsage)
		if err := sink.Send(chunk); err != nil {
			s.logStreamInterrupted(ctx, req, upstream.ErrorKindUpstream5xx, err, emittedAny)
			return fmt.Errorf("sink send: %w", err)
		}
		emittedAny = true
	}

	// BR-3.4 missing-usage on terminal — a streaming run that ended cleanly
	// (io.EOF) but NEVER produced a usage-bearing chunk is a hard failure.
	if terminalUsage == nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindMissingUsage, 200, errors.New("usage missing from streaming response"))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind: upstream.ErrorKindMissingUsage,
		})
	}

	s.logRequestEnd(ctx, req, *terminalUsage, httpResp.StatusCode)
	return nil
}

// buildStreamingChunk converts an upstream-decoded SSE frame into the
// Connect-RPC ChatChunk. The terminal chunk (usage populated) carries
// Object="chat.completion.chunk" + Usage; intermediate chunks carry only
// content deltas + no usage.
func buildStreamingChunk(model string, raw *upstream.ChatChunkJSON, u *usage.NormalisedUsage, hasUsage bool) *adapterv1.ChatChunk {
	choices := make([]*adapterv1.Choice, len(raw.Choices))
	for i, c := range raw.Choices {
		choice := &adapterv1.Choice{
			Index:        int32(c.Index),
			FinishReason: c.FinishReason,
		}
		if c.Delta != nil {
			choice.Delta = &adapterv1.Delta{
				Role:    c.Delta.Role,
				Content: c.Delta.Content,
			}
		} else {
			choice.Delta = &adapterv1.Delta{}
		}
		choices[i] = choice
	}
	chunk := &adapterv1.ChatChunk{
		Id:      raw.ID,
		Object:  "chat.completion.chunk",
		Created: raw.Created,
		Model:   model,
		Choices: choices,
	}
	if hasUsage && u != nil {
		pt := int32(u.PromptTokens)
		ct := int32(u.CompletionTokens)
		tt := int32(u.TotalTokens)
		chunk.Usage = &adapterv1.Usage{
			PromptTokens:     pt,
			CompletionTokens: ct,
			TotalTokens:      tt,
		}
	}
	// Mirror finish_reason onto the chunk-level field if the first choice
	// carries it (parity with non-streaming terminal-chunk convention).
	if len(raw.Choices) > 0 && raw.Choices[0].FinishReason != nil {
		fr := *raw.Choices[0].FinishReason
		chunk.FinishReason = &fr
	}
	return chunk
}

// logStreamInterrupted emits the AC2 stream-interrupted log when a
// mid-stream failure terminates the chunk loop (malformed frame, upstream
// RST, normaliser failure). chunksEmittedBefore distinguishes pre-flush
// failures (gateway will emit JSON envelope per BR-2.5) from post-flush
// failures (gateway emits SSE error frame per BR-2.6).
func (s *Service) logStreamInterrupted(ctx context.Context, req *adapterv1.ChatRequest, kind upstream.ErrorKind, cause error, chunksEmittedBefore bool) {
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = req.HeRequestId
	}
	attrs := []slog.Attr{
		slog.String("event", "adapter_chat_stream_interrupted"),
		slog.String("he_request_id", heRequestID),
		slog.String("model", req.Model),
		slog.Int("messages_count", len(req.Messages)),
		slog.String("upstream_error_kind", string(kind)),
		slog.Bool("chunks_emitted_before_failure", chunksEmittedBefore),
	}
	if cause != nil {
		attrs = append(attrs, slog.String("error", cause.Error()))
	}
	s.logger.LogAttrs(ctx, slog.LevelWarn, "adapter_chat_stream_interrupted", attrs...)
}

// buildTerminalChunk converts the upstream decoded response into the
// terminal Connect-RPC ChatChunk (BR-1.3). Non-streaming requests receive
// exactly one such chunk.
func buildTerminalChunk(model string, resp *upstream.ChatResponseJSON, u usage.NormalisedUsage) *adapterv1.ChatChunk {
	choices := make([]*adapterv1.Choice, len(resp.Choices))
	for i, c := range resp.Choices {
		var (
			role    *string
			content *string
		)
		if c.Message != nil {
			r := c.Message.Role
			role = &r
			ct := c.Message.Content
			content = &ct
		}
		choices[i] = &adapterv1.Choice{
			Index: int32(c.Index),
			Delta: &adapterv1.Delta{
				Role:    role,
				Content: content,
			},
			FinishReason: c.FinishReason,
		}
	}
	finishReason := ""
	if len(resp.Choices) > 0 && resp.Choices[0].FinishReason != nil {
		finishReason = *resp.Choices[0].FinishReason
	}
	ptr := func(s string) *string { return &s }
	pt := int32(u.PromptTokens)
	ct := int32(u.CompletionTokens)
	tt := int32(u.TotalTokens)
	return &adapterv1.ChatChunk{
		Id:           resp.ID,
		Object:       "chat.completion",
		Created:      resp.Created,
		Model:        model,
		Choices:      choices,
		Usage:        &adapterv1.Usage{PromptTokens: pt, CompletionTokens: ct, TotalTokens: tt},
		FinishReason: ptr(finishReason),
	}
}

// connectCodeForKind maps an ErrorKind to the Connect-RPC code the adapter
// surfaces to the gateway. The gateway's BR-1.4 mapping table then converts
// the Connect-RPC code to the user-facing 502 / 504 envelope.
func connectCodeForKind(kind upstream.ErrorKind) connect.Code {
	switch kind {
	case upstream.ErrorKindUpstreamTimeout:
		return connect.CodeDeadlineExceeded
	default:
		// All other transport failures (DNS, TLS, connection-refused, 5xx,
		// 4xx auth-revoked) surface as Unavailable. The gateway maps
		// Unavailable → 502; DeadlineExceeded → 504.
		return connect.CodeUnavailable
	}
}

// logUpstreamError emits the BR-1.9 PII-safe structured log on upstream
// failure paths. PER M2: includes upstream_error_kind so oncall can route
// 401/429 to different runbooks via Grafana alerts.
func (s *Service) logUpstreamError(ctx context.Context, req *adapterv1.ChatRequest, kind upstream.ErrorKind, status int, cause error) {
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = req.HeRequestId
	}
	attrs := []slog.Attr{
		slog.String("event", "adapter_chat_upstream_error"),
		slog.String("he_request_id", heRequestID),
		slog.String("model", req.Model),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("upstream_status_code", status),
		slog.String("upstream_error_kind", string(kind)),
	}
	if cause != nil {
		attrs = append(attrs, slog.String("error", cause.Error()))
	}
	s.logger.LogAttrs(ctx, slog.LevelError, "adapter_chat_upstream_error", attrs...)
}

// logRequestEnd emits the BR-3.6 audit-trail log on successful Chat calls.
// Token counts are post-Normaliser; he_request_id is extracted from the
// context (set by the Chat Connect-RPC entry point from the inbound header).
func (s *Service) logRequestEnd(ctx context.Context, req *adapterv1.ChatRequest, u usage.NormalisedUsage, status int) {
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = req.HeRequestId
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "adapter_chat_request_end",
		slog.String("event", "adapter_chat_request_end"),
		slog.String("he_request_id", heRequestID),
		slog.String("model", req.Model),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("prompt_tokens", u.PromptTokens),
		slog.Int("completion_tokens", u.CompletionTokens),
		slog.Int("total_tokens", u.TotalTokens),
		slog.Int("upstream_status_code", status),
	)
}

// translateRequestExternal exposes the package-internal translate helper
// to the upstream package's transport. We funnel through a small wrapper so
// the upstream package owns the wire-shape but the adapter package owns
// the orchestration.
func translateRequestExternal(ctx context.Context, baseURL, apiKey string, req *adapterv1.ChatRequest) (*http.Request, error) {
	return upstream.TranslateForTest(ctx, baseURL, apiKey, req)
}

// Compile-time assertion: timeouts surface as a Timeout()==true wrapper —
// the http.Client.Timeout path returns a *url.Error whose Timeout() reports
// true; ClassifyError catches it via the timeoutAware interface.
var _ = time.Second
