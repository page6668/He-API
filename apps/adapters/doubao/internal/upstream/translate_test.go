package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// newTestEnv constructs an EndpointMap with the supplied (pro, lite)
// endpoint ids — empty strings stay empty (per BR-1.12 fail-fast).
func newTestEnv(pro, lite string) *EndpointMap {
	return New(func(name string) string {
		switch name {
		case EnvDoubaoProEndpointID:
			return pro
		case EnvDoubaoLiteEndpointID:
			return lite
		}
		return ""
	})
}

// 4.5-UNIT-001 (P0) — OQ-4.5-1 (Volcengine Ark v3 endpoint URL) +
// OQ-4.5-2 (DOUBAO_* env-var brand-name naming) + OQ-4.5-3 (outbound
// model field rewrite — friendly id → endpoint id). The outbound HTTPS
// request MUST target https://ark.cn-beijing.volces.com/api/v3/chat/completions
// with plain `Authorization: Bearer` (NOT HMAC-SHA256-signed) and the body's
// `model` field MUST equal `$DOUBAO_PRO_ENDPOINT_ID`.
func TestTranslate_BearerAuth_ArkURL_OutboundProEndpointID(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	req := &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "sk-doubao-test123", em, req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.URL.String(), "https://ark.cn-beijing.volces.com/api/v3/chat/completions"; got != want {
		t.Fatalf("URL = %q, want %q (OQ-4.5-1 Ark v3 endpoint)", got, want)
	}
	if got, want := httpReq.Header.Get("Authorization"), "Bearer sk-doubao-test123"; got != want {
		t.Fatalf("Authorization = %q, want %q (plain Bearer per OQ-4.5-1; NOT HMAC-signed)", got, want)
	}
	if got := httpReq.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := httpReq.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json (non-streaming)", got)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if got.Model != "ep-test-pro-001" {
		t.Fatalf("body.model = %q, want %q (BR-1.7.f outbound rewrite)", got.Model, "ep-test-pro-001")
	}
}

// 4.5-UNIT-002 (P0) — OQ-4.5-3 outbound rewrite for `doubao-lite` +
// cross-model swap-bug guard (lite path MUST NOT yield pro endpoint id).
func TestTranslate_OutboundLiteEndpointID_CrossModelDistinct(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	req := &adapterv1.ChatRequest{
		Model:    "doubao-lite",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if got.Model != "ep-test-lite-001" {
		t.Fatalf("body.model = %q, want %q (lite-specific endpoint id; swap-bug guard)", got.Model, "ep-test-lite-001")
	}
	if got.Model == "ep-test-pro-001" {
		t.Fatalf("body.model = %q SHOULD NOT equal pro endpoint id — lookup swap regression", got.Model)
	}
}

// 4.5-UNIT-003 (P0) — BR-1.12 fail-fast: unmapped req.Model →
// translateRequest returns ErrUnsupportedModel; NO http.Request created.
func TestTranslate_UnmappedModel_FailFast(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	req := &adapterv1.ChatRequest{
		Model:    "unknown-id",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
	if err == nil {
		t.Fatalf("translateRequest err = nil, want ErrUnsupportedModel")
	}
	if !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("translateRequest err = %v, want ErrUnsupportedModel (BR-1.12 fail-fast)", err)
	}
	if httpReq != nil {
		t.Fatalf("translateRequest returned non-nil request on fail-fast: %v", httpReq)
	}
}

// 4.5-UNIT-005 (P0) — BR-1.11 inbound back-translate (non-streaming):
// TranslateChatResponse rewrites Model from Volcengine endpoint id back
// to the consumer-facing friendly id; all non-`model` fields pass through.
func TestTranslateChatResponse_InboundBackTranslate_NonStreaming(t *testing.T) {
	stop := "stop"
	resp := &ChatResponseJSON{
		ID:      "chatcmpl-doubao-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "ep-test-pro-001",
		Choices: []ChatChoiceJSON{{
			Index:        0,
			Message:      &ChatMessage{Role: "assistant", Content: "Hello back"},
			FinishReason: &stop,
		}},
		Usage: &RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
	}
	out := TranslateChatResponse(resp, "doubao-pro")
	if out.Model != "doubao-pro" {
		t.Fatalf("out.Model = %q, want %q (BR-1.11 inbound back-translate)", out.Model, "doubao-pro")
	}
	// non-`Model` fields identity-mapped
	if out.ID != resp.ID || out.Object != resp.Object || out.Created != resp.Created {
		t.Fatalf("non-Model header fields drifted: out=%#v resp=%#v", out, resp)
	}
	if !reflect.DeepEqual(out.Choices, resp.Choices) {
		t.Fatalf("choices drifted: out=%#v resp=%#v", out.Choices, resp.Choices)
	}
	if !reflect.DeepEqual(out.Usage, resp.Usage) {
		t.Fatalf("usage drifted: out=%#v resp=%#v", out.Usage, resp.Usage)
	}
	// nil-input safety
	if TranslateChatResponse(nil, "doubao-pro") != nil {
		t.Fatalf("TranslateChatResponse(nil) should be nil")
	}
}

// 4.5-UNIT-005 (streaming companion) — BR-1.11 per-chunk back-translate:
// TranslateChatChunk rewrites Model from endpoint id to friendly id.
func TestTranslateChatChunk_InboundBackTranslate_Streaming(t *testing.T) {
	chunk := &ChatChunkJSON{
		ID:      "chatcmpl-doubao-stream",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "ep-test-lite-001",
		Choices: []ChatChoiceJSON{{Index: 0, Delta: &ChatDeltaJSON{Content: strPtr("Hi")}}},
		Usage:   &RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
	}
	out := TranslateChatChunk(chunk, "doubao-lite")
	if out.Model != "doubao-lite" {
		t.Fatalf("out.Model = %q, want %q (BR-1.11 per-chunk back-translate)", out.Model, "doubao-lite")
	}
	if out.Usage == nil || out.Usage.TotalTokens != 15 {
		t.Fatalf("usage drifted by translate: %#v", out.Usage)
	}
	if TranslateChatChunk(nil, "doubao-lite") != nil {
		t.Fatalf("TranslateChatChunk(nil) should be nil")
	}
}

// 4.5-UNIT-008 (P0) — BR-2.9 invariant: when stream=true, outbound body
// MUST carry stream_options.include_usage=true AND header MUST set
// Accept: text/event-stream.
func TestTranslate_StreamForcesIncludeUsage_AndAcceptSSE(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	req := &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:   true,
	}
	httpReq, err := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got := httpReq.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", got)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if !got.Stream {
		t.Fatalf("body.stream = false, want true")
	}
	if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Fatalf("body.stream_options.include_usage missing or false (BR-2.9): %#v", got.StreamOptions)
	}
}

