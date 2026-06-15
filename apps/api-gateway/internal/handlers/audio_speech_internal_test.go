// Story 9.7 (T2 + T6) — gateway TTS endpoint tests. Covers the OpenAI-Audio-
// Speech-compatible JSON endpoint (validation, defaults, capability gate, binary
// response shaping, dispatch) AND the input/output security contract (body cap,
// HARD rune cap, output bound, mime cross-check, PII no-leak, reject ordering).
//
// Scenario IDs ← docs/qa/assessments/9.7-test-design-20260615.md. Reuses the
// captureEmitter + asrAuthedCtx helpers from the sibling ASR test file (same
// package).
package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/billingemit"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// ---- fakes -------------------------------------------------------------

// fakeSynthesizer satisfies adapterclient.ClientHandle (for the registry map)
// AND adapterclient.Synthesizer (the seam the handler asserts).
type fakeSynthesizer struct {
	resp   *adapterv1.SynthesizeResponse
	err    error
	called bool
	gotReq *adapterv1.SynthesizeRequest
}

func (f *fakeSynthesizer) Chat(context.Context, *adapterv1.ChatRequest, http.Header) (adapterclient.Stream, error) {
	return nil, errors.New("chat not used in TTS tests")
}

func (f *fakeSynthesizer) Synthesize(_ context.Context, req *adapterv1.SynthesizeRequest, _ http.Header) (*adapterv1.SynthesizeResponse, error) {
	f.called = true
	f.gotReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// ---- helpers -----------------------------------------------------------

func newSpeechHandler(syn *fakeSynthesizer, em billingemit.UsageEmitter, logger *slog.Logger) *AudioSpeechHandler {
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		adapterclient.DoubaoTTSModelID: syn,
	})
	opts := []AudioSpeechHandlerOption{WithSpeechAdapterRegistry(reg)}
	if em != nil {
		opts = append(opts, WithSpeechUsageEmitter(em))
	}
	return NewAudioSpeechHandler(logger, opts...)
}

