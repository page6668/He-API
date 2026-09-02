// Package internal owns the AdapterService.Chat Connect-RPC handler for
// the Doubao (Volcengine Ark v3 OpenAI-compat API) adapter — Story 4.5.
//
// Architectural seams (Architect Round 1 OQ-4.5-1..6 rulings):
//   - Connect-RPC server-streaming (REUSE Story-4.1 OQ2 contract).
//   - HTTP/2-preferred with ALPN HTTP/1.1 fallback (OQ-4.5-6 cascade
//     default from Story-4.2 OQ-4.2-5; see upstream.NewClient).
//   - Ark v3 OpenAI-compat endpoint (OQ-4.5-1) → identity-mapped translate.go
//     for all non-`model` fields (OQ-4.5-3); the `model` field is
//     REWRITTEN bidirectionally (friendly id ↔ Volcengine endpoint id)
//     per BR-1.11 + BR-1.7.f via upstream.EndpointMap.Lookup +
//     TranslateChatResponse / TranslateChatChunk.
//   - Single service hosting `doubao-pro` + `doubao-lite` (OQ-4.5-2 brand
//     env-var policy, BR-1.10 N=2 case of the Story-4.2 multi-model-id
//     pattern — RESTORED after Story-4.4's N=1 detour).
//   - he_request_id propagated via packages/go-observability/requestid.
//   - Architect Round 1 m1 (Story 4.2) cascade: the adapter MUST capture
//     upstream's X-Request-Id response header and emit it as slog
//     attribute `upstream_request_id` on the adapter_chat_request_end
//     log so oncall can correlate from our he_request_id to Volcengine's
//     server-side logs.
//   - NO body-aware classifier — OQ-4.5-6 ratifies verbatim REUSE of
//     Story-4.2 BR-4.4 + Story-4.1 BR-1.4 (Story-4.3 m-1 body-aware
//     classifier is NOT cascaded).
//   - NEW Story-4.5 fail-fast: if upstream.EndpointMap.Lookup returns
//     ErrUnsupportedModel (env var unset), Service.Chat short-circuits
//     with connect.CodeFailedPrecondition per BR-1.12. The upstream
//     HTTPS call is NEVER initiated. Slog
//     `upstream_error_kind=endpoint_id_not_configured` per BR-4.6.
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
	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	"github.com/he-api/he-api/apps/adapters/doubao/internal/usage"
	sharedrid "github.com/he-api/he-api/packages/go-observability/requestid"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// connectHeaderRequestID — the Connect-RPC header name for the propagated
// request-id (BR-1.5). Matches Story-4.1/4.2/4.3/4.4.
const connectHeaderRequestID = "X-He-Request-Id"

// upstreamRequestIDHeader — Volcengine's response header name (m1 cascade).
const upstreamRequestIDHeader = "X-Request-Id"

// chunkSink is the minimal stream-send surface. Tests inject a capture
// implementation; the real *connect.ServerStream[adapterv1.ChatChunk]
// satisfies the interface via its Send method.
type chunkSink interface {
	Send(*adapterv1.ChatChunk) error
}

// Service is the Connect-RPC AdapterServiceHandler implementation for
// the Doubao (Volcengine Ark v3) adapter.
//
// BR-1.10 multi-model-id-per-service N=2 case: NewService accepts a
// `boundModelIDs` list (default `{"doubao-pro", "doubao-lite"}`) for
// slog tagging at startup; the per-call `model` slog attribute is
// sourced from req.Model, NOT this list. The list exists only as an
// observability artefact and gets reflected via the `supportedModels`
// ConfigMap key on the Helm chart.
//
// NEW for Story 4.5: Service owns an EndpointMap (the friendly-id →
// Volcengine endpoint-id static lookup) injected at construction time
// per Architect Round 1 m-1 (test-injectable refactor).
type Service struct {
	adapterv1connect.UnimplementedAdapterServiceHandler

	client        *upstream.Client
	asrClient     *upstream.ASRClient // Story 9.6 — Volcano ASR (nil → Transcribe fail-fasts)
	ttsClient     *upstream.TTSClient // Story 9.7 — Volcano TTS (nil → Synthesize fail-fasts)
	endpointMap   *upstream.EndpointMap
	normaliser    usage.Normaliser
	logger        *slog.Logger
	boundModelIDs []string
}

// WithASRClient wires the Story-9.6 Volcano ASR upstream client. A nil client
// (a chat-only deployment that has not set the ASR envs) makes Transcribe
// fail-fast with CodeUnavailable at request time (the 4.5 BR-1.12 precedent),
// leaving the existing Chat path untouched.
func (s *Service) WithASRClient(c *upstream.ASRClient) *Service {
	s.asrClient = c
	return s
}

// WithTTSClient wires the Story-9.7 Volcano TTS upstream client. A nil client (a
// chat/ASR-only deployment that has not set the TTS envs) makes Synthesize
// fail-fast with CodeUnavailable at request time (the 4.5 BR-1.12 precedent),
// leaving the existing Chat + Transcribe paths untouched.
func (s *Service) WithTTSClient(c *upstream.TTSClient) *Service {
	s.ttsClient = c
	return s
}

