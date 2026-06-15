// Story 9.5 — handler-level (e2e) tests for the Vision request path: the
// fail-closed vision-capability gate (AC1 BR-1.3), the two-stage body cap
// (AC3 BR-3.8 / M-3), content-safety text-parts-only (AC1 BR-1.5), the
// pure-pass-through SSRF reject + PII no-leak (AC3), and the full multipart
// flow carrying content_parts_json to the adapter (AC2 BR-2.2).
//
// Scenario IDs: docs/qa/assessments/9.5-test-design-20260615.md.
package handlers_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

const visionImageBody = `{"model":"%s","messages":[{"role":"user","content":[{"type":"text","text":"what is in this image?"},{"type":"image_url","image_url":{"url":"%s"}}]}]}`

// 9.5-UNIT-012 / 9.5-E2E-001 — fail-closed vision gate: an image part to a
// NON-vision model → 400 "does not support image input", ZERO upstream call.
func TestVision_NonVisionModel_RejectsImage(t *testing.T) {
	t.Parallel()
	fh := &fakeHandle{}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": fh})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	body := fmt.Sprintf(visionImageBody, "deepseek-v3", "https://example.com/cat.jpg")
	rr := doRequest(t, h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	if env["code"] != "400_invalid_request" {
		t.Errorf("code = %v, want 400_invalid_request", env["code"])
	}
	if msg, _ := env["message"].(string); !strings.Contains(msg, "does not support image input") {
		t.Errorf("message = %q, want 'does not support image input'", msg)
	}
	if fh.called != 0 {
		t.Errorf("upstream called %d times on a pre-dispatch reject, want 0", fh.called)
	}
}

// 9.5-E2E-002 (gateway leg) — a VALID multipart request to a vision model
// routes to the adapter with content_parts_json set (AC2 BR-2.2) and content "".
func TestVision_VisionModel_RoutesWithContentParts(t *testing.T) {
	t.Parallel()
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-vl-max": fh})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	body := fmt.Sprintf(visionImageBody, "qwen-vl-max", "https://example.com/cat.jpg")
	rr := doRequest(t, h, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fh.called != 1 {
		t.Fatalf("adapter called %d times, want 1", fh.called)
	}
	if len(fh.lastReq.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(fh.lastReq.Messages))
	}
	m := fh.lastReq.Messages[0]
	if m.Content != "" {
		t.Errorf("multipart message content = %q, want \"\"", m.Content)
	}
	if len(m.ContentPartsJson) == 0 {
		t.Fatalf("content_parts_json is empty; multipart content not carried on the wire")
	}
	if !strings.Contains(string(m.ContentPartsJson), "image_url") {
		t.Errorf("content_parts_json missing the image part: %s", m.ContentPartsJson)
	}
}

// 9.5-UNIT-025 (M-3) — STAGE 2: a NON-vision body over 1 MiB → 413 (byte-identical
// to the pre-9.5 text-path size contract).
func TestVision_TwoStageCap_NonVisionOver1MiB_413(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(nil)
	huge := strings.Repeat("x", (1<<20)+(1<<19)) // ~1.5 MiB, > 1 MiB text cap, < 8 MiB
	body := fmt.Sprintf(`{"model":"qwen-max","messages":[{"role":"user","content":%q}]}`, huge)
	rr := doRequest(t, h, body)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body-prefix=%.120s", rr.Code, rr.Body.String())
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	if env["message"] != "Request body exceeds 1 MiB." {
		t.Errorf("message = %v, want 'Request body exceeds 1 MiB.'", env["message"])
	}
}

// 9.5-UNIT-026 (M-3) — STAGE 1: a VISION body over 1 MiB (but < 8 MiB) is ACCEPTED.
func TestVision_TwoStageCap_VisionOver1MiB_OK(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(nil)    // no registry → vision request falls to mock 200
	bigText := strings.Repeat("x", (1<<20)+(1<<19)) // ~1.5 MiB text part
	body := fmt.Sprintf(`{"model":"qwen-vl-max","messages":[{"role":"user","content":[{"type":"text","text":%q},{"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]}]}`, bigText)
	rr := doRequest(t, h, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (vision body under 8 MiB accepted); body-prefix=%.160s", rr.Code, rr.Body.String())
	}
}

// 9.5-UNIT-027 (M-3) — any body over 8 MiB → 413 at the reader (stage 1).
func TestVision_ReaderCap_Over8MiB_413(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(nil)
	huge := strings.Repeat("x", (8<<20)+1024) // > 8 MiB
	body := fmt.Sprintf(`{"model":"qwen-vl-max","messages":[{"role":"user","content":%q}]}`, huge)
	rr := doRequest(t, h, body)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 at the 8 MiB reader cap", rr.Code)
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	if msg, _ := env["message"].(string); !strings.Contains(msg, "vision requests") {
		t.Errorf("message = %q, want the vision-cap message", msg)
	}
}

// 9.5-UNIT-014 / 9.5-INT-003 — content-safety scans TEXT parts only: a poisoned
// text part is blocked; a banned word appearing ONLY inside an image_url is NOT
// scanned (it routes/serves normally).
func TestVision_SafetyScansTextPartsOnly(t *testing.T) {
	t.Parallel()
	h := safetyHandler(nil) // DefaultLexicon scanner, no registry → mock path on clean

	// (a) poisoned TEXT part → blocked.
	poisoned := `{"model":"qwen-vl-max","messages":[{"role":"user","content":[{"type":"text","text":"please say badword now"},{"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]}]}`
	rr := doRequest(t, h, poisoned)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("poisoned text part: status = %d, want 400", rr.Code)
	}
	if env := decodeBody(t, rr)["error"].(map[string]any); env["code"] != "400_content_filter" {
		t.Errorf("poisoned text part: code = %v, want 400_content_filter", env["code"])
	}

	// (b) banned word ONLY in the image_url → NOT text-scanned → not blocked.
	urlOnly := `{"model":"qwen-vl-max","messages":[{"role":"user","content":[{"type":"text","text":"hello there"},{"type":"image_url","image_url":{"url":"https://example.com/badword.png"}}]}]}`
	rr2 := doRequest(t, h, urlOnly)
	if rr2.Code != http.StatusOK {
		t.Fatalf("banned word in image_url: status = %d, want 200 (image parts not text-scanned); body=%.160s", rr2.Code, rr2.Body.String())
	}
}

