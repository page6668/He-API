// Story 9.6 — POST /v1/audio/transcriptions handler (Whisper-compatible ASR).
//
// The FIRST multipart/form-data ingestion in the gateway (every prior endpoint
// is JSON). Mirrors the Story-3.5 embeddings 2nd-endpoint precedent (own
// body-cap const, own validator, own analytics hook) but for an HTTP-multipart,
// adapter-backed, audio-in path routed to Doubao (Volcano) ASR over the
// additive `Transcribe` RPC (Story 9.6 T1).
//
// Security posture (AC4, non-negotiable):
//   - 25 MiB read cap via http.MaxBytesReader BEFORE ParseMultipartForm;
//   - ParseMultipartForm(1<<20) so bulk audio spills to a bounded temp file;
//   - exactly one `file` part; mime allow-list + http.DetectContentType sniff;
//   - PII discipline: audio bytes AND the transcript are NEVER logged / spanned
//     / exported / echoed in errors — logs carry only size/mime/duration/model.
//   - defer r.MultipartForm.RemoveAll() on EVERY exit path (no audio on disk).
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
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

// MaxAudioBodyBytes is the 25 MiB body cap (BR-4.1 / Q-ASR-LIMITS RATIFIED —
// OpenAI Whisper parity). Distinct const from MaxEmbeddingBodyBytes (1 MiB) and
// the chat caps so per-endpoint caps diverge cleanly (the 3.5 precedent).
const MaxAudioBodyBytes int64 = 25 << 20

// audioMultipartMemBytes is the in-memory cap handed to ParseMultipartForm
// (BR-4.1): small (1 MiB) so a multi-MiB audio part spills to a bounded temp
// file rather than the heap (memory-pressure DoS guard).
const audioMultipartMemBytes int64 = 1 << 20

// maxAudioPromptLen caps the optional `prompt` text field (Q-ASR-LIMITS — 2048).
const maxAudioPromptLen = 2048

// allowedAudioMIME is the Q-ASR-LIMITS allow-list (∩ what Volcano accepts). A
// `file` whose resolved type is outside this set → 400.
var allowedAudioMIME = map[string]bool{
	"audio/mpeg":   true, // mp3
	"audio/mp3":    true,
	"audio/mp4":    true, // m4a/mp4 audio
	"audio/m4a":    true,
	"audio/x-m4a":  true,
	"audio/wav":    true,
	"audio/x-wav":  true,
	"audio/wave":   true,
	"audio/webm":   true,
	"audio/flac":   true,
	"audio/x-flac": true,
	"audio/ogg":    true,
}

// extToAudioMIME maps a file extension to a canonical allow-listed mime, used
// when the multipart part declares a generic/blank Content-Type.
var extToAudioMIME = map[string]string{
	".mp3":  "audio/mpeg",
	".mp4":  "audio/mp4",
	".m4a":  "audio/mp4",
	".wav":  "audio/wav",
	".webm": "audio/webm",
	".flac": "audio/flac",
	".ogg":  "audio/ogg",
}

// supportedResponseFormats are the Q-ASR-RESPFORMAT-shipped formats; srt/vtt are
// deferred (→ explicit 400).
var supportedResponseFormats = map[string]bool{
	"json":         true,
	"text":         true,
	"verbose_json": true,
}

// audioTranscriptionRequest is the parsed multipart form (no audio bytes here —
// those ride a separate []byte the handler never logs).
type audioTranscriptionRequest struct {
	Model          string
	Language       string
	Prompt         string
	ResponseFormat string
	Temperature    *float64
}

// AudioTranscriptionsHandlerOption customizes the handler at construction.
type AudioTranscriptionsHandlerOption func(*AudioTranscriptionsHandler)

// WithAudioAdapterRegistry wires the adapter registry (resolves doubao-asr → the
// Doubao Transcriber handle). Nil leaves the handler adapter-less (→ 503).
func WithAudioAdapterRegistry(reg *adapterclient.Registry) AudioTranscriptionsHandlerOption {
	return func(h *AudioTranscriptionsHandler) {
		if reg != nil {
			h.adapterRegistry = reg
		}
	}
}

// WithAudioUsageEmitter wires the Story-7.1 usage.recorded producer (PER_MINUTE).
func WithAudioUsageEmitter(e billingemit.UsageEmitter) AudioTranscriptionsHandlerOption {
	return func(h *AudioTranscriptionsHandler) {
		if e != nil {
			h.usageEmitter = e
		}
	}
}

