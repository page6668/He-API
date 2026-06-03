// Story 5.2 T2.5 — body-peek helper for the AC3 model-scope check.
//
// The policy middleware needs request.model BEFORE the chat-completions
// handler consumes the body. PeekModel buffers the body, decodes just the
// `model` field, and replaces r.Body with a fresh reader over the same bytes
// so the downstream handler receives byte-identical input (the tee pattern;
// 5.2-UNIT-088 asserts pre/post byte equality). Body size is capped at 1 MiB
// per Story-3.3 BR-1.2.
package keypolicy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// maxPeekBody bounds the buffered body at 1 MiB (Story-3.3 BR-1.2). A larger
// body is truncated for the peek; the downstream handler re-enforces its own
// MaxBytesReader and rejects oversize input with its canonical envelope.
const maxPeekBody = 1 << 20

// PeekModel returns the `model` field from a JSON request body without
// consuming it for the downstream handler. On any read/parse error it returns
// ("", nil) — a missing/invalid model is NOT the policy middleware's error to
// surface; the downstream handler rejects it with 400 per BR-3.5. The only
// side effect is replacing r.Body with an equivalent reader.
func PeekModel(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxPeekBody))
	_ = r.Body.Close()
	// Restore the body for the downstream handler regardless of parse outcome.
	r.Body = io.NopCloser(bytes.NewReader(buf))
	if err != nil {
		return "", nil
	}

	var probe struct {
		Model string `json:"model"`
	}
	if jsonErr := json.Unmarshal(buf, &probe); jsonErr != nil {
		return "", nil
	}
	return probe.Model, nil
}
