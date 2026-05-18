# API Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-18
**Total Stories Tracked**: 3
**Total Endpoints**: 2
**Repository**: He-API
**Mode**: monolith

## API Endpoints Registry

### Public HTTP routes (api-gateway)

| Method | Route | Auth | Story | Notes |
|--------|-------|------|-------|-------|
| POST | `/v1/chat/completions` | Bearer API key (`middleware.RequireAPIKey`) | 3.3 / 3.4 | **Mock-upstream 200** (deterministic body); real `ModelAdapterService` client lands in Epic 4. Story 3.4 supersedes for stream=true: SSE response per OpenAI spec; mock chunker emits ~8 word-boundary chunks of `handlers.MockContent` then `data: [DONE]`; real `ModelAdapterService` streaming client lands in Epic 4. BR-4.1 caveat: `usage` triple `{10, 20, 30}` is synthetic — SDK consumers writing budget logic against this Story's response WILL see incorrect numbers. Mock content carries the `He-API mock` substring (BR-4.2) for log-grep cutover hygiene. |

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