func doSpeech(h *AudioSpeechHandler, jsonBody string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(asrAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func okSpeechResp(mime string, audio []byte) *adapterv1.SynthesizeResponse {
	return &adapterv1.SynthesizeResponse{Audio: audio, MimeType: mime}
}

func silent() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ============================================================
// AC1 — validation (UNIT-001..006 + boundary)
// ============================================================

func Test_validateAudioSpeechRequest(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	mk := func(mut func(*audioSpeechRequest)) *audioSpeechRequest {
		r := &audioSpeechRequest{Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3"}
		mut(r)
		return r
	}
	cases := []struct {
		name      string
		req       *audioSpeechRequest
		wantValid bool
		wantParam string
	}{
		{"UNIT-001 model empty", mk(func(r *audioSpeechRequest) { r.Model = "" }), false, "model"},
		{"UNIT-001 model >100", mk(func(r *audioSpeechRequest) { r.Model = strings.Repeat("x", 101) }), false, "model"},
		{"BLIND-BOUNDARY-003 model ==100", mk(func(r *audioSpeechRequest) { r.Model = strings.Repeat("x", 100) }), true, ""},
		{"BLIND-BOUNDARY-001 input empty", mk(func(r *audioSpeechRequest) { r.Input = "" }), false, "input"},
		{"UNIT-026 input 4096 runes ok", mk(func(r *audioSpeechRequest) { r.Input = strings.Repeat("世", 4096) }), true, ""},
		{"UNIT-026 input 4097 runes bad", mk(func(r *audioSpeechRequest) { r.Input = strings.Repeat("世", 4097) }), false, "input"},
		{"UNIT-003 voice empty", mk(func(r *audioSpeechRequest) { r.Voice = "" }), false, "voice"},
		{"UNIT-004 aac deferred", mk(func(r *audioSpeechRequest) { r.ResponseFormat = "aac" }), false, "response_format"},
		{"UNIT-004 wav ok", mk(func(r *audioSpeechRequest) { r.ResponseFormat = "wav" }), true, ""},
		{"UNIT-004 opus ok", mk(func(r *audioSpeechRequest) { r.ResponseFormat = "opus" }), true, ""},
		{"UNIT-006 speed 0.24 bad", mk(func(r *audioSpeechRequest) { r.Speed = f(0.24) }), false, "speed"},
		{"UNIT-006 speed 4.01 bad", mk(func(r *audioSpeechRequest) { r.Speed = f(4.01) }), false, "speed"},
		{"BLIND-BOUNDARY-004 speed 0.25 ok", mk(func(r *audioSpeechRequest) { r.Speed = f(0.25) }), true, ""},
		{"BLIND-BOUNDARY-004 speed 4.0 ok", mk(func(r *audioSpeechRequest) { r.Speed = f(4.0) }), true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, param, valid := validateAudioSpeechRequest(c.req)
			if valid != c.wantValid {
				t.Fatalf("valid=%v, want %v", valid, c.wantValid)
			}
			if !valid && (param == nil || *param != c.wantParam) {
				t.Fatalf("param=%v, want %q", param, c.wantParam)
			}
		})
	}
}

// ============================================================
// AC1/AC2 — handler integration
// ============================================================

// 9.7-INT-001 + INT-008 — JSON happy path → 200 RAW audio body with the correct
// Content-Type + Content-Length; adapter received the validated fields; a
// PER_CHARACTER usage event carries the gateway-computed rune count.
func TestSpeech_HappyPath_BinaryResponse(t *testing.T) {
	audio := []byte{0x49, 0x44, 0x33, 0x01, 0x02, 0x03} // arbitrary mp3-ish bytes
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", audio)}
	em := &captureEmitter{}
	h := newSpeechHandler(syn, em, silent())

	rr := doSpeech(h, `{"model":"doubao-tts","input":"你好世界","voice":"zh_female_1","response_format":"mp3"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	// INT-008 — binary-response integrity: raw bytes, NOT JSON-wrapped.
	if !bytes.Equal(rr.Body.Bytes(), audio) {
		t.Fatalf("body not raw audio: %x", rr.Body.Bytes())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("Content-Type=%q, want audio/mpeg", ct)
	}
	if cl := rr.Header().Get("Content-Length"); cl != "6" {
		t.Fatalf("Content-Length=%q, want 6", cl)
	}
	if !syn.called {
		t.Fatal("adapter Synthesize was not called")
	}
	if syn.gotReq.GetInput() != "你好世界" || syn.gotReq.GetVoice() != "zh_female_1" || syn.gotReq.GetResponseFormat() != "mp3" {
		t.Fatalf("synthesize req fields: %+v", syn.gotReq)
	}
	// INT-020 — PER_CHARACTER usage event with character_count = runes (4), tokens 0.
	if len(em.events) != 1 {
		t.Fatalf("want 1 usage event, got %d", len(em.events))
	}
	ev := em.events[0]
	if ev.GetBillingMode() != billingv1.BillingMode_BILLING_MODE_PER_CHARACTER {
		t.Fatalf("billing_mode=%v, want PER_CHARACTER", ev.GetBillingMode())
	}
	if ev.GetCharacterCount() != 4 || ev.GetTotalTokens() != 0 || ev.GetAudioDurationSeconds() != 0 {
		t.Fatalf("usage event wrong: chars=%d tokens=%d dur=%v", ev.GetCharacterCount(), ev.GetTotalTokens(), ev.GetAudioDurationSeconds())
	}
}

// 9.7-UNIT-005 — defaults: response_format omitted → mp3; speed omitted → 1.0
// (the wire carries the resolved speed).
func TestSpeech_DefaultsApplied(t *testing.T) {
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1, 2})}
	h := newSpeechHandler(syn, &captureEmitter{}, silent())
	rr := doSpeech(h, `{"model":"doubao-tts","input":"hi","voice":"v"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if syn.gotReq.GetResponseFormat() != "mp3" {
		t.Fatalf("default response_format=%q, want mp3", syn.gotReq.GetResponseFormat())
	}
	if syn.gotReq.Speed == nil || syn.gotReq.GetSpeed() != 1.0 {
		t.Fatalf("default speed=%v, want 1.0", syn.gotReq.Speed)
	}
}

// 9.7-UNIT-008 / INT-002 — non-speech model → 400, ZERO upstream (fail-closed).
func TestSpeech_CapabilityGate_FailClosed(t *testing.T) {
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1})}
	// qwen-max is chat-capable but NOT speech-capable; route it via the fake handle
	// so resolution would succeed and only the capability gate stops it.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": syn})
	h := NewAudioSpeechHandler(silent(), WithSpeechAdapterRegistry(reg))
	rr := doSpeech(h, `{"model":"qwen-max","input":"hi","voice":"v"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "does not support speech synthesis") {
		t.Fatalf("body=%s", rr.Body.String())
	}
	if syn.called {
		t.Fatal("upstream must NOT be called on a capability reject (fail-closed)")
	}
}

// 9.7-UNIT-009 — malformed JSON → 400; an unknown field (DisallowUnknownFields) → 400.
func TestSpeech_MalformedJSON(t *testing.T) {
	h := newSpeechHandler(&fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1})}, nil, silent())
	if rr := doSpeech(h, `{not json`); rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed: status=%d", rr.Code)
	}
	if rr := doSpeech(h, `{"model":"doubao-tts","input":"hi","voice":"v","bogus":1}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status=%d", rr.Code)
	}
}

// 9.7-INT-026 — body over the 64 KiB cap → 413 (before decode).
func TestSpeech_BodyCapExceeded(t *testing.T) {
	h := newSpeechHandler(&fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1})}, nil, silent())
	big := strings.Repeat("a", int(maxSpeechBodyBytes)+1024)
	rr := doSpeech(h, `{"model":"doubao-tts","voice":"v","input":"`+big+`"}`)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", rr.Code)
	}
}

