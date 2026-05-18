# Models & Types Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-19
**Total Stories Tracked**: 2
**Total Models**: 13
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

## Models by Story

- **3.2** — Bearer-token API-key auth:
  - `ValidateApiKeyRequest` / `ValidateApiKeyResponse` / `ApiKeyValidationReason` (proto).
  - `ApiKeyRow` (Go repository type).
  - `middleware.CachedClaims` (gateway-local JSON cache envelope; not part of the cross-service contract).
- **3.5** — `/v1/models` + `/v1/embeddings` Go types (handler-local, package `handlers`):
  - `ModelEntry` / `ModelsResponse` / `ModelsHandler` / `ModelsHandlerOption`.
  - `EmbeddingRequest` / `EmbeddingResponse` / `EmbeddingData` / `EmbeddingUsage` / `EmbeddingsHandler` / `EmbeddingsHandlerOption`.
  - Promotion rule (Architect-ratified): if Epic 4 adapter packages need to import any of these, promote to a sibling shared package (e.g., `apps/api-gateway/internal/openai/types`). Story 3.5 does NOT pre-promote per Go's "rule of three".
