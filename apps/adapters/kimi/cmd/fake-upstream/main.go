// CI-only adapter-fake upstream — DO NOT package in production images.
//
// Story 4.8 T1.3 — Kimi (Moonshot AI) fake-upstream. Path matches Moonshot
// OpenAI-compat `/v1/chat/completions`. ALSO exposes the BR-4.5 body-aware
// 400 context-length-exceeded path gated by request `messages[0].content
// == "TRIGGER_CONTEXT_LENGTH_EXCEEDED"`. Wire shape inlines
// `apps/adapters/kimi/internal/upstream/types.go` (BR-1.1 internal barrier).
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
	chatCompletionsPath          = "/v1/chat/completions"
	fakeCreated                  = int64(1715000000)
	triggerContextLengthExceeded = "TRIGGER_CONTEXT_LENGTH_EXCEEDED"
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
	flag.StringVar(&listen, "listen", ":19020", "TCP listen address")
	flag.StringVar(&certFile, "cert", "/tmp/he-api/fake-tls.crt", "TLS cert (PEM); empty disables TLS")
	flag.StringVar(&keyFile, "key", "/tmp/he-api/fake-tls.key", "TLS key (PEM); empty disables TLS")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc(chatCompletionsPath, handleChat)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{Addr: listen, Handler: mux}
	log.Printf("kimi-fake-upstream listen=%s tls=%v path=%s", listen, certFile != "", chatCompletionsPath)
	if certFile == "" {
		log.Fatal(srv.ListenAndServe())
	}
	log.Fatal(srv.ListenAndServeTLS(certFile, keyFile))
}

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
	// BR-4.5 body-aware path: when first message content matches the trigger,
	// emit the Moonshot 400 context-length envelope shape so the adapter's
	// ClassifyMoonshotErrorBody surfaces ErrorKindContextLengthExceeded.
	if len(req.Messages) > 0 && req.Messages[0].Content == triggerContextLengthExceeded {
		writeMoonshotContextLengthExceeded(w)
		return
	}
	fakeID := "chatcmpl-fake-" + hashID(req.Model)
	if req.Stream {
		emitStream(w, req.Model, fakeID)
		return
	}
	emitNonStream(w, req.Model, fakeID)
}

// writeMoonshotContextLengthExceeded emits the canonical Moonshot error
// envelope shape per Story 4.3 BR-4.5 — body-aware classifier dependency.
func writeMoonshotContextLengthExceeded(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprint(w,
		`{"error":{"type":"invalid_request_error",`+
			`"message":"messages: maximum context length is 8000 tokens, however you requested 9999",`+
			`"code":"invalid_request_error"}}`)
}

func emitNonStream(w http.ResponseWriter, model, id string) {
	content := "Hello from kimi-fake-upstream."
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
	sseSend(w, flusher, chatChunk{
		ID: id, Object: "chat.completion.chunk", Created: fakeCreated, Model: model,
		Choices: []chatChoice{{Index: 0, Delta: &chatDelta{Role: &role}}},
	})
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
	return hex.EncodeToString(sum[:4])
}
