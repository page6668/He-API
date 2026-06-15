// Story 9.6 (T2 + T6) — gateway ASR endpoint tests. Covers the Whisper-
// compatible multipart endpoint (validation, capability gate, response
// shaping, dispatch) AND the audio-input security contract (body cap, single
// file part, mime sniff, PII no-leak, temp hygiene, reject ordering).
//
// Scenario IDs ← docs/qa/assessments/9.6-test-design-20260615.md.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/billingemit"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

const asrTestAPIKeyID = "11111111-1111-1111-1111-111111111111"

// ---- fakes -------------------------------------------------------------

// fakeTranscriber satisfies adapterclient.ClientHandle (for the registry map)
// AND adapterclient.Transcriber (the seam the handler asserts).
type fakeTranscriber struct {
	resp     *adapterv1.TranscribeResponse
	err      error
	called   bool
	gotReq   *adapterv1.TranscribeRequest
	gotAudio []byte
}

func (f *fakeTranscriber) Chat(context.Context, *adapterv1.ChatRequest, http.Header) (adapterclient.Stream, error) {
	return nil, errors.New("chat not used in ASR tests")
}

func (f *fakeTranscriber) Transcribe(_ context.Context, req *adapterv1.TranscribeRequest, _ http.Header) (*adapterv1.TranscribeResponse, error) {
	f.called = true
	f.gotReq = req
	f.gotAudio = req.GetAudio()
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// captureEmitter records emitted usage events.
type captureEmitter struct{ events []*billingv1.UsageEvent }

func (c *captureEmitter) Emit(_ context.Context, ev *billingv1.UsageEvent) {
	c.events = append(c.events, ev)
}

var _ billingemit.UsageEmitter = (*captureEmitter)(nil)

// ---- helpers -----------------------------------------------------------

func asrAuthedCtx(ctx context.Context) context.Context {
	ctx = middleware.WithAPIKeyID(ctx, asrTestAPIKeyID)
	ctx = middleware.BearerWithUserID(ctx, "22222222-2222-2222-2222-222222222222")
	ctx = middleware.WithTeamID(ctx, "")
	ctx = middleware.WithScope(ctx, `{}`)
	return ctx
}

type filePart struct {
	field    string
	filename string
	ct       string
	data     []byte
}

// buildMultipart builds a multipart/form-data body with the given text fields
// and file parts. Returns the body + the Content-Type header.
func buildMultipart(fields map[string]string, files []filePart) (*bytes.Buffer, string) {
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for _, fp := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="`+fp.field+`"; filename="`+fp.filename+`"`)
		if fp.ct != "" {
			h.Set("Content-Type", fp.ct)
		}
		part, _ := mw.CreatePart(h)
		_, _ = part.Write(fp.data)
	}
	_ = mw.Close()
	return buf, mw.FormDataContentType()
}

// mp3Bytes returns bytes that http.DetectContentType classifies as audio/mpeg
// (an ID3-tagged MP3 header) followed by filler.
func mp3Bytes() []byte {
	b := append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0x00}, 64)...)
	return b
}

// doASR runs the handler against a built multipart body with an authed context.
func doASR(h *AudioTranscriptionsHandler, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(asrAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func newASRHandler(tr *fakeTranscriber, em billingemit.UsageEmitter, logger *slog.Logger) *AudioTranscriptionsHandler {
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		adapterclient.DoubaoASRModelID: tr,
	})
	opts := []AudioTranscriptionsHandlerOption{WithAudioAdapterRegistry(reg)}
	if em != nil {
		opts = append(opts, WithAudioUsageEmitter(em))
	}
	return NewAudioTranscriptionsHandler(logger, opts...)
}

func okResp() *adapterv1.TranscribeResponse {
	return &adapterv1.TranscribeResponse{Text: "你好世界", Language: "zh", DurationSeconds: 3.2}
}

// ============================================================
// AC1 — validation (UNIT-001..005)
// ============================================================

func Test_validateAudioTranscriptionRequest(t *testing.T) {
	t.Parallel()
	mk := func(mut func(*audioTranscriptionRequest)) *audioTranscriptionRequest {
		r := &audioTranscriptionRequest{Model: "doubao-asr", ResponseFormat: "json"}
		mut(r)
		return r
	}
	temp := func(f float64) *float64 { return &f }
	cases := []struct {
		name      string
		req       *audioTranscriptionRequest
		wantValid bool
		wantParam string
	}{
		{"UNIT-001 model empty", mk(func(r *audioTranscriptionRequest) { r.Model = "" }), false, "model"},
		{"UNIT-001 model >100", mk(func(r *audioTranscriptionRequest) { r.Model = strings.Repeat("x", 101) }), false, "model"},
		{"BLIND-BOUNDARY-005 model ==100", mk(func(r *audioTranscriptionRequest) { r.Model = strings.Repeat("x", 100) }), true, ""},
		{"UNIT-002 srt deferred", mk(func(r *audioTranscriptionRequest) { r.ResponseFormat = "srt" }), false, "response_format"},
		{"UNIT-002 vtt deferred", mk(func(r *audioTranscriptionRequest) { r.ResponseFormat = "vtt" }), false, "response_format"},
		{"UNIT-002 verbose_json ok", mk(func(r *audioTranscriptionRequest) { r.ResponseFormat = "verbose_json" }), true, ""},
		{"UNIT-003 temp -0.1", mk(func(r *audioTranscriptionRequest) { r.Temperature = temp(-0.1) }), false, "temperature"},
		{"UNIT-003 temp 1.1", mk(func(r *audioTranscriptionRequest) { r.Temperature = temp(1.1) }), false, "temperature"},
		{"BLIND-BOUNDARY-002 temp 0", mk(func(r *audioTranscriptionRequest) { r.Temperature = temp(0) }), true, ""},
		{"BLIND-BOUNDARY-002 temp 1", mk(func(r *audioTranscriptionRequest) { r.Temperature = temp(1) }), true, ""},
		{"UNIT-004 lang bad len", mk(func(r *audioTranscriptionRequest) { r.Language = "eng" }), false, "language"},
		{"UNIT-004 lang ok", mk(func(r *audioTranscriptionRequest) { r.Language = "zh" }), true, ""},
		{"UNIT-005 prompt over cap", mk(func(r *audioTranscriptionRequest) { r.Prompt = strings.Repeat("p", 2049) }), false, "prompt"},
		{"UNIT-005 prompt at cap", mk(func(r *audioTranscriptionRequest) { r.Prompt = strings.Repeat("p", 2048) }), true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, param, valid := validateAudioTranscriptionRequest(c.req)
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
// AC4 — mime resolution + sniff (UNIT-023)
// ============================================================

func Test_resolveAudioMIME(t *testing.T) {
	t.Parallel()
	mkFH := func(filename, ct string) *multipart.FileHeader {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		return &multipart.FileHeader{Filename: filename, Header: h}
	}
	htmlBytes := []byte("<!DOCTYPE html><html><body>hi</body></html>")
	flacBytes := append([]byte("fLaC\x00\x00\x00\x22"), bytes.Repeat([]byte{0}, 64)...)

	cases := []struct {
		name     string
		fh       *multipart.FileHeader
		audio    []byte
		wantOK   bool
		wantMIME string
	}{
		{"mp3 declared+sniff agree", mkFH("hello.mp3", "audio/mpeg"), mp3Bytes(), true, "audio/mpeg"},
		{"flac ext, octet sniff accepted", mkFH("a.flac", "audio/flac"), flacBytes, true, "audio/flac"},
		{"m4a via ext only (blank CT)", mkFH("a.m4a", ""), append([]byte("ID3"), bytes.Repeat([]byte{0}, 64)...), true, "audio/mp4"},
		{"text/html mislabelled audio/mpeg → reject", mkFH("evil.mp3", "audio/mpeg"), htmlBytes, false, ""},
		{"unknown ext + non-audio CT → reject", mkFH("a.txt", "text/plain"), []byte("just text here"), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := resolveAudioMIME(c.fh, c.audio)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (mime=%q)", ok, c.wantOK, got)
			}
			if ok && got != c.wantMIME {
				t.Fatalf("mime=%q want %q", got, c.wantMIME)
			}
		})
	}
}

// ============================================================
// AC1 — response shaping (UNIT-007)
// ============================================================

func Test_writeTranscriptionResponse_shapes(t *testing.T) {
	t.Parallel()
	resp := &adapterv1.TranscribeResponse{Text: "hi there", Language: "en", DurationSeconds: 2.5, SegmentsJson: []byte(`[{"id":0}]`)}

	// json
	rr := httptest.NewRecorder()
	writeTranscriptionResponse(rr, "json", resp)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("json content-type=%q", ct)
	}
	var jb map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &jb)
	if jb["text"] != "hi there" || len(jb) != 1 {
		t.Fatalf("json body=%v", jb)
	}

	// text
	rr = httptest.NewRecorder()
	writeTranscriptionResponse(rr, "text", resp)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("text content-type=%q", ct)
	}
	if rr.Body.String() != "hi there" {
		t.Fatalf("text body=%q", rr.Body.String())
	}

	// verbose_json with segments
	rr = httptest.NewRecorder()
	writeTranscriptionResponse(rr, "verbose_json", resp)
	var vb map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &vb)
	if vb["task"] != "transcribe" || vb["language"] != "en" || vb["text"] != "hi there" {
		t.Fatalf("verbose body=%v", vb)
	}
	if _, ok := vb["segments"]; !ok {
		t.Fatalf("verbose_json must carry segments when present: %v", vb)
	}

	// verbose_json WITHOUT segments → omitted (never fabricated)
	rr = httptest.NewRecorder()
	writeTranscriptionResponse(rr, "verbose_json", &adapterv1.TranscribeResponse{Text: "x", Language: "en", DurationSeconds: 1})
	var vb2 map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &vb2)
	if _, ok := vb2["segments"]; ok {
		t.Fatalf("segments must be omitted when absent: %v", vb2)
	}
}

// ============================================================
// AC1/AC4 — handler integration
// ============================================================

// 9.6-INT-001 — multipart happy path → 200 {"text":...}; adapter received the audio.
func TestASR_HappyPath(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()}
	em := &captureEmitter{}
	h := newASRHandler(tr, em, slog.New(slog.NewTextHandler(io.Discard, nil)))

	audio := mp3Bytes()
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr", "language": "zh"},
		[]filePart{{field: "file", filename: "hello.mp3", ct: "audio/mpeg", data: audio}})
	rr := doASR(h, body, ct)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var jb map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &jb)
	if jb["text"] != "你好世界" {
		t.Fatalf("body=%v", jb)
	}
	if !tr.called {
		t.Fatal("adapter Transcribe was not called")
	}
	if string(tr.gotAudio) != string(audio) {
		t.Fatal("adapter did not receive the audio bytes verbatim")
	}
	if tr.gotReq.GetModel() != "doubao-asr" || tr.gotReq.GetMimeType() != "audio/mpeg" || tr.gotReq.GetLanguage() != "zh" {
		t.Fatalf("transcribe req fields: %+v", tr.gotReq)
	}
	// 9.6-INT-015 — PER_MINUTE usage event with duration, tokens 0.
	if len(em.events) != 1 {
		t.Fatalf("want 1 usage event, got %d", len(em.events))
	}
	ev := em.events[0]
	if ev.GetBillingMode() != billingv1.BillingMode_BILLING_MODE_PER_MINUTE {
		t.Fatalf("billing_mode=%v", ev.GetBillingMode())
	}
	if ev.GetAudioDurationSeconds() != 3.2 || ev.GetTotalTokens() != 0 {
		t.Fatalf("usage event wrong: dur=%v tokens=%d", ev.GetAudioDurationSeconds(), ev.GetTotalTokens())
	}
}

// 9.6-UNIT-006 / INT-006 / INT-022 — non-transcription model → 400, ZERO upstream.
func TestASR_CapabilityGate_FailClosed(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()}
	h := newASRHandler(tr, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// qwen-max is Chat-capable but NOT transcription-capable; route it via the
	// fake handle so resolution succeeds and only the capability gate stops it.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": tr})
	h = NewAudioTranscriptionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), WithAudioAdapterRegistry(reg))

	body, ct := buildMultipart(map[string]string{"model": "qwen-max"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "does not support audio transcription") {
		t.Fatalf("body=%s", rr.Body.String())
	}
	if tr.called {
		t.Fatal("upstream must NOT be called on a capability reject (fail-closed)")
	}
}

// 9.6-UNIT-024 — single `file` part: zero → 400, multiple → 400.
func TestASR_FilePartCount(t *testing.T) {
	h := newASRHandler(&fakeTranscriber{resp: okResp()}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// zero file parts
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"}, nil)
	if rr := doASR(h, body, ct); rr.Code != http.StatusBadRequest {
		t.Fatalf("zero-file status=%d", rr.Code)
	}
	// two file parts
	body, ct = buildMultipart(map[string]string{"model": "doubao-asr"}, []filePart{
		{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()},
		{field: "file", filename: "b.mp3", ct: "audio/mpeg", data: mp3Bytes()},
	})
	if rr := doASR(h, body, ct); rr.Code != http.StatusBadRequest {
		t.Fatalf("two-file status=%d", rr.Code)
	}
}

// 9.6-BLIND-BOUNDARY-001 — empty 0-byte file → 400.
func TestASR_EmptyFile(t *testing.T) {
	h := newASRHandler(&fakeTranscriber{resp: okResp()}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "empty.mp3", ct: "audio/mpeg", data: nil}})
	if rr := doASR(h, body, ct); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty-file status=%d body=%s", rr.Code, rr.Body.String())
	}
}

// 9.6-UNIT-023 — text/html mislabelled audio/mpeg → 400 (sniff catches it).
func TestASR_MislabelledFile(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()}
	h := newASRHandler(tr, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "evil.mp3", ct: "audio/mpeg", data: []byte("<!DOCTYPE html><html></html>")}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rr.Code)
	}
	if tr.called {
		t.Fatal("mislabelled file must reject BEFORE upstream")
	}
}

// 9.6-UNIT-026 — non-multipart content-type → 415.
func TestASR_NonMultipart(t *testing.T) {
	h := newASRHandler(&fakeTranscriber{resp: okResp()}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader(`{"model":"doubao-asr"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(asrAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	// Canonical 400 family (no 415 code in the §5.1.2 taxonomy — BR-1.1).
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (canonical family for non-multipart)", rr.Code)
	}
}

// 9.6-UNIT-025 — malformed multipart stream → 400.
func TestASR_MalformedMultipart(t *testing.T) {
	h := newASRHandler(&fakeTranscriber{resp: okResp()}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("not a real multipart body"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	req = req.WithContext(asrAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
}

// 9.6-UNIT-022 / INT-019 — body-cap boundary: just over 25 MiB → 413.
func TestASR_BodyCapExceeded(t *testing.T) {
	h := newASRHandler(&fakeTranscriber{resp: okResp()}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	big := bytes.Repeat([]byte{0x00}, int(MaxAudioBodyBytes)+1024) // > 25 MiB
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "big.mp3", ct: "audio/mpeg", data: big}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", rr.Code)
	}
}

// 9.6-BLIND-ERROR-004 — adapter deadline → 504, ZERO usage emitted.
func TestASR_UpstreamTimeout(t *testing.T) {
	tr := &fakeTranscriber{err: connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout"))}
	em := &captureEmitter{}
	h := newASRHandler(tr, em, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d, want 504", rr.Code)
	}
	if len(em.events) != 0 {
		t.Fatal("no usage event must be emitted on an upstream fault")
	}
}

// 9.6-UNIT-019 / BR-2.5 — vendor reports no duration → fail-closed (no zero-cost
// ledger row; the gateway does not emit a usage event).
func TestASR_MissingDuration_FailClosed(t *testing.T) {
	tr := &fakeTranscriber{resp: &adapterv1.TranscribeResponse{Text: "hi", DurationSeconds: 0}}
	em := &captureEmitter{}
	h := newASRHandler(tr, em, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", rr.Code)
	}
	if len(em.events) != 0 {
		t.Fatal("a zero-duration result must NOT emit a usage event (no silent zero-cost)")
	}
}

// 9.6-INT-020 — PII no-leak audit: audio bytes + transcript NEVER in the logs.
func TestASR_NoPIILeak(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	secretTranscript := "TOP_SECRET_TRANSCRIPT_DO_NOT_LOG"
	tr := &fakeTranscriber{resp: &adapterv1.TranscribeResponse{Text: secretTranscript, Language: "en", DurationSeconds: 1.0}}
	h := newASRHandler(tr, &captureEmitter{}, logger)

	audioMarker := append([]byte("ID3"), []byte("AUDIO_SECRET_BYTES_MARKER")...)
	audioMarker = append(audioMarker, bytes.Repeat([]byte{0}, 64)...)
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr", "prompt": "context"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: audioMarker}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	logs := logBuf.String()
	if strings.Contains(logs, secretTranscript) {
		t.Fatal("PII LEAK: transcript text appeared in logs")
	}
	if strings.Contains(logs, "AUDIO_SECRET_BYTES_MARKER") {
		t.Fatal("PII LEAK: audio bytes appeared in logs")
	}
	// The non-PII signals SHOULD be present.
	if !strings.Contains(logs, "audio_bytes_size") || !strings.Contains(logs, "doubao-asr") {
		t.Fatalf("expected non-PII signals in logs: %s", logs)
	}
}

// 9.6-UNIT-027 — duration is vendor-authoritative: a client-supplied `duration`
// form field is IGNORED (no client field can set the billing lever).
func TestASR_ClientDurationIgnored(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()} // vendor says 3.2
	em := &captureEmitter{}
	h := newASRHandler(tr, em, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body, ct := buildMultipart(map[string]string{"model": "doubao-asr", "duration": "9999", "audio_duration_seconds": "9999"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()}})
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if em.events[0].GetAudioDurationSeconds() != 3.2 {
		t.Fatalf("client-supplied duration must be ignored; got %v", em.events[0].GetAudioDurationSeconds())
	}
}
