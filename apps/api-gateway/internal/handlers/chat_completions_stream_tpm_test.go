// Story 5.3 ISSUE-001 — streaming TPM post-deduction (Architect Q10).
// Cases:
//
//	5.3-UNIT-019  stream error mid-flight → TPMDeduct(prompt_tokens) — partial
//	5.3-UNIT-020  missing-tail-usage      → NO TPMDeduct + WARN slog
//	5.3-UNIT-021  normal completion       → TPMDeduct(total_tokens)
//	5.3-UNIT-022  client disconnect       → TPMDeduct(prompt_tokens) — partial
//
// The tests use a recording fake TokenDeducter so we observe (apiKeyID,
// tokens) pairs the handler actually emits to the ratelimit middleware.

package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// recordingDeducter captures every TPMDeduct call the handler emits.
type recordingDeducter struct {
	mu    sync.Mutex
	calls []deductCall
}

type deductCall struct {
	apiKeyID string
	tokens   int
}

func (d *recordingDeducter) TPMDeduct(_ context.Context, apiKeyID string, tokens int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, deductCall{apiKeyID, tokens})
}

func (d *recordingDeducter) Calls() []deductCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]deductCall, len(d.calls))
	copy(out, d.calls)
	return out
}

// streamingChunksWithUsage returns 3 content chunks + 1 terminal-with-usage.
func streamingChunksWithUsage(prompt, completion, total int32) []*adapterv1.ChatChunk {
	role := "assistant"
	hi := "Hi"
	there := " there"
	stop := "stop"
	return []*adapterv1.ChatChunk{
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role}}}},
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &hi}}}},
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &there}, FinishReason: &stop}}},
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{},
			Usage:   &adapterv1.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}},
	}
}

// 5.3-UNIT-021 — happy path: streaming success deducts total_tokens.
func TestStreamingTPMDeduct_NormalCompletion(t *testing.T) {
	deducter := &recordingDeducter{}
	fh := &fakeHandle{resp: &fakeStream{chunks: streamingChunksWithUsage(7, 13, 20)}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil,
		handlers.WithAdapterRegistry(reg),
		handlers.WithTokenDeducter(deducter),
	)
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	calls := deducter.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 TPMDeduct call, got %d: %+v", len(calls), calls)
	}
	if calls[0].tokens != 20 {
		t.Errorf("normal completion deduct tokens=%d, want total_tokens=20", calls[0].tokens)
	}
}

// 5.3-UNIT-019 — stream error mid-flight: deduct prompt_tokens only.
// Requires that the upstream EMITS a usage chunk on the chunk that errors
// (or any prior chunk) — covers the OpenAI `stream_options.include_usage`
// style "early prompt-tokens broadcast" precedent. If upstream sends no
// usage at all before erroring, falls into UNIT-020 territory (no-deduct).
func TestStreamingTPMDeduct_MidFlightError_PartialPromptOnly(t *testing.T) {
	deducter := &recordingDeducter{}
	role := "assistant"
	hi := "Hi"
	chunks := []*adapterv1.ChatChunk{
		// Chunk 1: bootstrap with prompt_tokens-only usage (OpenAI
		// stream_options.include_usage style "early" usage broadcast).
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role}}},
			Usage:   &adapterv1.Usage{PromptTokens: 11, CompletionTokens: 0, TotalTokens: 11}},
		// Chunk 2: content delta; no usage.
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &hi}}}},
		// Stream then errors mid-flight (no terminal usage chunk).
	}
	fh := &fakeHandle{resp: &fakeStream{
		chunks: chunks,
		err:    connect.NewError(connect.CodeUnavailable, errors.New("mid-stream RST")),
	}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil,
		handlers.WithAdapterRegistry(reg),
		handlers.WithTokenDeducter(deducter),
	)
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)
	// Status remains 200 because the first chunks flushed.
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (post-flush)", rr.Code)
	}
	calls := deducter.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 TPMDeduct call (partial), got %d: %+v", len(calls), calls)
	}
	if calls[0].tokens != 11 {
		t.Errorf("mid-flight partial deduct tokens=%d, want prompt_tokens=11", calls[0].tokens)
	}
}

// 5.3-UNIT-020 — missing-tail-usage: stream completes without ANY usage
// chunk → NO deduct (Q10 case iv).
func TestStreamingTPMDeduct_MissingTailUsage_NoDeduct(t *testing.T) {
	deducter := &recordingDeducter{}
	role := "assistant"
	hi := "Hi"
	stop := "stop"
	// Three normal chunks, all without usage. Stream completes successfully.
	chunks := []*adapterv1.ChatChunk{
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role}}}},
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &hi}}}},
		{Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &hi}, FinishReason: &stop}}},
	}
	fh := &fakeHandle{resp: &fakeStream{chunks: chunks}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil,
		handlers.WithAdapterRegistry(reg),
		handlers.WithTokenDeducter(deducter),
	)
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	calls := deducter.Calls()
	if len(calls) != 0 {
		t.Errorf("missing-tail-usage must NOT deduct; got %d calls: %+v", len(calls), calls)
	}
}

// 5.3-UNIT-019b — pre-flush error (adapter Chat returns connect err before
// any chunk): NO deduct (no usage signal at all + JSON-envelope response).
func TestStreamingTPMDeduct_PreFlushError_NoDeduct(t *testing.T) {
	deducter := &recordingDeducter{}
	fh := &fakeHandle{err: connect.NewError(connect.CodeUnavailable, errors.New("dial failed"))}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil,
		handlers.WithAdapterRegistry(reg),
		handlers.WithTokenDeducter(deducter),
	)
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", rr.Code)
	}
	if calls := deducter.Calls(); len(calls) != 0 {
		t.Errorf("pre-flush error must NOT deduct; got %d calls: %+v", len(calls), calls)
	}
}
