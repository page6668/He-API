// CI-only adapter-fake upstream — DO NOT package in production images.
//
// Story 4.8 T1.1 — DeepSeek fake-upstream binary. Serves the OpenAI-canonical
// `/v1/chat/completions` shape with a deterministic fixture response so the
// `gateway-openai-sdk-contract` CI lane can exercise the adapter end-to-end
// without consuming vendor quota.
//
// Wire shape inlines `apps/adapters/deepseek/internal/upstream/types.go`
// (BR-1.1: test-only binaries cannot cross-import the production adapter's
// internal/ package). Determinism per BR-1.10 — fixed timestamps, no
// `time.Now()` / `rand.*`; chat-completion id derived from request-model hash.
//
// CLI flags:
//
//	-listen   :19000                           TCP listen address
//	-cert     /tmp/he-api/fake-tls.crt         TLS cert (PEM)
//	-key      /tmp/he-api/fake-tls.key         TLS key  (PEM)
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
)

const (
	chatCompletionsPath = "/v1/chat/completions"
	fakeCreated         = int64(1715000000)
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type rawUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
type chatDelta struct {
	Role    *string `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}
type chatChoice struct {
	Index        int          `json:"index"`
	Message      *chatMessage `json:"message,omitempty"`
	Delta        *chatDelta   `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}
type chatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *rawUsage    `json:"usage,omitempty"`
}
type chatChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *rawUsage    `json:"usage,omitempty"`
}
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
}

func main() {
	var listen, certFile, keyFile string
	flag.StringVar(&listen, "listen", ":19000", "TCP listen address")
	flag.StringVar(&certFile, "cert", "/tmp/he-api/fake-tls.crt", "TLS cert (PEM); empty disables TLS")
	flag.StringVar(&keyFile, "key", "/tmp/he-api/fake-tls.key", "TLS key (PEM); empty disables TLS")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc(chatCompletionsPath, handleChat)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{Addr: listen, Handler: mux}
	log.Printf("deepseek-fake-upstream listen=%s tls=%v path=%s", listen, certFile != "", chatCompletionsPath)
	if certFile == "" {
		log.Fatal(srv.ListenAndServe())
	}
	log.Fatal(srv.ListenAndServeTLS(certFile, keyFile))
}

// handleChat serves the canonical OpenAI chat-completions shape for the
// deepseek adapter. Identity-mapped model echo (no endpoint-id rewrite).
func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "io_read", err.Error())
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	echoModel := req.Model
	fakeID := "chatcmpl-fake-" + hashID(req.Model)
	if req.Stream {
		emitStream(w, echoModel, fakeID)
		return
	}
	emitNonStream(w, echoModel, fakeID)
}

func emitNonStream(w http.ResponseWriter, model, id string) {
	content := "Hello from deepseek-fake-upstream."
	finish := "stop"
	resp := chatResponse{
		ID: id, Object: "chat.completion", Created: fakeCreated, Model: model,
		Choices: []chatChoice{{
			Index:        0,
			Message:      &chatMessage{Role: "assistant", Content: content},
			FinishReason: &finish,
		}},
		Usage: &rawUsage{PromptTokens: 5, CompletionTokens: 4, TotalTokens: 9},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func emitStream(w http.ResponseWriter, model, id string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	flusher, _ := w.(http.Flusher)
	role := "assistant"
	// bootstrap (role-only)
	sseSend(w, flusher, chatChunk{
		ID: id, Object: "chat.completion.chunk", Created: fakeCreated, Model: model,
		Choices: []chatChoice{{Index: 0, Delta: &chatDelta{Role: &role}}},
	})
	// content deltas
	pieces := []string{"He", "llo", "!"}
	for i, piece := range pieces {
		p := piece
		choice := chatChoice{Index: 0, Delta: &chatDelta{Content: &p}}
		if i == len(pieces)-1 {
			fin := "stop"
			choice.FinishReason = &fin
		}
		sseSend(w, flusher, chatChunk{
			ID: id, Object: "chat.completion.chunk", Created: fakeCreated, Model: model,
			Choices: []chatChoice{choice},
		})
	}
	// terminal usage-only chunk (empty choices + populated usage)
	sseSend(w, flusher, chatChunk{
		ID: id, Object: "chat.completion.chunk", Created: fakeCreated, Model: model,
		Choices: []chatChoice{},
		Usage:   &rawUsage{PromptTokens: 5, CompletionTokens: 4, TotalTokens: 9},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func sseSend(w io.Writer, fl http.Flusher, c chatChunk) {
	b, _ := json.Marshal(c)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if fl != nil {
		fl.Flush()
	}
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":"%d_%s","message":%q,"type":"invalid_request_error","param":null}}`,
		status, kind, msg)
}

func hashID(model string) string {
	sum := sha256.Sum256([]byte(model))
	return hex.EncodeToString(sum[:4]) // 8 hex chars
}