// WithAudioSafetyScanner wires the Story-8.2 input scanner (prompt text only).
func WithAudioSafetyScanner(s *contentsafety.Scanner) AudioTranscriptionsHandlerOption {
	return func(h *AudioTranscriptionsHandler) { h.safetyScanner = s }
}

// WithAudioSafetyRecorder wires the Story-8.5 interception recorder.
func WithAudioSafetyRecorder(r contentsafety.Recorder) AudioTranscriptionsHandlerOption {
	return func(h *AudioTranscriptionsHandler) {
		if r != nil {
			h.safetyRecorder = r
		}
	}
}

// WithAudioNow overrides the clock (deterministic usage-event ts in tests).
func WithAudioNow(f func() time.Time) AudioTranscriptionsHandlerOption {
	return func(h *AudioTranscriptionsHandler) {
		if f != nil {
			h.now = f
		}
	}
}

// AudioTranscriptionsHandler serves POST /v1/audio/transcriptions. Stateless
// beyond its wired collaborators; construct once at startup.
type AudioTranscriptionsHandler struct {
	logger          *slog.Logger
	now             func() time.Time
	adapterRegistry *adapterclient.Registry
	usageEmitter    billingemit.UsageEmitter
	safetyScanner   *contentsafety.Scanner
	safetyRecorder  contentsafety.Recorder
}

