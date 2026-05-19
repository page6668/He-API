# Models & Types Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-19
**Total Stories Tracked**: 3
**Total Models**: 16
**Repository**: He-API
**Mode**: monolith

## Protobuf Messages (`packages/proto/he/auth/v1/auth.proto`)

| Message | Story | Notes |
|---------|-------|-------|
| `ValidateApiKeyRequest` | 3.2 | `plaintext_key` (string) + `client_ip` (string) + `user_agent` (string). Client-attribution fields are informational only — auth-svc forwards them for future audit-svc logging. |
| `ValidateApiKeyResponse` | 3.2 | `ok` (bool) + `api_key_id` / `user_id` / `team_id` / `scope` (all string, populated only when ok=true) + `reason` (enum). |

## Protobuf Enums

| Enum | Story | Values |
|------|-------|--------|
| `ApiKeyValidationReason` | 3.2 | `API_KEY_VALIDATION_REASON_UNSPECIFIED` (ok=true) · `API_KEY_VALIDATION_REASON_NOT_FOUND` · `API_KEY_VALIDATION_REASON_REVOKED`. **Internal-only** — gateway maps both NOT_FOUND and REVOKED to a single 401 envelope per BR-2.4. |

## Go Repository Types (`apps/auth-svc/internal/repository`)

| Type | Story | Notes |
|------|-------|-------|
| `ApiKeyRow` | 3.2 | Mirrors `he_api.api_keys` for the Validate hot path. Nullable columns surface as `pgtype.UUID` / `pgtype.Timestamptz` so the handler distinguishes "no team" from "empty UUID". |

## Go Handler Types (`apps/api-gateway/internal/handlers`)

| Type | Story | Notes |
|------|-------|-------|
| `handlers.ModelEntry` | 3.5 | One row in the `/v1/models` response data array. Fields: `ID`/`Object`/`Created`/`OwnedBy` (snake_case JSON tags per OpenAI canonical). Promoted to a shared package if Epic 4 adapters need to import it. |
| `handlers.ModelsResponse` | 3.5 | Top-level `/v1/models` wrapper (`object="list"` + `data []ModelEntry`). |
| `handlers.ModelsHandler` | 3.5 | Handler struct (`logger` + `now` + `startedAt`). Constructed via `NewModelsHandler(logger, opts...)`; `WithModelsNow(f)` injects a deterministic clock for the BR-1.6 stable-`created` test. |
| `handlers.ModelsHandlerOption` | 3.5 | Option-function type for `ModelsHandler` (Architect Round 1 OQ4 ratified pattern; mirrors `ChatHandlerOption`). |
| `handlers.EmbeddingRequest` | 3.5 | Inbound `/v1/embeddings` JSON body. Critical: `Input json.RawMessage` for BR-2.8 dual-shape parsing (string OR array). |
| `handlers.EmbeddingResponse` | 3.5 | Top-level `/v1/embeddings` response. Field order per BR-2.9: `object` → `data` → `model` → `usage`. |
| `handlers.EmbeddingData` | 3.5 | One embedding entry. Field order: `object="embedding"` → `index` → `embedding ([]float32)`. |
| `handlers.EmbeddingUsage` | 3.5 | Mock token-count. Fields: `prompt_tokens int` + `total_tokens int`. No `completion_tokens` (embeddings have no completion dimension). |
| `handlers.EmbeddingsHandler` | 3.5 | Handler struct (`logger` + `dim`). Constructed via `NewEmbeddingsHandler(logger, opts...)`. |
| `handlers.EmbeddingsHandlerOption` | 3.5 | Option-function type for `EmbeddingsHandler`; `WithEmbeddingDim(d int)` is test-only. |

## Go Error-Envelope Types (`apps/api-gateway/internal/openaierr`)

| Type | Story | Notes |
|------|-------|-------|
| `openaierr.body` (private) | 3.6 | The §5.1.2 5-field envelope payload. Field declaration order locks JSON marshal order per BR-1.7 (Architect Round 1 OQ2): `code` → `message` → `type` → `param` → `he_request_id`. |
| `openaierr.envelope` (private) | 3.6 | Top-level wrapper `{"error": body}`. |
| `openaierr.CodeMetadata` (exported map) | 3.6 | `map[string]struct{HTTPStatus int; ErrorType string}` — single source of truth for the canonical taxonomy. Lookup derives both HTTP status + error.type. RETIRED row `501_streaming_not_implemented` is OMITTED (Architect Round 2 L1). |

