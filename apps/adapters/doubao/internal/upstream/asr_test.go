// Story 9.6 (T3) — Volcano ASR upstream client tests (UNIT-011..015). The
// non-identity translation Whisper→Volcano + the Volcano→Whisper parse are
// exercised against an httptest fake-upstream implementing the documented
// OpenSpeech contract.
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

func testASRConfig(baseURL string) ASRConfig {
	return ASRConfig{BaseURL: baseURL, Path: "/api/v1/asr", Token: "tok-123", AppID: "app-1", Cluster: "volc_cluster"}
}

// 9.6-UNIT-011 + UNIT-012 — request build (app/audio/format/base64) +
// response parse (text + duration ms→s + segments).
func TestASR_Recognize_RoundTrip(t *testing.T) {
	var gotBody volcASRRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1000,"message":"success","result":[{"text":"你好"},{"text":"世界"}],"audio_info":{"duration":3200},"utterances":[{"text":"你好世界","start_time":0}]}`))
	}))
	defer srv.Close()

	c := NewASRClient(testASRConfig(srv.URL), 5*time.Second)
	audio := []byte{0xff, 0xfb, 0x01, 0x02}
	res, err := c.Recognize(context.Background(), ASRRequest{
		Model: "doubao-asr", Audio: audio, MimeType: "audio/mpeg", Language: "zh", HeRequestID: "he-1",
	})
	if err != nil {
		t.Fatalf("Recognize err=%v", err)
	}
	// UNIT-012 — text concatenated, duration ms→s, segments captured.
	if res.Text != "你好世界" {
		t.Fatalf("text=%q", res.Text)
	}
	if res.DurationSeconds != 3.2 {
		t.Fatalf("duration=%v, want 3.2 (3200ms)", res.DurationSeconds)
	}
	if len(res.SegmentsJSON) == 0 {
		t.Fatal("segments_json should carry the utterances passthrough")
	}
	// UNIT-011 — request build: app credentials, base64 audio, mp3 format, auth.
	if gotBody.App.AppID != "app-1" || gotBody.App.Token != "tok-123" || gotBody.App.Cluster != "volc_cluster" {
		t.Fatalf("app block wrong: %+v", gotBody.App)
	}
	if gotBody.Audio.Format != "mp3" {
		t.Fatalf("format=%q, want mp3", gotBody.Audio.Format)
	}
	if dec, _ := base64.StdEncoding.DecodeString(gotBody.Audio.Data); string(dec) != string(audio) {
		t.Fatal("audio not base64-encoded verbatim")
	}
	if gotBody.Request.Language != "zh" || gotBody.Request.ReqID != "he-1" {
		t.Fatalf("request meta wrong: %+v", gotBody.Request)
	}
	if !strings.HasPrefix(gotAuth, "Bearer; ") {
		t.Fatalf("auth header=%q, want 'Bearer; <token>'", gotAuth)
	}
}

// 9.6-UNIT-013 — Volcano HTTP 4xx/5xx → UpstreamError with the right kind.
func TestASR_Recognize_HTTPError(t *testing.T) {
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
		client := NewASRClient(testASRConfig(srv.URL), 5*time.Second)
		_, err := client.Recognize(context.Background(), ASRRequest{Model: "doubao-asr", Audio: []byte{1}, MimeType: "audio/wav"})
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected error", c.status)
		}
		var ue *UpstreamError
		if !asUpstream(err, &ue) || ue.Kind != c.want {
			t.Fatalf("status %d: kind=%v, want %v", c.status, errKind(err), c.want)
		}
	}
}

// 9.6-UNIT-014 — duration is vendor-authoritative; a MISSING duration → error
// (fail-closed, no guess — never a zero-cost bill).
func TestASR_Recognize_MissingDuration_FailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// recognised text but NO audio_info (duration).
		_, _ = w.Write([]byte(`{"code":1000,"result":[{"text":"hi"}]}`))
	}))
	defer srv.Close()
	c := NewASRClient(testASRConfig(srv.URL), 5*time.Second)
	_, err := c.Recognize(context.Background(), ASRRequest{Model: "doubao-asr", Audio: []byte{1}, MimeType: "audio/wav"})
	if err == nil {
		t.Fatal("missing duration must fail-closed, got nil error")
	}
}

// 9.6-UNIT-013 — a non-success code in a 2xx body → error (no partial result).
func TestASR_Recognize_NonSuccessCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":4001,"message":"bad audio","result":[]}`))
	}))
	defer srv.Close()
	c := NewASRClient(testASRConfig(srv.URL), 5*time.Second)
	if _, err := c.Recognize(context.Background(), ASRRequest{Model: "doubao-asr", Audio: []byte{1}, MimeType: "audio/wav"}); err == nil {
		t.Fatal("non-success code must error")
	}
}

// 9.6-UNIT-011 — fail-fast when ASR config is not set (no upstream call).
func TestASR_Recognize_NotConfigured(t *testing.T) {
	c := NewASRClient(ASRConfig{}, time.Second) // empty config
	_, err := c.Recognize(context.Background(), ASRRequest{Model: "doubao-asr", Audio: []byte{1}})
	if err != ErrASRNotConfigured {
		t.Fatalf("err=%v, want ErrASRNotConfigured", err)
	}
}

// ---- tiny error helpers (avoid importing errors in two spots) ----------

func asUpstream(err error, target **UpstreamError) bool {
	for e := err; e != nil; {
		if ue, ok := e.(*UpstreamError); ok {
			*target = ue
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func errKind(err error) ErrorKind {
	var ue *UpstreamError
	if asUpstream(err, &ue) {
		return ue.Kind
	}
	return ""
}
