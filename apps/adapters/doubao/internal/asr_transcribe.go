// Story 9.6 (T3) — the Doubao service's AdapterService.Transcribe RPC. The
// EXISTING Doubao service hosts ASR alongside Chat (Q-ASR-TOPOLOGY, single-
// service-per-vendor). It does a REAL translation: Whisper-shaped
// TranscribeRequest → Volcano ASR upstream (internal/upstream/asr.go) →
// TranscribeResponse. The 5 non-Doubao adapters never serve this (they inherit
// UnimplementedAdapterServiceHandler → CodeUnimplemented).
//
// PII discipline (BR-4.4 cascade): the audio bytes and the recognised transcript
// are NEVER logged / spanned. The structured logs carry only model / duration /
// he_request_id / error-kind.
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

// Transcribe is the Connect-RPC AdapterServiceHandler.Transcribe entry point
// (unary). It overrides the embedded Unimplemented stub on the Doubao service.
func (s *Service) Transcribe(ctx context.Context, req *connect.Request[adapterv1.TranscribeRequest]) (*connect.Response[adapterv1.TranscribeResponse], error) {
	r := req.Msg
	if r == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil TranscribeRequest"))
	}
	// BR-1.5 — propagate he_request_id from the header when the proto field is
	// empty (defence-in-depth, mirrors Chat).
	if r.HeRequestId == "" {
		if hdr := req.Header().Get(connectHeaderRequestID); hdr != "" {
			r.HeRequestId = hdr
		}
	}
	if r.HeRequestId != "" {
		ctx = sharedrid.WithRequestID(ctx, r.HeRequestId)
	}

	// BR-1.12-style fail-fast — a chat-only deployment without ASR config gets
	// CodeUnavailable at request time (NOT a boot crash that would take down the
	// shared Doubao chat service).
	if s.asrClient == nil {
		s.logASRError(ctx, r, upstream.ErrorKindEndpointIDNotConfigured, upstream.ErrASRNotConfigured)
		return nil, connect.NewError(connect.CodeUnavailable, upstream.ErrASRNotConfigured)
	}

	res, err := s.asrClient.Recognize(ctx, upstream.ASRRequest{
		Model:       r.GetModel(),
		Audio:       r.GetAudio(),
		MimeType:    r.GetMimeType(),
		Language:    r.GetLanguage(),
		Prompt:      r.GetPrompt(),
		HeRequestID: r.GetHeRequestId(),
	})
	if err != nil {
		if errors.Is(err, upstream.ErrASRNotConfigured) {
			s.logASRError(ctx, r, upstream.ErrorKindEndpointIDNotConfigured, err)
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		kind := upstream.ClassifyError(err)
		var ue *upstream.UpstreamError
		if errors.As(err, &ue) {
			kind = ue.Kind
		}
		s.logASRError(ctx, r, kind, err)
		return nil, connect.NewError(connectCodeForKind(kind), err)
	}

	// BR-4.4 — success log carries duration/model only (never the transcript).
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = r.GetHeRequestId()
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "asr_transcribe_ok",
		slog.String("event", "asr_transcribe_ok"),
		slog.String("model", r.GetModel()),
		slog.String("he_request_id", heRequestID),
		slog.Int("audio_bytes_size", len(r.GetAudio())),
		slog.Float64("duration_seconds", res.DurationSeconds),
	)

	return connect.NewResponse(&adapterv1.TranscribeResponse{
		Text:            res.Text,
		Language:        res.Language,
		DurationSeconds: res.DurationSeconds,
		SegmentsJson:    res.SegmentsJSON,
	}), nil
}

// logASRError emits a PII-safe failure log (no audio bytes, no transcript).
func (s *Service) logASRError(ctx context.Context, r *adapterv1.TranscribeRequest, kind upstream.ErrorKind, cause error) {
	heRequestID, _ := sharedrid.FromContext(ctx)
	if heRequestID == "" {
		heRequestID = r.GetHeRequestId()
	}
	s.logger.LogAttrs(ctx, slog.LevelWarn, "asr_transcribe_error",
		slog.String("event", "asr_transcribe_error"),
		slog.String("model", r.GetModel()),
		slog.String("he_request_id", heRequestID),
		slog.Int("audio_bytes_size", len(r.GetAudio())),
		slog.String("upstream_error_kind", string(kind)),
		slog.String("error", cause.Error()),
	)
}
