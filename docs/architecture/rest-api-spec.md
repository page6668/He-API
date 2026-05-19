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
| 429 | `429_rate_limit_qps` | QPS 超限 |
| 429 | `429_rate_limit_tpm` | TPM 超限 |
| 429 | `429_rate_limit_gdpr_export` | GDPR 数据导出 24 小时内的限流（Story 2.6 AC2 BR-2.5；正常幂等路径返回 200 + 已存在的导出记录，仅在 100ms 级竞态下命中） |
| 413 | `413_payload_too_large` | 请求体超出 1 MiB 限制（Story 3.3 BR-1.2 — chat-completions handler） |
| 500 | `500_gateway_misconfigured` | 网关中间件未正确串联（防御性）— precedented at apps/api-gateway/internal/handlers/2fa_disable.go:38（Story 2.4 backfill） |
| 500 | `500_internal_error` | 系统异常 |
| 501 | `501_not_implemented` | 端点占位，未实现（Story 3.2 backfill — chatPlaceholder 501 stub deleted in Story 3.3 T0.4） |
| 501 | `501_streaming_not_implemented` | 流式响应未实现；将由 Story 3.4 落地（Story 3.3 BR-2.3 — non-streaming mock returns 501 for stream=true）— **RETIRED by Story 3.4 (2026-05-18)**: `stream=true` now serves SSE per §5.1.1.1; row preserved for git-blame traceability. |
| 502 | `502_upstream_unavailable` | 上游模型不可用（即将 failover） |
| 504 | `504_upstream_timeout` | 上游模型超时（即将 failover） |
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
GET  /v1/models                    返回模型列表 + 能力矩阵
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
  rpc RevokeApiKey(RevokeApiKeyRequest) returns (Empty);
}

service RoutingService {
  rpc SelectModel(SelectModelRequest) returns (SelectModelResponse);
}

message SelectModelRequest {
  string user_id = 1;
  string requested_model = 2;       // 'qwen-max' or 'he-router-cost'
  string strategy = 3;              // quality / cost / latency
  repeated string ab_models = 4;
}

message SelectModelResponse {
  string selected_model = 1;
  string adapter_endpoint = 2;
  bool is_ab_test = 3;
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

---
