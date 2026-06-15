# He-API Go SDK

`github.com/he-api/sdk-go` — the official Go SDK for the [He-API](https://he-api.com)
gateway. It is a **drop-in** wrapper over the official OpenAI Go SDK
([`github.com/openai/openai-go/v3`](https://pkg.go.dev/github.com/openai/openai-go/v3)):
`heapi.NewClient()` returns the **upstream `openai.Client` unchanged**, pre-configured
to target the He-API gateway and read the `HE_API_KEY` credential. Because the gateway
speaks the OpenAI wire protocol, every chat / streaming / embeddings / models / audio
call, every request and response type, and the full error model are openai-go's own.

Migrating an existing openai-go program is a two-line diff: keep importing
`github.com/openai/openai-go/v3` for the parameter and response types, and change the
constructor from `openai.NewClient(...)` to `heapi.NewClient()`.

## Install

```sh
go get github.com/he-api/sdk-go
```

Requires Go 1.22+.

## Quickstart

```go
package main

import (
	"context"
	"fmt"

	heapi "github.com/he-api/sdk-go"
	"github.com/openai/openai-go/v3"
)

func main() {
	// Reads HE_API_KEY from the environment and targets the He-API gateway.
	client := heapi.NewClient()

	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "qwen-max",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Hello!")},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Choices[0].Message.Content)
}
```

## Configuration

| What | Default | Override (highest precedence first) |
|------|---------|-------------------------------------|
| Base URL | `https://api.he-api.com/v1` (`heapi.DEFAULT_BASE_URL`) | `option.WithBaseURL(...)` › `HE_API_BASE_URL` env › default |
| API key | _(none)_ | `option.WithAPIKey(...)` › `HE_API_KEY` env |

The SDK deliberately reads `HE_API_*` and **never** OpenAI's `OPENAI_API_KEY` /
`OPENAI_BASE_URL`, so a process holding a real OpenAI credential can't accidentally
route He-API traffic with it.

## Streaming

```go
stream := client.Chat.Completions.NewStreaming(ctx, params)
defer stream.Close()
for stream.Next() {
	chunk := stream.Current()
	fmt.Print(chunk.Choices[0].Delta.Content)
}
if err := stream.Err(); err != nil {
	panic(err)
}
```

## He-API extensions

Routing and A/B selection are plain per-request headers (the gateway validates them):

```go
resp, err := client.Chat.Completions.New(ctx, params,
	option.WithHeader("X-He-Routing-Strategy", "cost"),       // quality | cost | latency
	option.WithHeader("X-He-AB-Models", "qwen-max,glm-4"),
)
```

Read He-API response headers via openai-go's raw-response hook:

```go
var raw *http.Response
_, err := client.Chat.Completions.New(ctx, params, option.WithResponseInto(&raw))
fmt.Println(raw.Header.Get("X-He-Selected-Model"), raw.Header.Get("X-He-Request-Id"))
```

> Note: `X-He-Cost-Usd` is intentionally absent / non-authoritative at the gateway.
> Never derive billing from a response header; read consumption from `/v1/usage` and
> `/v1/balance` (below).

The non-OpenAI endpoints are thin package-level helpers (the returned client is the
upstream `openai.Client`, which has no place to hang custom methods):

```go
bal, err := heapi.Balance(ctx, client, "usd") // GET /v1/balance?currency=usd
use, err := heapi.Usage(ctx, client, "rmb")   // GET /v1/usage?currency=rmb
```

Errors are openai-go's own; the He-API correlation id is read with `heapi.HeRequestID`:

```go
var apiErr *openai.Error
if errors.As(err, &apiErr) {
	fmt.Println(apiErr.Code, heapi.HeRequestID(err))
}
```

## License

[Apache-2.0](./LICENSE)
