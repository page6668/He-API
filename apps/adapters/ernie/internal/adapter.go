// Package internal owns the AdapterService.Chat Connect-RPC handler for
// the Ernie (Baidu Qianfan v2 OpenAI-compat API) adapter — Story 4.6.
//
// Architectural seams (Architect Round 1 OQ-4.6-1..6 rulings):
//   - Connect-RPC server-streaming (REUSE Story-4.1 OQ2 contract).
//   - HTTP/2-preferred with ALPN HTTP/1.1 fallback (OQ-4.6-5 cascade
//     default from Story-4.2 OQ-4.2-5; see upstream.NewClient).
//   - Qianfan v2 OpenAI-compat endpoint (OQ-4.6-1) → identity-mapped
//     translate.go (OQ-4.6-3).
//   - Single service hosting `ernie-4.0` (OQ-4.6-2 brand env-var policy,
//     BR-1.10 N=1 degenerate case — closes the Epic-4 six-vendor
//     matrix). req.Model is forwarded VERBATIM to upstream's `model`
//     body field; the Service does NOT distinguish models at the
//     service layer.
//   - he_request_id propagated via packages/go-observability/requestid.
//   - Architect Round 1 m1 (Story 4.2) cascade + m-2 (Story 4.6): the
//     adapter MUST capture upstream's `x-bce-request-id` response header
//     (Baidu BCE platform convention) and emit it as slog attribute
//     `upstream_request_id` on `adapter_chat_request_end` so oncall can
//     correlate from our he_request_id to Baidu's server-side logs.
//     Empirical-verify deferred to Phase 0 staging deploy per m-2.
//   - NO body-aware classifier — OQ-4.6-6 ratifies verbatim REUSE of
//     Story-4.2 BR-4.4 + Story-4.1 BR-1.4 (Story-4.3 m-1
//     ClassifyMoonshotErrorBody helper is NOT cascaded; if a future
//     Baidu-specific failure shape surfaces, append to errors.go per the
//     Story-4.3 m-1 precedent without an Architect Round 2 cycle).
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
	"github.com/he-api/he-api/apps/adapters/ernie/internal/upstream"
	"github.com/he-api/he-api/apps/adapters/ernie/internal/usage"
	sharedrid "github.com/he-api/he-api/packages/go-observability/requestid"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// connectHeaderRequestID — the Connect-RPC header name for the propagated
// request-id (BR-1.5). Matches Story-4.1/4.2/4.3/4.4/4.5.
const connectHeaderRequestID = "X-He-Request-Id"

// upstreamRequestIDHeader — Baidu Qianfan v2's response header name per
// m-2 SM-leaned BCE platform convention (`x-bce-request-id`). Centralised
// as a constant so the Phase-0 empirical-verify can flip the value with
// a one-line change should Qianfan v2 use a different header (e.g.,
// `qianfan-request-id`, `x-trace-id`).
const upstreamRequestIDHeader = "X-Bce-Request-Id"

// chunkSink is the minimal stream-send surface. Tests inject a capture
// implementation; the real *connect.ServerStream[adapterv1.ChatChunk]
// satisfies the interface via its Send method.
type chunkSink interface {
	Send(*adapterv1.ChatChunk) error
}

// Service is the Connect-RPC AdapterServiceHandler implementation for Ernie.
//
// BR-1.10 multi-model-id-per-service N=1 degenerate case: NewService
// accepts a `boundModelIDs` list (default `{"ernie-4.0"}`) for slog
// tagging at startup; the per-call `model` slog attribute is sourced
// from req.Model, NOT this list. The list exists only as an
// observability artefact and gets reflected via the `supportedModels`
// ConfigMap key on the Helm chart.
type Service struct {
	adapterv1connect.UnimplementedAdapterServiceHandler

	client        *upstream.Client
	normaliser    usage.Normaliser
	logger        *slog.Logger
	boundModelIDs []string
}

