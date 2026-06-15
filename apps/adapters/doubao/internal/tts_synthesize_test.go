// Story 9.7 (T3) — Doubao Service.Synthesize integration (INT-013) over a fake
// Volcano TTS upstream + the nil-client fail-fast path + the empty-voice
// defence (UNIT-017) + upstream-error mapping.
package internal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// 9.7-INT-013 — Synthesize over a fake Volcano TTS upstream → SynthesizeResponse
// with the base64-decoded audio + mime_type.
func TestService_Synthesize_FakeUpstream(t *testing.T) {
	audio := []byte{0xff, 0xfb, 0x90, 0x00, 0x10}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{"code": 3000, "data": base64.StdEncoding.EncodeToString(audio)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	svc := silentSvc().WithTTSClient(upstream.NewTTSClient(
		upstream.TTSConfig{BaseURL: srv.URL, Path: "/api/v1/tts", Token: "t", AppID: "a", Cluster: "c"},
		5*time.Second,
	))

	resp, err := svc.Synthesize(context.Background(), connect.NewRequest(&adapterv1.SynthesizeRequest{
		Model: "doubao-tts", Input: "你好世界", Voice: "zh_female_1", ResponseFormat: "mp3", HeRequestId: "he-9",
	}))
	if err != nil {
		t.Fatalf("Synthesize err=%v", err)
	}
	if string(resp.Msg.GetAudio()) != string(audio) {
		t.Fatalf("audio=%x, want %x", resp.Msg.GetAudio(), audio)
	}
	if resp.Msg.GetMimeType() != "audio/mpeg" {
		t.Fatalf("mime=%q, want audio/mpeg", resp.Msg.GetMimeType())
	}
}

// 9.7 — a chat/ASR-only deployment (no TTS client) fail-fasts with CodeUnavailable.
func TestService_Synthesize_NotConfigured(t *testing.T) {
	svc := silentSvc() // no WithTTSClient
	_, err := svc.Synthesize(context.Background(), connect.NewRequest(&adapterv1.SynthesizeRequest{
		Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err=%v code=%v, want CodeUnavailable", err, connect.CodeOf(err))
	}
}

// 9.7-BLIND-ERROR-004 — Volcano 503 → CodeUnavailable (mapped envelope).
func TestService_Synthesize_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	svc := silentSvc().WithTTSClient(upstream.NewTTSClient(
		upstream.TTSConfig{BaseURL: srv.URL, Path: "/api/v1/tts", Token: "t", AppID: "a", Cluster: "c"}, 5*time.Second))
	_, err := svc.Synthesize(context.Background(), connect.NewRequest(&adapterv1.SynthesizeRequest{
		Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err=%v code=%v, want CodeUnavailable", err, connect.CodeOf(err))
	}
}

// 9.7-UNIT-017 — an unusable (empty) voice with no configured default →
// CodeInvalidArgument (defence-in-depth; the gateway normally validates voice).
func TestService_Synthesize_EmptyVoice_InvalidArgument(t *testing.T) {
	svc := silentSvc().WithTTSClient(upstream.NewTTSClient(
		upstream.TTSConfig{BaseURL: "http://example.invalid", Path: "/api/v1/tts", Token: "t", AppID: "a", Cluster: "c"}, time.Second))
	_, err := svc.Synthesize(context.Background(), connect.NewRequest(&adapterv1.SynthesizeRequest{
		Model: "doubao-tts", Input: "hi", Voice: "", ResponseFormat: "mp3",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err=%v code=%v, want CodeInvalidArgument", err, connect.CodeOf(err))
	}
}
