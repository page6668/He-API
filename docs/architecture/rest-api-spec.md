# 5. API 规范（API Specification）

## 5.1 对外 API（OpenAI 兼容）

### 5.1.1 Chat Completions

```
POST /v1/chat/completions
Authorization: Bearer he-xxxxxxxxxxxxxxxxxxxxxxxxxxxx
Content-Type: application/json
Accept: application/json (or text/event-stream for SSE)

Optional headers:
  X-He-Routing-Strategy: quality | cost | latency
  X-He-AB-Models: qwen-max,deepseek-v3
  Accept-Language: en (locale for error messages)

Request body (与 OpenAI 一致):
{
  "model": "qwen-max" | "he-router-cost" | "he-router-quality" | ...,
  "messages": [...],
  "stream": false,
  "temperature": 0.7,
  "max_tokens": 2048,
  "tools": [...],
  "tool_choice": "auto",
  "response_format": {"type": "json_object"}
}

Response (success, non-stream):
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1715000000,
  "model": "qwen-max",
  "choices": [...],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 200,
    "total_tokens": 300
  }
}

Response headers (always present):
  X-He-Request-Id: req_xxxxxxxxxxxx       # 客服追溯
  X-He-Selected-Model: qwen-max           # 实际路由到的模型
  X-He-Cost-Usd: 0.000123                 # 本次消费

Error response (与 OpenAI 一致 + 自定义字段):
{
  "error": {
    "message": "Insufficient quota.",
    "type": "insufficient_quota",
    "code": "402_quota_exhausted",
    "param": null,
    "he_request_id": "req_xxxxxxxxxxxx"
  }
}
```

### 5.1.1.1 Streaming Response (SSE)

When the request body includes `"stream": true`, the response is `text/event-stream`:

```text
Content-Type: text/event-stream; charset=utf-8
Cache-Control: no-cache, no-transform
Connection: keep-alive
X-Accel-Buffering: no

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":...,"model":"...","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":...,"model":"...","choices":[{"index":0,"delta":{"content":"Hello "},"finish_reason":null}]}

... (additional content chunks) ...

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":...,"model":"...","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

```

Each event is `data: <single-line JSON>\n\n` per W3C EventSource §9.2.6. The terminator `data: [DONE]\n\n` is the OpenAI SDK extension that signals iterator completion. `id` / `created` / `model` are threaded unchanged across every chunk; `object` is the literal `chat.completion.chunk`; `delta` carries `{"role":"assistant"}` on the bootstrap chunk, `{"content":"..."}` on content chunks, and `{}` on the terminal chunk (via `omitempty` on Role + Content fields per Architect Round 1 AR1-R2). `finish_reason` is `null` for bootstrap + content chunks and `"stop"` for the terminal chunk.

### 5.1.2 标准错误码