// NewService constructs a Service. logger must be non-nil — pass
// slog.Default() if no custom handler is desired.
func NewService(client *upstream.Client, logger *slog.Logger, boundModelIDs []string) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	cp := make([]string, len(boundModelIDs))
	copy(cp, boundModelIDs)
	return &Service{
		client:        client,
		normaliser:    usage.NewErnie(),
		logger:        logger,
		boundModelIDs: cp,
	}
}

// BoundModelIDs returns the model-id list this service hosts. Useful for
// startup observability + tests.
func (s *Service) BoundModelIDs() []string {
	cp := make([]string, len(s.boundModelIDs))
	copy(cp, s.boundModelIDs)
	return cp
}

// Chat is the Connect-RPC AdapterServiceHandler.Chat entry point.
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
func (s *Service) ChatInto(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	if req.Stream {
		return s.chatStreaming(ctx, req, sink)
	}
	return s.chatNonStreaming(ctx, req, sink)
}

// chatNonStreaming performs ONE upstream POST and emits ONE terminal
// ChatChunk to the sink (BR-1.3 non-streaming branch).
func (s *Service) chatNonStreaming(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	httpReq, err := upstream.TranslateForTest(ctx, s.client.BaseURL, s.client.APIKey, req)
	if err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindUpstream5xx, 0, "", err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("translate: %w", err))
	}
	httpResp, err := s.client.HTTPClient.Do(httpReq)
	if err != nil {
		kind := upstream.ClassifyError(err)
		s.logUpstreamError(ctx, req, kind, 0, "", err)
		code := connectCodeForKind(kind)
		return connect.NewError(code, &upstream.UpstreamError{Kind: kind, Cause: err})
	}
	defer httpResp.Body.Close()

	upstreamRequestID := httpResp.Header.Get(upstreamRequestIDHeader)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Bounded read for ops triage; body is NOT logged or echoed per BR-1.9.
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		// OQ-4.6-6 verbatim REUSE: status-only classification; no
		// Baidu-specific body-aware classifier (drop Story-4.3 m-1
		// cascade).
		kind := upstream.ClassifyHTTPStatus(httpResp.StatusCode)
		s.logUpstreamError(ctx, req, kind, httpResp.StatusCode, upstreamRequestID, fmt.Errorf("upstream body: %s", body))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   kind,
			Status: httpResp.StatusCode,
			Cause:  fmt.Errorf("upstream returned %d", httpResp.StatusCode),
		})
	}

	var decoded upstream.ChatResponseJSON
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindMalformedChunk, httpResp.StatusCode, upstreamRequestID, err)
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindMalformedChunk,
			Status: httpResp.StatusCode,
			Cause:  err,
		})
	}
	if len(decoded.Choices) == 0 {
		s.logUpstreamError(ctx, req, upstream.ErrorKindEmptyChoices, httpResp.StatusCode, upstreamRequestID, errors.New("empty choices"))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindEmptyChoices,
			Status: httpResp.StatusCode,
		})
	}
	if decoded.Usage == nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindMissingUsage, httpResp.StatusCode, upstreamRequestID, errors.New("usage missing"))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind:   upstream.ErrorKindMissingUsage,
			Status: httpResp.StatusCode,
		})
	}
	normUsage, err := s.normaliser.Normalise(*decoded.Usage)
	if err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindUsageConstraint, httpResp.StatusCode, upstreamRequestID, err)
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

	s.logRequestEnd(ctx, req, normUsage, httpResp.StatusCode, upstreamRequestID)
	return nil
}

