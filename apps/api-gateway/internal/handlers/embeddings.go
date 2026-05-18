// Story 3.5 — POST /v1/embeddings handler (deterministic mock vector).
//
// Returns an OpenAI-compatible Embedding response so the OpenAI Python SDK
// can call client.embeddings.create(...) end-to-end before Epic 4 wires the
// real adapter-backed embedding service.
//
// Architect Round 1 rulings:
//   - OQ2: declares sibling constant MaxEmbeddingBodyBytes (1 MiB) — kept
//     distinct from chat_completions.maxChatBodyBytes so per-endpoint caps
//     can diverge in Story 9.x (multimodal) without a rename refactor.
//   - OQ3: mock vector dimension is 128 — intentionally non-canonical to
//     signal "this is a mock" at wire inspection. Real Epic-4 adapters
//     emit their upstream-native dimension (typically 1024 or 1536).
//   - OQ4: validateEmbeddingRequest returns the 5-tuple
//     (status, code, message, param, valid) — adds *string param vs the
//     Story-3.3 4-tuple so the §5.1.2 error envelope can carry per-field
//     params (model / input). Story 3.6 may refactor to a struct return.
//
// BR-2.4: GenerateMockEmbedding is exported so Epic 4 contract tests can
// reuse it as the canonical "what mock output looks like" reference.
// The function is pure (no I/O) and byte-stable on the same architecture
// (±1 ULP across linux/amd64, linux/arm64, darwin/arm64 per IEEE-754).
//
// BR-2.7 PII discipline: req.Input content is NEVER logged. Only size
// signals (input_count, total_input_bytes) appear in the structured log.
package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// MaxEmbeddingBodyBytes is the 1 MiB body cap (BR-2.2 / Architect OQ2).
// Same value as the Story-3.3 maxChatBodyBytes today; kept distinct so
// per-endpoint caps can diverge in Story 9.x (multimodal vision payloads
// extend to ~8 MiB) without renaming the chat-specific constant.
const MaxEmbeddingBodyBytes int64 = 1 << 20

// maxEmbeddingInputBytes is the 256 KiB sub-cap on the summed length of
// all input strings (BR-2.3 step 5). Sits below the 1 MiB body cap so the
// mock generator's CPU + memory pressure stays bounded under large batch
// inputs; Epic 4 real adapters may relax this.
const maxEmbeddingInputBytes = 256 * 1024 // 262144 bytes

// defaultEmbeddingDim is the BR-2.4 / OQ3 mock dimension. 128 is
// intentionally non-canonical (real OpenAI text-embedding-3-small emits
// 1536) so the response shape signals "mock" at wire inspection.
const defaultEmbeddingDim = 128

// ----- Request / response types (OpenAI shape) --------------------------

// EmbeddingRequest is the inbound JSON body. Input is json.RawMessage to
// support BR-2.8 dual-shape parsing (string OR array). Unknown OpenAI
// fields (encoding_format, dimensions, user) are tolerated as RawMessage
// for forward-compat; Epic 4 real adapters may honour them.
type EmbeddingRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat json.RawMessage `json:"encoding_format,omitempty"`
	Dimensions     json.RawMessage `json:"dimensions,omitempty"`
	User           json.RawMessage `json:"user,omitempty"`
}

// EmbeddingData is one entry in the response data array. Field order per
// BR-2.9: object → index → embedding.
type EmbeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// EmbeddingUsage carries the mock token counts. No completion_tokens
// field — embeddings have no completion dimension; OpenAI's actual
// embedding response omits it too.
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// EmbeddingResponse is the top-level response. Field order per BR-2.9:
// object → data → model → usage.
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  EmbeddingUsage  `json:"usage"`
}

// ----- Pure functions (testable independently) --------------------------

// parseEmbeddingInputs decodes the dual-shape `input` field (BR-2.8).
// OpenAI's wire contract permits `input` as either a single string OR an
// array of strings; the parser surfaces SHAPE only — semantic non-empty
// is the validator's job (UNIT-008 / UNIT-009 separation).
//
// Rejects JSON null explicitly because json.Unmarshal of null into a
// non-pointer string silently succeeds with the zero value, which would
// conflate null with the empty-string "" case.
func parseEmbeddingInputs(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("input must be a string or array of strings")
	}

	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	} else {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return nil, err
		}
		// fall through to array parse
	}

	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