| HTTP | error.code | 说明 |
|------|-----------|------|
| 401 | `401_invalid_api_key` | Key 无效或被吊销 |
| 402 | `402_balance_insufficient` | 余额不足 |
| 402 | `402_quota_exhausted` | 月度上限熔断 |
| 403 | `403_ip_not_whitelisted` | IP 不在白名单 |
| 403 | `403_model_not_in_scope` | Key 无权调用该模型 |
| 400 | `400_content_filter` | 命中内容安全过滤 |
| 400 | `400_invalid_request` | 参数错误 |
| 429 | `429_rate_limit_qps` | QPS 超限（每秒请求数）|
| 429 | `429_rate_limit_rpm` | RPM 超限（每分钟请求数）— Story 5.3 Architect Q5 ratified |
| 429 | `429_rate_limit_tpm` | TPM 超限（每分钟上游 token 数）|
| 429 | `429_rate_limit_gdpr_export` | GDPR 数据导出 24 小时内的限流（Story 2.6 AC2 BR-2.5；正常幂等路径返回 200 + 已存在的导出记录，仅在 100ms 级竞态下命中） |
| 405 | `405_method_not_allowed` | 端点不接受请求方法（Story 4.7 OQ-4.7-6 — `/public/models` POST/PUT/DELETE/PATCH 命中此码；handler 同步发送 `Allow: GET` 响应头 per RFC 7231 §6.5.5） |
| 413 | `413_payload_too_large` | 请求体超出 1 MiB 限制（Story 3.3 BR-1.2 — chat-completions handler） |
| 500 | `500_gateway_misconfigured` | 网关中间件未正确串联（防御性）— precedented at apps/api-gateway/internal/handlers/2fa_disable.go:38（Story 2.4 backfill） |
| 500 | `500_internal_error` | 系统异常 |
| 501 | `501_not_implemented` | 端点占位，未实现（Story 3.2 backfill — chatPlaceholder 501 stub deleted in Story 3.3 T0.4） |
| 501 | `501_streaming_not_implemented` | 流式响应未实现；将由 Story 3.4 落地（Story 3.3 BR-2.3 — non-streaming mock returns 501 for stream=true）— **RETIRED by Story 3.4 (2026-05-18)**: `stream=true` now serves SSE per §5.1.1.1; row preserved for git-blame traceability. |
| 502 | `502_upstream_unavailable` | 上游模型不可用（**Story 6.3: 触发 failover** — 网关按 `failover_chain` 顺序重试，最多 3 次 / 30s；耗尽后返回此终态码） |
| 504 | `504_upstream_timeout` | 上游模型超时（**Story 6.3: 触发 failover** — 同上；30s 总预算耗尽或 3 次上限后返回此终态码） |
| 400 | `400_invalid_email` | 注册/重发验证/登录 — 请求体非法或邮箱格式不通过（Stories 2.2 / 2.3，Story 3.6 promoted） |
| 400 | `400_invalid_token` | 验证邮件 — token 缺失/形态不通过（Story 2.2，Story 3.6 promoted） |
| 400 | `400_invalid_body` | PUT /v1/me/profile — 请求体格式异常或超长（Story 2.5，Story 3.6 promoted） |
| 400 | `400_unknown_field` | PUT /v1/me/profile — 请求体含未声明字段（Story 2.5，Story 3.6 promoted） |
| 400 | `400_password_breached` | 注册 — 密码命中 HIBP（Story 2.2，Story 3.6 promoted） |
| 400 | `400_invalid_factor` | 2FA — factor 字段非法（Story 2.4，Story 3.6 promoted） |
| 400 | `400_invalid_totp_format` | 2FA — TOTP 编码格式不通过（Story 2.4，Story 3.6 promoted） |
| 400 | `400_invalid_recovery_code_format` | 2FA — 恢复码格式不通过（Story 2.4，Story 3.6 promoted） |
| 400 | `400_oauth_invalid_return_to` / `400_oauth_state_invalid` | OAuth — 返回地址/state 非法（Story 2.3，Story 3.6 promoted） |
| 401 | `401_invalid_credentials` | 登录 — 凭据非法（Story 2.2，Story 3.6 promoted） |
| 401 | `401_unauthenticated` | JWT — 缺少 access cookie（Story 2.4，Story 3.6 promoted） |
| 401 | `401_unauthorized` | GET /v1/me — 凭据缺失（Story 2.5，Story 3.6 promoted） |
| 401 | `401_access_token_expired` | JWT — access cookie 过期（Story 2.4，Story 3.6 promoted） |
| 401 | `401_cross_token_rejected` | JWT — 跨用途 token 拒绝（Story 2.4，Story 3.6 promoted） |
| 401 | `401_invalid_totp_code` / `401_mfa_token_invalid` / `401_mfa_token_binding_mismatch` | 2FA challenge — 各类 mfa_token 校验失败（Story 2.4，Story 3.6 promoted） |
| 403 | `403_csrf_check_failed` | CSRF — Origin/Referer 校验失败（Story 2.2，Story 3.6 promoted） |
| 403 | `403_aal2_required` | JWT — 二次因子要求未满足（Story 2.4，Story 3.6 promoted） |
| 403 | `403_account_pending_deletion` | 帐号待删 — 软删除窗口期内拒绝登录（Story 2.5，Story 3.6 promoted） |
| 410 | `410_token_expired` / `410_token_used` | 邮件验证 — token 过期或已用（Story 2.2，Story 3.6 promoted） |
| 412 | `412_etag_mismatch` | PUT /v1/me/profile — If-Match 不匹配（Story 2.5，Story 3.6 promoted） |
| 423 | `423_account_locked` | 帐号锁定 — 登录尝试超限（Story 2.2，Story 3.6 promoted） |
| 428 | `428_precondition_required` | PUT /v1/me/profile — If-Match 头缺失（Story 2.5，Story 3.6 promoted） |
| 429 | `429_rate_limit_signup` | 注册接口限流（Story 2.2，Story 3.6 promoted） |
| 429 | `429_rate_limit_oauth` | OAuth initiate/callback 限流（Story 2.3，Story 3.6 promoted） |
| 429 | `429_rate_limit_resend_ip` | resend-verification 单 IP 限流（Story 2.2，Story 3.6 promoted） |
| 429 | `429_rate_limit_profile_update` | profile 更新限流（Story 2.5，Story 3.6 promoted） |
| 500 | `500_email_send_failed` | 邮件投递失败（Story 2.2，Story 3.6 promoted） |
| 502 | `502_auth_svc_unavailable` | auth-svc gRPC 连接失败（Stories 2.2 / 2.5，Story 3.6 promoted） |
| 502 | `502_notification_svc_unavailable` | notification-svc gRPC 连接失败（Story 2.6，Story 3.6 promoted） |
| 502 | `502_oauth_provider_error` | OAuth 上游错误（Story 2.3，Story 3.6 promoted） |
| 503 | `503_auth_unavailable` | bearer-auth — auth-svc 不可用（Story 3.2，Story 3.6 promoted） |
| 503 | `503_hibp_unavailable` | HIBP 服务不可用（Story 2.2，Story 3.6 promoted） |
| 503 | `503_database_unavailable` | DB 不可用（Story 2.5，Story 3.6 promoted） |
| 503 | `503_jwks_unavailable` | JWKS endpoint 失效（Story 2.5，Story 3.6 promoted） |
| 503 | `503_notification_svc_unavailable` | notification-svc 不可用（Story 2.6，Story 3.6 promoted） |
| 503 | `503_service_unavailable` | OAuth ratelimit — Redis 失效 fail-closed（Story 2.3，Story 3.6 promoted） |
| 504 | `504_notification_svc_timeout` | notification-svc 超时（Story 2.6，Story 3.6 promoted） |

