// Story 9.6 (L-3 / AC4 resource hygiene) — the temp-file / disconnect / heap-
// spill scenarios designed in the test-design but previously unasserted.
//
// QA review round 1, gate item L-3: 9.6-INT-021 (temp RemoveAll on success AND
// every error path), 9.6-BLIND-FLOW-001 (client disconnect mid-upload → temp
// cleaned, no upstream) and 9.6-BLIND-RESOURCE-003 (large part spills to a
// BOUNDED temp file, not the heap) were correct in code but had no test. These
// pin them.
//
// Mechanism: each test points $TMPDIR at a fresh dir, then a probe transcriber
// inspects that dir DURING dispatch (the deferred cleanupMultipart has not run
// yet) to observe the spilled `multipart-*` temp file, and the test re-inspects
// AFTER ServeHTTP returns to prove the deferred RemoveAll fired.
package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// spillProbeTranscriber inspects $TMPDIR mid-dispatch (before the handler's
// deferred cleanup runs) and records whether a spilled multipart temp file
// existed at that moment.
type spillProbeTranscriber struct {
	resp   *adapterv1.TranscribeResponse
	tmpDir string
	sawTmp bool
	called bool
}

func (s *spillProbeTranscriber) Chat(context.Context, *adapterv1.ChatRequest, http.Header) (adapterclient.Stream, error) {
	return nil, errors.New("chat not used in ASR resource tests")
}

func (s *spillProbeTranscriber) Transcribe(context.Context, *adapterv1.TranscribeRequest, http.Header) (*adapterv1.TranscribeResponse, error) {
	s.called = true
	s.sawTmp = countMultipartTemp(s.tmpDir) > 0
	return s.resp, nil
}

// countMultipartTemp counts `multipart-*` spill files in dir (best-effort).
func countMultipartTemp(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "multipart-") {
			n++
		}
	}
	return n
}

// bigAudio returns a valid-sniffing (ID3) audio buffer of n bytes — large
// enough (> audioMultipartMemBytes = 1 MiB) to force ParseMultipartForm to spill
// to a temp file, but well under MaxAudioBodyBytes (25 MiB).
func bigAudio(n int) []byte {
	b := append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0x00}, n)...)
	return b
}

// 9.6-BLIND-RESOURCE-003 + 9.6-INT-021 (success path) — a multi-MiB part spills
// to a BOUNDED temp file during dispatch, then the deferred RemoveAll cleans it.
func TestASR_LargePart_SpillsToTemp_AndCleansUp(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	probe := &spillProbeTranscriber{resp: okResp(), tmpDir: tmpDir}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		adapterclient.DoubaoASRModelID: probe,
	})
	h := NewAudioTranscriptionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithAudioAdapterRegistry(reg))

	body, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "big.mp3", ct: "audio/mpeg", data: bigAudio(3 << 20)}}) // 3 MiB
	rr := doASR(h, body, ct)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !probe.called {
		t.Fatal("adapter must be called on a valid large upload")
	}
	// BLIND-RESOURCE-003 — the bulk audio was held in a bounded temp file (off the
	// heap), observed mid-dispatch before cleanup.
	if !probe.sawTmp {
		t.Fatal("large part should have spilled to a multipart temp file (bounded memory)")
	}
	// INT-021 — the deferred RemoveAll cleaned the spill on the success path.
	if n := countMultipartTemp(tmpDir); n != 0 {
		t.Fatalf("temp spill not cleaned after success: %d leftover multipart-* files", n)
	}
}

// 9.6-INT-021 (error path) — a part that spills then hits a post-parse reject
// (capability gate) is STILL cleaned up, and the upstream is never called.
func TestASR_TempCleanup_OnErrorPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	probe := &spillProbeTranscriber{resp: okResp(), tmpDir: tmpDir}
	// Route a NON-transcription model so the capability gate rejects AFTER parse
	// (the spill has already happened) but BEFORE dispatch.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": probe})
	h := NewAudioTranscriptionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithAudioAdapterRegistry(reg))

	body, ct := buildMultipart(map[string]string{"model": "qwen-max"},
		[]filePart{{field: "file", filename: "big.mp3", ct: "audio/mpeg", data: bigAudio(3 << 20)}})
	rr := doASR(h, body, ct)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (capability reject); body=%s", rr.Code, rr.Body.String())
	}
	if probe.called {
		t.Fatal("upstream must NOT be called on a capability reject")
	}
	if n := countMultipartTemp(tmpDir); n != 0 {
		t.Fatalf("temp spill not cleaned after an error path: %d leftover multipart-* files", n)
	}
}

// 9.6-BLIND-FLOW-001 — client disconnects mid-upload: the bounded read aborts,
// ParseMultipartForm fails → 400, the upstream is never called, and any spilled
// temp file is cleaned.
func TestASR_ClientDisconnect_MidUpload(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	probe := &spillProbeTranscriber{resp: okResp(), tmpDir: tmpDir}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		adapterclient.DoubaoASRModelID: probe,
	})
	h := NewAudioTranscriptionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithAudioAdapterRegistry(reg))

	// Build a valid multipart body, then truncate it and append a non-EOF read
	// error to model a mid-stream client disconnect.
	full, ct := buildMultipart(map[string]string{"model": "doubao-asr"},
		[]filePart{{field: "file", filename: "a.mp3", ct: "audio/mpeg", data: bigAudio(2 << 20)}})
	truncated := full.Bytes()[:full.Len()/2]
	body := &disconnectReader{r: bytes.NewReader(truncated)}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(asrAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 on a mid-upload disconnect; body=%s", rr.Code, rr.Body.String())
	}
	if probe.called {
		t.Fatal("upstream must NOT be called when the upload aborts mid-stream")
	}
	if n := countMultipartTemp(tmpDir); n != 0 {
		t.Fatalf("temp spill not cleaned after a disconnect: %d leftover multipart-* files", n)
	}
}

// disconnectReader yields the wrapped bytes then a non-EOF error, modelling a
// client connection dropped mid-upload.
type disconnectReader struct {
	r   *bytes.Reader
	hit bool
}

func (d *disconnectReader) Read(p []byte) (int, error) {
	if d.r.Len() > 0 {
		return d.r.Read(p)
	}
	if !d.hit {
		d.hit = true
		return 0, io.ErrUnexpectedEOF
	}
	return 0, io.ErrUnexpectedEOF
}
