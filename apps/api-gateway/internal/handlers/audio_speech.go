// Story 9.7 — POST /v1/audio/speech handler (OpenAI-Audio-Speech-compatible TTS).
//
// The FIRST binary (non-JSON / non-SSE) response body in the gateway: the request
// is plain JSON ({model, input, voice, response_format?, speed?}) but the RESPONSE
// is the raw synthesized audio bytes (audio/mpeg | audio/wav | audio/ogg).
// Mirrors the Story-3.5 embeddings JSON 2nd-endpoint precedent (own body-cap
// const, own validator) and the Story-9.6 ASR adapter-backed path (Synthesizer
// seam, content-safety on the TEXT input, PII discipline) — but audio rides the
// RESPONSE, not the request.
//
// Security posture (AC4, non-negotiable):
//   - 64 KiB read cap via http.MaxBytesReader BEFORE json.Decode (DisallowUnknownFields);
//   - HARD 4096-rune input cap (also the billing lever — gateway-computed, anti-underpay);
//   - 20 MiB synthesized-audio OUTPUT bound (enforced here AND at the adapter);
//   - content-safety scans the INPUT text (do-NOT-regress vs 9.6's opaque audio);
//     the synthesized OUTPUT audio is model output and is NOT scanned;
//   - PII discipline: the input text AND the audio bytes are NEVER logged / spanned
//     / echoed in errors — logs carry only size/voice/format/model signals.
//   - no durable audio / no temp files (the JSON request needs no multipart).
//
// Money one-SoT: the gateway emits the RAW character_count (len([]rune(input)))
// to billing-svc (the SOLE cost authority — PER_CHARACTER). The gateway holds NO
// USD cost (project_cost_source_oq5_usage_ledger — X-He-Cost-Usd absent,
// contract-enforced); 消费 is computed once by billing-svc into usage_ledger and
// read back by the dashboard/logs. The hot-path pre-flight 402 is the SHARED
// billingGate middleware (balance>0, same chain as chat/ASR — BR-1.7); an EXACT
// gateway-side char×price gate would make the gateway a second cost SoT, which
// the ratified cost-SoT invariant forbids (flagged in the Dev Notes).
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/analyticslog"
	"github.com/he-api/he-api/apps/api-gateway/internal/billingemit"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// maxSpeechBodyBytes is the small JSON body cap (BR-4.1 / Q-TTS-LIMITS RATIFIED —
// 64 KiB, generous for a 4096-char input + fields). Distinct const from the chat
// / embeddings / ASR caps (the 3.5 per-endpoint-cap precedent).
const maxSpeechBodyBytes int64 = 64 << 10

// maxSpeechInputChars is the HARD input rune cap (BR-4.2 / Q-TTS-LIMITS — OpenAI
// parity 4096). It is ALSO the billing lever, so it is gateway-computed and
// enforced pre-dispatch (a client cannot under-report to underpay — BR-4.5).
const maxSpeechInputChars = 4096

// maxSynthesizedAudioBytes is the 20 MiB OUTPUT bound (BR-4.3 / Q-TTS-LIMITS),
// enforced here AND at the adapter (upstream.MaxSynthesizedAudioBytes).
const maxSynthesizedAudioBytes = 20 << 20

const (
	speechSpeedMin = 0.25
	speechSpeedMax = 4.0
)

// speechFormatMIME is the Q-TTS-RESPFORMAT RATIFIED response_format→Content-Type
// map for the 3 shipped formats. opus→audio/ogg (Volcano emits ogg_opus). An
// unsupported value (aac/flac/pcm — deferred) is rejected by the validator.
var speechFormatMIME = map[string]string{
	"mp3":  "audio/mpeg",
	"wav":  "audio/wav",
	"opus": "audio/ogg",
}

// audioSpeechRequest is the OpenAI-Audio-Speech JSON body (no audio here — the
// audio is the RESPONSE).
type audioSpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	ResponseFormat string   `json:"response_format"`
	Speed          *float64 `json:"speed"`
}

// AudioSpeechHandlerOption customizes the handler at construction.
type AudioSpeechHandlerOption func(*AudioSpeechHandler)

