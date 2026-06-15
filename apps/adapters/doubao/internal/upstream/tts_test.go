// Story 9.7 (T3) — Volcano TTS upstream client tests. The non-identity translate
// (OpenAI-speech → Volcano) + the Volcano base64-in-JSON parse are exercised
// against an httptest fake-upstream implementing the documented Volcano TTS
// `query` contract. Covers UNIT-013/014/015/016/017, INT-027 (output bound),
// BLIND-ERROR-001/002.
package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testTTSConfig(baseURL string) TTSConfig {
	return TTSConfig{BaseURL: baseURL, Path: "/api/v1/tts", Token: "tok-123", AppID: "app-1", Cluster: "volc_tts", VoiceType: "zh_female_default"}
}

func ttsServer(t *testing.T, audio []byte, captured *volcTTSRequest, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		b, _ := io.ReadAll(r.Body)
		if captured != nil {
			_ = json.Unmarshal(b, captured)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"code": 3000, "message": "success", "data": base64.StdEncoding.EncodeToString(audio)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// 9.7-UNIT-013 + UNIT-014 — request build (voice_type/encoding/speed_ratio,
// operation:"query", Bearer; auth, app block) + response decode (base64 data →
// audio, mime set per response_format).
func TestTTS_Synthesize_RoundTrip(t *testing.T) {
	audio := []byte{0xff, 0xfb, 0x90, 0x00, 0x13, 0x37}
	var gotBody volcTTSRequest
	var gotAuth string
	srv := ttsServer(t, audio, &gotBody, &gotAuth)
	defer srv.Close()

	c := NewTTSClient(testTTSConfig(srv.URL), 5*time.Second)
	res, err := c.Synthesize(context.Background(), TTSRequest{
		Model: "doubao-tts", Input: "你好世界", Voice: "zh_female_1", ResponseFormat: "mp3", Speed: 1.25, HeRequestID: "he-1",
	})
	if err != nil {
		t.Fatalf("Synthesize err=%v", err)
	}
	if string(res.Audio) != string(audio) {
		t.Fatalf("audio not base64-decoded verbatim: %x", res.Audio)
	}
	if res.MimeType != "audio/mpeg" {
		t.Fatalf("mime=%q, want audio/mpeg", res.MimeType)
	}
	// UNIT-013 — request build.
	if gotBody.App.AppID != "app-1" || gotBody.App.Token != "tok-123" || gotBody.App.Cluster != "volc_tts" {
		t.Fatalf("app block wrong: %+v", gotBody.App)
	}
	if gotBody.Audio.VoiceType != "zh_female_1" || gotBody.Audio.Encoding != "mp3" || gotBody.Audio.SpeedRatio != 1.25 {
		t.Fatalf("audio block wrong: %+v", gotBody.Audio)
	}
	if gotBody.Request.Text != "你好世界" || gotBody.Request.Operation != "query" || gotBody.Request.ReqID != "he-1" {
		t.Fatalf("request meta wrong: %+v", gotBody.Request)
	}
	// Auth header uses the semicolon form with NO space (pinned).
	if !strings.HasPrefix(gotAuth, "Bearer;") || strings.HasPrefix(gotAuth, "Bearer; ") {
		t.Fatalf("auth header=%q, want 'Bearer;<token>' (semicolon, no space)", gotAuth)
	}
}

// 9.7-UNIT-013 — encoding + mime map for the 3 shipped formats (Q-TTS-RESPFORMAT).
func TestTTS_EncodingFor(t *testing.T) {
	cases := []struct{ fmtIn, enc, mime string }{
		{"", "mp3", "audio/mpeg"},
		{"mp3", "mp3", "audio/mpeg"},
		{"wav", "wav", "audio/wav"},
		{"opus", "ogg_opus", "audio/ogg"},
	}
	for _, c := range cases {
		enc, mime, err := ttsEncodingFor(c.fmtIn)
		if err != nil || enc != c.enc || mime != c.mime {
			t.Fatalf("ttsEncodingFor(%q) = (%q,%q,%v), want (%q,%q,nil)", c.fmtIn, enc, mime, err, c.enc, c.mime)
		}
	}
	if _, _, err := ttsEncodingFor("flac"); err == nil {
		t.Fatal("flac (deferred) must error")
	}
}

// 9.7-UNIT-015 — Volcano HTTP 4xx/5xx → UpstreamError with the right kind.
func TestTTS_Synthesize_HTTPError(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{http.StatusUnauthorized, ErrorKindAuthRevoked},
		{http.StatusTooManyRequests, ErrorKindRateLimitThrottle},
		{http.StatusBadRequest, ErrorKindUpstream4xx},
		{http.StatusServiceUnavailable, ErrorKindUpstream5xx},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"code":5000,"message":"err"}`))
		}))
		client := NewTTSClient(testTTSConfig(srv.URL), 5*time.Second)
		_, err := client.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3"})
		srv.Close()
		var ue *UpstreamError
		if !asUpstream(err, &ue) || ue.Kind != c.want {
			t.Fatalf("status %d: kind=%v, want %v", c.status, errKind(err), c.want)
		}
	}
}

// 9.7-UNIT-016 — Configured() all-or-nothing on BASE_URL/TOKEN/APP_ID/CLUSTER.
func TestTTS_Config_Configured(t *testing.T) {
	full := TTSConfig{BaseURL: "u", Token: "t", AppID: "a", Cluster: "c"}
	if !full.Configured() {
		t.Fatal("full config must be Configured")
	}
	for _, miss := range []TTSConfig{
		{Token: "t", AppID: "a", Cluster: "c"},
		{BaseURL: "u", AppID: "a", Cluster: "c"},
		{BaseURL: "u", Token: "t", Cluster: "c"},
		{BaseURL: "u", Token: "t", AppID: "a"},
	} {
		if miss.Configured() {
			t.Fatalf("partial config must NOT be Configured: %+v", miss)
		}
	}
}

// 9.7-UNIT-016 — fail-fast when TTS config is not set (no upstream call).
func TestTTS_Synthesize_NotConfigured(t *testing.T) {
	c := NewTTSClient(TTSConfig{}, time.Second)
	_, err := c.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "v"})
	if err != ErrTTSNotConfigured {
		t.Fatalf("err=%v, want ErrTTSNotConfigured", err)
	}
}

// 9.7-UNIT-017 — empty voice with no default → ErrEmptyVoice (the handler maps it
// to CodeInvalidArgument).
func TestTTS_Synthesize_EmptyVoice(t *testing.T) {
	cfg := testTTSConfig("http://example.invalid")
	cfg.VoiceType = "" // no default
	c := NewTTSClient(cfg, time.Second)
	_, err := c.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "", ResponseFormat: "mp3"})
	if err != ErrEmptyVoice {
		t.Fatalf("err=%v, want ErrEmptyVoice", err)
	}
}

// 9.7-BLIND-ERROR-001 — malformed JSON / missing data → upstream_error, no audio.
func TestTTS_Synthesize_MalformedAndMissingData(t *testing.T) {
	// missing data field
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":3000,"message":"ok"}`))
	}))
	c := NewTTSClient(testTTSConfig(srv.URL), 5*time.Second)
	if _, err := c.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3"}); err == nil {
		t.Fatal("missing data must error")
	}
	srv.Close()

	// malformed JSON body
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv2.Close()
	c2 := NewTTSClient(testTTSConfig(srv2.URL), 5*time.Second)
	if _, err := c2.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3"}); err == nil {
		t.Fatal("malformed JSON must error")
	}
}