// 9.7-UNIT-026 / BLIND-BOUNDARY-002 — rune (not byte) cap: 4096 multi-byte runes
// accepted (byte length ≫ 4096, well under the 64 KiB body cap); 4097 → 400.
func TestSpeech_CharCap_Runes(t *testing.T) {
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1})}
	h := newSpeechHandler(syn, &captureEmitter{}, silent())
	ok4096 := strings.Repeat("世", 4096) // 3 bytes each = 12288 bytes < 64 KiB
	rr := doSpeech(h, `{"model":"doubao-tts","voice":"v","input":"`+ok4096+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("4096 runes: status=%d body=%s", rr.Code, rr.Body.String())
	}
	syn2 := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1})}
	h2 := newSpeechHandler(syn2, &captureEmitter{}, silent())
	bad := strings.Repeat("世", 4097)
	rr2 := doSpeech(h2, `{"model":"doubao-tts","voice":"v","input":"`+bad+`"}`)
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("4097 runes: status=%d, want 400", rr2.Code)
	}
	if syn2.called {
		t.Fatal("over-cap input must reject before dispatch")
	}
}

// 9.7-INT-027 — synthesized audio over the 20 MiB cap → 502, ZERO billing.
func TestSpeech_OutputBound(t *testing.T) {
	big := make([]byte, maxSynthesizedAudioBytes+1)
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", big)}
	em := &captureEmitter{}
	h := newSpeechHandler(syn, em, silent())
	rr := doSpeech(h, `{"model":"doubao-tts","input":"hi","voice":"v"}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", rr.Code)
	}
	if len(em.events) != 0 {
		t.Fatal("over-cap synthesis must NOT bill (no charge for undelivered audio)")
	}
}

// 9.7-INT-016 — adapter mime_type ≠ resolved response_format map → 502 (defence).
func TestSpeech_MimeMismatch(t *testing.T) {
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/wav", []byte{1, 2})} // asked mp3, got wav
	em := &captureEmitter{}
	h := newSpeechHandler(syn, em, silent())
	rr := doSpeech(h, `{"model":"doubao-tts","input":"hi","voice":"v","response_format":"mp3"}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", rr.Code)
	}
	if len(em.events) != 0 {
		t.Fatal("mime mismatch must NOT bill")
	}
}

// 9.7-BLIND-ERROR-003 — adapter deadline → 504, ZERO usage emitted.
func TestSpeech_UpstreamTimeout(t *testing.T) {
	syn := &fakeSynthesizer{err: connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout"))}
	em := &captureEmitter{}
	h := newSpeechHandler(syn, em, silent())
	rr := doSpeech(h, `{"model":"doubao-tts","input":"hi","voice":"v"}`)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d, want 504", rr.Code)
	}
	if len(em.events) != 0 {
		t.Fatal("no usage event must be emitted on an upstream fault")
	}
}

// 9.7-INT-028 — PII no-leak: the input text + the audio bytes NEVER appear in the
// logs; only non-PII signals (model/voice/format/character_count) do.
func TestSpeech_NoPIILeak(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	audioMarker := []byte("AUDIO_SECRET_BYTES_MARKER_xyz")
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", audioMarker)}
	h := newSpeechHandler(syn, &captureEmitter{}, logger)

	secretInput := "TOP_SECRET_INPUT_DO_NOT_LOG"
	rr := doSpeech(h, `{"model":"doubao-tts","voice":"zh_f","input":"`+secretInput+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	logs := logBuf.String()
	if strings.Contains(logs, secretInput) {
		t.Fatal("PII LEAK: input text appeared in logs")
	}
	if strings.Contains(logs, "AUDIO_SECRET_BYTES_MARKER") {
		t.Fatal("PII LEAK: audio bytes appeared in logs")
	}
	if !strings.Contains(logs, "character_count") || !strings.Contains(logs, "doubao-tts") {
		t.Fatalf("expected non-PII signals in logs: %s", logs)
	}
}

// ============================================================
// AC4 — content-safety on the INPUT text (do-NOT-regress vs 9.6)
// ============================================================

func newSpeechSafetyHandler(syn *fakeSynthesizer) *AudioSpeechHandler {
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		adapterclient.DoubaoTTSModelID: syn,
	})
	return NewAudioSpeechHandler(
		silent(),
		WithSpeechAdapterRegistry(reg),
		WithSpeechSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)),
	)
}

// 9.7-INT-030 — a flagged input → canonical 400_content_filter, reject-before-dispatch.
func TestSpeech_Safety_FlaggedInput_Blocked(t *testing.T) {
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1})}
	h := newSpeechSafetyHandler(syn)
	rr := doSpeech(h, `{"model":"doubao-tts","voice":"v","input":"please say badword now"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "400_content_filter") {
		t.Fatalf("want 400_content_filter; status=%d body=%s", rr.Code, rr.Body.String())
	}
	if syn.called {
		t.Fatal("upstream must NOT be called on a content-safety block (reject-before-dispatch)")
	}
}

// 9.7-INT-030 — a clean input falls through to 200 (output audio is never scanned).
func TestSpeech_Safety_CleanInput_FallsThrough(t *testing.T) {
	syn := &fakeSynthesizer{resp: okSpeechResp("audio/mpeg", []byte{1, 2})}
	h := newSpeechSafetyHandler(syn)
	rr := doSpeech(h, `{"model":"doubao-tts","voice":"v","input":"a clean friendly sentence"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !syn.called {
		t.Fatal("adapter must be called on a clean input")
	}
}