// NewAudioTranscriptionsHandler builds the handler. logger may be nil.
func NewAudioTranscriptionsHandler(logger *slog.Logger, opts ...AudioTranscriptionsHandlerOption) *AudioTranscriptionsHandler {
	h := &AudioTranscriptionsHandler{
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
func (h *AudioTranscriptionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// BR defence-in-depth — bearer-auth middleware must have populated APIKeyID.
	apiKeyID, ok := middleware.APIKeyIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}

	// BR-1.1 — multipart/form-data only. A non-multipart content-type (e.g.
	// application/json) → 415 (canonical 400 family per §5.1.2). Check BEFORE
	// parse so a JSON caller gets a clear error.
	mediaType, _, mtErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mtErr != nil || mediaType != "multipart/form-data" {
		// Canonical 400 family per §5.1.2 (no 415 code in the taxonomy).
		_ = openaierr.Write(w, ctx, http.StatusBadRequest,
			"400_invalid_request", "Endpoint requires multipart/form-data.", nil)
		return
	}

	// BR-4.1 — 25 MiB read cap BEFORE ParseMultipartForm; bulk spills to a
	// bounded temp file (audioMultipartMemBytes in-memory).
	r.Body = http.MaxBytesReader(w, r.Body, MaxAudioBodyBytes)
	if err := r.ParseMultipartForm(audioMultipartMemBytes); err != nil {
		// BR-4.6 — even a failed parse may have spilled a temp file.
		h.cleanupMultipart(r)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			_ = openaierr.Write(w, ctx, http.StatusRequestEntityTooLarge,
				"413_payload_too_large", "Request body exceeds the maximum size (25 MiB).", nil)
			return
		}
		_ = openaierr.Write(w, ctx, http.StatusBadRequest,
			"400_invalid_request", "Malformed multipart/form-data body.", nil)
		return
	}
	// BR-4.6 — remove any spilled audio temp files on EVERY exit path.
	defer h.cleanupMultipart(r)

	// BR-1.1 / BR-4.2 — exactly ONE `file` part.
	fileParam := "file"
	fileHeaders := fileParts(r)
	if len(fileHeaders) != 1 {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Field 'file' is required and must be a single audio file.", &fileParam)
		return
	}
	fh := fileHeaders[0]

	// Read the audio bytes under the (already-capped) reader. The bytes are PII —
	// they NEVER leave this function except on the adapter wire.
	audio, openErr := readFilePart(fh)
	if openErr != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Field 'file' could not be read.", &fileParam)
		return
	}
	// BLIND-BOUNDARY-001 — an empty/0-byte file is distinct from a missing part.
	if len(audio) == 0 {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Field 'file' must be a non-empty audio file.", &fileParam)
		return
	}

	// BR-1.2 — Whisper field schema validation.
	req := audioFormValues(r)
	if status, code, msg, param, valid := validateAudioTranscriptionRequest(req); !valid {
		_ = openaierr.Write(w, ctx, status, code, msg, param)
		return
	}

	// BR-4.3 — mime/format allow-list + magic-byte sniff (declared CT + ext +
	// http.DetectContentType must agree to an allowed audio type). Runs AFTER
	// structural validation, BEFORE the capability gate / safety / dispatch
	// (BR-4.5 reject ordering — zero upstream call on reject).
	resolvedMIME, mimeOK := resolveAudioMIME(fh, audio)
	if !mimeOK {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Unsupported audio format.", &fileParam)
		return
	}

	// BR-1.3 — transcription-capability gate (fail-closed, pre-dispatch). A model
	// that is NOT transcription-capable → 400 BEFORE routing/safety/dispatch.
	if !capabilitiesByModelID[req.Model].Transcription {
		modelParam := "model"
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"Model '"+req.Model+"' does not support audio transcription.", &modelParam)
		return
	}

	// BR-1.7 — content-safety on the `prompt` TEXT field only (the audio bytes
	// are opaque binary, outside the 8.1 text-lexicon scope; the transcript is
	// model OUTPUT and is NOT scanned — 8.2 scans INPUT only). Reject-before-
	// dispatch: zero upstream call on a block.
	if h.safetyScanner != nil && req.Prompt != "" {
		strictness := resolveStrictnessFromClaims(ctx)
		min := contentsafety.MinSeverity(strictness)
		if match, hit := h.safetyScanner.ScanTextMin(req.Prompt, min); hit {
			h.recordSafetyBlock(ctx, apiKeyID, match, strictness)
			_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_content_filter",
				"Request was blocked by the content safety filter.", nil)
			return
		}
	}

	// AC2 — resolve the adapter (doubao-asr → the Doubao Transcriber handle).
	tr, ok := h.resolveTranscriber(req.Model)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable,
			"503_service_unavailable", "Audio transcription is not available.", nil)
		return
	}

	// BR-4.4 — ONE structured log line per accepted request. NEVER the audio
	// bytes or the transcript; only non-PII size/mime/model signals.
	h.logger.InfoContext(ctx, "audio_transcription",
		slog.String("event", "audio_transcription"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("audio_bytes_size", len(audio)),
		slog.String("mime", resolvedMIME),
		slog.String("response_format", req.ResponseFormat),
	)

	heRequestID, _ := requestid.FromContext(ctx)
	transReq := &adapterv1.TranscribeRequest{
		Model:          req.Model,
		Audio:          audio,
		MimeType:       resolvedMIME,
		Language:       req.Language,
		Prompt:         req.Prompt,
		ResponseFormat: req.ResponseFormat,
		Temperature:    req.Temperature,
		HeRequestId:    heRequestID,
	}
	headers := http.Header{}
	if heRequestID != "" {
		headers.Set("X-He-Request-Id", heRequestID)
	}

	resp, err := tr.Transcribe(ctx, transReq, headers)
	if err != nil {
		h.writeTranscribeError(w, ctx, err)
		return
	}
	if resp == nil || resp.GetDurationSeconds() <= 0 {
		// BR-2.5 / BR-D-3 — a missing/zero duration is fail-closed (never bill 0).
		_ = openaierr.Write(w, ctx, http.StatusBadGateway,
			"502_upstream_unavailable", "Transcription returned no usable result.", nil)
		return
	}

	// BR-1.4 — shape the HTTP body per response_format. The transcript is the
	// model output; it is written to the client but NEVER logged.
	writeTranscriptionResponse(w, req.ResponseFormat, resp)

	// AC3 — emit the PER_MINUTE usage event (billing-svc computes cost; money
	// one-SoT) + the analytics record (tokens 0). TPM axis is NOT incremented
	// for ASR (BR-3.7 — no token dimension); RPM/QPS still apply upstream.
	h.emitASRUsage(ctx, apiKeyID, req.Model, resp.GetDurationSeconds())
}

// validateAudioTranscriptionRequest applies the BR-1.2 Whisper field schema.
// Returns (status, code, msg, param, valid). Pure — no I/O.
func validateAudioTranscriptionRequest(req *audioTranscriptionRequest) (int, string, string, *string, bool) {
	modelParam, langParam, fmtParam, tempParam, promptParam := "model", "language", "response_format", "temperature", "prompt"

	if req.Model == "" || len(req.Model) > modelMaxLen {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'model' is required and must be a transcription-capable model.", &modelParam, false
	}
	if req.Language != "" && !isISO639_1(req.Language) {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'language' must be a valid ISO-639-1 code.", &langParam, false
	}
	if req.ResponseFormat != "" && !supportedResponseFormats[req.ResponseFormat] {
		// srt/vtt are deferred (Q-ASR-RESPFORMAT) → explicit 400.
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'response_format' '" + req.ResponseFormat + "' is not supported. Use one of: json, text, verbose_json.", &fmtParam, false
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 1) {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'temperature' must be between 0 and 1.", &tempParam, false
	}
	if len(req.Prompt) > maxAudioPromptLen {
		return http.StatusBadRequest, "400_invalid_request",
			"Field 'prompt' exceeds the maximum length (2048 characters).", &promptParam, false
	}
	return 0, "", "", nil, true
}