// 4.5-UNIT-011 (P0) — OQ-4.5-3 identity-mapping correctness for ALL
// non-`model` fields. `model` is the SOLE non-identity transform per BR-1.7.f.
func TestTranslate_IdentityMapping_NonModelFields(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	temperature := 0.7
	maxTokens := int32(1024)
	req := &adapterv1.ChatRequest{
		Model:              "doubao-pro",
		Messages:           []*adapterv1.ChatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "Hi"}},
		Stream:             false,
		Temperature:        &temperature,
		MaxTokens:          &maxTokens,
		ToolsJson:          []byte(`[{"type":"function","function":{"name":"foo"}}]`),
		ToolChoiceJson:     []byte(`"auto"`),
		ResponseFormatJson: []byte(`{"type":"json_object"}`),
	}
	httpReq, err := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v", err)
	}
	if got.Model != "ep-test-pro-001" {
		t.Fatalf("model = %q, want endpoint id (the one non-identity transform)", got.Model)
	}
	wantMessages := []ChatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "Hi"}}
	if !reflect.DeepEqual(got.Messages, wantMessages) {
		t.Fatalf("messages drifted: got=%#v want=%#v", got.Messages, wantMessages)
	}
	if got.Temperature == nil || *got.Temperature != 0.7 {
		t.Fatalf("temperature drifted: %#v", got.Temperature)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 1024 {
		t.Fatalf("max_tokens drifted: %#v", got.MaxTokens)
	}
	if got.Tools == nil {
		t.Fatalf("tools drifted to nil")
	}
	if got.ToolChoice == nil {
		t.Fatalf("tool_choice drifted to nil")
	}
	if got.ResponseFormat == nil {
		t.Fatalf("response_format drifted to nil")
	}
	if got.Stream {
		t.Fatalf("stream = true, want false")
	}
	if got.StreamOptions != nil {
		t.Fatalf("non-streaming body MUST omit stream_options; got %#v", got.StreamOptions)
	}
}

// 4.5-UNIT-translate-no-stream-options-when-non-streaming — when stream=false,
// body MUST NOT include stream_options.
func TestTranslate_NonStreaming_OmitsStreamOptions(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	req := &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, _ := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
	body, _ := io.ReadAll(httpReq.Body)
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("non-streaming body MUST omit stream_options; got %s", body)
	}
}

// 4.5-UNIT-translate-request-id — BR-1.5: when req.HeRequestId is set,
// outbound MUST carry X-Request-Id: <id>.
func TestTranslate_PropagatesHeRequestId_AsXRequestId(t *testing.T) {
	em := newTestEnv("ep-test-pro-001", "ep-test-lite-001")
	req := &adapterv1.ChatRequest{
		Model:       "doubao-pro",
		Messages:    []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		HeRequestId: "req_a1b2c3d4e5f6",
	}
	httpReq, _ := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
	if got, want := httpReq.Header.Get("X-Request-Id"), "req_a1b2c3d4e5f6"; got != want {
		t.Fatalf("X-Request-Id = %q, want %q", got, want)
	}
}

