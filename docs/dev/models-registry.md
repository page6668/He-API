# Models & Types Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-26
**Total Stories Tracked**: 7
**Total Models**: 31
**Repository**: He-API
**Mode**: monolith

## Protobuf Messages (`packages/proto/he/auth/v1/auth.proto`)

| Message | Story | Notes |
|---------|-------|-------|
| `ValidateApiKeyRequest` | 3.2 | `plaintext_key` (string) + `client_ip` (string) + `user_agent` (string). Client-attribution fields are informational only — auth-svc forwards them for future audit-svc logging. |
| `ValidateApiKeyResponse` | 3.2 | `ok` (bool) + `api_key_id` / `user_id` / `team_id` / `scope` (all string, populated only when ok=true) + `reason` (enum). |
| `CreateApiKeyRequest` | **5.1** | `user_id` (UUID v4; gateway-extracted from JWT `sub` per BR-1.3 IDOR defence — client body MUST NOT carry it) + `name` (NFC + trim + 1-100 runes; class L/M/N/P/Sc + ASCII space per BR-1.7) + `client_ip` + `user_agent` (audit payload). |
| `CreateApiKeyResponse` | **5.1** | `api_key_id` + `key_prefix` (12 chars: `"he-"` + 9 base62) + `name` (as-stored) + `created_at` (google.protobuf.Timestamp) + `plaintext` (~46 chars; returned ONCE per BR-1.5 — never persisted, never re-fetchable). |
| `ListApiKeysRequest` | **5.1** | `user_id` (UUID v4 from JWT). |
| `ListApiKeysResponse` | **5.1** | `keys` (repeated `ApiKeyEntry`; 0..100 entries ORDER BY `created_at DESC, id ASC`). |
| `ApiKeyEntry` | **5.1** | `api_key_id` + `name` + `key_prefix` + `scope` (JSON string of JSONB) + `monthly_cost_cap_usd` (optional string-decimal) + `current_month_cost_usd` (string-decimal) + `last_used_at` (optional Timestamp) + `revoked_at` (optional Timestamp) + `created_at`. **`key_hash` field INTENTIONALLY ABSENT** per BR-2.5 defence-in-depth at the proto boundary. |
| `RevokeApiKeyRequest` | **5.1** | `user_id` + `api_key_id` (both UUID v4) + `client_ip` + `user_agent`. |
| `RevokeApiKeyResponse` | **5.1** | `api_key_id` + `revoked_at` (Timestamp — historical on idempotent re-revoke per BR-3.3) + `was_already_revoked` (bool — true on idempotent path). Architect Q-Spec-1 UPGRADES the rest-api-spec.md §5.2 sketch which previously declared `returns (Empty)`. |
| `UpdateApiKeyRequest` | **5.2** | `user_id` + `api_key_id` (UUID v4; gateway-extracted) + `scope` (`ScopePatch`; nil → untouched) + `monthly_cost_cap_usd` (proto3 `optional string`; present → set) + `clear_monthly_cap` (bool; true → set NULL) + `client_ip` + `user_agent`. |
| `ScopePatch` | **5.2** | `models` (repeated string) + `models_present` (bool) + `ip_whitelist` (repeated string) + `ip_whitelist_present` (bool). The `*_present` flags model proto3's lack of repeated-field presence — distinguish "omitted (preserve)" from "present + empty (clear)" per BR-1.7. |
| `UpdateApiKeyResponse` | **5.2** | Mirrors `ApiKeyEntry`'s field set (minus `key_hash`): `api_key_id` + `name` + `key_prefix` + `scope` + `monthly_cost_cap_usd` (optional) + `current_month_cost_usd` + `last_used_at` (optional) + `revoked_at` (optional; always null on UPDATE) + `created_at`. |
| `ValidateApiKeyResponse` | **5.2 EXTENDED** | Story 3.2 fields + NEW `monthly_cost_cap_usd` (proto3 `optional string`; carried to the gateway bearer cache so the keypolicy AC4 gate enforces the cap without a per-request PG round-trip). |

## Protobuf Enums

| Enum | Story | Values |
|------|-------|--------|
| `ApiKeyValidationReason` | 3.2 | `API_KEY_VALIDATION_REASON_UNSPECIFIED` (ok=true) · `API_KEY_VALIDATION_REASON_NOT_FOUND` · `API_KEY_VALIDATION_REASON_REVOKED`. **Internal-only** — gateway maps both NOT_FOUND and REVOKED to a single 401 envelope per BR-2.4. |

## Go Repository Types (`apps/auth-svc/internal/repository`)

| Type | Story | Notes |
|------|-------|-------|
| `ApiKeyRow` | 3.2 / **5.1** (extended) | Mirrors `he_api.api_keys`. Story-5.1 extends with `Name`, `MonthlyCostCapUSD` (pgtype.Numeric), `CurrentMonthCostUSD` (pgtype.Numeric), `LastUsedAt` (pgtype.Timestamptz) — populated by `ListAPIKeysByUser`. The Story-3.2 Validate hot path leaves these as zero values (intentional carve-out — no money/name shipping across the bcrypt-compare path). `KeyHash` field is forcibly zeroed in the List path post-Scan (defence-in-depth at the struct boundary). |
| `apikey.Service` | 3.2 / **5.1** (extended) | Concrete service type at `apps/auth-svc/internal/apikey/`. Story-3.2 provided `Validate` (bcrypt hot path). Story-5.1 adds `CreateApiKey` + `ListApiKeys` + `RevokeApiKey`. Construction: `NewServiceWithMgmt(repo, tracer, logger, auditPub, sentinelStore)` for production wiring; `NewService(repo, tracer, logger)` for Validate-only test contexts (Create/Revoke will WARN-log + skip when `Audit` or `Sentinel` are nil). |
| `apikey.AuditPublisher` interface | **5.1** | Narrow Story-5.1 dep injected into `apikey.Service` for `Create` / `Revoke` audit emission. `Publish(ctx, audit.Event) error`. Production wires the same `audit.Publisher` (KafkaPublisher or NoOp) auth-svc constructs at boot. Nil-safe per BR audit-graceful-degradation (Architect Q-Spec-3 ratified). |
| `apikey.SentinelStore` interface | **5.1** | Narrow Story-5.1 dep injected into `apikey.Service` for the BR-3.8 cross-pod cache-invalidation sentinel write. `SetRevokedSentinel(ctx, apiKeyID uuid.UUID) error`. Production wires `redisclient.RevokeSentinel`. Nil-safe per BR-3.13 fail-open (lag falls back to security.md §8.2.1 5-min baseline on failure). |
| `redisclient.RevokeSentinel` | **5.1** | Production sentinel store at `apps/auth-svc/internal/redisclient/revoke_sentinel.go`. `SentinelKeyPrefix = "auth:apikey:revoked:"` (mirrors the api-gateway `middleware.SentinelKeyPrefix` constant for parity); `SentinelTTL = 300 * time.Second` (matches positive-cache TTL — bound by the cache it must outlive). |

## Go Handler Types (`apps/api-gateway/internal/handlers`)

