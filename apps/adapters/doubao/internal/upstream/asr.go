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

// Story 9.6 (T3, Q-ASR-UPSTREAM) — Volcano (豆包语音) ASR upstream client.
//
// Volcano ASR is NOT OpenAI-Whisper-compatible — it is BytanceDance's own
// OpenSpeech protocol (a distinct base URL + auth + request/response schema), so
// this is a REAL (non-identity) translation, unlike 9.5's compat-mode reuse.
//
// VENDOR-DOC-CONFIRM (Architect Round 1, confidence: Medium): the request shape
// below models the documented OpenSpeech sync recognition contract
// (openspeech.bytedance.com): a JSON body carrying `app{appid,token,cluster}` +
// inline base64 audio, auth via the `Authorization: Bearer; <token>` header,
// returning recognised text + an audio duration. The EXACT path / field names /
// success codes are env-/const-configurable and are exercised against a
// fake-upstream implementing this contract; a live-Volcano confirmation of
// (path, auth, encoding) remains an ops gate before production traffic.
//
// The adapter presents a SYNC contract to the gateway (the Whisper transcription
// contract is sync); for the inline-bytes wire we POST the audio in one request
// and parse the recognised text synchronously. If a future deployment must use
// the async submit→poll file-recognition API, the polling loop belongs here,
// bounded by ctx (the request deadline) → surfaced as a timeout.

// DefaultASRPath is the default sync-recognition path (env-overridable).
const DefaultASRPath = "/api/v1/asr"

// ASRConfig is the Volcano ASR upstream config — DISTINCT from the Ark chat
// config (different product/endpoint). All fields are env-sourced at boot.
type ASRConfig struct {
	BaseURL string // e.g. https://openspeech.bytedance.com
	Path    string // sync recognition path (default DefaultASRPath)
	Token   string // access token → Authorization: Bearer; <token>
	AppID   string // OpenSpeech app id
	Cluster string // OpenSpeech cluster (e.g. volcengine_input_common)
}

// Configured reports whether the mandatory ASR upstream fields are all set.
// The Doubao service warns at boot if unset and fail-fasts at request time
// (the 4.5 BR-1.12 precedent) so a chat-only Doubao deployment is unaffected.
func (c ASRConfig) Configured() bool {
	return c.BaseURL != "" && c.Token != "" && c.AppID != "" && c.Cluster != ""
}

// ErrASRNotConfigured is returned by Recognize when the ASR upstream env is
// unset — the adapter maps it to connect.CodeUnavailable (never a silent zero).
var ErrASRNotConfigured = errors.New("doubao ASR upstream is not configured")

// ASRClient performs sync Volcano ASR recognition.
type ASRClient struct {
	cfg        ASRConfig
	httpClient *http.Client
}

// NewASRClient builds an ASRClient with the same HTTP/2-preferred + otel
// transport policy as the chat client (BR-TR-6).
func NewASRClient(cfg ASRConfig, timeout time.Duration) *ASRClient {
	if cfg.Path == "" {
		cfg.Path = DefaultASRPath
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
	return &ASRClient{
		cfg: cfg,
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   timeout,
		},
	}
}

// ASRRequest is the gateway→adapter request (already validated upstream).
type ASRRequest struct {
	Model       string
	Audio       []byte
	MimeType    string
	Language    string
	Prompt      string
	HeRequestID string
}

// ASRResult is the parsed recognition result.
type ASRResult struct {
	Text            string
	Language        string
	DurationSeconds float64
	SegmentsJSON    []byte // raw utterances passthrough; nil when absent
}

// ---- Volcano wire shapes (VENDOR-DOC-CONFIRM) --------------------------

type volcApp struct {
	AppID   string `json:"appid"`
	Token   string `json:"token"`
	Cluster string `json:"cluster"`
}

type volcUser struct {
	UID string `json:"uid"`
}

type volcAudio struct {
	Format string `json:"format"`
	Data   string `json:"data"` // base64 inline audio
}

type volcReqMeta struct {
	ReqID    string `json:"reqid,omitempty"`
	Language string `json:"language,omitempty"`
}

type volcASRRequest struct {
	App     volcApp     `json:"app"`
	User    volcUser    `json:"user"`
	Audio   volcAudio   `json:"audio"`
	Request volcReqMeta `json:"request"`
}