// audioFormValues extracts the text fields + parses temperature into the form
// struct. Default response_format is json (BR-1.4).
func audioFormValues(r *http.Request) *audioTranscriptionRequest {
	req := &audioTranscriptionRequest{
		Model:          strings.TrimSpace(r.FormValue("model")),
		Language:       strings.TrimSpace(r.FormValue("language")),
		Prompt:         r.FormValue("prompt"),
		ResponseFormat: strings.TrimSpace(r.FormValue("response_format")),
	}
	if req.ResponseFormat == "" {
		req.ResponseFormat = "json"
	}
	if raw := strings.TrimSpace(r.FormValue("temperature")); raw != "" {
		if t, err := strconv.ParseFloat(raw, 64); err == nil {
			req.Temperature = &t
		} else {
			// An unparseable temperature is out-of-contract; surface via the
			// validator by pinning an out-of-range sentinel.
			bad := -1.0
			req.Temperature = &bad
		}
	}
	return req
}

// fileParts returns the `file` multipart headers (0, 1, or many).
func fileParts(r *http.Request) []*multipart.FileHeader {
	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		return nil
	}
	return r.MultipartForm.File["file"]
}

// readFilePart opens + reads a multipart file header fully (already bounded by
// the MaxBytesReader on r.Body). The returned bytes are PII.
func readFilePart(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// resolveAudioMIME applies BR-4.3: the declared part Content-Type, the filename
// extension, and the http.DetectContentType sniff must agree to an ALLOWED
// audio type. Returns the canonical mime + ok. A mislabelled non-audio file
// (e.g. text/html declared audio/mpeg) is rejected via the sniff.
func resolveAudioMIME(fh *multipart.FileHeader, audio []byte) (string, bool) {
	// 1. Declared Content-Type (strip params), else infer from extension.
	declared := ""
	if ct := fh.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err == nil {
			declared = strings.ToLower(mt)
		}
	}
	candidate := ""
	switch {
	case allowedAudioMIME[declared]:
		candidate = declared
	default:
		if m, ok := extToAudioMIME[strings.ToLower(filepath.Ext(fh.Filename))]; ok {
			candidate = m
		}
	}
	if candidate == "" {
		return "", false
	}

	// 2. Magic-byte sniff (stdlib, no new dep). DetectContentType recognises a
	// few audio types + the common NON-audio types (text/html, image/*, pdf…).
	// Reject when the sniff is a recognised NON-audio, NON-octet type (the
	// mislabelled-file attack) or a contradicting audio/* type. application/
	// octet-stream is ambiguous (flac/webm/m4a sniff this way) → trust 1.
	sniff := http.DetectContentType(audio)
	sniffBase := sniff
	if i := strings.IndexByte(sniffBase, ';'); i >= 0 {
		sniffBase = sniffBase[:i]
	}
	sniffBase = strings.ToLower(strings.TrimSpace(sniffBase))
	switch {
	case sniffBase == "application/octet-stream":
		return candidate, true // ambiguous — accept on declared/ext
	case strings.HasPrefix(sniffBase, "audio/"):
		if allowedAudioMIME[sniffBase] {
			return candidate, true
		}
		return "", false // a non-allow-listed audio type
	default:
		// A recognised non-audio type (text/html, image/png, application/pdf…)
		// contradicts an audio declaration → reject (mislabelled file).
		return "", false
	}
}

// resolveTranscriber resolves the model to a Transcriber adapter handle.
func (h *AudioTranscriptionsHandler) resolveTranscriber(model string) (adapterclient.Transcriber, bool) {
	if h.adapterRegistry == nil {
		return nil, false
	}
	handle, ok := h.adapterRegistry.Resolve(model)
	if !ok {
		return nil, false
	}
	tr, ok := handle.(adapterclient.Transcriber)
	return tr, ok
}

