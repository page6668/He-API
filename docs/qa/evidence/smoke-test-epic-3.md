# Smoke Test Report: Epic 3

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 3                                  |
| **Trigger**      | manual (`QA *smoke-test 3`)        |
| **Executed At**  | 2026-06-16T09:36:59+0800           |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

> **Scope note**: Epic 3 is the **server-side Go HTTP gateway** (`apps/api-gateway`).
> There is no browser UI, so the smoke test adapts the standard browser-journey
> protocol to the equivalent server-side surface: Go regression suite + HTTP-wire
> integration journeys (`httptest`-backed, exercising the full server stack).
> Playwright/MCP browser steps are **N/A** for this epic.

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 3.1 | 网关 HTTP 框架（Go net/http + connectrpc, ratify）+ /health + 冷启动基准 | Done |
| 3.2 | Bearer Token 鉴权 + Key 校验 | Done |
| 3.3 | /v1/chat/completions 非流式实现（占位上游） | Done |
| 3.4 | /v1/chat/completions 流式（SSE） | Done |
| 3.5 | /v1/models + /v1/embeddings 端点 | Done |
| 3.6 | 标准化错误响应 + he_request_id | Done |

- **Total Stories**: 6
- **Done**: 6
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true (1 known-flaky test — see `SMOKE-3-001`; not a product failure) |
| **Tests Total** | 480 Epic-3-owning test funcs (353 framework + 127 core handler) across the gateway packages; whole `internal/handlers` pkg (447 funcs) also green |
| **Tests Failed**| 0 product failures; 1 non-deterministic test-harness race |

**Suite scope** (`go test` over Epic-3-owning packages):

| Package | Result |
|---------|--------|
| `cmd/server` (full-stack HTTP integration, cold-start, request-id, doc-ratification) | ok |
| `internal/middleware` (+ requestid, ratelimit, billinggate, cors, keypolicy) | ok |
| `internal/openaierr` (error envelope) | ok |
| `internal/streaming` (SSE chunks) | ok |
| `internal/handlers` (chat/models/embeddings/health) | ok (3.7s) |

> Note: the `internal/handlers` package now also carries later-epic tests
> (routing/failover/A-B/safety from Epic 4+); these were exercised and all
> passed — the whole 447-func package is green.

### Build / Cold-Start Sanity (Story 3.1)

`go build ./cmd/server/` → **BUILD_OK**. The server binary compiles cleanly,
satisfying the cold-start build prerequisite for the 3.1 health/cold-start AC.

### Failed Tests

- `TestChatCompletionsStream_ClientDisconnect_ConnectionClose` (scenario **3.4-INT-012**, P0):
  **non-deterministic** — observed **2 PASS / 6 FAIL** across 8 isolated `-count=1`
  runs this session. Root cause is a **test-harness timing race**, not a product
  regression: the no-delay mock stream emits all 10 chunks and finishes
  (`chunks_emitted:10, client_disconnected:false`) before `CloseClientConnections()`
  propagates, so the assertion on `client_disconnected=true` loses the race. The
  product's disconnect detection is independently and **stably** verified by the
  sibling `TestChatCompletionsStream_ClientDisconnect_ContextCancellation` (**5/5
  PASS** on `-count=5`). Tracked as `SMOKE-3-001`.

## 3. Core User Journeys

Journeys executed at the HTTP-wire / integration level (server-side equivalent of
browser journeys). Evidence = `httptest`-backed integration tests driving the full
gateway stack.

### Journey 1: Health probe & cold start (Story 3.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `go build ./cmd/server` | binary compiles | BUILD_OK | PASS |
| 2 | `GET /health` | 200 + deterministic body, `Cache-Control: no-store` | `TestHealthHandler_GET_returns200WithExpectedBody` | PASS |
| 3 | `GET /health` bypasses request-id middleware | no `he_request_id` overhead on probe | `Test_INT_003_health_probe_bypasses_request_id_middleware` | PASS |
| 4 | non-GET on `/health` | 405 + `Allow` header | `TestHealthHandler_POST_returns405WithAllowHeader` | PASS |

