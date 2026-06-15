// Story 9.6 (M-1 / 9.6-INT-005) — content-safety on the ASR `prompt` text field.
//
// The ASR handler scans the OPTIONAL `prompt` form field through the Story-8.2
// input scanner (same lexicon as chat) BEFORE dispatch — a flagged prompt
// returns the canonical 400_content_filter and never reaches the adapter. The
// audio bytes themselves are opaque binary and are NEVER scanned (8.2 scans text
// INPUT only; the transcript is model OUTPUT and is not scanned either).
//
// QA review round 1, gate item M-1: the safety branch (audio_transcriptions.go
// ~277-286) was present in code but exercised by NO test — the newASRHandler
// helper never wired WithAudioSafetyScanner, so h.safetyScanner was nil and the
// branch was dead under test. In a security_sensitive story this is a do-not-
// regress 8.2/8.4/8.5 gap; this file closes it.
package handlers

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// newASRSafetyHandler builds an ASR handler wired with the production
// DefaultLexicon input scanner (flags the known en/abuse/high term "badword").
func newASRSafetyHandler(tr *fakeTranscriber) *AudioTranscriptionsHandler {
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		adapterclient.DoubaoASRModelID: tr,
	})
	return NewAudioTranscriptionsHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithAudioAdapterRegistry(reg),
		WithAudioSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)),
	)
}

// 9.6-INT-005 (a) — a flagged `prompt` → canonical 400_content_filter, and the
// reject PRECEDES dispatch (zero upstream call).
func TestASR_Safety_FlaggedPrompt_Blocked(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()}
	h := newASRSafetyHandler(tr)
	body, ct := buildMultipart(
		map[string]string{"model": "doubao-asr", "prompt": "please transcribe badword now"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()}},
	)
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "400_content_filter") {
		t.Fatalf("want 400_content_filter envelope; body=%s", rr.Body.String())
	}
	if tr.called {
		t.Fatal("upstream must NOT be called on a content-safety block (reject-before-dispatch)")
	}
}

// 9.6-INT-005 (b) — the AUDIO bytes are NEVER scanned: the flagging term lives
// only inside the audio payload (no `prompt`), so the request falls through to
// 200 and reaches the adapter — audio is opaque binary, outside the text scope.
func TestASR_Safety_AudioBytesNotScanned(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()}
	h := newASRSafetyHandler(tr)
	// Valid ID3 header (so the mime sniff resolves to audio/mpeg) followed by the
	// flagging term inside the payload. The scanner must ignore the audio bytes.
	audio := append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), []byte("badword")...)
	audio = append(audio, bytes.Repeat([]byte{0x00}, 64)...)
	body, ct := buildMultipart(
		map[string]string{"model": "doubao-asr"}, // NO prompt
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: audio}},
	)
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (audio bytes must not be scanned); body=%s", rr.Code, rr.Body.String())
	}
	if !tr.called {
		t.Fatal("adapter must be called: audio is not subject to the text scanner")
	}
}

// 9.6-INT-005 (c) — a clean `prompt` falls through the scanner to 200.
func TestASR_Safety_CleanPrompt_FallsThrough(t *testing.T) {
	tr := &fakeTranscriber{resp: okResp()}
	h := newASRSafetyHandler(tr)
	body, ct := buildMultipart(
		map[string]string{"model": "doubao-asr", "prompt": "a clean helpful transcription hint"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: mp3Bytes()}},
	)
	rr := doASR(h, body, ct)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (clean prompt falls through); body=%s", rr.Code, rr.Body.String())
	}
	if !tr.called {
		t.Fatal("adapter must be called on a clean prompt")
	}
}
