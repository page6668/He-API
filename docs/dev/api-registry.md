# API Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-19
**Total Stories Tracked**: 5
**Total Endpoints**: 4
**Repository**: He-API
**Mode**: monolith

## API Endpoints Registry

### Public HTTP routes (api-gateway)

| Method | Route | Auth | Story | Notes |
|--------|-------|------|-------|-------|
| POST | `/v1/chat/completions` | Bearer API key (`middleware.RequireAPIKey`) | 3.3 / 3.4 | **Mock-upstream 200** (deterministic body); real `ModelAdapterService` client lands in Epic 4. Story 3.4 supersedes for stream=true: SSE response per OpenAI spec; mock chunker emits ~8 word-boundary chunks of `handlers.MockContent` then `data: [DONE]`; real `ModelAdapterService` streaming client lands in Epic 4. BR-4.1 caveat: `usage` triple `{10, 20, 30}` is synthetic — SDK consumers writing budget logic against this Story's response WILL see incorrect numbers. Mock content carries the `He-API mock` substring (BR-4.2) for log-grep cutover hygiene. |
| GET | `/v1/models` | Bearer API key (`middleware.RequireAPIKey`) | 3.5 | **Static catalogue** (11 entries per Architect Round 1 OQ1: `qwen-max`/`qwen-plus`/`deepseek-v3`/`moonshot-v1-128k`/`glm-4`/`doubao-pro`/`doubao-lite`/`ernie-4.0`/`he-router-cost`/`he-router-quality`/`he-router-latency`). Response shape = OpenAI `ModelList` (`id`/`object`/`created`/`owned_by`). `created` captured ONCE at handler construction (BR-1.6 — model creation is a vendor event, not a request event). Real DB-backed registry + full capability matrix land in Story 4.7. |
| POST | `/v1/embeddings` | Bearer API key (`middleware.RequireAPIKey`) | 3.5 | **Deterministic mock vector** placeholder (128-dim per Architect Round 1 OQ3 — intentionally non-canonical to signal mock at wire inspection). Response shape = OpenAI `Embedding` (`object`/`data[]`/`model`/`usage`); `usage.prompt_tokens = len(input)/4` synthetic per BR-2.5. Dual-shape `input` parser via `json.RawMessage` + `*json.UnmarshalTypeError` fallback (BR-2.8). 1 MiB body cap via `MaxEmbeddingBodyBytes` (OQ2 sibling constant, distinct from `maxChatBodyBytes`). Real adapter-backed embeddings land in Epic 4 (Stories 4.1-4.6). |

### Internal Connect/gRPC RPCs (auth-svc)

| Service | RPC | Story | Notes |
|---------|-----|-------|-------|
| `he.auth.v1.AuthService` | `ValidateApiKey(ValidateApiKeyRequest) returns (ValidateApiKeyResponse)` | 3.2 | Bcrypt-compare bearer-token plaintext against `he_api.api_keys` rows matching the 12-char `key_prefix`. Returns `ok=true` + identity / scope on match; `ok=false` with `reason ∈ {NOT_FOUND, REVOKED}` (anti-enumeration parity — gateway maps both to a single 401 envelope, BR-2.4). |

## Endpoints by Story

- **3.2** — Bearer-token API-key auth + key validation:
  - Public: `POST /v1/chat/completions` (placeholder, bearer-auth wrapped — superseded by Story 3.3).
  - Internal: `he.auth.v1.AuthService/ValidateApiKey`.
- **3.3** — Non-streaming chat-completions mock (replaces Story 3.2 placeholder):
  - Public: `POST /v1/chat/completions` (real handler, deterministic mock body, OpenAI Python SDK-compatible; `stream=true` deferred to Story 3.4 via `501_streaming_not_implemented`).
  - New testing convention: `apps/api-gateway/tests/` Python contract tests (OpenAI SDK pinned to `==1.40.*`).