// WithSpeechAdapterRegistry wires the adapter registry (resolves doubao-tts → the
// Doubao Synthesizer handle). Nil leaves the handler adapter-less (→ 503).
func WithSpeechAdapterRegistry(reg *adapterclient.Registry) AudioSpeechHandlerOption {
	return func(h *AudioSpeechHandler) {
		if reg != nil {
			h.adapterRegistry = reg
		}
	}
}

// WithSpeechUsageEmitter wires the Story-7.1 usage.recorded producer (PER_CHARACTER).
func WithSpeechUsageEmitter(e billingemit.UsageEmitter) AudioSpeechHandlerOption {
	return func(h *AudioSpeechHandler) {
		if e != nil {
			h.usageEmitter = e
		}
	}
}

// WithSpeechSafetyScanner wires the Story-8.2 input scanner (the input TEXT).
func WithSpeechSafetyScanner(s *contentsafety.Scanner) AudioSpeechHandlerOption {
	return func(h *AudioSpeechHandler) { h.safetyScanner = s }
}

// WithSpeechSafetyRecorder wires the Story-8.5 interception recorder.
func WithSpeechSafetyRecorder(r contentsafety.Recorder) AudioSpeechHandlerOption {
	return func(h *AudioSpeechHandler) {
		if r != nil {
			h.safetyRecorder = r
		}
	}
}

// WithSpeechNow overrides the clock (deterministic usage-event ts in tests).
func WithSpeechNow(f func() time.Time) AudioSpeechHandlerOption {
	return func(h *AudioSpeechHandler) {
		if f != nil {
			h.now = f
		}
	}
}

// AudioSpeechHandler serves POST /v1/audio/speech. Stateless beyond its wired
// collaborators; construct once at startup.
type AudioSpeechHandler struct {
	logger          *slog.Logger
	now             func() time.Time
	adapterRegistry *adapterclient.Registry
	usageEmitter    billingemit.UsageEmitter
	safetyScanner   *contentsafety.Scanner
	safetyRecorder  contentsafety.Recorder
}