// validateEmbeddingRequest applies BR-2.3 ordered validation. Returns
// (0, "", "", nil, true) on success and (status, code, message, &param,
// false) on the first failing rule. Pure function — no I/O.
//
// Per Architect Round 1 OQ4, the 5-tuple signature (vs Story 3.3's
// 4-tuple) carries the param field that the §5.1.2 envelope requires
// (model / input). Story 3.6 may unify all validators into a struct
// return; until then, the 5-tuple preserves the pure-function tuple
// idiom Story 3.3 established.
func validateEmbeddingRequest(req *EmbeddingRequest) (int, string, string, *string, bool) {
	modelParam := "model"
	inputParam := "input"

	// BR-2.3 step 3 — model non-empty + ≤ modelMaxLen (100 chars,
	// matching he_api.models.id VARCHAR(100) per data-models.md §4.1)
	if req.Model == "" || len(req.Model) > modelMaxLen {
		return http.StatusBadRequest,
			"400_invalid_request",
			"Field 'model' is required and must be a non-empty string.",
			&modelParam,
			false
	}

	// BR-2.3 step 4 — input must parse and be non-empty (array OR string),
	// with every array element non-empty.
	inputs, err := parseEmbeddingInputs(req.Input)
	if err != nil || len(inputs) == 0 {
		return http.StatusBadRequest,
			"400_invalid_request",
			"Field 'input' is required and must be a non-empty string or non-empty array of strings.",
			&inputParam,
			false
	}
	totalBytes := 0
	for _, s := range inputs {
		if s == "" {
			return http.StatusBadRequest,
				"400_invalid_request",
				"Field 'input' is required and must be a non-empty string or non-empty array of strings.",
				&inputParam,
				false
		}
		totalBytes += len(s)
	}

	// BR-2.3 step 5 — 256 KiB sub-cap on the summed input bytes (inclusive
	// ceiling: 262144 valid, 262145 invalid).
	if totalBytes > maxEmbeddingInputBytes {
		return http.StatusBadRequest,
			"400_invalid_request",
			"Field 'input' total bytes exceed 256 KiB.",
			&inputParam,
			false
	}

	return 0, "", "", nil, true
}

// GenerateMockEmbedding produces a deterministic 128-dim (or `dim`-dim)
// L2-normalized embedding vector for `input` (BR-2.4). EXPORTED so Epic 4
// adapter contract tests can reuse it as the reference vector.
//
// Algorithm:
//  1. seed := sha256(input)[0..32]
//  2. for i in [0, dim): v_i = sin((i+1) * seed[i % 32]); store float32(v_i)
//  3. L2-normalize so ‖v‖₂ ≈ 1.0 (tolerance ±0.01 for float32 precision drift)
//
// Byte-stability across linux/amd64, linux/arm64, darwin/arm64 holds to
// within ~1 ULP per element (IEEE-754 determinism); tests assert
// element-equality with a 1e-6 tolerance, NOT byte-exact.
func GenerateMockEmbedding(input string, dim int) []float32 {
	seed := sha256.Sum256([]byte(input))
	out := make([]float32, dim)
	var sumsq float64
	for i := 0; i < dim; i++ {
		v := math.Sin(float64(i+1) * float64(seed[i%32]))
		out[i] = float32(v)
		sumsq += v * v
	}
	norm := math.Sqrt(sumsq)
	if norm > 0 {
		for i := range out {
			out[i] = float32(float64(out[i]) / norm)
		}
	}
	return out
}

// mockTokenCount is the BR-2.5 synthetic token estimator: len(s) / 4
// (mirrors OpenAI's "1 token ≈ 4 English chars" rule of thumb). BYTE
// length, not rune count — multi-byte UTF-8 inputs count more tokens
// than rune-count would suggest. Documented as a synthetic-source
// caveat in Dev Notes; Epic 4 real adapters report upstream-native counts.
func mockTokenCount(s string) int {
	return len(s) / 4
}

