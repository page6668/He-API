package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Story 9.7 (T3, Q-TTS-UPSTREAM RATIFIED) — Volcano (火山引擎) TTS upstream client.
//
// Volcano TTS is NOT OpenAI-Audio-Speech-compatible — it is ByteDance's own
// protocol (a distinct base URL + auth + request/response schema), so this is a
// REAL (non-identity) translation, the MIRROR of 9.6's ASR translate (audio on
// the RESPONSE instead of the request).
//
// RATIFIED contract (Architect Q-TTS-UPSTREAM): SYNC HTTP one-shot endpoint, base
// path `/api/v1/tts`, `request.operation:"query"` (returns the FULL audio in one
// response). The response is base64-encoded audio in the JSON `data` field → the
// adapter base64-decodes it into SynthesizeResponse.audio. Auth header is
// `Authorization: Bearer;{token}` (semicolon — VENDOR-DOC-CONFIRM, pinned). Map
// voice→audio.voice_type, response_format→audio.encoding, speed→audio.speed_ratio;
// carry app.{appid,cluster,token} from the new env. The EXACT base URL is an ops
// gate before production traffic (T7.1); the request/response SHAPE is the
// contract, exercised against a fake-upstream.

// DefaultTTSPath is the default Volcano TTS sync-query path (env-overridable).
const DefaultTTSPath = "/api/v1/tts"

// MaxSynthesizedAudioBytes is the 20 MiB output bound (Q-TTS-LIMITS RATIFIED,
// BR-4.3) enforced on the decoded audio at the adapter (and again at the gateway).
const MaxSynthesizedAudioBytes = 20 << 20

// ttsSuccessCode is the Volcano TTS success code (3000 documented; 0 tolerated).
const ttsSuccessCode = 3000

// TTSConfig is the Volcano TTS upstream config — DISTINCT from the Ark chat + the
// 9.6 ASR config (a different product/endpoint). All fields are env-sourced.
type TTSConfig struct {
	BaseURL   string // e.g. https://openspeech.bytedance.com
	Path      string // sync query path (default DefaultTTSPath)
	Token     string // access token → Authorization: Bearer;<token>
	AppID     string // TTS app id
	Cluster   string // TTS cluster
	VoiceType string // default voice_type fallback when the request voice is empty (optional)
}

// Configured reports whether the mandatory TTS upstream fields are all set. The
// Doubao service warns at boot if unset and fail-fasts at request time (the 4.5
// BR-1.12 precedent) so a chat/ASR-only deployment is unaffected.
func (c TTSConfig) Configured() bool {
	return c.BaseURL != "" && c.Token != "" && c.AppID != "" && c.Cluster != ""
}

// ErrTTSNotConfigured is returned by Synthesize when the TTS upstream env is
// unset — the adapter maps it to connect.CodeUnavailable.
var ErrTTSNotConfigured = errors.New("doubao TTS upstream is not configured")

// ErrEmptyVoice is returned when neither the request voice nor a configured
// default voice_type is present (BR-2.5 defence-in-depth — the gateway validates
// voice, so this should never trip in production). The adapter maps it to
// connect.CodeInvalidArgument.
var ErrEmptyVoice = errors.New("doubao TTS: no voice resolved")

// TTSClient performs sync Volcano TTS synthesis.
type TTSClient struct {
	cfg        TTSConfig
	httpClient *http.Client
}

// NewTTSClient builds a TTSClient with the same HTTP/2-preferred + otel transport
// policy as the chat + ASR clients (BR-TR-6).
func NewTTSClient(cfg TTSConfig, timeout time.Duration) *TTSClient {
	if cfg.Path == "" {
		cfg.Path = DefaultTTSPath
	}
	if timeout == 0 {
		timeout = DefaultUpstreamTimeout
	}
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}
	return &TTSClient{
		cfg: cfg,
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   timeout,
		},
	}
}

// TTSRequest is the gateway→adapter request (already validated upstream).
type TTSRequest struct {
	Model          string
	Input          string
	Voice          string
	ResponseFormat string
	Speed          float64
	HeRequestID    string
}

// TTSResult is the parsed synthesis result.
type TTSResult struct {
	Audio    []byte
	MimeType string
}

// ---- Volcano TTS wire shapes (VENDOR-DOC-CONFIRM) ----------------------
// volcApp / volcUser are shared with asr.go (same package).

type volcTTSAudio struct {
	VoiceType  string  `json:"voice_type"`
	Encoding   string  `json:"encoding"`
	SpeedRatio float64 `json:"speed_ratio,omitempty"`
}

type volcTTSReqMeta struct {
	ReqID     string `json:"reqid,omitempty"`
	Text      string `json:"text"`
	Operation string `json:"operation"` // "query" — full-audio one-shot (Q-TTS-UPSTREAM)
}

type volcTTSRequest struct {
	App     volcApp        `json:"app"`
	User    volcUser       `json:"user"`
	Audio   volcTTSAudio   `json:"audio"`
	Request volcTTSReqMeta `json:"request"`
}

type volcTTSResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"` // base64-encoded audio
}

// Synthesize POSTs the text to the Volcano TTS query endpoint and base64-decodes
// the returned audio. Enforces the 20 MiB output bound (BR-4.3); never echoes the
// input text or audio bytes in an error.
func (c *TTSClient) Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error) {
	if !c.cfg.Configured() {
		return TTSResult{}, ErrTTSNotConfigured
	}

	encoding, mime, err := ttsEncodingFor(req.ResponseFormat)
	if err != nil {
		// Defence-in-depth: the gateway already validated response_format; an
		// unmappable value here is an invalid argument, not an upstream fault.
		return TTSResult{}, &UpstreamError{Kind: ErrorKindUpstream4xx, Cause: err}
	}
	voice := req.Voice
	if voice == "" {
		voice = c.cfg.VoiceType
	}
	if voice == "" {
		return TTSResult{}, ErrEmptyVoice
	}

	body := volcTTSRequest{
		App:  volcApp{AppID: c.cfg.AppID, Token: c.cfg.Token, Cluster: c.cfg.Cluster},
		User: volcUser{UID: "he-api"},
		Audio: volcTTSAudio{
			VoiceType:  voice,
			Encoding:   encoding,
			SpeedRatio: req.Speed,
		},
		Request: volcTTSReqMeta{
			ReqID:     req.HeRequestID,
			Text:      req.Input,
			Operation: "query",
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return TTSResult{}, fmt.Errorf("tts marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + path.Clean("/"+strings.TrimLeft(c.cfg.Path, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return TTSResult{}, fmt.Errorf("tts build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Volcano auth: `Authorization: Bearer;<token>` (note the semicolon — pinned).
	httpReq.Header.Set("Authorization", "Bearer;"+c.cfg.Token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return TTSResult{}, &UpstreamError{Kind: ClassifyError(err), Cause: err}
	}
	defer func() { _ = resp.Body.Close() }() // RESOURCE: always release the conn

	// Bound the read generously above the base64 of a 20 MiB body (~27 MiB).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return TTSResult{}, &UpstreamError{Kind: ErrorKindUpstream5xx, Status: resp.StatusCode, Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TTSResult{}, &UpstreamError{
			Kind:   ClassifyHTTPStatus(resp.StatusCode),
			Status: resp.StatusCode,
			Cause:  fmt.Errorf("volcano tts http %d", resp.StatusCode),
		}
	}

	var parsed volcTTSResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return TTSResult{}, &UpstreamError{Kind: ErrorKindMalformedChunk, Status: resp.StatusCode, Cause: fmt.Errorf("tts decode: %w", err)}
	}
	if parsed.Code != ttsSuccessCode && parsed.Code != 0 {
		return TTSResult{}, &UpstreamError{
			Kind:   ErrorKindUpstream4xx,
			Status: resp.StatusCode,
			Cause:  fmt.Errorf("volcano tts code=%d msg=%q", parsed.Code, parsed.Message),
		}
	}
	if parsed.Data == "" {
		return TTSResult{}, &UpstreamError{Kind: ErrorKindEmptyChoices, Status: resp.StatusCode, Cause: errors.New("volcano tts returned no audio data")}
	}

	audio, err := base64.StdEncoding.DecodeString(parsed.Data)
	if err != nil {
		return TTSResult{}, &UpstreamError{Kind: ErrorKindMalformedChunk, Status: resp.StatusCode, Cause: fmt.Errorf("tts base64 decode: %w", err)}
	}
	if len(audio) == 0 {
		return TTSResult{}, &UpstreamError{Kind: ErrorKindEmptyChoices, Status: resp.StatusCode, Cause: errors.New("volcano tts decoded to empty audio")}
	}
	// BR-4.3 — output bound enforced at the adapter (and again at the gateway).
	if len(audio) > MaxSynthesizedAudioBytes {
		return TTSResult{}, &UpstreamError{Kind: ErrorKindOutputTooLarge, Status: resp.StatusCode, Cause: fmt.Errorf("synthesized audio %d bytes exceeds the %d cap", len(audio), MaxSynthesizedAudioBytes)}
	}

	return TTSResult{Audio: audio, MimeType: mime}, nil
}

// ttsEncodingFor maps the gateway-validated response_format to the Volcano
// `audio.encoding` token AND the HTTP Content-Type the gateway will set
// (Q-TTS-RESPFORMAT RATIFIED): mp3→audio/mpeg, wav→audio/wav, opus→audio/ogg
// (Volcano emits ogg_opus). 9.7 ships exactly these three.
func ttsEncodingFor(responseFormat string) (encoding, mime string, err error) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "mp3":
		return "mp3", "audio/mpeg", nil
	case "wav":
		return "wav", "audio/wav", nil
	case "opus":
		return "ogg_opus", "audio/ogg", nil
	default:
		return "", "", fmt.Errorf("unsupported response_format %q", responseFormat)
	}
}