- **3.4** — Streaming chat-completions (SSE) replaces the Story-3.3 `stream=true` 501 path:
  - Public: `POST /v1/chat/completions` with `stream=true` → `text/event-stream` response; mock chunker emits ~8 word-boundary chunks of `handlers.MockContent` then the `data: [DONE]` sentinel.
  - New package: `apps/api-gateway/internal/streaming/` (first occupant; the directory was reserved by `source-tree.md §6` since Epic 1).
  - SDK contract test: `apps/api-gateway/tests/openai_sdk_streaming_contract_test.py` (reuses Story-3.3 SDK pin `openai==1.40.*`).
  - New constructor option: `handlers.WithNow(f func() time.Time)` — enables deterministic `created` timestamps for the AC2 TTFB tests + INT-007 golden-file regression guard.
- **3.5** — `/v1/models` + `/v1/embeddings` endpoints (static catalogue + mock vector):
  - Public: `GET /v1/models` (11-entry static catalogue, OpenAI `ModelList` shape) + `POST /v1/embeddings` (128-dim deterministic mock vector, OpenAI `Embedding` shape).
  - New constants: `handlers.MaxEmbeddingBodyBytes int64 = 1 << 20` (per Architect Round 1 OQ2 sibling — distinct identity from `maxChatBodyBytes` so per-endpoint caps can diverge in Story 9.x multimodal).
  - New constructor options: `handlers.WithModelsNow(f func() time.Time)` (test-only — stable-`created` invariant); `handlers.WithEmbeddingDim(d int)` (test-only — production stays at 128).
  - New exported pure function: `handlers.GenerateMockEmbedding(input string, dim int) []float32` — Epic 4 contract-test reference vector (deterministic via sha256-seeded sin generator, L2-normalized).
  - SDK contract tests: `apps/api-gateway/tests/openai_sdk_models_contract_test.py` + `apps/api-gateway/tests/openai_sdk_embeddings_contract_test.py` (reuse Story-3.3 `openai==1.40.*` pin; `HE_API_TEST_GATEWAY_URL` skipif convention).
  - Scope deferral: FR-2.3 model-capability 405 → Epic 4 (Story 4.7 matrix + Stories 4.1-4.6 per-adapter checks).
- **3.6** — Standardized error responses + `he_request_id` (cross-cutting middleware + canonical error writer):
  - **New shared package**: `apps/api-gateway/internal/openaierr/` — `openaierr.Write(w, ctx, status, code, message, *param) error` is the SINGLE canonical writer for the §5.1.2 5-field envelope. `openaierr.CodeMetadata` exported map keys = active §5.1.2 codes; lookup derives `error.type` (BR-1.4).
  - **New middleware subpackage**: `apps/api-gateway/internal/middleware/requestid/` — `requestid.RequestID` stamps `X-He-Request-Id` on the response, the request context, and the OTel span attribute `he.request_id`; `requestid.FromContext(ctx) (string, bool)` is the public accessor. Outer-most user-traffic wrap (probeMux still bypasses).
  - **Response header added**: `X-He-Request-Id: req_<12 hex>` on every /v1/* + /v1/auth/* + /v1/me* + /v1/account/* response (success + error). Probe routes (`/health`, `/healthz`) BYPASS — Story 3.1 BR-1.3 invariant preserved.
  - **OTel span attribute added**: `he.request_id` (first reserved `he.*` namespace member — see `docs/architecture/11-可观测性observability.md` §11.5).
  - **Refactor**: 7 divergent error writers deleted in favour of `openaierr.Write` — `handlers/auth.go writeError`, `handlers/chat_completions.go writeChatError`, `middleware/bearer_auth.go writeAPIKeyError`, `middleware/jwt_verify.go writeJWTError`, the helper `writeCSRFViolation` in `middleware/csrf.go`, plus the two inline `http.Error` JSON literals in `middleware/oauth_ratelimit.go`. All envelope-emitting code paths now flow through the single canonical writer.
  - **Logger extension**: `obs.NewLogger(level, obs.WithRequestIDExtractor(requestid.FromContext))` — every slog record now carries `he_request_id` alongside `trace_id` / `span_id` when the context carries a stamped id (BR-2.10).
  - **No endpoints added / removed**; the change is envelope-shape across existing surfaces.
  - SDK contract tests: `apps/api-gateway/tests/openai_sdk_error_contract_test.py` (3.6-E2E-001..005 — bad-bearer envelope shape, 413 envelope, `X-He-Request-Id` success-path header).
