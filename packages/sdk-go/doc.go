// Package heapi is the official Go SDK for the He-API gateway: a drop-in
// wrapper over the official OpenAI Go SDK (github.com/openai/openai-go/v3).
//
// Because the He-API gateway speaks the OpenAI wire protocol, the SDK does not
// reimplement any transport: NewClient simply pre-seeds the default base URL
// (the He-API gateway) and the HE_API_KEY credential, then returns the upstream
// openai.Client unchanged. Every chat/embeddings/models/audio/streaming call,
// every request/response type, and the full error model are openai-go's own —
// so migrating an existing openai-go program is a two-line diff: change the
// import's constructor call from openai.NewClient(...) to heapi.NewClient().
//
//	import (
//		heapi "github.com/he-api/sdk-go"
//		"github.com/openai/openai-go/v3"
//	)
//
//	client := heapi.NewClient() // reads HE_API_KEY, targets the He-API gateway
//	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
//		Model:    "qwen-max",
//		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")},
//	})
//
// He-API extensions (routing strategy, A/B headers, the /v1/balance and
// /v1/usage endpoints, response headers) are reached through openai-go's own
// per-request options and this package's thin package-level helpers (Balance,
// Usage, HeRequestID); see extensions.go.
package heapi