## Go Middleware Types (`apps/api-gateway/internal/middleware/requestid`)

| Type | Story | Notes |
|------|-------|-------|
| `requestid.requestIDKey` (private) | 3.6 | Empty-struct context key for the per-request `he_request_id` (Go idiom — avoids string-key collisions). Tests reach in via `requestid.WithRequestID(ctx, id)`. |
| `obs.LoggerOption` (exported) | 3.6 | Functional-option type for `obs.NewLogger`; landed in `packages/go-observability/logger.go` so the gateway can wire `obs.WithRequestIDExtractor(requestid.FromContext)` per BR-2.10. |
| `obs.RequestIDExtractor` (exported) | 3.6 | `func(ctx context.Context) (string, bool)` — the signature of `requestid.FromContext`. Lives in `packages/go-observability` so the package does not need a hard dep on `apps/api-gateway/internal/middleware/requestid`. |

## Models by Story

- **3.2** — Bearer-token API-key auth:
  - `ValidateApiKeyRequest` / `ValidateApiKeyResponse` / `ApiKeyValidationReason` (proto).
  - `ApiKeyRow` (Go repository type).
  - `middleware.CachedClaims` (gateway-local JSON cache envelope; not part of the cross-service contract).
- **3.5** — `/v1/models` + `/v1/embeddings` Go types (handler-local, package `handlers`):
  - `ModelEntry` / `ModelsResponse` / `ModelsHandler` / `ModelsHandlerOption`.
  - `EmbeddingRequest` / `EmbeddingResponse` / `EmbeddingData` / `EmbeddingUsage` / `EmbeddingsHandler` / `EmbeddingsHandlerOption`.
  - Promotion rule (Architect-ratified): if Epic 4 adapter packages need to import any of these, promote to a sibling shared package (e.g., `apps/api-gateway/internal/openai/types`). Story 3.5 does NOT pre-promote per Go's "rule of three".
- **3.6** — Standardized error envelope + request-id middleware:
  - New package `openaierr` (`body`, `envelope` private structs; `CodeMetadata` exported map; `Write(w, ctx, status, code, message, *param) error` canonical writer).
  - New subpackage `middleware/requestid` (`requestIDKey` private context key; `RequestID(next)` middleware; `FromContext(ctx) (string, bool)` accessor; `WithRequestID(ctx, id) context.Context` test-and-cross-package helper).
  - `packages/go-observability` extended with `LoggerOption` + `RequestIDExtractor` + `WithRequestIDExtractor` so `obs.NewLogger` can auto-inject `he_request_id` into every slog record via the `TraceContextHandler` (BR-2.10 — Architect Round 1 OQ5 RATIFIED).