type volcASRResponse struct {
	Code       int              `json:"code"`
	Message    string           `json:"message"`
	Result     []volcASRSegment `json:"result"`
	AudioInfo  *volcAudioInfo   `json:"audio_info"`
	Utterances json.RawMessage  `json:"utterances,omitempty"`
}

type volcASRSegment struct {
	Text string `json:"text"`
}

type volcAudioInfo struct {
	Duration int `json:"duration"` // milliseconds
}

// volcSuccessCode is the OpenSpeech success code. 1000 is the documented
// success; 0 is also tolerated (some endpoints use 0). VENDOR-DOC-CONFIRM.
const volcSuccessCode = 1000

// Recognize POSTs the audio to the Volcano sync recognition endpoint and parses
// the result. Fail-closed on a missing duration (the billing lever — BR-2.5).
func (c *ASRClient) Recognize(ctx context.Context, req ASRRequest) (ASRResult, error) {
	if !c.cfg.Configured() {
		return ASRResult{}, ErrASRNotConfigured
	}

	body := volcASRRequest{
		App:   volcApp{AppID: c.cfg.AppID, Token: c.cfg.Token, Cluster: c.cfg.Cluster},
		User:  volcUser{UID: "he-api"},
		Audio: volcAudio{Format: mimeToVolcanoFormat(req.MimeType), Data: base64.StdEncoding.EncodeToString(req.Audio)},
		Request: volcReqMeta{
			ReqID:    req.HeRequestID,
			Language: req.Language,
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ASRResult{}, fmt.Errorf("asr marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + path.Clean("/"+strings.TrimLeft(c.cfg.Path, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return ASRResult{}, fmt.Errorf("asr build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// OpenSpeech auth: `Authorization: Bearer; <token>` (note the semicolon —
	// VENDOR-DOC-CONFIRM).
	httpReq.Header.Set("Authorization", "Bearer; "+c.cfg.Token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ASRResult{}, &UpstreamError{Kind: ClassifyError(err), Cause: err}
	}
	defer func() { _ = resp.Body.Close() }() // RESOURCE: always release the conn

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // bounded read
	if err != nil {
		return ASRResult{}, &UpstreamError{Kind: ErrorKindUpstream5xx, Status: resp.StatusCode, Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ASRResult{}, &UpstreamError{
			Kind:   ClassifyHTTPStatus(resp.StatusCode),
			Status: resp.StatusCode,
			Cause:  fmt.Errorf("volcano asr http %d", resp.StatusCode),
		}
	}

	var parsed volcASRResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ASRResult{}, &UpstreamError{Kind: ErrorKindMalformedChunk, Status: resp.StatusCode, Cause: fmt.Errorf("asr decode: %w", err)}
	}
	if parsed.Code != volcSuccessCode && parsed.Code != 0 {
		return ASRResult{}, &UpstreamError{
			Kind:   ErrorKindUpstream4xx,
			Status: resp.StatusCode,
			Cause:  fmt.Errorf("volcano asr code=%d msg=%q", parsed.Code, parsed.Message),
		}
	}

	var sb strings.Builder
	for _, seg := range parsed.Result {
		sb.WriteString(seg.Text)
	}
	text := sb.String()
	if text == "" {
		return ASRResult{}, &UpstreamError{Kind: ErrorKindEmptyChoices, Status: resp.StatusCode, Cause: errors.New("volcano asr returned no text")}
	}

	// BR-2.5 — duration is the billing lever; fail-closed if the vendor omits it
	// (never surface a zero-duration result that would become a zero-cost bill).
	if parsed.AudioInfo == nil || parsed.AudioInfo.Duration <= 0 {
		return ASRResult{}, &UpstreamError{Kind: ErrorKindUsageConstraint, Status: resp.StatusCode, Cause: errors.New("volcano asr returned no audio duration")}
	}

	res := ASRResult{
		Text:            text,
		Language:        req.Language,
		DurationSeconds: float64(parsed.AudioInfo.Duration) / 1000.0, // ms → s
	}
	if len(parsed.Utterances) > 0 {
		res.SegmentsJSON = []byte(parsed.Utterances)
	}
	return res, nil
}

// mimeToVolcanoFormat maps an audio mime type to the Volcano `audio.format`
// token. Defaults to "wav" for an unrecognised type (the allow-list upstream
// already bounds the inputs).
func mimeToVolcanoFormat(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	case "audio/webm":
		return "webm"
	default:
		return "wav"
	}
}
