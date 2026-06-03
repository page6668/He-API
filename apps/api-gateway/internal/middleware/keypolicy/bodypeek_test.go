// Story 5.2 — UNIT-086..088 (body-peek tee pattern).
package keypolicy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPeekModel(t *testing.T) {
	t.Run("reads model and preserves body bytes (tee)", func(t *testing.T) {
		body := `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`
		r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		model, err := PeekModel(r)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if model != "qwen-max" {
			t.Fatalf("model=%q want qwen-max", model)
		}
		// 5.2-UNIT-088 — downstream handler must receive identical bytes.
		got, _ := io.ReadAll(r.Body)
		if string(got) != body {
			t.Fatalf("body bytes changed: got %q want %q", got, body)
		}
	})

	t.Run("missing model → empty string, body preserved", func(t *testing.T) {
		body := `{"messages":[]}`
		r, _ := http.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		model, err := PeekModel(r)
		if err != nil || model != "" {
			t.Fatalf("model=%q err=%v want ('' nil)", model, err)
		}
		got, _ := io.ReadAll(r.Body)
		if string(got) != body {
			t.Fatalf("body changed")
		}
	})

	t.Run("invalid JSON → empty string, no error", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "/x", strings.NewReader(`not json`))
		model, err := PeekModel(r)
		if err != nil || model != "" {
			t.Fatalf("model=%q err=%v", model, err)
		}
	})

	t.Run("nil body → empty string", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/x", nil)
		model, err := PeekModel(r)
		if err != nil || model != "" {
			t.Fatalf("model=%q err=%v", model, err)
		}
	})
}