// NewAudioSpeechHandler builds the handler. logger may be nil.
func NewAudioSpeechHandler(logger *slog.Logger, opts ...AudioSpeechHandlerOption) *AudioSpeechHandler {
	h := &AudioSpeechHandler{
		logger:         logger,
		now:            time.Now,
		usageEmitter:   billingemit.Nop{},
		safetyRecorder: contentsafety.NopRecorder{},
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ServeHTTP implements http.Handler.
func (h *AudioSpeechHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Defence-in-depth — bearer-auth middleware must have populated APIKeyID.
	apiKeyID, ok := middleware.APIKeyIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}

	// BR-4.1 — 64 KiB read cap BEFORE decode. An oversize body → 413.
	r.Body = http.MaxBytesReader(w, r.Body, maxSpeechBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req audioSpeechRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			_ = openaierr.Write(w, ctx, http.StatusRequestEntityTooLarge,
				"413_payload_too_large", "Request body exceeds the maximum size (64 KiB).", nil)
			return
		}
		_ = openaierr.Write(w, ctx, http.StatusBadRequest,
			"400_invalid_request", "Request body must be valid JSON.", nil)
		return
	}

	// BR-1.2 default mapping: absent response_format → mp3; absent speed → 1.0.
	if req.ResponseFormat == "" {
		req.ResponseFormat = "mp3"
	}
	speed := 1.0
	if req.Speed != nil {
		speed = *req.Speed
	}

	// BR-1.2 / BR-4.2 — field schema (incl the HARD rune cap). Pure validation.
	if status, code, msg, param, valid := validateAudioSpeechRequest(&req); !valid {
		_ = openaierr.Write(w, ctx, status, code, msg, param)
		return
	}

	// BR-1.3 — speech-capability gate (fail-closed, pre-dispatch). A model that is
	// NOT speech-capable → 400 BEFORE safety / routing / dispatch (zero upstream).
	if !capabilitiesByModelID[req.Model].Speech {
		modelParam := "model"
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Model '"+req.Model+"' does not support speech synthesis.", &modelParam)
		return
	}

	// BR-1.7 / BR-4.6 — content-safety on the INPUT text (do-NOT-regress vs 9.6:
	// the TTS input is plain text and MUST be scanned). The synthesized OUTPUT
	// audio is model output and is NOT scanned. Reject-before-dispatch.
	if h.safetyScanner != nil {
		strictness := resolveStrictnessFromClaims(ctx)
		min := contentsafety.MinSeverity(strictness)
		if match, hit := h.safetyScanner.ScanTextMin(req.Input, min); hit {
			h.recordSpeechSafetyBlock(ctx, apiKeyID, match, strictness)
			_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_content_filter",
				"Request was blocked by the content safety filter.", nil)
			return
		}
	}

	// AC2 — resolve the adapter (doubao-tts → the Doubao Synthesizer handle).
	syn, ok := h.resolveSynthesizer(req.Model)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable,
			"503_service_unavailable", "Speech synthesis is not available.", nil)
		return
	}

	// BR-4.2 — the char count is GATEWAY-computed from the validated input (the
	// billing lever; a client cannot under-report — BR-4.5).
	characterCount := len([]rune(req.Input))

	// BR-4.4 — ONE structured log line per accepted request. NEVER the input text
	// or the audio bytes; only non-PII signals.
	h.logger.InfoContext(ctx, "audio_speech",
		slog.String("event", "audio_speech"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.String("voice", req.Voice),
		slog.String("response_format", req.ResponseFormat),
		slog.Int("character_count", characterCount),
	)

	heRequestID, _ := requestid.FromContext(ctx)
	synReq := &adapterv1.SynthesizeRequest{
		Model:          req.Model,
		Input:          req.Input,
		Voice:          req.Voice,
		ResponseFormat: req.ResponseFormat,
		Speed:          &speed,
		HeRequestId:    heRequestID,
	}
	headers := http.Header{}
	if heRequestID != "" {
		headers.Set("X-He-Request-Id", heRequestID)
	}

	resp, err := syn.Synthesize(ctx, synReq, headers)
	if err != nil {
		h.writeSynthesizeError(w, ctx, err)
		return
	}
	audio := resp.GetAudio()
	if len(audio) == 0 {
		_ = openaierr.Write(w, ctx, http.StatusBadGateway,
			"502_upstream_unavailable", "Speech synthesis returned no audio.", nil)
		return
	}
	// BR-4.3 — output bound enforced at the gateway too (defence-in-depth); zero
	// billing on an over-cap synthesis (no charge for undelivered audio).
	if len(audio) > maxSynthesizedAudioBytes {
		_ = openaierr.Write(w, ctx, http.StatusBadGateway,
			"502_upstream_unavailable", "Synthesized audio exceeded the maximum size.", nil)
		return
	}
	// BR-2.6 — the adapter mime_type must agree with the resolved response_format
	// map; a mismatch is an upstream contract violation (defence-in-depth).
	wantMIME := speechFormatMIME[req.ResponseFormat]
	if resp.GetMimeType() != wantMIME {
		_ = openaierr.Write(w, ctx, http.StatusBadGateway,
			"502_upstream_unavailable", "Speech synthesis returned an unexpected audio format.", nil)
		return
	}

	// BR-1.4 — the FIRST binary response body. Content-Type + Content-Length set
	// BEFORE the first write (the SSE header discipline; the analytics captureWriter
	// passes a 200 body through unwrapped — Architect-cleared). NO JSON envelope.
	w.Header().Set("Content-Type", wantMIME)
	w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)

	// AC3 — emit the PER_CHARACTER usage event (billing-svc computes cost; money
	// one-SoT) + the analytics record (tokens 0). TPM NOT incremented (no tokens).
	h.emitTTSUsage(ctx, apiKeyID, req.Model, characterCount)
}

// validateAudioSpeechRequest applies the BR-1.2 OpenAI-speech field schema.
// Returns (status, code, msg, param, valid). Pure — no I/O.
func validateAudioSpeechRequest(req *audioSpeechRequest) (int, string, string, *string, bool) {
	modelParam, inputParam, voiceParam, fmtParam, speedParam := "model", "input", "voice", "response_format", "speed"

	if req.Model == "" || len(req.Model) > modelMaxLen {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'model' is required and must be a speech-capable model.", &modelParam, false
	}
	if req.Input == "" {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'input' is required and must be a non-empty string.", &inputParam, false
	}
	if len([]rune(req.Input)) > maxSpeechInputChars {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'input' must be ≤ 4096 characters.", &inputParam, false
	}
	if req.Voice == "" {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'voice' is required and must be a supported voice.", &voiceParam, false
	}
	if _, ok := speechFormatMIME[req.ResponseFormat]; !ok {
		// aac/flac/pcm are deferred (Q-TTS-RESPFORMAT) → explicit 400.
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'response_format' '" + req.ResponseFormat + "' is not supported. Use one of: mp3, wav, opus.", &fmtParam, false
	}
	if req.Speed != nil && (*req.Speed < speechSpeedMin || *req.Speed > speechSpeedMax) {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'speed' must be between 0.25 and 4.0.", &speedParam, false
	}
	return 0, "", "", nil, true
}