// writeTranscriptionResponse maps the adapter TranscribeResponse to the Whisper
// HTTP body per response_format (BR-1.4).
func writeTranscriptionResponse(w http.ResponseWriter, format string, resp *adapterv1.TranscribeResponse) {
	switch format {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, resp.GetText())
	case "verbose_json":
		body := map[string]any{
			"task":     "transcribe",
			"language": resp.GetLanguage(),
			"duration": resp.GetDurationSeconds(),
			"text":     resp.GetText(),
		}
		// segments only when the upstream supplied them (never fabricated).
		if seg := resp.GetSegmentsJson(); len(seg) > 0 {
			var segments json.RawMessage = seg
			body["segments"] = segments
		}
		writeChatJSON(w, http.StatusOK, body)
	default: // json
		writeChatJSON(w, http.StatusOK, map[string]string{"text": resp.GetText()})
	}
}

// emitASRUsage emits the PER_MINUTE usage.recorded event + the analytics record.
// billing-svc is the SOLE cost authority (money one-SoT) — the gateway emits
// RAW audio_duration_seconds, not cost. Token fields are 0; TPM NOT incremented.
func (h *AudioTranscriptionsHandler) emitASRUsage(ctx context.Context, apiKeyID, model string, durationSeconds float64) {
	heRequestID, _ := requestid.FromContext(ctx)
	userID, _ := middleware.BearerUserIDFromContext(ctx)
	var teamID string
	if claims, ok := middleware.CacheValueFromContext(ctx); ok {
		teamID = claims.TeamID
	}

	h.usageEmitter.Emit(ctx, &billingv1.UsageEvent{
		LedgerKey:            heRequestID,
		HeRequestId:          heRequestID,
		UserId:               userID,
		ApiKeyId:             apiKeyID,
		TeamId:               teamID,
		Model:                model,
		IsStreaming:          false,
		Ts:                   h.now().UTC().Format(time.RFC3339),
		BillingMode:          billingv1.BillingMode_BILLING_MODE_PER_MINUTE,
		AudioDurationSeconds: durationSeconds,
	})

	// Story 9.1 — analytics record (tokens 0; consumption derived downstream
	// from usage_ledger per the per-metric SoT split). Nil-safe.
	analyticslog.FromContext(ctx).Populate(model, 0, 0, 0, false)
}

// recordSafetyBlock emits the Story-8.5 interception event for a prompt block.
func (h *AudioTranscriptionsHandler) recordSafetyBlock(ctx context.Context, apiKeyID string, m safetylexicon.Match, level contentsafety.Strictness) {
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

// writeTranscribeError maps an adapter Transcribe fault to the canonical
// §5.1.2 envelope. A deadline/timeout → 504; CodeUnimplemented (an adapter that
// does not serve audio) → 502. NO audio bytes / transcript ever appear here.
func (h *AudioTranscriptionsHandler) writeTranscribeError(w http.ResponseWriter, ctx context.Context, err error) {
	switch connect.CodeOf(err) {
	case connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, ctx, http.StatusGatewayTimeout, "504_upstream_timeout", "Transcription timed out.", nil)
	case connect.CodeInvalidArgument:
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "The audio could not be transcribed.", nil)
	default:
		// Unavailable / Unimplemented / ResourceExhausted / Internal / unknown —
		// all surface as a single canonical upstream-unavailable envelope (the
		// taxonomy has no generic 429/502_error code).
		_ = openaierr.Write(w, ctx, http.StatusBadGateway, "502_upstream_unavailable", "The transcription service is unavailable.", nil)
	}
}

// cleanupMultipart removes any spilled multipart temp files (BR-4.6). Safe to
// call multiple times / when no form was parsed.
func (h *AudioTranscriptionsHandler) cleanupMultipart(r *http.Request) {
	if r.MultipartForm != nil {
		_ = r.MultipartForm.RemoveAll()
	}
}

// isISO639_1 is a lightweight shape check for a 2-letter ISO-639-1 language
// code (BR-1.2). We validate SHAPE (two ASCII letters), not membership in the
// full ISO registry — the upstream rejects a truly-unknown code, and a strict
// registry table is out of scope. OpenAI's Whisper also accepts the broad set.
func isISO639_1(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		c := s[i]
		if c < 'a' || c > 'z' {
			// accept upper-case too (case-insensitive)
			if c < 'A' || c > 'Z' {
				return false
			}
		}
	}
	return true
}