| Type | Story | Notes |
|------|-------|-------|
| `handlers.ModelEntry` | 3.5 / **4.7** (extended) | One row in the `/v1/models` response data array. Fields: `ID`/`Object`/`Created`/`OwnedBy`/`Capabilities` (snake_case JSON tags per OpenAI canonical + He-API extension LAST per Story-4.7 BR-1.4). Promoted to a shared package if Epic 4 adapters need to import it. |
| `handlers.ModelCapabilities` | 4.7 | He-API extension surfaced on every `ModelEntry`. Fields in BR-1.2 declaration order: `Chat bool` / `Streaming bool` / `FunctionCalling bool` / `Vision bool` / `JSONMode bool` / `ContextWindowTokens int` / `MaxOutputTokens int`. NOT a `map[string]any` (BR-1.5) — typed struct lets `golangci-lint` catch missing-field omissions at compile time. |
| `handlers.PublicModelsHandler` | 4.7 | Unauthenticated handler for `GET /public/models` mirroring `/v1/models` body bytes. Constructed via `NewPublicModelsHandler(logger, snapshot []ModelEntry)`; snapshot built ONCE at boot via `BuildPublicModelsSnapshot(startedAt int64)`. Method gate: GET/HEAD allowed; other methods emit `405_method_not_allowed` envelope + `Allow: GET`. |
| `handlers.ModelsResponse` | 3.5 | Top-level `/v1/models` + `/public/models` wrapper (`object="list"` + `data []ModelEntry`). Reused by Story 4.7's unauthenticated mirror — same canonical envelope shape. |
| `handlers.ModelsHandler` | 3.5 | Handler struct (`logger` + `now` + `startedAt`). Constructed via `NewModelsHandler(logger, opts...)`; `WithModelsNow(f)` injects a deterministic clock for the BR-1.6 stable-`created` test. |
| `handlers.ModelsHandlerOption` | 3.5 | Option-function type for `ModelsHandler` (Architect Round 1 OQ4 ratified pattern; mirrors `ChatHandlerOption`). |
| `handlers.EmbeddingRequest` | 3.5 | Inbound `/v1/embeddings` JSON body. Critical: `Input json.RawMessage` for BR-2.8 dual-shape parsing (string OR array). |
| `handlers.EmbeddingResponse` | 3.5 | Top-level `/v1/embeddings` response. Field order per BR-2.9: `object` → `data` → `model` → `usage`. |
| `handlers.EmbeddingData` | 3.5 | One embedding entry. Field order: `object="embedding"` → `index` → `embedding ([]float32)`. |
| `handlers.EmbeddingUsage` | 3.5 | Mock token-count. Fields: `prompt_tokens int` + `total_tokens int`. No `completion_tokens` (embeddings have no completion dimension). |
| `handlers.EmbeddingsHandler` | 3.5 | Handler struct (`logger` + `dim`). Constructed via `NewEmbeddingsHandler(logger, opts...)`. |
| `handlers.EmbeddingsHandlerOption` | 3.5 | Option-function type for `EmbeddingsHandler`; `WithEmbeddingDim(d int)` is test-only. |
| `handlers.CreateKeyRequest` | **5.1** | Inbound `POST /v1/me/keys` body. Single field `Name string` (BR-1.2 strict-field — DisallowUnknownFields). |
| `handlers.CreateKeyResponse` | **5.1** | Outbound `POST /v1/me/keys` body. Field declaration order LOCKS JSON marshal order per BR-1.13: `APIKeyID` → `KeyPrefix` → `Name` → `Plaintext` → `CreatedAt` → `Warning`. 5.1-UNIT-031 + 5.1-INT-013 grep-assert byte ordering. |
| `handlers.KeyEntry` | **5.1** | One row in `ListKeysResponse.Data`. Pointers on nullable timestamps (`*string` for `LastUsedAt` / `RevokedAt`) so JSON renders null (not omitted / zero). Money fields are STRINGS per BR-2.10 / Architect Q-Spec-4 (Stripe precedent — preserves NUMERIC precision). `Scope json.RawMessage` for JSONB pass-through. |
| `handlers.ListKeysResponse` | **5.1** | Outbound `GET /v1/me/keys` body. `Object="list"` + `Data []KeyEntry` (NEVER nil — empty list yields `[]` per BR-2.8). |
| `handlers.RevokeKeyResponse` | **5.1** | Outbound `DELETE /v1/me/keys/{id}` body. Field order per BR-3.11: `APIKeyID` → `RevokedAt` → `WasAlreadyRevoked`. |
| `handlers.MeKeysHandler` | **5.1** | Struct hosting `HandleCreate` / `HandleList` / `HandleRevoke`. Construction: `NewMeKeysHandler(upstream, redisFunc, logger)`. Lazy Redis init via `sync.Once` (Story-3.2 cold-start pattern). |
| `handlers.CreateKeyRateLimitDecision` | **5.1** | Result of `CheckCreateKeyRateLimit`: `{Allowed bool; RetryAfter int; Count int64}`. Rate-limit ceiling `CreateKeyRateLimitMax = 10`; window `CreateKeyRateLimitWindow = 1 * time.Hour`; counter key prefix `ratelimit:apikey:create:`. Fail-open on Redis error (Story-2.3 OQ3 cascade). |

## Go Error-Envelope Types (`apps/api-gateway/internal/openaierr`)

| Type | Story | Notes |
|------|-------|-------|
| `openaierr.body` (private) | 3.6 | The §5.1.2 5-field envelope payload. Field declaration order locks JSON marshal order per BR-1.7 (Architect Round 1 OQ2): `code` → `message` → `type` → `param` → `he_request_id`. |
| `openaierr.envelope` (private) | 3.6 | Top-level wrapper `{"error": body}`. |
| `openaierr.CodeMetadata` (exported map) | 3.6 | `map[string]struct{HTTPStatus int; ErrorType string}` — single source of truth for the canonical taxonomy. Lookup derives both HTTP status + error.type. RETIRED row `501_streaming_not_implemented` is OMITTED (Architect Round 2 L1). |

## Go Rate-Limit Middleware Types (`apps/api-gateway/internal/middleware/ratelimit`)