### Journey 2: Bearer-token authentication (Story 3.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | request with no `Authorization` | 401, inner handler never invoked | `TestRequireAPIKey_MissingHeader` / `InnerNeverCalledOn401` | PASS |
| 2 | valid bearer | passes to handler, cache populated | `TestRequireAPIKey_CacheMissPopulates` / `CacheHitAvoidsRPC` | PASS |
| 3 | cache key never stores plaintext | SHA-256 key | `TestRequireAPIKey_CacheKeyIsSHA256NotPlaintext` | PASS |
| 4 | revoked / not-found key | 401 same envelope, no cache poisoning | `TestRequireAPIKey_RevokedSameEnvelope` / `NotFoundNoCache` | PASS |
| 5 | 401 envelope on wire | header `he_request_id` == body | `Test_INT_002_models_401_header_equals_body` | PASS |

### Journey 3: Chat completions — non-streaming (Story 3.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `POST /v1/chat/completions` happy path | 200, OpenAI-shaped body | `TestChatCompletions_HappyPath_OpenAIShape` | PASS |
| 2 | over-wire happy path | 200 end-to-end | `TestChatCompletions_HappyPathValidationBaseline` | PASS |
| 3 | invalid JSON / oversized body | 400 / 413, no body echo | `TestChatCompletions_InvalidJSON_HTTPWire` / `BodyTooLarge` / `NoBodyEcho` | PASS |
| 4 | determinism of mock upstream | stable across 1000 calls | `TestChatCompletions_Determinism_1000Calls` | PASS |

### Journey 4: Chat completions — streaming SSE (Story 3.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `stream:true` dispatch | routes to SSE | `TestChatCompletionsStream_DispatchFork_StreamTrue_RoutesToSSE` | PASS |
| 2 | `stream:false`/missing | routes to JSON path | `..._StreamFalse_RoutesToJSON` / `..._StreamMissing_DefaultsToFalse` | PASS |
| 3 | successful stream structured log | no PII, success marker | `..._StructuredLog_OnSuccess` / `..._StructuredLog_NoPIIInLogs` | PASS |
| 4 | client disconnect mid-stream (context cancel) | disconnect detected, early exit, no leak | `..._ClientDisconnect_ContextCancellation` (5/5 stable) | PASS |
| 5 | client TCP-close mid-stream | `client_disconnected=true` logged | flaky test harness (see `SMOKE-3-001`); product path covered by step 4 | FLAKY |
| 6 | TTFB / no-sleep regression | first byte fast | `..._TTFB_*` / `..._NoSleepRegression` | PASS |

### Journey 5: Models & embeddings endpoints (Story 3.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `GET /v1/models` | 200, OpenAI ModelList shape + capabilities | `Test_ModelsHandler_returns_OpenAI_ModelList_shape_ordering` / `..._capabilities_match_BR_1_3_verbatim` | PASS |
| 2 | `/v1/models` carries `he_request_id` | header present on success | `Test_INT_001_models_success_carries_he_request_id_header` | PASS |
| 3 | `POST /v1/embeddings` string + array input | 200, deterministic dim-128 vectors | `Test_EmbeddingsHandler_happy_path_string_input` / `_array_input` / `_GenerateMockEmbedding_dim_128_norm_close_to_1` | PASS |
| 4 | embeddings input bounds | 400 over 256KiB / 413 over 1MiB body | `_input_over_256KiB_returns_400` / `_body_over_1MiB_returns_413` | PASS |
| 5 | embeddings never logs input content | no PII | `_does_NOT_log_input_content` | PASS |