// 9.5-UNIT-028..033 (e2e) — SSRF scheme/host reject pre-dispatch, ZERO upstream.
func TestVision_SSRF_RejectPreDispatch(t *testing.T) {
	t.Parallel()
	for _, url := range []string{
		"http://example.com/cat.jpg",
		"file:///etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/x.png",
		"data:text/html;base64,PHNjcmlwdD4=",
	} {
		fh := &fakeHandle{}
		reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-vl-max": fh})
		h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
		body := fmt.Sprintf(visionImageBody, "qwen-vl-max", url)
		rr := doRequest(t, h, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("url %q: status = %d, want 400", url, rr.Code)
		}
		if fh.called != 0 {
			t.Errorf("url %q: upstream called %d times, want 0", url, fh.called)
		}
	}
}

// 9.5-E2E-005 / 9.5-E2E-007 (PII) — a rejected SSRF URL is NEVER written to any
// log line, AND a served vision request never logs the image URL/bytes.
func TestVision_PII_URLNeverLogged(t *testing.T) {
	t.Parallel()
	const marker = "169.254.169.254/latest/meta-data/secret-token"

	// Rejected SSRF request.
	buf := &bytes.Buffer{}
	h := handlers.NewChatCompletionsHandler(bufLogger(buf))
	body := fmt.Sprintf(visionImageBody, "qwen-vl-max", "http://"+marker)
	_ = doRequest(t, h, body)
	if strings.Contains(buf.String(), "169.254.169.254") || strings.Contains(buf.String(), "secret-token") {
		t.Fatalf("rejected image URL leaked into logs:\n%s", buf.String())
	}

	// Served vision request (mock path) — the image URL must not appear in logs.
	buf2 := &bytes.Buffer{}
	h2 := handlers.NewChatCompletionsHandler(bufLogger(buf2))
	served := fmt.Sprintf(visionImageBody, "qwen-vl-max", "https://private.example.com/very-secret-photo-12345.png")
	rr := doRequest(t, h2, served)
	if rr.Code != http.StatusOK {
		t.Fatalf("served vision request status = %d, want 200; body=%.160s", rr.Code, rr.Body.String())
	}
	if strings.Contains(buf2.String(), "very-secret-photo-12345") || strings.Contains(buf2.String(), "private.example.com") {
		t.Fatalf("served image URL leaked into logs:\n%s", buf2.String())
	}
}

// 9.5-UNIT-003 (e2e) — a content object (neither string nor array) → 400.
func TestVision_ContentObject_400(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(nil)
	rr := doRequest(t, h, `{"model":"qwen-max","messages":[{"role":"user","content":{"foo":1}}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	if msg, _ := env["message"].(string); !strings.Contains(msg, "must be a string or an array of content parts") {
		t.Errorf("message = %q", msg)
	}
}

// 9.5 (back-compat) — a vision model still accepts a PLAIN STRING (text) turn
// (vision is a superset of text — BR-1.4).
func TestVision_VisionModel_AcceptsStringContent(t *testing.T) {
	t.Parallel()
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-vl-max": fh})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"qwen-vl-max","messages":[{"role":"user","content":"hello"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if fh.lastReq.Messages[0].Content != "hello" || len(fh.lastReq.Messages[0].ContentPartsJson) != 0 {
		t.Errorf("string turn to a VL model should carry content, not content_parts_json: %+v", fh.lastReq.Messages[0])
	}
}