// 9.7-BLIND-ERROR-002 — invalid base64 in data → upstream_error (malformed).
func TestTTS_Synthesize_BadBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":3000,"data":"!!!not-base64!!!"}`))
	}))
	defer srv.Close()
	c := NewTTSClient(testTTSConfig(srv.URL), 5*time.Second)
	_, err := c.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3"})
	var ue *UpstreamError
	if !asUpstream(err, &ue) || ue.Kind != ErrorKindMalformedChunk {
		t.Fatalf("kind=%v, want malformed_chunk", errKind(err))
	}
}

// 9.7-INT-027 (adapter half) — synthesized audio over the 20 MiB cap →
// ErrorKindOutputTooLarge (zero billing; the gateway returns 502/413).
func TestTTS_Synthesize_OutputTooLarge(t *testing.T) {
	big := make([]byte, MaxSynthesizedAudioBytes+1)
	srv := ttsServer(t, big, nil, nil)
	defer srv.Close()
	c := NewTTSClient(testTTSConfig(srv.URL), 10*time.Second)
	_, err := c.Synthesize(context.Background(), TTSRequest{Model: "doubao-tts", Input: "hi", Voice: "v", ResponseFormat: "mp3"})
	var ue *UpstreamError
	if !asUpstream(err, &ue) || ue.Kind != ErrorKindOutputTooLarge {
		t.Fatalf("kind=%v, want output_too_large", errKind(err))
	}
}