**Envelope shape (post-Story-3.6 ratification, BR-1.7)**: every error response carries the canonical 5-field envelope in this struct-marshalled field order:

```json
{"error": {"code": "<string>", "message": "<string>", "type": "<\"invalid_request_error\" | \"server_error\">", "param": "<string | null>", "he_request_id": "req_<12 hex>"}}
```

The single canonical writer is `apps/api-gateway/internal/openaierr.Write`. `error.type` is derived from `apps/api-gateway/internal/openaierr.CodeMetadata` (NOT a runtime regex on the leading status digit) — `invalid_request_error` for 4xx canonical codes, `server_error` for 5xx. `error.he_request_id` is populated by `apps/api-gateway/internal/middleware/requestid.RequestID` per §5.1.1.

### 5.1.3 其他端点

```
GET  /v1/models                    返回模型列表 + 能力矩阵（Story 4.7：每条 ModelEntry 末尾追加 capabilities 子结构 — 7 字段 BR-1.2 declaration order）
GET  /public/models                返回模型能力矩阵（公开，Story 4.7；与 /v1/models 字节同型；OUTSIDE bearer middleware chain；OQ-4.7-7 `*` CORS scoped to `/public/*`，绝不携带 Allow-Credentials）
POST /v1/embeddings                文本 embedding
POST /v1/audio/transcriptions      ASR (Whisper 兼容)
POST /v1/audio/speech              TTS
POST /v1/images/generations        V1.1
GET  /v1/usage                     查询本月用量（自定义端点）
GET  /v1/balance                   查询余额
```

## 5.2 内部 gRPC 服务（protobuf 节选）

```proto
syntax = "proto3";
package he.api.v1;

service AuthService {
  rpc ValidateApiKey(ValidateApiKeyRequest) returns (ValidateApiKeyResponse);
  rpc CreateApiKey(CreateApiKeyRequest) returns (CreateApiKeyResponse);
  rpc ListApiKeys(ListApiKeysRequest) returns (ListApiKeysResponse);  // Story 5.1 — NEW
  rpc RevokeApiKey(RevokeApiKeyRequest) returns (RevokeApiKeyResponse); // Story 5.1 UPGRADED from (Empty) per Architect Q-Spec-1 — see §5.3 Change Log 2026-05-25
}

// RoutingService — REALISED by Story 6.1 as `he.routing.v1.RoutingService`
// (Connect-RPC) at packages/proto/he/routing/v1/routing.proto. The block below
// reflects the realised contract (NOT the pre-Epic-6 string-strategy sketch);
// see §5.3 Change Log 2026-06-03. Server-side only in 6.1; gateway wiring +
// real scoring land in Story 6.2.
service RoutingService {
  rpc SelectModel(SelectModelRequest) returns (SelectModelResponse);
}

enum Strategy {                    // Q-D: enum (was `string strategy`)
  STRATEGY_UNSPECIFIED = 0;        // engine treats 0 ≡ DEFAULT(1); never errors
  STRATEGY_DEFAULT = 1;
  STRATEGY_QUALITY = 2;
  STRATEGY_COST = 3;
  STRATEGY_LATENCY = 4;
}

message SelectModelRequest {
  string user_id = 1;
  string requested_model = 2;       // 'qwen-max' or 'he-router-cost'
  Strategy strategy = 3;            // Q-D enum upgrade (was `string strategy`)
  repeated string ab_models = 4;    // ignored in 6.1 (A/B is Story 6.4)
  string he_request_id = 5;         // Q-H: gateway correlation id (Story 3.6); slog-only echo
}

message SelectModelResponse {
  string selected_model = 1;
  string adapter_endpoint = 2;      // Q-K: RESERVED/empty in 6.1; gateway resolves via adapterclient.Registry
  bool is_ab_test = 3;
  repeated string ab_selected_models = 4;  // additive (Story 6.4); empty in 6.1
  Strategy strategy_used = 5;       // Q-I: which strategy actually fired
  string score_source = 6;          // Story 6.2: model_pricing|clickhouse|fallback|default
  repeated string failover_chain = 7;  // Story 6.3: ranked fallback tail AFTER selected_model (concrete-only; empty on DEFAULT path)
}

service ModelAdapterService {
  rpc Chat(ChatRequest) returns (stream ChatChunk);  // 流式
}

service BillingService {
  rpc CheckBalance(CheckBalanceRequest) returns (CheckBalanceResponse);
  rpc DeductBalance(DeductBalanceRequest) returns (DeductBalanceResponse);
  rpc CreateRechargeOrder(...) returns (...);
}

service SafetyService {
  rpc CheckContent(CheckContentRequest) returns (CheckContentResponse);
}

service QuotaService {
  rpc CheckQuota(CheckQuotaRequest) returns (CheckQuotaResponse);
  rpc ConsumeQuota(ConsumeQuotaRequest) returns (Empty);
}
```

## 5.3 Change Log

| Date | Story | Change |
|------|-------|--------|
| 2026-05-18 | Story 3.3 (Wright Round 1 OQ2 ruling) | §5.1.2 4 rows added: `413_payload_too_large` + `501_streaming_not_implemented` (Story 3.3 introductions) + `501_not_implemented` (Story 3.2 backfill) + `500_gateway_misconfigured` (Story 2.4 backfill). |
| 2026-05-18 | Story 3.4 (Linus / Dev) | §5.1.1 appended new subsection §5.1.1.1 documenting the SSE streaming response shape (Content-Type, event framing, `[DONE]` terminator); §5.1.2 row `501_streaming_not_implemented` annotated **RETIRED** (preserved for git-blame). |
| 2026-05-19 | Story 3.6 (SM + Dev) | §5.1.1 + §5.1.2 ratified: `X-He-Request-Id` taxonomy (`req_<12 hex>`) + 5-field envelope SHAPE now enforced gateway-wide via `apps/api-gateway/internal/openaierr` (single canonical writer) + `apps/api-gateway/internal/middleware/requestid` (single canonical stamper). Pre-Story-3.6 divergent writers (`writeError`, `writeChatError`, `writeAPIKeyError`, `writeJWTError`, inline `http.Error` in `csrf.go` + `oauth_ratelimit.go`) all deleted in favour of `openaierr.Write`. §5.1.2 PROMOTED non-OpenAI auth/account codes (formerly per-handler local constants across Stories 2.2-2.6 + 3.2) into the single canonical superset per Architect Round 1 ruling — `openaierr.CodeMetadata` is now the runtime mirror of §5.1.2's full active row-set. Dev discovered an additional 19 codes already used by source callers beyond the SM-enumerated 22 (see Story 3.6 Dev Log → Implementation Decisions Log "codeMetadata superset expansion") — these are also promoted in this row. |
| 2026-05-19 | Story 4.1 (Dev / Linus) | §5.2 `ModelAdapterService` proto sketch realised as `he.adapter.v1.AdapterService` (Connect-RPC server-streaming) at `packages/proto/he/adapter/v1/adapter.proto`. First concrete implementation: `apps/adapters/deepseek/` (DeepSeek adapter). Public route behaviour change (no spec edit): `POST /v1/chat/completions` with `model=deepseek-v3` now dispatches to the real adapter via `apps/api-gateway/internal/adapterclient.Registry` (replaces the Story-3.3 / 3.4 mock for this specific model id ONLY; other model ids continue to receive the mock until Stories 4.2-4.6 land). Both `stream=false` (BR-1.2) and `stream=true` (BR-2.1) paths exercised. §5.1.1.1 streaming-response shape unchanged — the adapter's SSE chunks pass through the Story-3.4 streaming.Writer + a new sister `AdapterChunker`. Streaming-side failure-envelope additions: BR-2.5 pre-flush boundary emits §5.1.2 JSON envelope (unchanged shape); BR-2.6 post-flush boundary emits an inline `data: {"error":{...}}\n\n` + `data: [DONE]\n\n` terminal sequence carrying the canonical 5-field envelope. NO new §5.1.2 codes introduced — all reuses are `502_upstream_unavailable` / `504_upstream_timeout` / `400_invalid_request` / `500_gateway_misconfigured`. Architect Round 2 OQ1-OQ8 rulings recorded under Story 4.1 Architect Review Results. |
| 2026-05-19 | Story 4.4 (Dev / Linus) | Public route behaviour change (no spec edit): `POST /v1/chat/completions` with `model=glm-4` now dispatches to the real GLM (Zhipu) adapter via the Story-4.1 `apps/api-gateway/internal/adapterclient.Registry` (extended; Story-4.1/4.2/4.3 entries preserved as four-vendor cross-vendor regression guards per 4.4-INT-009). §5.2 `he.adapter.v1.AdapterService` proto contract REUSED VERBATIM. §5.1.1 / §5.1.1.1 / §5.1.2 shapes UNCHANGED — adapter chunks pass through the Story-4.1 `streaming.AdapterChunker` and BR-1.4 mapping reuses Story-4.2 BR-4.4 + Story-4.1 BR-1.4 verbatim per Architect Round 1 OQ-4.4-6 (no Zhipu-specific envelope-code additions; Story-4.3 body-aware context-length classifier NOT cascaded). NO new §5.1.2 codes introduced. |
| 2026-05-19 | Story 4.5 (Dev / Linus) | Public route behaviour change (no spec edit): `POST /v1/chat/completions` with `model ∈ {doubao-pro, doubao-lite}` now dispatches to the real Doubao (Volcengine Ark v3) adapter via the Story-4.1 `apps/api-gateway/internal/adapterclient.Registry` (extended; Story-4.1/4.2/4.3/4.4 entries preserved as **FIVE-vendor** cross-vendor regression guards per 4.5-INT-009 — `ernie-4.0` continues to hit the mock until Story 4.6 lands). §5.2 `he.adapter.v1.AdapterService` proto contract REUSED VERBATIM. §5.1.1 / §5.1.1.1 / §5.1.2 shapes UNCHANGED on the gateway-egress side — the adapter performs an internal bidirectional `model`-field rewrite (friendly id ↔ Volcengine endpoint id) per BR-1.11 + BR-1.7.f so the wire shape `response.model == request.model` is preserved per OpenAI's echo convention (consumers see `"doubao-pro"`/`"doubao-lite"` and NEVER the Volcengine endpoint id `"ep-..."`; SDK-observed verification at 4.5-CONTRACT-001..004). BR-1.4 mapping REUSES Story-4.1/4.2/4.3/4.4 verbatim per Architect Round 1 OQ-4.5-6 (NO Volcengine-specific envelope-code additions). NEW internal `ErrorKind` `endpoint_id_not_configured` per BR-4.6 (BR-1.12 fail-fast path — surfaced as `connect.CodeFailedPrecondition` from adapter → gateway, mapped to existing `502_upstream_unavailable` envelope code; slog disambiguation only — no new envelope code on the user-facing wire). FIRST Epic-4 non-identity translate; precedent for Story 4.6 (Ernie) which faces a different transform shape (URL path segment, not body field). |
| 2026-05-19 | Story 4.6 (Dev / Linus) | Public route behaviour change (no spec edit): `POST /v1/chat/completions` with `model = ernie-4.0` now dispatches to the real Ernie (Baidu Qianfan v2 OpenAI-compat) adapter via the Story-4.1 `apps/api-gateway/internal/adapterclient.Registry` (extended; Story-4.1/4.2/4.3/4.4/4.5 entries preserved as **SIX-vendor** cross-vendor regression guards per 4.6-INT-009 — the FIRST integration test exercising ALL ten model-id entries without shadowing; closes the Epic-4 vendor matrix). §5.2 `he.adapter.v1.AdapterService` proto contract REUSED VERBATIM. §5.1.1 / §5.1.1.1 / §5.1.2 shapes UNCHANGED — Architect Round 1 ratified OQ-4.6-1 option (a) Qianfan v2 OpenAI-compat over legacy aip.baidubce.com path-segment endpoint (REJECTED on KISS + operational-safety grounds: token-refresh failure adds a new blast-radius surface, and the architecture's pre-flagged URL-path-segment hint at `models-registry.md §4.5 Promotion rule` was written pre-Qianfan-v2-GA and is now obsolete advice). Identity-mapping translate per OQ-4.6-3 cascade verbatim — `apps/adapters/ernie/internal/upstream/translate.go` is byte-for-byte equivalent to Story-4.4 glm `translate.go`. BR-1.4 mapping REUSES Story-4.1/4.2/4.3/4.4/4.5 verbatim per Architect Round 1 OQ-4.6-6 (NO Baidu-specific envelope-code additions). NO new `ErrorKind` (option-(b)-scoped `ErrorKindAccessTokenRefreshFailed` REJECTED with option (b); Story-4.5 `endpoint_id_not_configured` N/A — no endpoint-id lookup). Empirical-curl verification of OQ-4.6-1/3/4/5 + m-2 upstream-request-id header DEFERRED to first staging deploy per Architect Recommendation. Six-vendor matrix COMPLETE — Epic-4 DoD line 1 ("6 家模型均可通过 /v1/chat/completions 以 OpenAI 协议调通") final precondition before Stories 4.7 (capability matrix endpoint + UI) and 4.8 (contract tests anti-regression) close the Epic. |
| 2026-05-20 | Story 4.7 (Dev / Linus) | (a) `GET /v1/models` response shape EXTENDED — every `data[i]` entry now carries a non-null `capabilities` sub-struct (7 fields per BR-1.2 — `chat`/`streaming`/`function_calling`/`vision`/`json_mode`/`context_window_tokens`/`max_output_tokens`). He-API extension placement convention: capability field appended LAST after OpenAI canonical fields per BR-1.4 (precedent: Story-3.6 `error.he_request_id`). Backward-compat verified via OpenAI Python SDK Pydantic `extra="allow"` round-trip (4.7-CONTRACT-001/002). (b) NEW endpoint `GET /public/models` — first unauthenticated route on the gateway, mounted OUTSIDE the bearer middleware chain (OQ-4.7-4 URL ratified; OQ-4.7-5 constructor-injection shared snapshot ratified). Byte-identical body for the catalogue payload (4.7-INT-001). (c) NEW envelope code `405_method_not_allowed` per OQ-4.7-6 added to §5.1.2 — used by `/public/models` 405 path with `Allow: GET` header per RFC 7231 §6.5.5. (d) `/public/*` CORS policy per OQ-4.7-7: `Access-Control-Allow-Origin: *` scoped to `/public/*` only; bearer-protected `/v1/*` retains tight allow-list; `Access-Control-Allow-Credentials` NEVER emitted on `/public/*` per Architect m-1 anti-credential guard (4.7-INT-007 mandatory test). Capability data home: in-memory Go map (OQ-4.7-2 option B; `he_api.models` migration deferred to Epic-5/6 routing-svc Stories). NO new gRPC services. Frontend deliverable: NEW `(marketing)` route group + `(marketing)/models/page.tsx` consume `/public/models` server-side. Epic-4 DoD line 3 ("能力矩阵公布") CLOSED. |
| 2026-05-25 | Story 5.1 (Dev / Linus) | **§5.1.2 NEW envelope codes (3)** per Architect Q4 ratified: `404_api_key_not_found` (BR-3.2 anti-enumeration collapse — cross-user / nonexistent share envelope); `400_invalid_key_name` (BR-1.7 / Q10 — Story-2.5 display_name parity; field=`name`); `429_rate_limit_key_create` (BR-1.10 anti-abuse; carries `Retry-After` seconds header). **REJECTED (per Q4 ratification)**: `403_api_key_owner_mismatch` (would leak id-space existence — collapsed to 404); `409_api_key_already_revoked` (revoke is idempotent 200 per BR-3.4 with `was_already_revoked` field). **§5.1.3 NEW endpoints (3)** under "其他端点" section: `POST /v1/me/keys` (create — body `{name}` strict-field; plaintext returned ONCE; rate-limited); `GET /v1/me/keys` (list — `key_hash` OMITTED at SQL boundary; LIMIT 100; money fields as string-decimals); `DELETE /v1/me/keys/{api_key_id}` (revoke — idempotent + anti-enumerable + immediate-effect via Redis sentinel per security.md §8.2.1 UPGRADE). **§5.2 `AuthService` extended** with `CreateApiKey` (sketch realised) + `ListApiKeys` (NET-NEW) + `RevokeApiKey` (sketch realised; **return type UPGRADED from `(Empty)` → `RevokeApiKeyResponse{api_key_id, revoked_at, was_already_revoked}` per Architect Q-Spec-1 ratification — the historical `revoked_at` is needed on the idempotent re-revoke path for UI rendering without a re-SELECT**). **NEW canonical contract — money-field serialization (Architect Q-Spec-4 ratified)**: NUMERIC PG columns surface on the OpenAPI as JSON strings (e.g., `"50.00"`) — NOT JSON numbers. Stripe precedent; preserves NUMERIC(10,2) / NUMERIC(12,4) precision on round-trip. FIRST money field on the API surface (`monthly_cost_cap_usd` + `current_month_cost_usd` via GET /v1/me/keys); future money-field stories (5.4, billing-svc) MUST cascade-lock. **Q-Spec-2 envelope discipline ratified**: JWT-path auth-svc failures use `502_auth_svc_unavailable` (existing — Story-3.6 promotion); NOT `503_auth_unavailable` (Story-3.2 bearer-path code). |
| 2026-06-03 | Story 5.2 (Dev / Linus) | **§5.1.3 NEW endpoint**: `PATCH /v1/me/keys/{api_key_id}` (JWT-cookie + global origin-CSRF) — partial-update of `scope.models` / `scope.ip_whitelist` / `monthly_cost_cap_usd`; strict 2-level field validation; cross-user / nonexistent / revoked collapse to `404_api_key_not_found`. **§5.2 `AuthService` extended** with `UpdateApiKey(UpdateApiKeyRequest) → UpdateApiKeyResponse` (+`ScopePatch`); **`ValidateApiKeyResponse` EXTENDED** with `optional monthly_cost_cap_usd` (carried to the gateway bearer cache for the AC4 cap gate — no per-request PG round-trip). **§5.1.2 FIRST-EMISSION** of three pre-declared codes (no new codes): `403_ip_not_whitelisted` (AC2), `403_model_not_in_scope` (AC3), `402_quota_exhausted` (AC4) — emitted by the NEW `keypolicy` middleware on the bearer hot path. Money-field discipline (Q-Spec-4) cascades: `monthly_cost_cap_usd` accepted/returned as string-decimal (`"50.00"` / `null`); a JSON number is rejected with `400_invalid_request` (BR-1.6). |
| 2026-06-03 | Story 5.5 (Dev / Linus) | **NO contract change** — the Console Keys page is a pure CONSUMER of the existing `/v1/me/keys{,/{id}}` family (Stories 5.1 + 5.2) and the §5.1.2 envelope codes. **Architect Q-E5 OVERRULED**: the proposed `cap_tripped:bool` field on the `GET /v1/me/keys` `ApiKeyEntry` shape was DE-SCOPED to keep Story 5.5 frontend-only; the UI computes the cap-tripped badge via a client heuristic (`current_month_cost_usd >= monthly_cost_cap_usd` in `CapBudgetBar`), accepting the documented GAP-CAP-001 cron-reset edge case. Console-side `KeyEntrySchema.monthly_cost_cap_usd` tightened to canonical `^\d+\.\d{2}$` (BR-L-4 safety net; `current_month_cost_usd` left loose — the shipped 5.1 contract emits `"0"`). No gateway / proto / DB change. |
| 2026-06-03 | Story 5.4 (Dev / Linus) | NO new REST endpoints, NO new envelope codes (reuses `402_quota_exhausted`). **§5.2 `NotificationService` EXTENDED** with `NotifyMonthlyCapThreshold(api_key_id, ThresholdLevel{WARNING_80\|TRIPPED}, request_id) → (was_already_notified, email_sent)` — fired fire-and-forget by the gateway keypolicy middleware; notification-svc owns the SETNX dedupe + localized SendGrid dispatch. **§5.2 `AuthService` EXTENDED** with `GetCapNotificationContext(api_key_id) → (user_email, user_locale, user_display_name, key_name, key_monthly_cost_cap_usd)` (Architect Round 1 Q-L Fix-A — internal lookup; preserves auth-svc as sole canonical `api_keys` reader). **`EmailTemplate` enum** ADDS `MONTHLY_CAP_WARNING=7` + `MONTHLY_CAP_TRIPPED=8`; NEW `ThresholdLevel` enum. The cap-trip 402 is emitted by the keypolicy sticky-trip sentinel fast-path (EXISTS hit, no counter GET) OR the counter-comparison slow path. |
| 2026-06-03 | Story 6.3 (Dev / Linus) | **§5.1.2 `502_upstream_unavailable` / `504_upstream_timeout` "即将 failover" REALISED**: a strategy-routed `POST /v1/chat/completions` whose selected upstream returns 502/504 now triggers sequential failover over the ranked `failover_chain` — max **3 total upstream attempts** + **30s total wall-clock budget** (`MaxFailoverAttempts=3` / `FailoverBudget=30s`); on success the response serves from a fallback model and `X-He-Selected-Model` reflects the FINAL served model (Q-F); on exhaustion the LAST 502/504 envelope is returned (zero regression). Failover triggers on these two codes ONLY (Q-C); non-retriable (400/403/402/429/content-filter) is terminal. The concrete-model DEFAULT path does NOT failover (empty chain, Q-D). Streaming failover is PRE-FLUSH only (Q-E). **§5.2 ONE additive RESPONSE field `SelectModelResponse.failover_chain=7`** (`repeated string` — the ranked fallback model ids AFTER `selected_model`, concrete-only; same additive pattern as `score_source=6`; passes `buf breaking: FILE`); populated by a NEW optional `RankedStrategy` engine capability (`SelectRanked`), the `SelectModelRequest` contract REUSED verbatim (Q-A Option A). Billing/rate-limit invariants: exactly ONE `TPMDeduct` on success, ONE QPS/RPM tick per request regardless of attempts (Q-H/Q-L). Failover-tail scope-filtered by the key's `scope.models` before dispatch (Q-K). Observability: `he_routing_failover_total{from_model,to_model,reason}` + `he_routing_failover_attempts` histogram (buckets `[1,2,3]`) + per-hop slog `routing_action=failover` (non-PII). NO new envelope code, NO new REST endpoint, NO migration. |
| 2026-06-03 | Story 6.2 (Dev / Linus) | **§5.1.1 documented-but-unwired routing behaviour REALISED**: `POST /v1/chat/completions` now consults `routing-svc` on every request — a `he-router-{quality,cost,latency}` meta-model in `model` OR the `X-He-Routing-Strategy: quality\|cost\|latency` header resolves (precedence meta-model > header > default, Q-I) to a real model selection. `X-He-Selected-Model` value source MIGRATES `req.Model` → the routing decision and is now a UNIVERSAL success-path invariant (set on mock + adapter non-stream + adapter stream — BR1-2; the prior mock-path-absent behaviour is intentionally retired, Story-6.1 UNIT-013 flipped). A concrete `model` with no strategy routes via STRATEGY_DEFAULT → `selected == req.Model` byte-for-byte (zero regression for the 6-vendor matrix). **§5.2 `RoutingService.SelectModel` REQUEST contract REUSED verbatim**; ONE additive RESPONSE field `SelectModelResponse.score_source=6` (string — `model_pricing`\|`clickhouse`\|`fallback`\|`default`; same additive pattern as `ab_selected_models=4`/`strategy_used=5`; passes `buf breaking: FILE`). FIRST gateway→routing-svc Connect-RPC client (`apps/api-gateway/internal/routingclient/`, 100ms deadline Q-E). **§5.1.2 gRPC→envelope mapping lands gateway-side (Q-H, NO new codes)**: `InvalidArgument→400_invalid_request`; `NotFound` on a `he-router-*` meta-model→`502_upstream_unavailable`, on a concrete-default miss→`400_invalid_request`. **Q-G fail-open/closed**: routing-svc Unavailable/DeadlineExceeded + concrete model → fail-OPEN passthrough of `req.Model` (chat keeps working); + `he-router-*` meta-model → fail-CLOSED `502_upstream_unavailable`. `X-He-AB-Models` IGNORED (A/B is Story 6.4, Q-N). NO new REST endpoint. Observability: slog decision line (non-PII) + Prometheus `he_routing_decisions_total{strategy,selected_model,score_source}` + `he_routing_select_duration_seconds`. |
| 2026-06-03 | Story 6.1 (Dev / Linus) | **§5.2 `RoutingService` proto sketch REALISED** as `he.routing.v1.RoutingService` (Connect-RPC) at `packages/proto/he/routing/v1/routing.proto` + vendored Go at `packages/proto/gen/go/he/routing/v1/`. FIRST `routing-svc` Go service (`apps/routing-svc/`, server-side only — gateway client wiring + real scoring are Story 6.2 per Q-B). The realised contract supersedes the pre-Epic-6 sketch: **Q-D** `strategy` upgraded `string`→`enum Strategy{UNSPECIFIED=0,DEFAULT=1,QUALITY=2,COST=3,LATENCY=4}` (engine maps `UNSPECIFIED(0) ≡ DEFAULT(1)`, never errors on zero-value); **Q-H** NEW `SelectModelRequest.he_request_id=5` (gateway correlation, slog-only echo); **additive** `SelectModelResponse.ab_selected_models=4` (Story 6.4 A/B; empty in 6.1) + **Q-I** `strategy_used=5` (echoes the actually-fired strategy); **Q-K** `adapter_endpoint=2` documented RESERVED/empty in 6.1 (the gateway resolves the endpoint via `adapterclient.Registry`; field number held stable). Field numbers 1/2/3 preserved from the sketch; this is a NEW proto file (`he.routing.v1`, not `he.api.v1`) so `buf breaking: FILE` is not crossed. **NO new §5.1.2 envelope codes** — routing-svc surfaces gRPC codes only (`InvalidArgument`/`NotFound`/`Internal`); the gRPC→§5.1.2 envelope translation lands gateway-side in Story 6.2 (BR1-3). **Q-E** SelectModel deadline budget = 100ms documented as a lifetime contract value (the actual `context.WithTimeout` lands gateway-side in 6.2). Models-catalogue lifted to shared `packages/models-catalogue/` (Q-A option (a)); `BuildPublicModelsSnapshot()` byte-identical (4.7-INT-001 unchanged). |

---