// ----- Handler ----------------------------------------------------------

// EmbeddingsHandlerOption customizes an EmbeddingsHandler at construction
// time. Production omits all options; WithEmbeddingDim is test-only.
type EmbeddingsHandlerOption func(*EmbeddingsHandler)

// WithEmbeddingDim overrides the default 128-dim output (production
// stays at 128 per OQ3). Non-positive values are ignored.
func WithEmbeddingDim(d int) EmbeddingsHandlerOption {
	return func(h *EmbeddingsHandler) {
		if d > 0 {
			h.dim = d
		}
	}
}

// EmbeddingsHandler serves POST /v1/embeddings. Stateless beyond logger
// + dim; safe to construct once at startup and share across all bearer-
// protected requests.
type EmbeddingsHandler struct {
	logger *slog.Logger
	dim    int
}

// NewEmbeddingsHandler builds the handler. logger may be nil — falls back
// to slog.Default(). dim defaults to defaultEmbeddingDim (128).
func NewEmbeddingsHandler(logger *slog.Logger, opts ...EmbeddingsHandlerOption) *EmbeddingsHandler {
	h := &EmbeddingsHandler{
		logger: logger,
		dim:    defaultEmbeddingDim,
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ServeHTTP implements http.Handler.
func (h *EmbeddingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// BR-2.6 defence-in-depth — bearer-auth middleware must have populated
	// APIKeyID; if not, the per-route wrap regressed in main.go.
	apiKeyID, ok := middleware.APIKeyIDFromContext(ctx)
	if !ok {
		writeChatError(w, http.StatusInternalServerError,
			"500_gateway_misconfigured",
			"Bearer-auth middleware not wired", nil)
		return
	}

	// BR-2.2 — 1 MiB body cap via MaxBytesReader. *http.MaxBytesError
	// surfaces as 413; other decode errors are 400 with the §5.1.2 envelope.
	r.Body = http.MaxBytesReader(w, r.Body, MaxEmbeddingBodyBytes)
	var req EmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeChatError(w, http.StatusRequestEntityTooLarge,
				"413_payload_too_large",
				"Request body exceeds 1 MiB.", nil)
			return
		}
		writeChatError(w, http.StatusBadRequest,
			"400_invalid_request",
			"Request body is not valid JSON.", nil)
		return
	}

	if status, code, msg, param, valid := validateEmbeddingRequest(&req); !valid {
		writeChatError(w, status, code, msg, param)
		return
	}

	// Validator already guarantees parse success; re-parse here is the
	// defence-in-depth path that supplies the per-input slice to the
	// generator. Cheap (pure function, no I/O).
	inputs, err := parseEmbeddingInputs(req.Input)
	if err != nil {
		inputParam := "input"
		writeChatError(w, http.StatusBadRequest,
			"400_invalid_request",
			"Field 'input' is required and must be a non-empty string or non-empty array of strings.",
			&inputParam)
		return
	}

	// BR-2.7 — exactly one structured log line per accepted request. NEVER
	// log req.Input content; surface only non-PII size signals.
	totalInputBytes := 0
	for _, s := range inputs {
		totalInputBytes += len(s)
	}
	h.logger.InfoContext(
		ctx, "embeddings_mock",
		slog.String("event", "embeddings_mock"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("input_count", len(inputs)),
		slog.Int("total_input_bytes", totalInputBytes),
	)

	data := make([]EmbeddingData, len(inputs))
	promptTokens := 0
	for i, s := range inputs {
		data[i] = EmbeddingData{
			Object:    "embedding",
			Index:     i,
			Embedding: GenerateMockEmbedding(s, h.dim),
		}
		promptTokens += mockTokenCount(s)
	}

	writeChatJSON(w, http.StatusOK, EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: EmbeddingUsage{
			PromptTokens: promptTokens,
			TotalTokens:  promptTokens,
		},
	})
}