// chatStreaming performs ONE upstream POST with stream=true and emits one
// ChatChunk per decoded SSE frame to the sink (BR-1.3 streaming + BR-2.3
// strict-RFC decoder + BR-2.4 terminal-chunk-carries-usage + BR-2.9
// forced include_usage).
func (s *Service) chatStreaming(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	httpReq, err := upstream.TranslateForTest(ctx, s.client.BaseURL, s.client.APIKey, req)
	if err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindUpstream5xx, 0, "", err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("translate: %w", err))
	}
	httpResp, err := s.client.HTTPClient.Do(httpReq)
	if err != nil {
		kind := upstream.ClassifyError(err)
		s.logUpstreamError(ctx, req, kind, 0, "", err)
		return connect.NewError(connectCodeForKind(kind), &upstream.UpstreamError{Kind: kind, Cause: err})
	}
	defer httpResp.Body.Close()

	upstreamRequestID := httpResp.Header.Get(upstreamRequestIDHeader)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		kind := upstream.ClassifyHTTPStatus(httpResp.StatusCode)
		s.logUpstreamError(ctx, req, kind, httpResp.StatusCode, upstreamRequestID, fmt.Errorf("upstream body: %s", body))
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

	if terminalUsage == nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindMissingUsage, 200, upstreamRequestID, errors.New("usage missing from streaming response"))
		return connect.NewError(connect.CodeUnavailable, &upstream.UpstreamError{
			Kind: upstream.ErrorKindMissingUsage,
		})
	}

	s.logRequestEnd(ctx, req, *terminalUsage, httpResp.StatusCode, upstreamRequestID)
	return nil
}

// buildStreamingChunk converts an upstream-decoded SSE frame into the
// Connect-RPC ChatChunk.
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
	if len(raw.Choices) > 0 && raw.Choices[0].FinishReason != nil {
		fr := *raw.Choices[0].FinishReason
		chunk.FinishReason = &fr
	}
	return chunk
}

// buildTerminalChunk converts the upstream non-streaming response into the
// terminal ChatChunk (BR-1.3).
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

// connectCodeForKind maps an ErrorKind to the Connect-RPC code surfaced
// to the gateway.
func connectCodeForKind(kind upstream.ErrorKind) connect.Code {
	switch kind {
	case upstream.ErrorKindUpstreamTimeout:
		return connect.CodeDeadlineExceeded
	default:
		return connect.CodeUnavailable
	}
}

// logUpstreamError emits the BR-1.9 PII-safe structured log on failure.
// m1 cascade: `upstream_request_id` is recorded when present so oncall
// can correlate from our he_request_id → Baidu's server-side logs.
func (s *Service) logUpstreamError(ctx context.Context, req *adapterv1.ChatRequest, kind upstream.ErrorKind, status int, upstreamRequestID string, cause error) {
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
	if upstreamRequestID != "" {
		attrs = append(attrs, slog.String("upstream_request_id", upstreamRequestID))
	}
	if cause != nil {
		attrs = append(attrs, slog.String("error", cause.Error()))
	}
	s.logger.LogAttrs(ctx, slog.LevelError, "adapter_chat_upstream_error", attrs...)
}

// logStreamInterrupted emits the BR-2 stream-interrupted log on
// mid-stream failure.
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

// logRequestEnd emits the BR-3.6 audit-trail log on successful Chat calls.
// m1 — also emits `upstream_request_id` (Baidu-generated) for oncall
// correlation when Qianfan v2 sets the `x-bce-request-id` response
// header (SM lean per m-2; pending Phase 0 empirical-verify).
func (s *Service) logRequestEnd(ctx context.Context, req *adapterv1.ChatRequest, u usage.NormalisedUsage, status int, upstreamRequestID string) {
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = req.HeRequestId
	}
	attrs := []slog.Attr{
		slog.String("event", "adapter_chat_request_end"),
		slog.String("he_request_id", heRequestID),
		slog.String("model", req.Model),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("prompt_tokens", u.PromptTokens),
		slog.Int("completion_tokens", u.CompletionTokens),
		slog.Int("total_tokens", u.TotalTokens),
		slog.Int("upstream_status_code", status),
	}
	if upstreamRequestID != "" {
		attrs = append(attrs, slog.String("upstream_request_id", upstreamRequestID))
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "adapter_chat_request_end", attrs...)
}

// Compile-time time import retention.
var _ = time.Second

// Compile-time assertion the package's http.Request type aliasing.
var _ = http.MethodPost