// NewService constructs a Service. logger must be non-nil — pass
// slog.Default() if no custom handler is desired. endpointMap MUST be
// non-nil; production callers use upstream.NewFromOS().
func NewService(client *upstream.Client, endpointMap *upstream.EndpointMap, logger *slog.Logger, boundModelIDs []string) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if endpointMap == nil {
		endpointMap = upstream.NewFromOS()
	}
	cp := make([]string, len(boundModelIDs))
	copy(cp, boundModelIDs)
	return &Service{
		client:        client,
		endpointMap:   endpointMap,
		normaliser:    usage.NewDoubao(),
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
	// BR-1.12 fail-fast: probe the endpoint-id lookup BEFORE issuing the
	// HTTPS call. translateRequest also performs this check, but probing
	// here lets the adapter surface CodeFailedPrecondition (NOT
	// CodeUnavailable) per BR-1.12, with the dedicated slog kind
	// `endpoint_id_not_configured` per BR-4.6.
	if _, err := s.endpointMap.Lookup(req.GetModel()); err != nil {
		s.logUpstreamError(ctx, req, upstream.ErrorKindEndpointIDNotConfigured, 0, "", err)
		return connect.NewError(connect.CodeFailedPrecondition, &upstream.UpstreamError{
			Kind:  upstream.ErrorKindEndpointIDNotConfigured,
			Cause: fmt.Errorf("endpoint id not configured for model %s", req.GetModel()),
		})
	}
	if req.Stream {
		return s.chatStreaming(ctx, req, sink)
	}
	return s.chatNonStreaming(ctx, req, sink)
}

// chatNonStreaming performs ONE upstream POST and emits ONE terminal
// ChatChunk to the sink (BR-1.3 non-streaming branch). The response
// `Model` field is back-translated from the Volcengine endpoint id to
// the consumer-facing friendly id (captured from req.Model into a
// per-request closure) per BR-1.11.
func (s *Service) chatNonStreaming(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	friendlyModelID := req.GetModel() // BR-1.11 per-request closure capture
	httpReq, err := upstream.TranslateForTest(ctx, s.client.BaseURLSafe(), s.client.Key(), s.endpointMap, req)
	if err != nil {
		kind := upstream.ClassifyError(err)
		s.logUpstreamError(ctx, req, kind, 0, "", err)
		code := connect.CodeInternal
		if kind == upstream.ErrorKindEndpointIDNotConfigured {
			code = connect.CodeFailedPrecondition
		}
		return connect.NewError(code, fmt.Errorf("translate: %w", err))
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

	// BR-1.11 inbound back-translate — REWRITE response `Model` from
	// the Volcengine endpoint-id echo to the consumer-facing friendly id.
	rewritten := upstream.TranslateChatResponse(&decoded, friendlyModelID)
	chunk := buildTerminalChunk(friendlyModelID, rewritten, normUsage)
	if err := sink.Send(chunk); err != nil {
		return fmt.Errorf("sink send: %w", err)
	}

	s.logRequestEnd(ctx, req, normUsage, httpResp.StatusCode, upstreamRequestID)
	return nil
}

// chatStreaming performs ONE upstream POST with stream=true and emits one
// ChatChunk per decoded SSE frame to the sink (BR-1.3 streaming + BR-2.3
// strict-RFC decoder + BR-2.4 terminal-chunk-carries-usage + BR-2.9
// forced include_usage + BR-1.11 per-chunk back-translate).
func (s *Service) chatStreaming(ctx context.Context, req *adapterv1.ChatRequest, sink chunkSink) error {
	friendlyModelID := req.GetModel() // BR-1.11 per-request closure capture
	httpReq, err := upstream.TranslateForTest(ctx, s.client.BaseURLSafe(), s.client.Key(), s.endpointMap, req)
	if err != nil {
		kind := upstream.ClassifyError(err)
		s.logUpstreamError(ctx, req, kind, 0, "", err)
		code := connect.CodeInternal
		if kind == upstream.ErrorKindEndpointIDNotConfigured {
			code = connect.CodeFailedPrecondition
		}
		return connect.NewError(code, fmt.Errorf("translate: %w", err))
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
		terminalUsage *usage.NormalisedUsage
		emittedAny    bool
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
		// BR-1.11 per-chunk back-translate — REWRITE chunk `Model` from
		// Volcengine endpoint-id echo to consumer-facing friendly id.
		rewritten := upstream.TranslateChatChunk(raw, friendlyModelID)
		var (
			normUsage *usage.NormalisedUsage
			hasUsage  bool
		)
		if rewritten.Usage != nil {
			n, err := s.normaliser.Normalise(*rewritten.Usage)
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
		chunk := buildStreamingChunk(friendlyModelID, rewritten, normUsage, hasUsage)
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
// Connect-RPC ChatChunk. `model` is the consumer-facing friendly id
// (already back-translated per BR-1.11).
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
// terminal ChatChunk (BR-1.3). `model` is the consumer-facing friendly id
// (already back-translated per BR-1.11).
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
// to the gateway. NEW for Story 4.5: ErrorKindEndpointIDNotConfigured
// → CodeFailedPrecondition per BR-1.12 fail-fast.
func connectCodeForKind(kind upstream.ErrorKind) connect.Code {
	switch kind {
	case upstream.ErrorKindUpstreamTimeout:
		return connect.CodeDeadlineExceeded
	case upstream.ErrorKindEndpointIDNotConfigured:
		return connect.CodeFailedPrecondition
	default:
		return connect.CodeUnavailable
	}
}

// logUpstreamError emits the BR-1.9 PII-safe structured log on failure.
// m1 cascade: `upstream_request_id` is recorded when present so oncall
// can correlate from our he_request_id → Volcengine's server-side logs.
//
// BR-1.9 EXPANDED for Story 4.5: the slog `model` attribute carries the
// consumer-facing friendly id (req.Model = doubao-pro/doubao-lite),
// NEVER the Volcengine endpoint id. The endpoint id is deployment-time
// configuration and MUST NOT leak to log-aggregation per BR-1.9
// expanded.
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
// m1 — also emits `upstream_request_id` (Volcengine-generated) for oncall
// correlation when Volcengine sets the X-Request-Id response header.
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

// Compile-time time + http import retention.
var (
	_ = time.Second
	_ = http.MethodPost
)