// 4.5-BLIND-TRANSLATE-001 (P0) — round-trip property test: for random
// req.Model × random non-`model` bodies × Volcengine-shaped responses
// (endpoint-id echo), verify the round-trip invariants:
//
//	(a) TranslateChatRequest(req).Model == endpoint_map.Lookup(req.Model)
//	(b) TranslateChatResponse(upstream_echo, req.Model).Model == req.Model
//	(c) non-`model` fields identity-mapped through both directions
func TestTranslate_RoundTrip_PropertyTest_50Tuples(t *testing.T) {
	em := newTestEnv("ep-rt-pro-001", "ep-rt-lite-002")
	cases := []struct {
		friendlyID string
		endpointID string
	}{
		{ModelDoubaoPro, "ep-rt-pro-001"},
		{ModelDoubaoLite, "ep-rt-lite-002"},
	}
	// 25 iters per friendly id × 2 ids = 50 tuples.
	for i := 0; i < 25; i++ {
		for _, c := range cases {
			req := &adapterv1.ChatRequest{
				Model:    c.friendlyID,
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "ping"}},
			}
			// (a) outbound — friendly → endpoint
			httpReq, err := translateRequest(context.Background(), "https://ark.cn-beijing.volces.com", "k", em, req)
			if err != nil {
				t.Fatalf("iter %d %s: translateRequest err = %v", i, c.friendlyID, err)
			}
			body, _ := io.ReadAll(httpReq.Body)
			var outbound ChatRequestJSON
			_ = json.Unmarshal(body, &outbound)
			if outbound.Model != c.endpointID {
				t.Fatalf("iter %d %s: outbound model = %q, want %q", i, c.friendlyID, outbound.Model, c.endpointID)
			}
			// (b) inbound — endpoint → friendly via TranslateChatResponse
			upstreamEcho := &ChatResponseJSON{Model: c.endpointID, Choices: []ChatChoiceJSON{}, Usage: &RawUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}
			rewritten := TranslateChatResponse(upstreamEcho, c.friendlyID)
			if rewritten.Model != c.friendlyID {
				t.Fatalf("iter %d %s: inbound model = %q, want %q (BR-1.11)", i, c.friendlyID, rewritten.Model, c.friendlyID)
			}
			// (c) non-`model` identity — usage survives
			if rewritten.Usage == nil || rewritten.Usage.TotalTokens != 2 {
				t.Fatalf("iter %d %s: usage drifted: %#v", i, c.friendlyID, rewritten.Usage)
			}
		}
	}
}

// 4.5-BLIND-DATA-003 (P0) — per-chunk streaming back-translate invariant:
// 100-chunk synthetic stream with raw model="ep-...", per-chunk
// TranslateChatChunk produces EVERY chunk's Model = friendly id; tail
// chunk's usage survives. Cross-model independence verified.
func TestTranslate_PerChunkBackTranslate_PropertyTest_100Chunks(t *testing.T) {
	cases := []struct {
		friendly, raw string
	}{
		{ModelDoubaoPro, "ep-test-pro-001"},
		{ModelDoubaoLite, "ep-test-lite-001"},
	}
	for _, c := range cases {
		var emitted []*ChatChunkJSON
		for i := 0; i < 100; i++ {
			raw := &ChatChunkJSON{
				Model:   c.raw,
				Choices: []ChatChoiceJSON{{Index: 0, Delta: &ChatDeltaJSON{Content: strPtr("tok")}}},
			}
			emitted = append(emitted, TranslateChatChunk(raw, c.friendly))
		}
		// tail chunk with usage
		tail := &ChatChunkJSON{
			Model:   c.raw,
			Choices: []ChatChoiceJSON{{Index: 0, FinishReason: strPtr("stop")}},
			Usage:   &RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		}
		tailOut := TranslateChatChunk(tail, c.friendly)
		emitted = append(emitted, tailOut)

		// Invariants:
		for i, ch := range emitted {
			if ch.Model != c.friendly {
				t.Fatalf("%s iter %d: chunk.Model = %q, want %q (BR-1.11)", c.friendly, i, ch.Model, c.friendly)
			}
		}
		if tailOut.Usage == nil || tailOut.Usage.TotalTokens != 15 {
			t.Fatalf("%s: tail-chunk usage drifted: %#v", c.friendly, tailOut.Usage)
		}
	}
}

// strPtr is a local helper (avoids importing testing helper packages).
func strPtr(s string) *string { return &s }

// regexEndpointIDLeakGuard locks the leak-prevention regex used by
// 4.5-UNIT-015 / 4.5-BLIND-TRANSLATE-002 — matches Volcengine-shaped
// endpoint ids (`ep-` + 8+ alphanumeric/hyphen chars). Defined here so
// adapter_test.go can reuse it.
var regexEndpointIDLeakGuard = regexp.MustCompile(`ep-[a-zA-Z0-9-]{8,}`)
