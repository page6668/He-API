// Story 9.7 (T3) — the Doubao service's AdapterService.Synthesize RPC. The
// EXISTING Doubao service hosts TTS alongside Chat + Transcribe (Q-TTS-TOPOLOGY,
// single-service-per-vendor). It does a REAL translation: the validated
// SynthesizeRequest → Volcano TTS upstream (internal/upstream/tts.go) →
// SynthesizeResponse{audio, mime_type}. The 5 non-Doubao adapters never serve
// this (they inherit UnimplementedAdapterServiceHandler → CodeUnimplemented).
//
// PII discipline (BR-4.4 cascade): the input text and the synthesized audio bytes
// are NEVER logged / spanned. The structured logs carry only model / voice /
// response_format / character/byte SIZES / he_request_id / error-kind.
package internal

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	sharedrid "github.com/he-api/he-api/packages/go-observability/requestid"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// Synthesize is the Connect-RPC AdapterServiceHandler.Synthesize entry point
// (unary). It overrides the embedded Unimplemented stub on the Doubao service.
func (s *Service) Synthesize(ctx context.Context, req *connect.Request[adapterv1.SynthesizeRequest]) (*connect.Response[adapterv1.SynthesizeResponse], error) {
	r := req.Msg
	if r == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil SynthesizeRequest"))
	}
	// BR-1.5 — propagate he_request_id from the header when the proto field is
	// empty (defence-in-depth, mirrors Chat + Transcribe).
	if r.HeRequestId == "" {
		if hdr := req.Header().Get(connectHeaderRequestID); hdr != "" {
			r.HeRequestId = hdr
		}
	}
	if r.HeRequestId != "" {
		ctx = sharedrid.WithRequestID(ctx, r.HeRequestId)
	}

	// BR-1.12-style fail-fast — a chat/ASR-only deployment without TTS config gets
	// CodeUnavailable at request time (NOT a boot crash that would take down the
	// shared Doubao chat service).
	if s.ttsClient == nil {
		s.logTTSError(ctx, r, upstream.ErrorKindEndpointIDNotConfigured, upstream.ErrTTSNotConfigured)
		return nil, connect.NewError(connect.CodeUnavailable, upstream.ErrTTSNotConfigured)
	}

	res, err := s.ttsClient.Synthesize(ctx, upstream.TTSRequest{
		Model:          r.GetModel(),
		Input:          r.GetInput(),
		Voice:          r.GetVoice(),
		ResponseFormat: r.GetResponseFormat(),
		Speed:          r.GetSpeed(),
		HeRequestID:    r.GetHeRequestId(),
	})
	if err != nil {
		if errors.Is(err, upstream.ErrTTSNotConfigured) {
			s.logTTSError(ctx, r, upstream.ErrorKindEndpointIDNotConfigured, err)
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		if errors.Is(err, upstream.ErrEmptyVoice) {
			// BR-2.5 defence-in-depth: an unusable voice that survived the AC1
			// gate → invalid_argument (UNIT-017).
			s.logTTSError(ctx, r, upstream.ErrorKindUpstream4xx, err)
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		kind := upstream.ClassifyError(err)
		var ue *upstream.UpstreamError
		if errors.As(err, &ue) {
			kind = ue.Kind
		}
		s.logTTSError(ctx, r, kind, err)
		return nil, connect.NewError(connectCodeForKind(kind), err)
	}

	// BR-4.4 — success log carries SIZES + non-PII signals only (never the input
	// text or the audio bytes).
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = r.GetHeRequestId()
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "tts_synthesize_ok",
		slog.String("event", "tts_synthesize_ok"),
		slog.String("model", r.GetModel()),
		slog.String("he_request_id", heRequestID),
		slog.String("voice", r.GetVoice()),
		slog.String("response_format", r.GetResponseFormat()),
		slog.Int("input_chars_size", len([]rune(r.GetInput()))),
		slog.Int("audio_bytes_size", len(res.Audio)),
	)

	return connect.NewResponse(&adapterv1.SynthesizeResponse{
		Audio:    res.Audio,
		MimeType: res.MimeType,
	}), nil
}

// logTTSError emits a PII-safe failure log (no input text, no audio bytes).
func (s *Service) logTTSError(ctx context.Context, r *adapterv1.SynthesizeRequest, kind upstream.ErrorKind, cause error) {
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = r.GetHeRequestId()
	}
	s.logger.LogAttrs(ctx, slog.LevelWarn, "tts_synthesize_error",
		slog.String("event", "tts_synthesize_error"),
		slog.String("model", r.GetModel()),
		slog.String("he_request_id", heRequestID),
		slog.String("voice", r.GetVoice()),
		slog.String("response_format", r.GetResponseFormat()),
		slog.Int("input_chars_size", len([]rune(r.GetInput()))),
		slog.String("upstream_error_kind", string(kind)),
		slog.String("error", cause.Error()),
	)
}