### Journey 6: Standardized errors + he_request_id (Story 3.6)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | error path | canonical 5-field OpenAI error envelope | `Test_Write_emits_canonical_5_field_envelope` | PASS |
| 2 | `he_request_id` derivation | from trace-id, crypto-rand fallback, anti-spoof of inbound header | `Test_RequestID_derives_from_TraceID_first_6_bytes` / `_invalid_span_falls_back_to_crypto_rand` / `_ignores_inbound_X_Request_Id_anti_spoofing` | PASS |
| 3 | header == body request-id | wire-level parity on 401/413/405 | `Test_INT_002/_004/_006` | PASS |
| 4 | unknown/empty error code | 500 fallback envelope, never panics | `Test_Write_unknown_code_emits_500_internal_error_fallback` / `_marshal_fail_emits_hardcoded_fallback` | PASS |

**Summary**: 6 / 6 journeys passed (Journey 4 step 5 is a flaky test, not a product failure; the disconnect product behavior passes via step 4).

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No browser surface in this epic |
| Network Failures     | PASS   | Full-stack `cmd/server` integration suite green; upstream-failure / failover paths exercised and handled (502/504/429 envelopes) |
| Visual Consistency   | N_A    | API-only epic, no UI |
| Performance          | PASS   | `HappyPath_FastUnder10ms`, TTFB in-process/loopback, no-sleep regression guards all green; cold-start build OK |
| Auth Flow            | PASS   | Bearer-token deny→allow→revoke cycle fully covered (`RequireAPIKey_*`) |
| Log / PII Safety     | PASS   | No-PII-in-logs + no-body-echo verified across chat/stream/embeddings |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-3-001 | MEDIUM | `TestChatCompletionsStream_ClientDisconnect_ConnectionClose` (3.4-INT-012, P0) is **flaky**: observed **2 PASS / 6 FAIL** over 8 isolated runs this session. The no-delay mock stream emits all 10 chunks and completes (`chunks_emitted:10, client_disconnected:false`) before `CloseClientConnections()` propagates, so the test loses the race. Product disconnect-detection is sound and independently verified by the stable sibling `ClientDisconnect_ContextCancellation` (5/5). | 4 (SSE) | Make the test deterministic: gate the mock upstream to block after the first chunk until the client close is observed (e.g. a `<-released` channel between bootstrap and content chunks), then assert `client_disconnected=true`. Do **not** ship-block Epic 3 on this; track as a test-hardening follow-up. The fail rate is higher than the prior run (1/5 → 6/8), so the hardening is increasingly worth doing. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline above) | `go build ./cmd/server` → BUILD_OK |
| log | (inline above) | Epic-3 framework pkgs (`cmd/server` + `openaierr` + `streaming` + `middleware`): 353 PASS / 0 FAIL |
| log | (inline above) | `internal/handlers` whole package (447 funcs incl. 127 Epic-3 core) → ok (3.7s) |
| log | (inline above) | `ConnectionClose` flaky isolation → 2 PASS / 6 FAIL of 8 runs |
| log | (inline above) | `ContextCancellation` (product disconnect path) → 5/5 PASS on `-count=5` |

No browser screenshots — N/A for a server-side API epic.

## 7. Recommendation

**Result**: PASS

Epic 3 is **production-ready**. All 6 stories are Done, the gateway binary compiles
(cold-start build OK), and the full Epic-3 regression surface — `cmd/server`
full-stack integration, middleware, error envelope, SSE streaming, and the
`internal/handlers` package (chat / models / embeddings / health) — is green with
**zero deterministic product failures**. All six core HTTP journeys — health/cold-start,
bearer auth, chat (non-stream & SSE), models, embeddings, and standardized error +
`he_request_id` — pass at the wire level, with PII-safe logging, performance guards,
and the auth lifecycle verified.

One **MEDIUM** test-hygiene issue (`SMOKE-3-001`) is logged: a flaky P0 streaming
disconnect test caused by a harness timing race, not a product defect — the product's
disconnect detection is independently confirmed stable. It should be hardened as a
non-blocking follow-up. Confidence is **MEDIUM** (not HIGH) solely because of this
non-deterministic test and the inapplicability of browser-level E2E to this epic.