| Type | Story | Notes |
|------|-------|-------|
| `ratelimit.Config` (exported) | **5.3** | `{Redis RedisClient; ResolveCeilings ResolveCeilingsFunc; FreeTierDefaults Ceilings; FailOpenTimeout time.Duration}`. Build once at startup; share across all bearer-protected routes. `Redis` may be nil — middleware degrades to no-op pass-through with cold-start WARN. `FailOpenTimeout <= 0` falls back to `DefaultFailOpenTimeout = 5*time.Millisecond` per Architect Q6. |
| `ratelimit.Ceilings` (exported) | **5.3** | `{QPSMax, RPMMax, TPMMax int}`. Pure value type; loaded from `RATELIMIT_FREE_TIER_{QPS,RPM,TPM}_MAX` env vars at boot per M-3 remediation. HALT-on-non-positive at boot (T0.11). |
| `ratelimit.Decision` (exported) | **5.3** | `{Allowed bool; RetryAfterSeconds int; ExhaustedAxis string}`. Returned from the atomic Lua check; `ExhaustedAxis ∈ {qps, rpm, tpm}` when `!Allowed`. |
| `ratelimit.ResolveCeilingsFunc` (exported) | **5.3** | `func(ctx context.Context, apiKeyID string) (Ceilings, error)`. MVP impl is pure constant (returns `FreeTierDefaults`) per Architect H-1 remediation. Signature preserved for forward-compat — a future 5.x Story may swap in a cached-claims-aware impl. |
| `ratelimit.RedisClient` (exported interface) | **5.3** | Narrow surface `interface { redis.Scripter }`. Production wires `*redis.Client`; tests wire `*redis.Client` from miniredis or fakes. Defined in the middleware package (not at the call site) so the unit tests can plug a thin fake without dragging the entire go-redis import surface. |
| `ratelimit.Middleware` (exported) | **5.3** | Internal struct constructed via `New(cfg, logger)`. Method `Wrap(next http.Handler) http.Handler` enforces the 3-axis check + emits 429 envelope / `Retry-After` header on denial. Method `TPMDeduct(ctx, apiKeyID, tokens int)` is the AC3 post-deduction hook (fire-and-forget; defensive on `tokens <= 0` / nil Redis). |
| `ratelimit.ErrAPIKeyIDMissing` / `ErrRedisUnavailable` (exported sentinels) | **5.3** | Errors returned from the misconfig / Redis-nil branches; callers may `errors.Is`-check via the public helper `IsAPIKeyIDMissing(err)`. |
| `handlers.TokenDeducter` (exported interface) | **5.3** | `interface { TPMDeduct(ctx context.Context, apiKeyID string, tokens int) }`. Decouples `ChatCompletionsHandler` + `EmbeddingsHandler` from the `ratelimit` package; production wires `*ratelimit.Middleware` (its method satisfies the interface implicitly). Constructor options: `handlers.WithTokenDeducter(d)` + `handlers.WithEmbeddingTokenDeducter(d)`. Default fallback is `handlers.nopTokenDeducter{}` (private). |

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
- **4.2** — Qwen (通义千问) adapter (second real model adapter):
  - **NEW shared Go module** `packages/adapter-usage/` (top-level — sibling to `packages/go-observability/`). Exports: `NormalisedUsage` struct (`PromptTokens` / `CompletionTokens` / `TotalTokens`), `ErrUsageConstraintViolation` sentinel, `ValidateInvariants(prompt, completion, total int) error` helper. Architect Round 1 OQ-4.2-3a partial-lift: the `Normaliser` INTERFACE itself stays vendor-local (parametric on each vendor's `RawUsage`). Story-4.1 `apps/adapters/deepseek/internal/usage/` is back-compat-retrofit: `NormalisedUsage` is now a type-alias and `ErrUsageConstraintViolation` is a re-export of the shared symbols — existing callers compile unchanged.
  - **New adapter packages** (`apps/adapters/qwen/`):
    - `internal.Service` — `AdapterServiceHandler` implementing `Chat` (server-streaming) with `NewService(client, logger, boundModelIDs []string) *Service` constructor accepting the BR-1.10 multi-model-id list (`["qwen-max", "qwen-plus"]` default). Non-streaming branch emits ONE terminal chunk; streaming branch emits N chunks with LAST carrying `usage` (BR-2.4 REUSE Story-4.1). New helper `Service.BoundModelIDs() []string` returns a defensive copy. Architect Round 1 m1: log records carry `upstream_request_id` (DashScope-generated) when the response header is present.
    - `internal/upstream.Client` — HTTP/2-preferred (`net/http.Transport{ForceAttemptHTTP2: true}`) HTTPS client over `https://dashscope.aliyuncs.com` per Architect Round 1 OQ-4.2-5 (Qwen-specific divergence from Story-4.1 OQ7 forced-HTTP/2 — allows ALPN HTTP/1.1 fallback for Aliyun gateway endpoints that lack h2). `NewClient(baseURL, apiKey, timeout)` constructor; default timeout 60s (BR-1.8 REUSE).
    - `internal/upstream.ErrorKind` enum — REUSE Story-4.1 enum + `rate_limit_throttle` (NEW per BR-4.4 + OQ-4.2-4) replacing DeepSeek's `quota_exhausted` for the upstream-429 case so oncall paging routes DashScope rate-limit incidents distinctly from other 4xx.
    - `internal/upstream.ClassifyError(err)` / `ClassifyHTTPStatus(status)` / `UpstreamError{Kind, Status, Cause}` — per-vendor replicas of Story-4.1's classifier (Architect Round 1 L2 ratification — `internal/` packages are NOT cross-importable across `apps/adapters/<vendor>/` module boundaries; acceptable code duplication).
    - `internal/upstream.Decoder` — strict-RFC SSE decoder REUSING Story-4.1 pattern byte-for-byte (Architect Round 1 L1 simplification — DashScope compat-mode SSE matches OpenAI shape; no Qwen-native heartbeat carve-out needed).
    - `internal/upstream.{RawUsage, ChatRequestJSON, StreamOptionsJSON, ChatMessage, ChatChoiceJSON, ChatDeltaJSON, ChatResponseJSON, ChatChunkJSON}` — wire-shape types (compat-mode == OpenAI shape).
    - `internal/usage.Normaliser` — vendor-local interface `Normalise(raw upstream.RawUsage) (NormalisedUsage, error)`. The Qwen implementation `qwenNormaliser` is identity-mapping (OQ-4.2-3 cascade) consuming the lifted `packages/adapter-usage.ValidateInvariants` helper. `NormalisedUsage` is type-aliased to the shared package; `ErrUsageConstraintViolation` is re-exported.
  - **Gateway registry expansion** (`apps/api-gateway/internal/adapterclient/`):
    - NEW constants `QwenMaxModelID = "qwen-max"`, `QwenPlusModelID = "qwen-plus"`, `QwenAdapterEndpointEnv = "QWEN_ADAPTER_ENDPOINT"` (Architect Round 1 OQ-4.2-6 ratification — model-family naming).
    - `LoadFromEnv()` EXTENDED to read `QWEN_ADAPTER_ENDPOINT` and populate BOTH `qwen-max` AND `qwen-plus` entries.
    - `NewRegistry` REFACTORED per Architect Round 1 M2 endpoint-dedup ruling — model ids pointing to the same endpoint URL share a single underlying `connectClientHandle` (preserves Story-4.1 single-endpoint behaviour bit-for-bit; enables HTTP/2 connection pool reuse for multi-model-id-per-service dispatches).
    - Story-4.1 `DeepSeekModelID` / `DeepSeekEndpointEnv` constants + `Resolve(modelID) (ClientHandle, ok)` signature + `connectClientHandle` type UNCHANGED.
  - **Promotion rule (Architect Round 1 ratified)**: Stories 4.3-4.6 add ENTRIES to the same `adapterclient.Registry`, supply per-vendor `Normaliser` implementations, and consume the lifted `packages/adapter-usage/` shape; they DO NOT invent parallel mechanisms.
- **4.3** — Kimi (Moonshot AI) adapter (third real model adapter):
  - **New adapter packages** (`apps/adapters/kimi/`):
    - `internal.Service` — `AdapterServiceHandler` implementing `Chat` (server-streaming) with `NewService(client, logger, boundModelIDs []string) *Service` constructor accepting the BR-1.10 multi-model-id list (`["moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"]` default — N=3 extends Story-4.2's N=2 precedent). Non-streaming branch emits ONE terminal chunk; streaming branch emits N chunks with LAST carrying `usage` (BR-2.4 REUSE Stories 4.1/4.2). `Service.BoundModelIDs()` returns a defensive copy. m1 cascade: log records carry `upstream_request_id` (Moonshot-generated) when the response header is present.
    - `internal/upstream.Client` — HTTP/2-preferred (`net/http.Transport{ForceAttemptHTTP2: true}`) HTTPS client over `https://api.moonshot.cn` per Architect Round 1 OQ-4.3-3 cascade-default from Story-4.2 OQ-4.2-5 (allows ALPN HTTP/1.1 fallback). `NewClient(baseURL, apiKey, timeout)` constructor; default timeout 60s (BR-1.8 REUSE).
    - `internal/upstream.ErrorKind` enum — REUSE Story-4.2 set + NEW `context_length_exceeded` (BR-4.5 Kimi-specific) for the upstream 400 invalid_request_error body shape. The status-only `ClassifyHTTPStatus(status)` is preserved; a NEW body-aware helper `ClassifyMoonshotErrorBody(status, body) ErrorKind` short-circuits on the canonical Moonshot context-length JSON body shape per Architect Round 1 m-1 ratification. `ClassifyError` / `UpstreamError{Kind, Status, Cause}` follow Story-4.2 patterns verbatim (per-vendor replicas per Story-4.2 L2 boundary).
    - `internal/upstream.Decoder` — strict-RFC SSE decoder REUSING Story-4.1/4.2 pattern byte-for-byte (Moonshot OpenAI-compat SSE matches OpenAI shape).
    - `internal/upstream.{RawUsage, ChatRequestJSON, StreamOptionsJSON, ChatMessage, ChatChoiceJSON, ChatDeltaJSON, ChatResponseJSON, ChatChunkJSON}` — wire-shape types (OpenAI shape).
    - `internal/usage.Normaliser` — vendor-local interface `Normalise(raw upstream.RawUsage) (NormalisedUsage, error)`. The Kimi implementation `kimiNormaliser` is identity-mapping (OQ-4.3-1 cascade from OQ-4.2-3) consuming the lifted `packages/adapter-usage.ValidateInvariants` helper. `NormalisedUsage` is type-aliased to the shared package; `ErrUsageConstraintViolation` is re-exported.
  - **Gateway registry expansion** (`apps/api-gateway/internal/adapterclient/`):
    - NEW constants `KimiV18kModelID = "moonshot-v1-8k"`, `KimiV132kModelID = "moonshot-v1-32k"`, `KimiV1128kModelID = "moonshot-v1-128k"`, `KimiAdapterEndpointEnv = "KIMI_ADAPTER_ENDPOINT"` (Architect Round 1 OQ-4.3-2 ratification — BRAND-name naming).
    - `LoadFromEnv()` EXTENDED to read `KIMI_ADAPTER_ENDPOINT` and populate ALL THREE Kimi model-id entries.
    - The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is UNCHANGED; verified to scale monomorphically to N=3 via 4.3-UNIT-011 `assert.Same(h_8k, h_32k); assert.Same(h_32k, h_128k)` chain (Architect Round 1 OQ-4.3-5 policy: future N-size-per-vendor stories MUST assert.Same across ALL N handles).
    - Story-4.1 `DeepSeekModelID` / `DeepSeekEndpointEnv` + Story-4.2 `QwenMaxModelID` / `QwenPlusModelID` / `QwenAdapterEndpointEnv` constants UNCHANGED.
  - **Promotion rule (Architect Round 1 ratified)**: Stories 4.4-4.6 add ENTRIES to the same `adapterclient.Registry`, supply per-vendor `Normaliser` implementations, declare per-vendor `errors.go` replicas (with vendor-specific ErrorKind additions where warranted, e.g., Kimi's `ErrorKindContextLengthExceeded`), and consume the lifted `packages/adapter-usage/` shape; they DO NOT invent parallel mechanisms.
- **4.4** — GLM (Zhipu AI) adapter (fourth real model adapter; SINGLE-model-id-per-vendor N=1 degenerate case of the Story-4.2/4.3 multi-model-id pattern):
  - **New adapter packages** (`apps/adapters/glm/`):
    - `internal.Service` — `AdapterServiceHandler` implementing `Chat` (server-streaming) with `NewService(client, logger, boundModelIDs []string) *Service` constructor accepting the BR-1.10 single-id list (`["glm-4"]` default — N=1 collapse from Story-4.3's N=3). Non-streaming branch emits ONE terminal chunk; streaming branch emits N chunks with LAST carrying `usage` (BR-2.4 REUSE Stories 4.1/4.2/4.3). `Service.BoundModelIDs()` returns a defensive copy. m1 cascade: log records carry `upstream_request_id` (Zhipu-generated) when the response header is present.
    - `internal/upstream.Client` — HTTP/2-preferred (`net/http.Transport{ForceAttemptHTTP2: true}`) HTTPS client over `https://open.bigmodel.cn` per Architect Round 1 OQ-4.4-4 cascade-default from Story-4.2 OQ-4.2-5 (allows ALPN HTTP/1.1 fallback). `NewClient(baseURL, apiKey, timeout)` constructor; default timeout 60s (BR-1.8 REUSE).
    - `internal/upstream.ErrorKind` enum — REUSE Story-4.2 set verbatim per OQ-4.4-6 (Story-4.3 `ErrorKindContextLengthExceeded` body-aware classifier NOT cascaded — Zhipu v4 has no documented body-aware failure shape). Status-only `ClassifyHTTPStatus(status)` is the sole classification surface; `ClassifyError` / `UpstreamError{Kind, Status, Cause}` follow Story-4.2 patterns verbatim (per-vendor replicas per Story-4.2 L2 boundary). Additive `ErrorKind` entries permitted without Architect Round 2 should a Zhipu-specific failure shape surface (Story-4.3 m-1 precedent).
    - `internal/upstream.Decoder` — strict-RFC SSE decoder REUSING Story-4.1/4.2/4.3 pattern byte-for-byte (Zhipu v4 OpenAI-compat SSE matches OpenAI shape).
    - `internal/upstream.{RawUsage, ChatRequestJSON, StreamOptionsJSON, ChatMessage, ChatChoiceJSON, ChatDeltaJSON, ChatResponseJSON, ChatChunkJSON}` — wire-shape types (OpenAI shape per OQ-4.4-3 identity cascade).
    - `internal/usage.Normaliser` — vendor-local interface `Normalise(raw upstream.RawUsage) (NormalisedUsage, error)`. The GLM implementation `glmNormaliser` is identity-mapping (OQ-4.4-3 cascade from OQ-4.2-3) consuming the lifted `packages/adapter-usage.ValidateInvariants` helper. `NormalisedUsage` is type-aliased to the shared package; `ErrUsageConstraintViolation` is re-exported.
  - **Gateway registry expansion** (`apps/api-gateway/internal/adapterclient/`):
    - NEW constants `GLMModelID = "glm-4"`, `GLMAdapterEndpointEnv = "GLM_ADAPTER_ENDPOINT"` (Architect Round 1 OQ-4.4-2 ratification — BRAND-name naming per Story-4.2 OQ-4.2-6 cascade text; brand wins over company name `zhipu`).
    - `LoadFromEnv()` EXTENDED to read `GLM_ADAPTER_ENDPOINT` and populate the SINGLE `glm-4` entry.
    - The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is UNCHANGED; at N=1 the byEndpoint map has one entry and the dedup branch is a clean NO-OP (no `assert.Same` chain to verify — 4.4-UNIT-011 documents the SKIPPED-branch test explicitly per Architect Round 1 R9 ratification).
    - Story-4.1/4.2/4.3 constants UNCHANGED.
  - **Promotion rule (Architect Round 1 ratified)**: Stories 4.5-4.6 add ENTRIES to the same `adapterclient.Registry`, supply per-vendor `Normaliser` implementations, declare per-vendor `errors.go` replicas, and consume the lifted `packages/adapter-usage/` shape; they DO NOT invent parallel mechanisms. Story 4.4 serves as the canonical N=1 case template (the simplest Epic-4 vendor onboarding); future single-size vendor Stories MAY cite it to compress cross-Story rationale.
- **4.5** — Doubao (Volcengine Ark v3) adapter (fifth real model adapter; FIRST Epic-4 non-identity translate; BR-1.10 N=2 RESTORATION from Story-4.4's N=1 degenerate):
  - **New adapter packages** (`apps/adapters/doubao/`):
    - `cmd/server/main.go` — Connect-RPC bootstrap; same shape as Story-4.4 `apps/adapters/glm/cmd/server/main.go` with `glm`→`doubao` substitution PLUS startup-validation slog (`event=adapter_startup_validation`) for the NEW endpoint-id env vars per Architect Round 1 OQ-4.5-4 rollover note. `defaultBoundModelIDs = []string{"doubao-pro", "doubao-lite"}` per BR-1.10 N=2.
    - `internal.{Service, NewService}` — Connect-RPC `AdapterServiceHandler.Chat` implementation REUSING Story-4.4 glm `Service` shape PLUS: (a) injected `*upstream.EndpointMap` (test-injectability per Architect Round 1 m-1); (b) BR-1.12 fail-fast probe at the top of `ChatInto` (short-circuits with `connect.CodeFailedPrecondition` BEFORE the upstream HTTPS call if `endpoint_map.Lookup` errors); (c) BR-1.11 per-request closure capture of `req.Model` for inbound back-translate (NO `ReverseLookup` per Architect Round 1 l-1); (d) `connectCodeForKind` mapper EXTENDED with `ErrorKindEndpointIDNotConfigured → CodeFailedPrecondition` case.
    - `internal/upstream.NewClient` — same as Story-4.4 glm (HTTP/2-preferred + ALPN fallback `http.Transport{ForceAttemptHTTP2:true}`).
    - `internal/upstream.{TranslateChatRequest, TranslateChatResponse, TranslateChatChunk}` — NEW bidirectional `model`-field rewrite per OQ-4.5-3 + BR-1.7.f + BR-1.11 + BR-3.8. Outbound: `req.Model` → `endpoint_map.Lookup(req.Model)`; inbound: `resp.Model` / `chunk.Model` → `friendlyModelID` (sourced from `req.Model` closure). All non-`model` fields identity-mapped per OQ-4.5-3 cascade from OQ-4.2-3.
    - `internal/upstream.{EndpointMap, New, NewFromOS, Lookup, ErrUnsupportedModel}` — **NEW Story-4.5-specific module** at `internal/upstream/endpoint_map.go`. Test-injectable `New(envProvider)` per Architect Round 1 m-1 refactor (replaces the brittle package-level `var x = map{...os.Getenv}` pattern). `Lookup` returns `ErrUnsupportedModel` when the env var is unset/empty per BR-1.12 fail-fast. NO `ReverseLookup` per Architect Round 1 l-1 (closure pattern is rotation-safe).
    - `internal/upstream.ErrorKind` enum — REUSE Story-4.4 set verbatim per OQ-4.5-6 (Story-4.3 `ErrorKindContextLengthExceeded` body-aware classifier NOT cascaded; Volcengine Ark v3 has no documented body-aware failure shape). NEW `ErrorKindEndpointIDNotConfigured` per BR-4.6 — surfaced from `ClassifyError` when err wraps `ErrUnsupportedModel`, NOT from an HTTP status. `UpstreamError{Kind, Status, Cause}` per-vendor replica per Story-4.2 L2 boundary.
    - `internal/upstream.Decoder` — strict-RFC SSE decoder REUSING Story-4.1/4.2/4.3/4.4 pattern byte-for-byte. **Vendor-neutral discipline (BR-2.3)**: the decoder emits raw `model` field (Volcengine endpoint id echo); per-chunk BR-1.11 back-translate happens OUTSIDE the decoder in `internal.Service.chatStreaming` via `TranslateChatChunk`.
    - `internal/upstream.{RawUsage, ChatRequestJSON, StreamOptionsJSON, ChatMessage, ChatChoiceJSON, ChatDeltaJSON, ChatResponseJSON, ChatChunkJSON}` — wire-shape types (OpenAI shape per OQ-4.5-3 identity cascade for non-`model` fields).
    - `internal/usage.Normaliser` — vendor-local interface `Normalise(raw upstream.RawUsage) (NormalisedUsage, error)`. The Doubao implementation `doubaoNormaliser` is identity-mapping (OQ-4.5-3 cascade from OQ-4.2-3 for the `usage` field) consuming the lifted `packages/adapter-usage.ValidateInvariants` helper. `NormalisedUsage` is type-aliased to the shared package; `ErrUsageConstraintViolation` is re-exported.
  - **Gateway registry expansion** (`apps/api-gateway/internal/adapterclient/`):
    - NEW constants `DoubaoProModelID = "doubao-pro"`, `DoubaoLiteModelID = "doubao-lite"`, `DoubaoAdapterEndpointEnv = "DOUBAO_ADAPTER_ENDPOINT"` (Architect Round 1 OQ-4.5-2 ratification — BRAND-name naming per Story-4.2 OQ-4.2-6 cascade text; brand wins over platform `volcengine` + company `bytedance`).
    - `LoadFromEnv()` EXTENDED to read `DOUBAO_ADAPTER_ENDPOINT` and populate BOTH `doubao-pro` AND `doubao-lite` entries.
    - The Story-4.2 M2 `NewRegistry` endpoint-dedup branch RETURNS TO ACTUALLY-EXECUTING form after Story-4.4's N=1 detour: 4.5-UNIT-013 `assert.Same(h_pro, h_lite)` chain verifies the byEndpoint map dedups both entries to ONE underlying `ClientHandle` (REUSE Story-4.2 4.2-UNIT-013 pattern verbatim per OQ-4.3-5 cascade-locked policy).
    - Story-4.1/4.2/4.3/4.4 constants UNCHANGED; cross-vendor regression verified at 4.5-UNIT-013 + 4.5-INT-009 (five-vendor coexistence).
  - **NEW Kubernetes ConfigMap `doubao-endpoint-ids`**: Story-4.5-specific resource declaring `DOUBAO_PRO_ENDPOINT_ID` + `DOUBAO_LITE_ENDPOINT_ID` keys (values templated from `values.yaml` `.endpointIDs.pro` + `.endpointIDs.lite`). Mounted via `envFrom: configMapRef` on the adapter Deployment. Per OQ-4.5-4 ratification: endpoint ids identify resources (parameter-like metadata), not authorise access; API key stays in Vault.
  - **Promotion rule (Architect Round 1 ratified)**: Story 4.6 (Ernie / Baidu Qianfan) inherits the **non-identity translate** precedent Story 4.5 establishes here, but with a DIFFERENT transform shape (Baidu Qianfan maps the model identifier to a URL path segment, not a body field). When Story 4.6 is drafted, SM should reference Story 4.5 OQ-4.5-3 ratification as the precedent for "non-identity translate is allowed and follows the per-vendor-translate.go pattern" but expect a different concrete implementation (URL-template rewrite vs. body-field static-lookup-map).
  - **Historical note (Story-4.6 Architect Round 1 OQ-4.6-1, 2026-05-19)**: Story 4.6 ratified **option (a) Qianfan v2 OpenAI-compat** — the URL-path-segment hint above anticipated Baidu's legacy `aip.baidubce.com` endpoint shape and is superseded by Qianfan v2 GA (post the original architecture pass). Story 4.6's `translate.go` is therefore identity-mapping, NOT URL-path-segment. The "non-identity translate is allowed" precedent Story 4.5 established remains valid for future vendor onboardings that face URL-path-segment endpoints (none in Epic 4 — the precedent is dormant until a future vendor warrants it).
- **4.6** — Ernie (Baidu Qianfan v2 OpenAI-compat) adapter (sixth and FINAL Epic-4 real model adapter; closes the six-vendor cross-vendor regression matrix):
  - **NEW Go module** `apps/adapters/ernie/`:
    - `internal/upstream.RawUsage`, `ChatRequestJSON`, `StreamOptionsJSON`, `ChatMessage`, `ChatChoiceJSON`, `ChatDeltaJSON`, `ChatResponseJSON`, `ChatChunkJSON` — wire-shape types REUSING Story-4.4 glm `types.go` byte-for-byte (Qianfan v2 emits OpenAI-canonical JSON; identity-mapping cascade per OQ-4.6-3). Per Architect Round 1 L2 ratification: per-vendor `types.go` REPLICA (not lifted) since `internal/` packages are not cross-importable across `apps/adapters/<vendor>/` module boundaries.
    - `internal/upstream.ErrorKind`, `ClassifyError`, `ClassifyHTTPStatus`, `UpstreamError{Kind, Status, Cause}` — per-vendor classifier REUSING Story-4.4 enum set verbatim per OQ-4.6-6 (NO Story-4.3 body-aware classifier; NO Story-4.5 `ErrorKindEndpointIDNotConfigured` — no endpoint-id lookup; NO option-(b) `ErrorKindAccessTokenRefreshFailed` — option (b) REJECTED). Additive entries permitted without Architect Round 2 should Baidu-specific failure shapes surface (Story-4.3 m-1 precedent).
    - `internal/upstream.translate.go` — IDENTITY-MAPPING per OQ-4.6-3 cascade verbatim. URL path `/v2/chat/completions` per OQ-4.6-1. `Authorization: Bearer $ERNIE_UPSTREAM_API_KEY` (Qianfan IAM key). The Story-4.5 `endpoint_map.go` + `TranslateChatResponse` / `TranslateChatChunk` mechanisms are N/A.
    - `internal/usage.Normaliser` (vendor-local interface) + `ernieNormaliser` struct + `NewErnie()` constructor — identity-mapping implementation consuming the lifted `packages/adapter-usage` types (Story-4.2 M1 lift cascade): `NormalisedUsage = sharedusage.NormalisedUsage` type alias, `ErrUsageConstraintViolation` re-export, `sharedusage.ValidateInvariants(prompt, completion, total)` for BR-3.3 invariant enforcement.
  - **Gateway registry expansion** (`apps/api-gateway/internal/adapterclient/registry.go`):
    - NEW constants `ErnieModelID = "ernie-4.0"`, `ErnieAdapterEndpointEnv = "ERNIE_ADAPTER_ENDPOINT"` (Architect Round 1 OQ-4.6-2 ratification — BRAND-name naming per Story-4.2 OQ-4.2-6 cascade text; brand wins over family marketing name `wenxin` / 文心 and company name `baidu`; final Epic-4 cascade closure).
    - `LoadFromEnv()` EXTENDED to read `ERNIE_ADAPTER_ENDPOINT` and populate the SINGLE `ernie-4.0` entry (RESTORES the Story-4.4 N=1 degenerate pattern after Story-4.5's N=2 RESTORATION).
    - The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is a clean NO-OP at N=1 (byEndpoint map has one entry; no `assert.Same` chain to verify — 4.6-UNIT-011 documents the SKIPPED-branch test explicitly per Story-4.4 R9 ratification cascade).
    - Story-4.1/4.2/4.3/4.4/4.5 constants UNCHANGED; SIX-vendor cross-vendor regression verified at 4.6-INT-009 (`TestRegistry_AllSixVendorsCoRegistered_NoCrossVendorShadowing` — the FIRST integration test that exercises ALL ten model-id entries without shadowing; closes the Epic-4 vendor matrix).

- **4.7** — Capability matrix + public page:
  - **New Go types** (`apps/api-gateway/internal/handlers/`):
    - `ModelCapabilities` (7 fields per BR-1.2 declaration order; non-pointer per BR-1.1 compile-time non-null guarantee; typed struct over `map[string]any` per BR-1.5).
    - `ModelEntry` EXTENDED with `Capabilities ModelCapabilities` appended LAST (BR-1.4 He-API extension placement convention).
    - `PublicModelsHandler` (constructor injection per OQ-4.7-5; emits the same `ModelsResponse` envelope as bearer-gated `/v1/models` from a pre-built snapshot).
    - `BuildPublicModelsSnapshot(startedAt int64) []ModelEntry` helper — single source of truth shared between bearer + public handlers.
    - `capabilitiesByModelID` package-private 11-row map per OQ-4.7-3 ratification (in-memory; OQ-4.7-2 option B — DB seed deferred).
  - **New TypeScript types** (`apps/console/lib/api/public-models.ts`):
    - `ModelCapabilitiesSchema` / `ModelCapabilities` — Zod schema mirroring the Go-side struct verbatim.
    - `ModelEntrySchema` / `ModelEntry` — Zod schema with `capabilities` as the trailing field (BR-1.4 convention preserved on the frontend).
    - `PublicModelsResponseSchema` / `PublicModelsResponse` — Top-level envelope schema.
    - `fetchPublicModels()` — never-throws fetcher; failures collapse to an empty matrix for the fallback banner path.
  - **New React component prop types**:
    - `CapabilityMatrix(props: { models: ModelEntry[] })` — desktop table + mobile card layout via Tailwind `md:` breakpoint.
    - `CapabilityBadge(props: { present: boolean; label: string })` — ✓/✗ with paired `sr-only` text for WCAG 2.1 AA.
  - **New i18n union** (`packages/i18n-keys/src/models.ts`): `ModelsKeys` literal union covering 36 keys in the `models` namespace.
  - **New envelope code**: `openaierr.CodeMetadata["405_method_not_allowed"] = {HTTPStatus: 405, ErrorType: "invalid_request_error"}` per OQ-4.7-6 ratification.
  - **New middleware package**: `apps/api-gateway/internal/middleware/cors/` exports `cors.PublicCORS(next)` + `cors.PublicPathPrefix` + `cors.PublicOriginWildcard` + `cors.PublicAllowedMethods` constants.

- **5.3** — Rate limit middleware Go types (per-Story summary; full table above in "Go Rate-Limit Middleware Types"):
  - `ratelimit.Config`, `ratelimit.Ceilings`, `ratelimit.Decision`, `ratelimit.ResolveCeilingsFunc`, `ratelimit.RedisClient`, `ratelimit.Middleware`, `ratelimit.ErrAPIKeyIDMissing`, `ratelimit.ErrRedisUnavailable`.
  - `handlers.TokenDeducter` interface + `nopTokenDeducter` (private) — decouples handlers from the ratelimit package; constructor options `handlers.WithTokenDeducter` + `handlers.WithEmbeddingTokenDeducter`.

## Test Infrastructure Types (Story 4.8)

Story 4.8 introduces test-only shared types under `apps/api-gateway/tests/`. These are NOT runtime types — they live in the pytest collection root only. Stories 4.9+ adding vendors update the listed constants (BR-2.8 single-source-of-truth) without touching runtime code.

- **Module `_protocol_invariants`** (`apps/api-gateway/tests/_protocol_invariants.py`):
  - Constants: `CHATCMPL_ID_RE` (compiled `^chatcmpl-`), `REQUEST_ID_RE` (compiled `^req_[0-9a-f]{12}$`), `CANONICAL_FINISH_REASONS` (frozenset of `{stop, length, tool_calls, content_filter}`), `CANONICAL_ERROR_TYPES` (frozenset of `{invalid_request_error, server_error}`).
  - Helpers (5): `assert_chat_completion_shape(response, expected_model)` / `assert_chat_completion_chunk_shape(chunk, expected_model, *, is_bootstrap=False, is_terminal=False)` (MED-2 three-way, MED-5 conditional usage) / `assert_model_entry_shape(entry, *, expected_id_set=None)` (MED-3 lifted) / `assert_embedding_shape(response, expected_model)` / `assert_error_envelope_shape(response_body, expected_code, expected_status)`.
- **Module `conftest`** (`apps/api-gateway/tests/conftest.py`):
  - Constant: `EXPECTED_VENDOR_MODELS` — 10-tuple of vendor model ids the M-2 compensating-control fixture probes for in the `/v1/models` catalogue.
  - Fixtures: `gateway_url` / `api_key` / `openai_client` (function-scoped per cross-vendor independence rule) / `httpx_client` / `expected_vendor_models_present` (session-scoped probe).
- **Module `openai_sdk_protocol_completeness_test`** (`apps/api-gateway/tests/openai_sdk_protocol_completeness_test.py`):
  - Constants: `MATRIX_MODELS: list[str]` (10 entries — BR-2.8 single-source-of-truth; appending here auto-grows the matrix), `MATRIX_STREAM: list[bool] = [False, True]`, `MATRIX_CELLS: list[tuple[str, bool]]` (20 entries), `VIRTUAL_HE_ROUTER_MODELS` (3-tuple of `he-router-*` ids EXCLUDED per OQ-4.8-6).

## Cost-Cap Types (Story 5.4)

- **Proto** (`packages/proto/he/{notification,auth}/v1`): `EmailTemplate{=7,=8}`, `ThresholdLevel{UNSPECIFIED,WARNING_80,TRIPPED}`, `NotifyMonthlyCapThreshold{Request,Response}`, `GetCapNotificationContext{Request,Response}`. Vendored stubs regenerated via `buf generate`.
- **Gateway** (`apps/api-gateway/internal`): NEW `notifyclient` package (`Client`, `Threshold{Warning80,Tripped}`, `NotifyCapThresholdAsync`); keypolicy `CapTrippedSentinel` interface + `RedisCapSentinel`, `CapThresholdNotifier` interface, named constants `MonthlyCapWarningThresholdRatio=0.80` / `MonthlyCapTrippedThresholdRatio=1.00`, Redis prefix constants.
- **auth-svc** (`apps/auth-svc/internal`): `repository.CapNotificationContext` + `LookupCapNotificationContext` + `ResetMonthlyCosts`; `handlers.GetCapNotificationContext`; `redisclient.PurgeMonthlyState` + `PurgeCounts` (cluster-aware via `ForEachMaster`, m-1) + cap-prefix constants; NEW binary `cmd/monthly-cost-reset`.
- **notification-svc** (`apps/notification-svc/internal`): NEW `authsvcclient` package (`Client`, `CapContext`); `handlers.CapThresholdServer` + `NotifyMonthlyCapThreshold` + `resolveDisplayName` (BR-2.5) + `CapDedupeStore`/`RedisDedupeStore`; `templates.TemplateMonthlyCapWarning`/`TemplateMonthlyCapTripped` (40 template files, 10 locales × 2 templates × {html,txt}).
- **console i18n**: NEW `account.keys.cap.email.{warning,tripped}.{subject,description}` across 10 locales (en + zh-CN full; 8 `[en-pending]`).

## Console Keys Page (Story 5.5)

- **No new models / shared types.** The Console Keys page CONSUMES the Story-4.7 `GET /public/models` catalogue (`fetchPublicModels` → model ids) to populate the ConfigureKeyDrawer model-scope selector — sourced server-side and passed as a prop (the gateway is server-only; no client cross-origin fetch). The wire types it reads (`KeyEntry`, `KeyConfigPatch`, list/create/update/revoke responses) are the shipped Story-5.1/5.2 Zod schemas in `apps/console/lib/api/me-keys.ts`; Story 5.5 adds only the `readScope()` reader, the `PLAINTEXT_RE` guard, and tightens `monthly_cost_cap_usd` to the canonical money regex. **Architect Q-E5 OVERRULED** — no `cap_tripped` field added to `KeyEntry`.

## Routing Core + shared models-catalogue (Story 6.1)

- **NEW shared Go module** `github.com/he-api/he-api/packages/models-catalogue` (Q-A option (a) lift): `ModelEntry{ID, DisplayName, Vendor, Capabilities}`, `Capabilities{Chat, Streaming, FunctionCalling, Vision, JSONMode, ContextWindowTokens, MaxOutputTokens}` (no JSON tags — pure domain type), `Catalogue` (immutable, `List`/`Find`/`Len`), `Registry`/`ModelSeed`, `NewFromRegistry` (panics on 1:1-invariant violation both directions + empty-ID), `DefaultRegistry` (11 rows, lifted verbatim from Story-4.7 — Architect OQ1), `DefaultCatalogue`. The Story-4.7 BR-1.3 1:1 invariant moved INTO this package (the gateway's local `init()` check removed per BR4-3). Consumed by `apps/api-gateway/internal/handlers` (reconstructs `modelsCatalogue`+`capabilitiesByModelID` via `buildGatewayCatalogue` — wire shape byte-identical) and `apps/routing-svc/internal/catalogue`.
- **NEW routing-svc Go types** (`apps/routing-svc/internal`): `engine.Strategy` interface (cascade-locked `Select(ctx, []ModelEntry, SelectionHints) (ModelEntry, error)` BR2-1), `engine.SelectionHints{UserID, RequestedModel, ABModels}`, `engine.ModelEntry` (alias of `modelscatalogue.ModelEntry`), exported sentinels `engine.ErrNoCandidates`/`engine.ErrUnknownStrategy`, `engine.Engine` + `NewEngine` + `Decide`; `strategy.{Quality,Cost,Latency,Default}` (+ `DefaultStrategies()` registry); `handler.RoutingServer`; `server.Server`/`Options`. Proto Go types `routingv1.{Strategy, SelectModelRequest, SelectModelResponse}`.

## Auto-recharge + Saved Methods + Invoices (Story 7.7)

- **NEW proto Go types** — `paymentv1.{ChargeOffSession,CreateSetupIntent,RetrievePaymentMethod}{Request,Response}` (+ `ChargeOffSessionRequest.OrderId` field 6); `billingv1.{SetAutoRecharge,SavePaymentMethod,ListPaymentMethods,DeletePaymentMethod,ListInvoices,GetInvoicePdf}{Request,Response}` (flat-scalar: list payloads as JSON strings `MethodsJson`/`InvoicesJson`, PDF as `[]byte Pdf`). Hand-authored pb.go (descriptorpb-built rawDesc; validated by init-parse + round-trip tests).
- **NEW billing-svc Go types** (`apps/billing-svc/internal`): `paymentmethod.{Store, Method, DB}`; `autorecharge.{Trigger, Result, Outcome, AlertKind, DB, Locker, TokenSource, Charger}`; `lowbalance.{Alerter, Notifier, Redis}`; `invoice.{Store, Generated, Invoice, Owned, RenderData, LineItem, Uploader, DB}` (+ `PeriodBounds`, `ObjectKey`, `RenderInvoicePDF`); `paymentclient.Charger`. `ledger.Ledger.SetPostDeduction` hook field.
- **NEW payment-svc Go types**: `provider.OffSessionProvider` interface + `ErrOffSessionUnsupported`; Stripe `ChargeOffSession`/`CreateSetupIntent`/`RetrievePaymentMethod` methods.
- **NEW gateway types** (`apps/api-gateway/internal/handlers`): `BillingWriteHandler` gains `UpdateAutoRecharge`/`CreatePaymentMethod`/`ConfirmPaymentMethod`/`ListPaymentMethods`/`DeletePaymentMethod`/`ListInvoices`/`GetInvoicePDF` methods + request structs `autoRechargeRequest`/`confirmPaymentMethodRequest`.
- **NEW notification template slugs**: `templates.{TemplateLowBalance, TemplateLowBalanceFailed}`.

## Subscription Tier Catalogue + Proto (Story 7.8)

- **NEW shared module `packages/plan-catalogue/`** (Go, mirrors `packages/models-catalogue/`; pure domain — no JSON tags on the wire-facing types except string-decimal money):
  - `PlanKey` (`free`|`pro`|`team`|`enterprise`); `Plan{Key, DisplayName, PriceUSD (string-decimal), Rank, Entitlement}`; `Entitlement{Plan, RPM, TPM, QPS, MonthlyIncludedCreditUSD (string-decimal), MonthlyQuotaUSD (string-decimal), Features []string}`; `SandboxCeiling{RPM, TPM, QPS, MonthlyQuotaUSD}`; `Catalogue` (List/Find/Entitlements/Rank/Sandbox/Has).
  - Invariants: panic-at-construction 1:1 plan↔entitlement (both directions) + strict-monotonic rank (4.7/6.1 precedent); `ErrUnknownPlan` is lookup-time (never panic). `DefaultCatalogue` resolved at init. Money = quoted JSON string (Q-Spec-4).
  - Consumed by: gateway `internal/entitlement` (enforcement) + billing-svc `internal/subscription` + grpc handlers (`GetSubscription`/`GetEntitlements`/`ChangePlan`) + gateway `handlers.SubscriptionHandler` (`GET /v1/billing/plans`).
- **Gateway entitlement contract**: `entitlement.Snapshot{Plan, Status}` (JSON at Redis `entitlement:user:{id}` — billing-svc SOLE writer, gateway read-only). `entitlement.Resolve` (fail-safe-LOW) + `Compose` (min(plan,sandbox)) + `Resolver` (→ `ratelimit.Ceilings`). `featureflag.Reader` (beta_mode read).
- **Proto (additive — `buf breaking: FILE` not crossed; round-trip GREEN)**: `he.payment.v1` += `UpdateProviderSubscription{Request,Response}` + `PaymentEvent.plan` (field 11); `he.billing.v1` += `GetSubscription{Request,Response}`, `ChangePlan{Request,Response}` (bool cancel/deferred), `GetEntitlements{Request,Response}` (int64 rpm/tpm/qps + string-decimal money). Hand-authored vendored `*.pb.go` via a `descriptorpb` regenerator ([[project_toolchain_env_limits]]).

## Usage Dashboard Types + Proto (Story 9.1)

- **NEW proto `he.analytics.v1`** (NEW file — `buf breaking: FILE` not crossed): `UsageLogEvent` (the `request.logged` payload) — 21 scalar fields (he_request_id, user_id, api_key_id, model, upstream_model, routing_strategy, **selected_by_strategy** [H-3], status_code:uint32, prompt/completion/total_tokens:uint32, **cost_usd:string** NON-NULL [Q-Spec-4/H-1], latency_ms_total/gateway/upstream, ttfb_ms, is_streaming:bool, client_ip, client_country, error_code, ts). Hand-authored vendored `analytics.pb.go` via a throwaway `descriptorpb` generator (byte-exact rawDesc; [[project_toolchain_env_limits]]); round-trip + protojson test (9.1-CONTRACT).
- **Gateway producer types (`apps/api-gateway/internal/analyticslog`)**: `Emitter`/`Nop`/`KafkaEmitter` (fire-and-forget, billingemit parity); `Record` (mutable per-request accumulator, set-once for Q-AB leg-A) + `NewContext`/`FromContext`; `Middleware` (single emission seam — captures status + parses error_code from the error envelope body, emits one event per terminal outcome).
- **Gateway reader types (`apps/api-gateway/internal/analyticsquery`)**: `Store` interface (`Today` direct request_logs / `AggSince` hourly_agg MV) + `chStore` (clickhouse-go/v2); `PeriodAgg`; `TimezoneResolver` + `NewPGTimezoneResolver` (fail-safe UTC); `Handler.HandleSummary`. Wire types: `summaryResponse{today,month,quarter}`, `period{requests:int, success_rate:*string, tokens, cost_usd:string}` (success_rate null at requests=0). NEW envelope `503_clickhouse_unavailable` in `openaierr.CodeMetadata`.
- **Writer types (`apps/analytics-svc/internal/clickhouse`)**: `Row` (typed request_logs row) + `EventToRow` (validate/clamp; `ErrMalformed`→DLQ); `Sink` interface + `chSink` (clickhouse-go/v2 named-column batch); `BatchWriter` (size/time flush, retain-on-error). Worker `workers.RequestLogWorker` + `DLQPublisher`.
- **TS types (`apps/console/lib/api/me-usage.ts`)**: Zod `UsageSummarySchema` / `UsagePeriodSchema` / `TokenBreakdownSchema` (money + success_rate as string|nullable — Q-Spec-4); `getUsageSummary` Server Action result union. Components `UsageStatCards` / `UsageStatCardsSkeleton`. i18n namespace `dashboard` (×10 locales) + generated `DashboardKeys` union. [`UsageSeries`/`UsageChart` DEFERRED to 9.1b — H-4.]