- **4.1** — DeepSeek adapter (first real model adapter):
  - **New proto module** `packages/proto/he/adapter/v1/adapter.proto` (`he.adapter.v1` package per OQ1; `option go_package = "...gen/go/he/adapter/v1;adapterv1"`). Messages: `ChatRequest` / `ChatMessage` / `ChatChunk` / `Choice` / `Delta` / `Usage`. Service: `AdapterService` with `Chat(ChatRequest) returns (stream ChatChunk)` (server-streaming RPC). Stories 4.2-4.6 inherit the contract verbatim.
  - **New shared package** `packages/go-observability/requestid/` (OQ8 lift). Exports: `HeaderName` constant (`"X-He-Request-Id"`), `SpanAttributeKey` constant (`"he.request_id"`), `FromContext(ctx) (string, bool)`, `WithRequestID(ctx, id) context.Context`, `ContextWith(ctx, id) context.Context`. The gateway middleware `apps/api-gateway/internal/middleware/requestid` becomes a thin re-export shim — all existing callers compile unchanged; a follow-up housekeeping pass deletes the shim.
  - **New adapter packages** (`apps/adapters/deepseek/`):
    - `internal.Service` — `AdapterServiceHandler` implementing `Chat` (server-streaming) with `NewService(client, logger)` constructor; non-streaming branch emits ONE terminal chunk, streaming branch emits N chunks with the LAST one carrying `usage` (BR-2.4).
    - `internal/upstream.Client` — HTTP/2-forced (`golang.org/x/net/http2.Transport`) HTTPS client over `https://api.deepseek.com` (OQ7). `NewClient(baseURL, apiKey, timeout)` constructor; default timeout 60s (BR-1.8).
    - `internal/upstream.ErrorKind` enum — `auth_revoked` / `quota_exhausted` / `upstream_5xx` / `upstream_4xx` / `upstream_timeout` / `tls` / `dns` / `connection_refused` / `missing_usage` / `empty_choices` / `malformed_chunk` / `usage_constraint_violation`. M2 disambiguation lets oncall route 401 vs 429 to different runbooks.
    - `internal/upstream.ClassifyError(err)` / `ClassifyHTTPStatus(status)` / `UpstreamError{Kind, Status, Cause}` — error classification + structured error type.
    - `internal/upstream.Decoder` — strict-RFC SSE decoder per OQ6. `NewDecoder(io.Reader)` + `NextChunk(ctx) (*ChatChunkJSON, error)`. Returns `io.EOF` on `data: [DONE]`; `ErrMalformedFrame` on any deviation (CRLF, missing space after `data:`, non-`data:` line prefix).
    - `internal/upstream.{RawUsage, ChatRequestJSON, StreamOptionsJSON, ChatMessage, ChatChoiceJSON, ChatDeltaJSON, ChatResponseJSON, ChatChunkJSON}` — wire-shape types for the DeepSeek (OpenAI-protocol-native) endpoint.
    - `internal/usage.Normaliser` — interface `Normalise(raw RawUsage) (NormalisedUsage, error)`. Stories 4.2-4.6 supply per-vendor implementations; the DeepSeek implementation is identity-mapping with BR-3.3 constraints (prompt > 0, completion ≥ 0, total = prompt + completion).
    - `internal/usage.NormalisedUsage` (`PromptTokens` / `CompletionTokens` / `TotalTokens`) + `ErrUsageConstraintViolation` sentinel.
  - **New gateway packages**:
    - `apps/api-gateway/internal/adapterclient.Registry` — model-id → `ClientHandle` map (OQ4 in-process resolver). `NewRegistry(map[string]string)` / `NewRegistryFromHandles(map[string]ClientHandle)` / `LoadFromEnv()` constructors; `Resolve(modelID) (ClientHandle, bool)` is the abstraction boundary Epic 6 routing-svc grafts onto.
    - `apps/api-gateway/internal/adapterclient.ClientHandle` — interface `Chat(ctx, *adapterv1.ChatRequest, http.Header) (Stream, error)`. Concrete implementation `connectClientHandle` wraps `adapterv1connect.AdapterServiceClient`; `connectStreamAdapter` wraps `connect.ServerStreamForClient[adapterv1.ChatChunk]` to surface the local `Stream` interface (`Receive() / Msg() / Err() / Close()`).
    - `apps/api-gateway/internal/adapterclient.{DeepSeekModelID, DeepSeekEndpointEnv}` constants — `"deepseek-v3"` model id + `"DEEPSEEK_ADAPTER_ENDPOINT"` env var.
    - `apps/api-gateway/internal/streaming.AdapterChunker` — sister to Story-3.4 `MockChunker`. `NewAdapterChunker(stream, model)` + `Stream(ctx, w) (chunksEmitted, firstFlushAt, err)`. Consumes adapter Connect-RPC chunks → emits `data: <json>\n\n` SSE events → terminates with `data: [DONE]\n\n`.
    - `apps/api-gateway/internal/streaming.AdapterChunkStream` — minimal iterator surface (`Receive() / Msg() / Err()`) the chunker consumes; gateway-internal `adapterclient.Stream` satisfies it implicitly without inverting the package layering.
    - `apps/api-gateway/internal/streaming.Writer.HeadersFlushed() bool` (new method, BR-2.5) — reports whether the SSE response headers have been written. Handler-level boundary check that drives the BR-2.5 pre-flush JSON envelope vs BR-2.6 post-flush SSE error frame decision.
  - **New gateway constructor option**: `handlers.WithAdapterRegistry(reg *adapterclient.Registry)` — mirrors `WithIDFactory` / `WithNow` pattern.
  - **Promotion rule (Architect Round 2 ratified)**: Stories 4.2-4.6 add ENTRIES to the same `adapterclient.Registry` and supply per-vendor `Normaliser` implementations; they DO NOT invent parallel mechanisms.
