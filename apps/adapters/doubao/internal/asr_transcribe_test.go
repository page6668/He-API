// Story 9.6 (T3) — Doubao Service.Transcribe integration (INT-008) over a
// fake Volcano upstream + the nil-client fail-fast path.
package internal

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

func silentSvc() *Service {
	return NewService(nil, upstream.NewFromOS(), slog.New(slog.NewTextHandler(discardWriter{}, nil)), []string{"doubao-pro"})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// 9.6-INT-008 — Transcribe over a fake Volcano upstream → TranscribeResponse.
func TestService_Transcribe_FakeUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":1000,"result":[{"text":"你好世界"}],"audio_info":{"duration":3200}}`))
	}))
	defer srv.Close()

	svc := silentSvc().WithASRClient(upstream.NewASRClient(
		upstream.ASRConfig{BaseURL: srv.URL, Path: "/api/v1/asr", Token: "t", AppID: "a", Cluster: "c"},
		5*time.Second,
	))

	resp, err := svc.Transcribe(context.Background(), connect.NewRequest(&adapterv1.TranscribeRequest{
		Model: "doubao-asr", Audio: []byte{0xff, 0xfb, 0x1, 0x2}, MimeType: "audio/mpeg", Language: "zh", HeRequestId: "he-9",
	}))
	if err != nil {
		t.Fatalf("Transcribe err=%v", err)
	}
	if resp.Msg.GetText() != "你好世界" {
		t.Fatalf("text=%q", resp.Msg.GetText())
	}
	if resp.Msg.GetDurationSeconds() != 3.2 {
		t.Fatalf("duration=%v, want 3.2", resp.Msg.GetDurationSeconds())
	}
}

// 9.6 — a chat-only deployment (no ASR client) fail-fasts with CodeUnavailable.
func TestService_Transcribe_NotConfigured(t *testing.T) {
	svc := silentSvc() // no WithASRClient
	_, err := svc.Transcribe(context.Background(), connect.NewRequest(&adapterv1.TranscribeRequest{
		Model: "doubao-asr", Audio: []byte{1}, MimeType: "audio/wav",
	}))
	if err == nil {
		t.Fatal("expected CodeUnavailable, got nil")
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code=%v, want CodeUnavailable", connect.CodeOf(err))
	}
}

// 9.6-BLIND-ERROR-002 — Volcano 503 → CodeUnavailable (mapped envelope).
func TestService_Transcribe_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	svc := silentSvc().WithASRClient(upstream.NewASRClient(
		upstream.ASRConfig{BaseURL: srv.URL, Path: "/api/v1/asr", Token: "t", AppID: "a", Cluster: "c"}, 5*time.Second))
	_, err := svc.Transcribe(context.Background(), connect.NewRequest(&adapterv1.TranscribeRequest{
		Model: "doubao-asr", Audio: []byte{1}, MimeType: "audio/wav",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err=%v code=%v, want CodeUnavailable", err, connect.CodeOf(err))
	}
}