// resolveSynthesizer resolves the model to a Synthesizer adapter handle.
func (h *AudioSpeechHandler) resolveSynthesizer(model string) (adapterclient.Synthesizer, bool) {
	if h.adapterRegistry == nil {
		return nil, false
	}
	handle, ok := h.adapterRegistry.Resolve(model)
	if !ok {
		return nil, false
	}
	syn, ok := handle.(adapterclient.Synthesizer)
	return syn, ok
}

// emitTTSUsage emits the PER_CHARACTER usage.recorded event + the analytics
// record. billing-svc is the SOLE cost authority (money one-SoT) — the gateway
// emits the RAW character_count, not cost. Token + duration fields are 0; the TPM
// axis is NOT incremented (no token dimension); 消费 is read from usage_ledger.
func (h *AudioSpeechHandler) emitTTSUsage(ctx context.Context, apiKeyID, model string, characterCount int) {
	heRequestID, _ := requestid.FromContext(ctx)
	userID, _ := middleware.BearerUserIDFromContext(ctx)
	var teamID string
	if claims, ok := middleware.CacheValueFromContext(ctx); ok {
		teamID = claims.TeamID
	}

	h.usageEmitter.Emit(ctx, &billingv1.UsageEvent{
		LedgerKey:      heRequestID,
		HeRequestId:    heRequestID,
		UserId:         userID,
		ApiKeyId:       apiKeyID,
		TeamId:         teamID,
		Model:          model,
		IsStreaming:    false,
		Ts:             h.now().UTC().Format(time.RFC3339),
		BillingMode:    billingv1.BillingMode_BILLING_MODE_PER_CHARACTER,
		CharacterCount: uint32(characterCount),
	})

	// Story 9.1 — analytics record (tokens 0; 消费 derived downstream from
	// usage_ledger per the per-metric SoT split). Nil-safe.
	analyticslog.FromContext(ctx).Populate(model, 0, 0, 0, false)
}

// recordSpeechSafetyBlock emits the Story-8.5 interception event for an input block.
func (h *AudioSpeechHandler) recordSpeechSafetyBlock(ctx context.Context, apiKeyID string, m safetylexicon.Match, level contentsafety.Strictness) {
	heRequestID, _ := requestid.FromContext(ctx)
	userID, _ := middleware.BearerUserIDFromContext(ctx)
	h.safetyRecorder.Record(ctx, contentsafety.SafetyEvent{
		Direction:   contentsafety.DirectionInput,
		MatchedRule: m.Canonical,
		Category:    string(m.Category),
		Severity:    string(m.Severity),
		Action:      contentsafety.ActionBlocked,
		UserID:      userID,
		APIKeyID:    apiKeyID,
		HeRequestID: heRequestID,
		Strictness:  string(level),
	})
}

// writeSynthesizeError maps an adapter Synthesize fault to the canonical §5.1.2
// envelope. A deadline/timeout → 504; CodeInvalidArgument → 400; everything else
// → 502. NO input text / audio bytes ever appear here.
func (h *AudioSpeechHandler) writeSynthesizeError(w http.ResponseWriter, ctx context.Context, err error) {
	switch connect.CodeOf(err) {
	case connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, ctx, http.StatusGatewayTimeout, "504_upstream_timeout", "Speech synthesis timed out.", nil)
	case connect.CodeInvalidArgument:
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "The request could not be synthesized.", nil)
	default:
		_ = openaierr.Write(w, ctx, http.StatusBadGateway, "502_upstream_unavailable", "The speech synthesis service is unavailable.", nil)
	}
}
