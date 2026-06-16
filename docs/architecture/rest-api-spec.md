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
  X-He-AB-Models: qwen-max,deepseek-v3   # Story 6.4 WIRED — A/B dual-leg compare (exactly 2 distinct concrete ids; OVERRIDES strategy/model selection, Q-I; non-streaming only — stream=true + this header → 400)
  Accept-Language: en (locale for error messages)

Routing-strategy precedence (Story 6.5 EXTENDED the Story-6.2 cascade):
  he-router-{quality,cost,latency} meta-model in `model`   WINS
    → else X-He-Routing-Strategy header
    → else the caller's persisted ACCOUNT DEFAULT (Story 6.5)   [NEW tier]
    → else STRATEGY_DEFAULT (passthrough of the concrete `model`)
  The account default is set via PUT /v1/me/profile.default_routing_strategy
  (§ /v1/me below) and resolved on the hot path from a gateway per-user cache
  (single Redis GET, sentinel-invalidated on save; Q-A Option B). It is a
  CONVENIENCE preference — an unresolvable default fails OPEN to STRATEGY_DEFAULT
  (chat never blocks, Q-F). When the account default drives the decision the slog
  carries strategy_source=user_default (non-PII). The account default does NOT
  bypass API-key scope: a default that resolves to a model outside the key's
  scope.models yields the same Story-5.2 keypolicy 403 the user would get today
  (Q-E — correctness, not a security regression). A/B (X-He-AB-Models) is itself a
  per-request directive and is NOT affected by the account default.

A/B mode (X-He-AB-Models, Story 6.4):
  When X-He-AB-Models: modelA,modelB is present, the gateway dispatches BOTH
  models in PARALLEL and returns BOTH outputs in ONE OpenAI-compatible
  chat.completion (Q-A Option A). The body stays a valid chat.completion:
  - choices[] = legA's choices followed by legB's, with `index` re-assigned
    0,1,…; each choice carries an additive `x_he_model` naming its source leg.
  - usage = the SUMMED tokens of the successful legs.
  - top-level `model` = leg A (the first X-He-AB-Models id).
  - response header `X-He-AB-Models: modelA,modelB` echoes the SUCCESSFULLY
    served ids in order; X-He-Selected-Model is NOT set on the A/B path.
  Partial failure (Q-E): if ≥1 leg succeeds → 200 with the success choice(s) +
  a failed-leg marker choice (finish_reason:"he_upstream_error" + x_he_model +
  an additive x_he_error §5.1.2 envelope); only successful legs are billed
  (Q-F dual-billing). If BOTH legs fail → one terminal §5.1.2 envelope
  (representative code 504 if any leg timed out, else 502). Legs are concrete
  pins and do NOT failover (Q-D). Both legs must pass the key scope.models gate
  (Q-K) — an out-of-scope leg → 403 for the whole request. One A/B request = one
  QPS/RPM tick (Q-J); TPM reflects both legs.

A/B success body (non-stream, X-He-AB-Models: qwen-max,deepseek-v3):
{
  "id": "chatcmpl-xxx", "object": "chat.completion", "created": 1715000000,
  "model": "qwen-max",
  "choices": [
    {"index": 0, "message": {...}, "finish_reason": "stop", "x_he_model": "qwen-max"},
    {"index": 1, "message": {...}, "finish_reason": "stop", "x_he_model": "deepseek-v3"}
  ],
  "usage": {"prompt_tokens": 200, "completion_tokens": 400, "total_tokens": 600}  // summed
}

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
| 400 | `400_unsupported_currency` | GET /v1/balance\|usage — `?currency=` 不在 {usd, rmb}（fail-loud，无静默回退 USD）— Story 7.2 Architect M-1 ratified；**Story 7.3 REUSE**: POST /v1/billing/recharge — `currency` 不在该 provider 启用集（默认 USD-only，Q-CURRENCY m3；**Story 7.5 alipay 启用 {USD, CNY}**；**Story 7.6 wechat 启用 {USD, HKD, CNY}**，per-provider currency gate） |
| 400 | `400_invalid_payment_request` | POST /v1/billing/{recharge,subscriptions} — 请求体非法 / `amount ≤ 0` / 未知 plan（Story 7.3 Architect ratified §5.1.2） |
| 400 | `400_unsupported_payment_provider` | POST /v1/billing/* — `provider` 不在 {stripe, paypal, coinbase, alipay, wechat}（**Story 7.4 加入 coinbase（USDC）**；**Story 7.5 加入 alipay（Alipay+）**；**Story 7.6 加入 wechat（WeChat Pay HK）**）— Story 7.3/7.4/7.5/7.6 |
| 402 | `402_payment_failed` | POST /v1/billing/* — 支付通道 CreateCheckout/CreateSubscription 失败（payment-domain 失败，**非** 5xx；5xx 仅保留给自有基础设施故障 BR-W-5）— Story 7.3 |
| 400 | `400_webhook_signature_invalid` | POST /v1/billing/webhooks/{stripe,paypal,coinbase,alipay,wechat} — 签名缺失/伪造/重放/篡改/过期（验签先于任何 body 解析，body 永不入账 BR-W-1）；**Story 7.6 wechat** 额外：AES-256-GCM `resource` 解密失败（tag 不匹配/错误 APIv3Key）同样 fail-closed → 400（验签 → 解密 → 解析，永不解析半明文 BR-W-3）— Story 7.3/7.4/7.5/7.6 |
| 429 | `429_rate_limit_qps` | QPS 超限（每秒请求数）|
| 429 | `429_rate_limit_rpm` | RPM 超限（每分钟请求数）— Story 5.3 Architect Q5 ratified |
| 429 | `429_rate_limit_tpm` | TPM 超限（每分钟上游 token 数）|
| 429 | `429_rate_limit_gdpr_export` | GDPR 数据导出 24 小时内的限流（Story 2.6 AC2 BR-2.5；正常幂等路径返回 200 + 已存在的导出记录，仅在 100ms 级竞态下命中） |
| 429 | `429_rate_limit_usage_log_export` | 历史日志导出 24 小时内的限流（Story 9.3 AC1 BR-EX-5；与 `gdpr_export` 完全独立的 key 命名空间 `usage_log_export:{user_id}`，互不消耗配额；正常幂等路径返回 200 + 已存在的导出记录，仅在 100ms 级竞态下命中） |
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
| 400 | `400_invalid_request` | PUT /v1/me/profile — `default_routing_strategy` 为空字符串 `""`（gateway 区分 `null`=清除 vs `""`=非法 vs 省略=不变；Story 6.5 BLIND-BOUNDARY-002） |
| 400 | `400_invalid_default_routing_strategy` | PUT /v1/me/profile — `default_routing_strategy` 非枚举值 `{quality,cost,latency}`（auth-svc 校验，无 DB 写；Story 6.5 UNIT-010） |
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
| 403 | `403_account_pending_deletion` | 帐号待删 — 宽限期内 console/业务 API 访问被拒（**Story 2.7 首个消费方**；OQ-1 RATIFIED 调和：签入/恢复页/取消端点**不**触发此码——登录鉴权成功但只发"恢复态会话"，console 路由重定向到恢复页。原 2.5「拒绝登录」措辞被 OQ-1 取代）（Story 2.5 预留，Story 3.6 promoted） |
| 403 | `403_bad_reauth` | POST /v1/account/deletion — 删除前的二次鉴权失败（密码/确认邮箱/TOTP 任一不匹配；AC2 BR-2.4 OWASP ASVS V8.3）；无状态变更（Story 2.7） |
| 409 | `409_account_not_deletable` | POST /v1/account/deletion — 账号 status ∈ {suspended,locked,deleted} 不可删（AC2 BR-2.6）（Story 2.7） |
| 410 | `410_grace_expired` | POST /v1/account/deletion/cancel — 宽限期已过/sweeper 已物理删除，恢复不再可能（AC3 BR-3.3）（Story 2.7） |
| 429 | `429_rate_limit_account_delete` | POST /v1/account/deletion — 删除请求 5/24h 防误触限流（AC2 BR-2.5；遵循 `429_rate_limit_<scope>` 命名约定 m-4）（Story 2.7） |
| 403 | `403_api_key_not_owned` | POST /v1/me/playground/chat — 体携 `api_key_id` 非本人/未知/已吊销（Story 10.6；IDOR fence `WHERE user_id=$JWT.sub`；QA 10.6-INT-002 钉死 403，区别于 5.1 管理面 `404_api_key_not_found` anti-enumeration） |
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
| 503 | `503_clickhouse_unavailable` | 用量大盘 — ClickHouse 读取不可用（Story 9.1 / Q-CODE M-3；`server_error`） |
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
GET  /v1/usage                     查询本月用量（自定义端点；Story 7.2：`?currency=usd|rmb` 币种感知）
GET  /v1/balance                   查询余额（Story 7.2：`?currency=usd|rmb` 币种感知）
GET  /v1/me/usage/summary          用量大盘汇总（Story 9.1；JWT-cookie，per-user IDOR；今日/本月/季度 × 请求数/成功率/Token/消费；无 query 参数 strict-reject；请求数/成功率/Token ← ClickHouse，消费 ← PG usage_ledger（H-1-R 计费 SoT）；cost_usd string-decimal 可空）
GET  /v1/me/usage/logs             实时调用日志（Story 9.2；JWT-cookie，per-user IDOR；最近 1000 条；多维过滤 limit/offset/model/status/is_streaming/api_key_id/start/end；ONLY 13 非-PII 字段，无 cost；CH 直读 request_logs）
POST /v1/me/usage/logs/export      历史日志异步导出（Story 9.3；JWT-cookie，per-user IDOR；body `{format:json|csv, range_days?|start?/end?}`；strict-fields reject；代理 notification-svc；返回 `{export_id,status,format,requested_at}`；ALWAYS 挂载，不依赖 CLICKHOUSE_DSN）
GET  /v1/me/usage/logs/export/current  当前历史日志导出状态（Story 9.3；kind-scoped；返回 `{export_id,status,format,requested_at,signed_url_expires_at|null}|null`；从不返回原始签名 URL）
POST /v1/me/playground/chat        Playground 代理 chat（Story 10.6；JWT-cookie，per-user IDOR；body 携已拥有 `api_key_id`(UUID) → 校验 ownership(`WHERE user_id=$JWT.sub`) → 复用既有 per-key policy + 内部 chat 管线(routing/adapters/content-safety/TPMDeduct/scope/A·B)；浏览器绝不持明文 Key；流式 SSE passthrough 同 §5.1.1；`Cache-Control: no-store`；无新 proto/RPC/migration）
```

**Story 9.3 — `POST /v1/me/usage/logs/export` + `GET /v1/me/usage/logs/export/current`（NEW，JWT-cookie，per-user）**：异步历史日志下载（90 天 + JSON/CSV）。EXTENDS 2.6 GDPR-export 管线（`data_export_requests` +`kind`(`gdpr_full`|`usage_logs`)+`format`(`json`|`csv`)，migration 0015），不重复造管线。**POST body** `{format:"json"|"csv"(必填), range_days?:1..90, start?/end?:rfc3339}`，strict-fields decode（`DisallowUnknownFields`，me_keys.go 先例）— 任何未知字段（含伪造 `user_id`）→ `400_invalid_request`（BR-EX-1，user_id ONLY from JWT `sub`）。**幂等（BR-EX-4）** 按 `(user_id, kind, format)` scope，24h 窗口内返回同一 `export_id`（json/csv 独立；failed 行不计）。**限流安全网（BR-EX-5）** 独立 key 命名空间 → `429_rate_limit_usage_log_export`（不消耗 GDPR 配额）。代理 notification-svc `UsageLogExportService`（`he.usagelog.v1`，Q-OWNER=notification-svc 拥有），PG commit 后发布 `usage.log.export.requested`（key=user_id）。analytics-svc `UsageLogExportWorker` dump `request_logs`（fenced `WHERE user_id=?`，≤90d，**13 个非-PII 列 = 9.2 BR-RD-9，cost_usd 永不导出** H-1-R）→ JSON-Lines 或 RFC-4180 CSV（OWASP 公式注入转义）→ OSS `usage-log-exports/{user_id}/{export_id}.{ext}` → 签名 24h URL → email（**链接仅通过邮件投递，控制台从不渲染原始 URL** BR-UI-3 / TS-CONS-008）。**GET .../current** kind-scoped（GDPR 导出绝不遮蔽 usage-log 导出，BR-EX-7）。缺 JWT → `401`；notification-svc 不可用 → `502/503`，超时 → `504`；`Cache-Control: no-store`。

**Story 10.6 — `POST /v1/me/playground/chat`（NEW，JWT-cookie，per-user，additive — OQ-10.6-1 (b)）**：console Playground 的计费 chat 代理，是 §5.1.1 `/v1/chat/completions` 的 **JWT-authed 同胞**。挂既有 `/v1/me/*` JWT-cookie 中间件；**身份 = JWT `sub` only**——浏览器**绝不持明文 bearer key**（避开 FR-5.2 落差 + XSS 外泄）。**计费/scope/限流归属（REUSE，不重造）**：请求体携带用户**已拥有的 `api_key_id`(UUID)**（console 经 me-keys 已可列、非明文）→ 网关校验 ownership（`WHERE user_id=$JWT.sub`，IDOR fence，复用 auth-svc `ListApiKeys` RPC，无新 RPC）→ 解析为既有 per-key policy → 注入 bearer-style context → 转发**同一内部 chat dispatch**（routing 6.2 / adapters / content-safety in+out 8.2-8.3 / `TPMDeduct` / `scope.models` / 7.1 billingGate 预检 402）。**strict-decode**（`DisallowUnknownFields`，me_keys.go 先例）— 未知字段含伪造 `user_id` → `400_invalid_request`（user_id ONLY from JWT `sub`）。**单模型 scope.models** 在本处门控（bearer 路径的 keyPolicy 中间件不在此 JWT 链）；**A/B** 经 `X-He-AB-Models` 头原样流经（非流式 only、双计费、两 leg 均过 scope，6.4 Q-K，在既有 dispatchAB 内门控）。**流式** = SSE passthrough 同 §5.1.1。`api_key_id` 非本人/未知/已吊销 → `403_api_key_not_owned`（QA 10.6-INT-002 钉死 403；区别于 5.1 管理面 404 anti-enumeration）；其余复用既有 envelope（401/403_model_not_in_scope/429/402/400/502/504）；`Cache-Control: no-store`。**无新 proto / 无新 gRPC RPC / 无 migration**——内部转发既有 chat dispatch。

**Story 9.1 — `GET /v1/me/usage/summary`（NEW，JWT-cookie，per-user）**：返回 `{today, month, quarter}`，每段 `{requests:int, success_rate:"string-decimal"|null, tokens:{prompt,completion,total}, cost_usd:"string-decimal"|null}`。**Per-metric SoT split（H-1-R / R2-2）**：`requests`/`success_rate`/`tokens` ← ClickHouse（`today` 直读 `request_logs`，Q-RT 实时 ≤~10s staleness；`month/quarter` 读 `request_logs_hourly_agg` MV）；`cost_usd`（消费）← PG `he_api.usage_ledger` 的 `COALESCE(SUM(cost_usd),0)`（计费 money SoT，billing-svc 计算，网关复用现有 `pgxpool`），与 CH 同一 user-tz period 边界。**两源独立降级（R2-4）**：`usage_ledger` 读失败 → 该 period 的 `cost_usd=null`（UI 渲染 "—"），summary 仍 200、CH cells 正常，**不**触发 `503_clickhouse_unavailable`（该 envelope 仅限 CH）。每条查询（CH **与** `usage_ledger`）`WHERE user_id=$JWT.sub`（IDOR fence，BR-RD-1/R2-6）；query 参数 strict-reject → `400_invalid_request`；CH 不可用 → `503_clickhouse_unavailable`；`Cache-Control: no-store`。区别于 bearer-API-key `GET /v1/usage`（7.1/7.2，per-key，本月，OpenAI-style，未改动）。`success_rate` 在 requests=0 时为 `null`（UI 渲染 "—"，永不 NaN）。`client_ip` 等 ops 字段永不返回（BR-ING-5）。`request_logs.cost_usd` 列保留但 **non-authoritative**（恒为 0；网关无 USD 定价 seam，OQ5；预留给 9.3 raw-row 下载），大盘消费**不**读此列。

**Story 9.2 — `GET /v1/me/usage/logs`（NEW，JWT-cookie，per-user，sibling to /summary）**：返回 `{items:LogEntry[], total_count:int, limit:int, offset:int, has_more:bool}`，`LogEntry` = 13 个非-PII 可观测字段 `{he_request_id, ts, model, upstream_model, status_code, is_streaming, prompt_tokens, completion_tokens, total_tokens, latency_ms_total, ttfb_ms, api_key_id, error_code}`。**最近 1000 条（BR-RD-2）**：服务端强制 `offset+limit ≤ 1000`（`offset∈[0,999]`、`limit∈[1,100]` 缺省 50），`total_count=min(matching,1000)`，`has_more=offset+len(items)<total_count`；`ORDER BY ts DESC, he_request_id DESC`（稳定 tie-break）。**多维过滤（BR-RD-5，AND 组合，全可选）**：`model`（精确）、`status∈{success,client_error,server_error}`（映射 `<400`/`400-499`/`≥500`）、`is_streaming∈{true,false}`、`api_key_id`（UUID，仍受 user_id fence）、`start`/`end`（RFC3339，`end>start`）。**IDOR fence（BR-RD-1）**：每条 SQL `WHERE user_id=$JWT.sub`；`user_id` 参数或任何未识别参数 strict-reject → `400_invalid_request`（`param`=违规字段）。**PII（BR-RD-9）**：`client_ip`/`client_country`/`user_agent`/`error_message`/`cost_usd` 永不返回（cost non-authoritative，H-1-R，无 per-request 消费）。复用 9.1 `request_logs` 直读（无新 store/migration/topic/envelope，BR-RD-10）；缺 JWT → `401_unauthenticated`；CH 不可用 → `503_clickhouse_unavailable`；DSN 未设 → 路由不挂载（404，graceful degradation）；`Cache-Control: no-store`。

**Story 7.2 — `GET /v1/balance` + `GET /v1/usage` 币种感知（additive，无新端点）**

- 选择器：stateless `?currency=usd|rmb`（缺省 `usd`；大小写不敏感、trim；Q-PREF）。MVP 仅支持 `{usd, rmb}`，其它值 → `400_unsupported_currency`（在任何 PG 读取之前 reject，BR-B-5）。
- 追加字段（全部 string-decimal，Q-Spec-4 / BR-B-4）：`currency`（复用 7.1 seam，现反映选择）、`current_display`/`total_cost_display`、`fx_rate`（8dp）、`fx_as_of`（RFC3339，= 汇率行 `fetched_at`，暴露 staleness，BR-B-8）。冷启动降级时追加 `fx_degraded:true`。
- USD 缺省路径与 7.1 字节兼容（`fx_rate:"1.00000000"`、`current_display==current_usd` 按 4dp echo SoT，BR-B-7）。RMB 路径 USD→CNY HALF-UP 2dp（display-only，Q-SOT — `current_usd` 永远在场，BR-B-3）。
- **混合币种约定（M-2 / Q-APISHAPE，MVP cut）**：`?currency=rmb` 时仅顶行 `total_cost_display` 转 RMB，`by_model[].cost_usd` 仍为 USD（per-model 转换 deferred 到 Console story）。
- 换算仅 read-side display（Q-SOT）：扣费/ledger/402 预检/对账与 7.1 字节一致，绝不写 `balances.*` / `usage_ledger`。汇率来自网关 `internal/fxrate` 进程内快照（boot+60s，最新行 `ORDER BY fetched_at DESC LIMIT 1`），绝不在读热路径调用 FX provider（BR-C-5）。

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
  repeated string ab_models = 4;    // Story 6.4 REALISED — gateway POPULATES from X-He-AB-Models (exactly 2 concrete ids); empty on the single-model path
  string he_request_id = 5;         // Q-H: gateway correlation id (Story 3.6); slog-only echo
}

message SelectModelResponse {
  string selected_model = 1;
  string adapter_endpoint = 2;      // Q-K: RESERVED/empty in 6.1; gateway resolves via adapterclient.Registry
  bool is_ab_test = 3;              // Story 6.4 REALISED — TRUE on the A/B path (was always false in 6.1/6.2/6.3)
  repeated string ab_selected_models = 4;  // Story 6.4 REALISED — the 2 resolved concrete leg ids (he-router-*/unknown rejected → InvalidArgument); empty on the single-model path
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
| 2026-06-16 | Story 6.5 (Dev / Linus) | **§5.1.1 routing-strategy precedence EXTENDED** with a NEW account-default tier: meta-model > `X-He-Routing-Strategy` header > **persisted account default** > `STRATEGY_DEFAULT`. The default is set via `PUT /v1/me/profile.default_routing_strategy` (additive optional field `quality\|cost\|latency\|null`) and surfaced on `GET /v1/me.default_routing_strategy` (always present, nullable). Resolved on the chat hot path from a gateway per-user cache (single Redis GET on the auth-svc-owned `user:routing_pref:{id}` key, sentinel-invalidated `user:pref_updated:{id}` on save — Q-A Option B; the JWT-`drs`-claim Option A was OVERRULED for violating the BR-3.6 claim-minimization invariant). Fails OPEN to `STRATEGY_DEFAULT` (Q-F); `strategy_source=user_default` slog (non-PII); an out-of-scope default still 403s via the Story-5.2 keypolicy gate (Q-E, not a security regression); A/B (`X-He-AB-Models`) is unaffected (Q-G sub-fork). **§5.1.2 +2 codes**: `400_invalid_request` (empty-string `""` rejected at the gateway — distinct from `null`=clear / omitted=unchanged, BLIND-BOUNDARY-002) + `400_invalid_default_routing_strategy` (non-enum value, auth-svc-validated, no DB write). **auth-svc gRPC `UpdateProfileRequest`/`GetMeResponse` + `GetMeResponse` gain an additive proto3-optional field (buf breaking: FILE green)**; migration `0019` ADD COLUMN `users.default_routing_strategy VARCHAR(20) NULL` (additive, nullable, reversible). NO new endpoint/RPC. |
| 2026-06-16 | Story 10.6 (Dev / Linus) | **§5.1.3 NEW endpoint `POST /v1/me/playground/chat`** (additive, JWT-cookie, per-user — OQ-10.6-1 (b)). JWT-authed sibling of §5.1.1 `/v1/chat/completions` for the console Playground: browser holds NO plaintext key; identity = JWT `sub`; the body carries the caller's OWN `api_key_id` (UUID) → ownership-verified (`WHERE user_id=$JWT.sub`, IDOR fence, reuses the auth-svc `ListApiKeys` RPC — NO new RPC) → resolved to the existing per-key policy → forwarded through the SAME internal chat dispatch (routing 6.2 / adapters / content-safety in+out 8.2-8.3 / `TPMDeduct` / `scope.models` / 7.1 billingGate). strict-decode (`DisallowUnknownFields`) rejects unknown/forged `user_id` → `400_invalid_request`; single-model scope gated at the handler (the bearer keyPolicy middleware is not in this JWT chain); A/B via `X-He-AB-Models` flows through unchanged (non-stream only, dual-billing, both legs scope-gated in dispatchAB); SSE passthrough per §5.1.1; `Cache-Control: no-store`. **§5.1.2 +1 code** `403_api_key_not_owned` (foreign/unknown/revoked api_key_id; QA 10.6-INT-002 fixes the contract at 403, distinct from the 5.1 management-surface `404_api_key_not_found` anti-enumeration). **NO new proto / gRPC RPC / migration / per-user billing path** — internal forward of the existing chat dispatch. Console: new `/{locale}/playground` (authed) + public `/{locale}/(marketing)/benchmark`; benchmark He-prices render from a console pricing SoT (curated seed never hardcodes He prices); Playground cost is a labeled display ESTIMATE (never `X-He-Cost-Usd`/`usage_ledger` — H-1-R money SoT unchanged). |
| 2026-06-11 | Story 9.1 (Dev / Linus, H-1-R re-wire) | **§5.1.3 `GET /v1/me/usage/summary` cost-source correction (NO new endpoint / NO new envelope code).** Architect Round-2 escalation **H-1-R** overturned H-1: the gateway has NO USD pricing seam (`X-He-Cost-Usd` ABSENT per ratified OQ5), so `cost_usd` is no longer read from ClickHouse `request_logs.cost_usd` (which is non-authoritative, always 0). **Per-metric SoT split**: `requests`/`success_rate`/`tokens` ← ClickHouse; **`cost_usd`（消费）← PG `he_api.usage_ledger`** `COALESCE(SUM(cost_usd),0)` (the billed-money SoT, computed once by billing-svc, reusing the gateway `pgxpool` — no new PG path; IDOR-fenced `WHERE user_id=$JWT.sub`; same user-tz period bounds as the CH queries). **Wire contract change**: `cost_usd` is now `"string-decimal"|null` (was NON-NULL) — a `usage_ledger` read error renders that period's `cost_usd=null` while the summary still returns 200 with the CH-sourced cells populated and does NOT emit `503_clickhouse_unavailable` (independent degradation, R2-4). `request_logs.cost_usd` stays in the schema/event but is reserved for the 9.3 raw-row download. ZERO proto/migration/writer churn (R2-3); the change is the AC2 read path only (`apps/api-gateway/internal/analyticsquery`). |
| 2026-06-11 | Story 8.3 (Dev / Linus) | **Behaviour change, NO §5.1.2 table edit, NO new error code, NO new endpoint, ZERO DB** — `POST /v1/chat/completions` now enforces the §9.3 **出参** (OUTPUT) content-safety filter, completing the bidirectional filter 8.2 began on the 入参 side. The model-generated completion is scanned against the SAME Story-8.1 `DefaultLexicon` via the EXACT 8.2 `contentsafety.Scanner` (no fork). **Non-stream**: after the completion is assembled and BEFORE the 200 body is written (mock path, adapter non-stream, AND the 6.4 A/B merge — each choice scanned + redacted INDEPENDENTLY, `x_he_model` preserved), the FIRST confirmed hit per choice REPLACES `message.content` with a fixed non-leaking 脱敏 notice and sets `finish_reason:"content_filter"` — the OpenAI-native **success-body** value (status STAYS 200; this is NOT an error — the `400_content_filter` code remains INPUT-only). **Stream**: a per-request bounded rune sliding-window guard (`contentsafety.StreamGuard`, window = `max(512, max(TermLengths()))` ≥ longest term → no-false-negative by construction) observes each content delta scan-BEFORE-forward; on a confirmed hit (incl. a term split across chunk boundaries) the stream IMMEDIATELY terminates — the term-completing delta is WITHHELD, an **empty-content** `content_filter` terminal + `data: [DONE]` is emitted via the normal `Writer.WriteEvent`+`WriteDone()` path (NOT an SSE error frame), and already-flushed chunks are unrecoverable (§9.3 "无法收回") but the interception IS logged. The detection seam lives at the CHUNKER level (optional nil-default guard on both `MockChunker`+`AdapterChunker` → byte-identical when unwired; AR1-R4 Writer-minimalism); the handler branches on the `streaming.ErrContentFiltered` sentinel before its `writeSSEErrorFrame` classification. **Billing UNCHANGED** (inverse of 8.2): 8.3 acts POST-dispatch so the upstream tokens are already consumed — the existing non-stream `TPMDeduct`/`emitUsage` and the stream `maybeStreamTPMDeduct` (partial-tail arm) fire on the real usage; redaction mutates only the body, never `resp.Usage`. The OUTPUT interception event REUSES the 8.2 `Recorder`/`SafetyEvent` seam with the additive `contentsafety.DirectionOutput = "output"` const (`direction:"output"`, `action:"blocked"`); handed to the injected no-op `Recorder` this story — **ZERO DB change**; Story 8.5 binds ONE persisting writer for BOTH directions with no contract change. Severity-agnostic block-on-any-match (the future "strict" posture; per-Key 严格度 is Story 8.4). Tool-arg / multimodal output remain DEFERRED (Epic 9). NO new CI lane (the gateway `-race` lane already covers it). |
| 2026-06-10 | Story 8.2 (Dev / Linus) | **Behaviour change, NO §5.1.2 table edit, NO new code** — `POST /v1/chat/completions` now enforces the §9.3 入参 content-safety filter. After `validateChatRequest` and BEFORE any routing / A·B / adapter / mock / stream / failover dispatch, every `messages[].content` string (all roles — all client-supplied) is Normalize-d via the shared `safetylexicon.Normalize` and resolved against the Story-8.1 `DefaultLexicon` (§9.3 Bloom 快速排除 → 词典匹配). The FIRST confirmed hit REJECTS the request with the **already-registered** `400_content_filter` envelope (`{400, invalid_request_error}`, §5.1.2:124 — 8.2 is its first caller, NOT a new code) with `param=null` (no matched substring leaks to the caller). A blocked request makes ZERO upstream calls and incurs ZERO usage/billing (the metering hooks fire downstream of dispatch). A clean request is byte-identical to the pre-8.2 path. The scanner lives in `apps/api-gateway/internal/contentsafety/` (in-gateway, billinggate-placement precedent); segmentation is consumer-owned (8.1 BR-3.4). The interception event is handed to an injected no-op `Recorder` this story — **ZERO DB change**; Story 8.5 supplies the `content_safety_logs`-writing impl + excerpt redaction + retention + 备案 PDF. Severity-agnostic block-on-any-match (the future "strict" posture); per-Key 严格度 is Story 8.4. Additive `safetylexicon.Lexicon.TermLengths()` accessor added (pure window-bound surface; existing behaviour unchanged). Tool-arg / multipart-multimodal scanning + S⇄T fold remain DEFERRED (tracked forward-carries — Epic 9 / a later 8.x). NO new CI lane (the gateway + safety-lexicon `-race` lanes already cover it). |
| 2026-06-10 | Story 7.8 (Dev / Linus) | **Epic-7 FINALE — subscription tiers + Beta-mode.** **§5.1.3 NEW endpoints** (bearer-API-key, user_id server-resolved — BR-S-7): `GET /v1/billing/subscription` (caller's plan + resolved entitlements + period end; default-free Q-PLAN-DEFAULT), `PUT /v1/billing/subscription` (upgrade-immediate / downgrade-deferred BR-S-2), `DELETE /v1/billing/subscription` (cancel → free at period end Q-REFUND), `GET /v1/billing/plans` (public tier catalogue render — in-process `packages/plan-catalogue`, no RPC). **⚠️ Endpoint-naming convention (Architect Medium-2, BINDING)**: the SINGULAR `/v1/billing/subscription` is the caller's **singleton management sub-resource** (the one-active-subscription-per-user invariant); it is INTENTIONALLY distinct from the 7.3 PLURAL `POST /v1/billing/subscriptions` create-rail — singular = manage-my-one, plural = create. Read together they are not a typo. **§5.1.2 codes (Architect Medium-1)**: NO new `400_invalid_plan` — an unknown/empty plan REUSES the 7.3 `400_invalid_payment_request`; a provider-domain plan-change failure REUSES `402_payment_failed`. The `admin-forbidden` code is DROPPED (Q-ADMIN-BETA — no in-app admin surface; the Beta toggle is Unleash-console-only). **§5.2 `he.billing.v1.BillingService` +3 additive RPCs**: `GetSubscription` / `ChangePlan` / `GetEntitlements`. **§5.2 `he.payment.v1.PaymentService` +1 additive RPC**: `UpdateProviderSubscription` (Stripe proration_behavior / PayPal revise; optional `SubscriptionUpdater` capability — Stripe/PayPal only); `PaymentEvent` += `plan` (field 11, carries the confirmed tier on the webhook — BR-S-3). `buf breaking: FILE` passes (all additive; round-trip GREEN). **Hot-path enforcement (AC2/AC3)**: the gateway resolves a cached `entitlement:user:{id}` snapshot (billing-svc SOLE writer) → composes `min(plan, sandbox)` when Beta ON → feeds the Story-5.3 limiter via the stable `ratelimit.ResolveCeilings` seam (BR-E-4 — keys/scripts/envelopes UNCHANGED); fail-safe-LOW on miss (BR-E-2). Money string-decimal (Q-Spec-4). **NO new money SoT / credit path** — the subscription charge reuses the 7.3 provider rail + exactly-once webhook credit. |
| 2026-06-10 | Story 7.6 (Dev / Linus) | **ADDITIVE (no new RPC / proto / migration / Kafka topic / endpoint shape)** — WeChat Pay HK / cross-border (5th/FINAL channel — closes the Epic-7 5-channel DoD). **§5.1.3 NEW route** `POST /v1/billing/webhooks/wechat` (UNAUTHENTICATED-but-SIGNED, mounted OUTSIDE bearer+CSRF; gateway byte-faithful proxy forwarding ALL headers verbatim — Wechatpay-Signature/Timestamp/Nonce/Serial — → payment-svc; same pattern as stripe/paypal/coinbase/alipay, NO proxy change). **§5.1.3 `POST /v1/billing/recharge` EXTENDED**: `provider` set adds `wechat`; the per-provider currency gate enables **wechat {USD, HKD, CNY}** (港澳=HKD, 海外华人=USD/CNY; a HKD/CNY settlement credits the USD single-SoT `balances.current_usd` at the latest 7.2 fx_rate at credit time, Q-CURRENCY); `checkout_url` is a WeChat Native `code_url` QR string; NO `usdc_address` (coinbase-only). **§5.1.2 codes REUSED unchanged** (`400_unsupported_payment_provider` now lists wechat; `400_unsupported_currency` per-provider; `400_webhook_signature_invalid` / `402_payment_failed` apply verbatim). ⚠️ FIRST ENCRYPTED webhook body: `Wechatpay-Signature` verified RSA-SHA256 with the WeChat PLATFORM public key SELECTED BY `Wechatpay-Serial` over `<Wechatpay-Timestamp>\n<Wechatpay-Nonce>\n<rawBody>\n` (NO method/URI → no `r.URL.Path` trap; unknown serial → reject); `Wechatpay-Timestamp` freshness window (replay layer 1); THEN the `resource` is AES-256-GCM-decrypted with the APIv3Key (fail-closed on tag mismatch, BR-W-3). Credit only on `trade_state=SUCCESS` (BR-C-1); minor-unit (HKD added) → string-decimal fail-closed (BR-W-8). ZERO credit.go change (the 7.5 paid-currency guard is inherited). |
| 2026-06-10 | Story 7.5 (Dev / Linus) | **ADDITIVE (no new RPC / proto / migration / Kafka topic)** — Alipay+（Antom international）. **§5.1.3 NEW route** `POST /v1/billing/webhooks/alipay` (UNAUTHENTICATED-but-SIGNED, mounted OUTSIDE bearer+CSRF; gateway byte-faithful proxy forwarding ALL headers verbatim — Client-Id/Request-Time/Signature — → payment-svc; same pattern as stripe/paypal/coinbase, NO proxy change). **§5.1.3 `POST /v1/billing/recharge` EXTENDED**: `provider` set adds `alipay`; the currency gate is now **per-provider** — alipay enables {USD, CNY} (a CNY settlement credits the USD single-SoT `balances.current_usd` at the latest 7.2 fx_rate at credit time, Q-CURRENCY); other providers stay USD-only. NO new response field (no `usdc_address` for alipay). **§5.1.2 codes REUSED unchanged** (`400_unsupported_payment_provider` now lists alipay; `400_unsupported_currency` now per-provider; `400_webhook_signature_invalid` / `402_payment_failed` apply verbatim). ⚠️ FIRST ASYMMETRIC webhook signature: RSA2 (SHA256withRSA) verified with Alipay+'s PUBLIC key over the constructed string `POST <publicNotifyPath>\n<Client-Id>.<Request-Time>.<rawBody>` (NOT raw-body HMAC); `Request-Time` freshness window RESTORES a signature-timestamp replay layer (BETTER than coinbase 7.4, on par with stripe; BR-W-4). Credit only on `resultStatus=S` (BR-C-1); minor-unit integer amounts → string-decimal (fail-closed on scale loss, BR-W-7). |
| 2026-06-10 | Story 7.4 (Dev / Linus) | **ADDITIVE (no new code / RPC / proto)** — USDC（Coinbase Commerce）. **§5.1.3 NEW route** `POST /v1/billing/webhooks/coinbase` (UNAUTHENTICATED-but-SIGNED, mounted OUTSIDE bearer+CSRF; gateway byte-faithful proxy → payment-svc — same pattern as stripe/paypal). **§5.1.3 `POST /v1/billing/recharge` EXTENDED**: `provider` set adds `coinbase`; the 200 response gains an additive `usdc_address` field (the USDC deposit address, Q-ADDRESS) carried through the existing `CreateCheckoutResponse.client_token` — `omitempty`, present only for coinbase. **§5.1.2 codes REUSED unchanged** (`400_unsupported_payment_provider` now lists coinbase; `400_webhook_signature_invalid` / `402_payment_failed` apply verbatim). NO new envelope code, NO new proto field, NO new Kafka topic. ⚠️ Coinbase `X-CC-Webhook-Signature` has NO timestamp (replay defence = `recharge_orders` state-machine alone, BR-W-4); credit only on `charge:confirmed` (BR-C-1). |
| 2026-06-09 | Story 7.3 (Dev / Linus) | **§5.1.2 4 NEW envelope codes**: `400_invalid_payment_request`, `400_unsupported_payment_provider`, `402_payment_failed`, `400_webhook_signature_invalid` (+ `400_unsupported_currency` REUSE for the USD-only recharge gate). Registered in the runtime mirror `apps/api-gateway/internal/openaierr.CodeMetadata` per the Story-3.6 single-canonical-writer rule. A provider CreateCheckout/Subscription error → `402_payment_failed` (NOT 502 — 5xx reserved for our-own-infra faults, BR-W-5). **§5.1.3 NEW endpoints**: `POST /v1/billing/recharge` (create order → provider checkout), `POST /v1/billing/subscriptions` (RAIL only, Q-SUBSCOPE), `POST /v1/billing/webhooks/{stripe,paypal}` (UNAUTHENTICATED-but-SIGNED, mounted OUTSIDE bearer+CSRF — the gateway is a byte-faithful reverse proxy to payment-svc, Q-WEBHOOK-INGRESS / BR-W-1/W-2). Money fields string-decimal (BR-R-5). **§5.2 `BillingService.CreateRechargeOrder` REALISED** (additive RPC — realises the §5.2-sketched-but-deferred method; passes `buf breaking: FILE`) + **NEW `he.payment.v1.PaymentService`** (`CreateCheckout` / `CreateSubscription`; NO webhook RPC). **NEW Kafka topic `payment.completed`** (payment-svc producer; billing-svc credit + notification-svc consumers — §4.4). FIRST balance CREDIT path + FIRST inbound webhook surface + FIRST payment-svc realisation. |
| 2026-06-10 | Story 7.7 (Dev / Linus) | **§5.1.2 3 NEW envelope codes** (registered in `openaierr.CodeMetadata`, Story-3.6 rule): `422_invalid_auto_recharge` (bad threshold/amount money fields), `422_invalid_payment_method` (foreign/non-existent/non-card method — cross-user-binding guard BR-R-7), `404_not_found` (invoice/method IDOR → 404 not 403, no existence disclosure BR-I-6). connect-code→envelope: InvalidArgument→422_invalid_auto_recharge, PermissionDenied→422_invalid_payment_method, NotFound→404_not_found. **§5.1.3 NEW endpoints**: `PUT /v1/billing/auto-recharge`, `POST` + `POST …/confirm` + `GET` + `DELETE /v1/billing/payment-methods[/{id}]`, `GET /v1/billing/invoices`, `GET /v1/billing/invoices/{id}/pdf` (owner-only `application/pdf` stream). All bearer-API-key, user_id server-resolved, money string-decimal. **§5.2 `he.billing.v1.BillingService` +6 additive RPCs** (Architect High #2): `SetAutoRecharge`, `Save`/`List`/`Delete`PaymentMethod, `ListInvoices`, `GetInvoicePdf` (flat-scalar: JSON-string list payloads + `bytes` PDF). **§5.2 `he.payment.v1.PaymentService` +3 additive RPCs** (Q-OFFSESSION): `ChargeOffSession` (+ req `order_id` field 6), `CreateSetupIntent`, `RetrievePaymentMethod`; optional `OffSessionProvider` interface (Stripe-only; non-card → `ErrOffSessionUnsupported`). **REUSE** `payment.completed` → the 7.3 exactly-once credit (NO new credit path). NEW `cmd/monthly-invoice` CronJob (`0 0 1 * *` UTC, H-1). `buf breaking: FILE` passes (additive). |
| 2026-06-09 | Story 7.2 (Dev / Linus) | **§5.1.2 NEW envelope code**: `400_unsupported_currency` (Architect M-1 ratified — `?currency=` ∉ {usd, rmb}; fail-loud, no silent USD fallback, BR-B-5). Registered in the runtime mirror `apps/api-gateway/internal/openaierr.CodeMetadata` as `invalid_request_error` per the Story-3.6 single-canonical-writer rule (NOT spec-table only). **§5.1.3 `GET /v1/balance` + `GET /v1/usage` EXTENDED (additive, NO new endpoint)**: currency-aware via stateless `?currency=usd\|rmb` (USD default, case-insensitive, trimmed — Q-PREF). Additive string-decimal fields `currency`/`current_display`/`total_cost_display`/`fx_rate`(8dp)/`fx_as_of`(RFC3339 = rate `fetched_at`) + `fx_degraded` on the cold-start guard (BR-B-4). USD default byte-compatible with 7.1 (`fx_rate:"1.00000000"`, display echoes the 4dp SoT — BR-B-7; verified extra-field-tolerant, 4.7-CONTRACT-001 precedent). RMB = USD→CNY HALF-UP 2dp (Q-ROUND); `current_usd` always present (BR-B-3). **Mixed-currency MVP cut (M-2 / Q-APISHAPE)**: on `?currency=rmb`, only the top-line `total_cost_display` converts; `by_model[].cost_usd` stays USD (documented). **Display-only (Q-SOT)**: conversion is read-side only — NO write to `balances.*`/`usage_ledger`; the deduction/ledger/402-gate/reconciliation are byte-identical to 7.1. Rate read from the gateway `internal/fxrate` in-proc snapshot (boot+60s, latest `ORDER BY fetched_at DESC LIMIT 1`); the FX provider is NEVER on the read hot path (BR-C-5). NO new gRPC RPC, NO new proto field, NO deduction-path change. |
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
| 2026-06-03 | Story 6.4 (Dev / Linus) | **§5.1.1 `X-He-AB-Models` REQUEST header WIRED + A/B RESPONSE shape DEFINED**: `X-He-AB-Models: modelA,modelB` (documented-but-unwired before 6.4) now dispatches BOTH models in PARALLEL (`sync.WaitGroup`, Architect High-1 — NOT errgroup) and returns BOTH outputs in ONE OpenAI-compatible `chat.completion` (Q-A Option A): `choices[]` = leg A's then leg B's with re-assigned `index`, each carrying an additive per-choice **`x_he_model`** attribution; `usage` SUMMED; top-level `model` = leg A; a NEW response header **`X-He-AB-Models: served-ids`** echoes the successfully-served legs in order; `X-He-Selected-Model` is NOT set on the A/B path (BR2-7). The header requires **exactly 2 distinct concrete ids** (Q-H — count≠2 / duplicate / `he-router-*` / unknown → `400_invalid_request`); A/B **OVERRIDES** strategy/meta-model selection (Q-I). A/B is **NON-STREAMING-ONLY** (Q-B — `stream=true` + the header → `400_invalid_request` before any dispatch). **Partial failure (Q-E)**: ≥1 leg ok → `200` with the success choice(s) + a failed-leg marker choice (`finish_reason:"he_upstream_error"` + `x_he_model` + an additive **`x_he_error`** §5.1.2 envelope); BOTH fail → ONE terminal envelope (representative code 504 if any leg timed out, else 502). Legs are concrete pins and do NOT failover (Q-D). **§5.2 THREE 6.1-preshaped proto fields FIRST-REALISED (no new field numbers, no `buf breaking`)**: gateway POPULATES `SelectModelRequest.ab_models=4`; routing-svc sets `SelectModelResponse.is_ab_test=3`=TRUE + `ab_selected_models=4`=[resolved concrete legs] (a `he-router-*`/unknown leg → `CodeInvalidArgument` → gateway 400, BR1-2). **Invariants**: DUAL-BILLING `TPMDeduct == legA.total + legB.total` for successful legs (Q-F — contrast 6.3's bill-once); ONE QPS/RPM tick per A/B request (Q-J); BOTH legs must pass the key `scope.models` gate → out-of-scope leg → `403_model_not_in_scope` for the whole request (Q-K / Architect High-2 — the upstream keypolicy middleware still gates `body.model`). NO new envelope code (reuses `400_invalid_request`/`403_model_not_in_scope`/`502`/`504`), NO new REST endpoint, NO new proto field number, NO migration. Refactor: non-writing `dispatchAdapterOnce` primitive extracted from `serveAdapterNonStream` (Q-G — single-model path byte-identical). Observability: `he_routing_ab_total{outcome=both_ok\|partial\|both_failed}` + non-PII slog `chat_completions_ab{ab_models,served_models,failed_models,he_request_id}`. |
| 2026-06-03 | Story 6.3 (Dev / Linus) | **§5.1.2 `502_upstream_unavailable` / `504_upstream_timeout` "即将 failover" REALISED**: a strategy-routed `POST /v1/chat/completions` whose selected upstream returns 502/504 now triggers sequential failover over the ranked `failover_chain` — max **3 total upstream attempts** + **30s total wall-clock budget** (`MaxFailoverAttempts=3` / `FailoverBudget=30s`); on success the response serves from a fallback model and `X-He-Selected-Model` reflects the FINAL served model (Q-F); on exhaustion the LAST 502/504 envelope is returned (zero regression). Failover triggers on these two codes ONLY (Q-C); non-retriable (400/403/402/429/content-filter) is terminal. The concrete-model DEFAULT path does NOT failover (empty chain, Q-D). Streaming failover is PRE-FLUSH only (Q-E). **§5.2 ONE additive RESPONSE field `SelectModelResponse.failover_chain=7`** (`repeated string` — the ranked fallback model ids AFTER `selected_model`, concrete-only; same additive pattern as `score_source=6`; passes `buf breaking: FILE`); populated by a NEW optional `RankedStrategy` engine capability (`SelectRanked`), the `SelectModelRequest` contract REUSED verbatim (Q-A Option A). Billing/rate-limit invariants: exactly ONE `TPMDeduct` on success, ONE QPS/RPM tick per request regardless of attempts (Q-H/Q-L). Failover-tail scope-filtered by the key's `scope.models` before dispatch (Q-K). Observability: `he_routing_failover_total{from_model,to_model,reason}` + `he_routing_failover_attempts` histogram (buckets `[1,2,3]`) + per-hop slog `routing_action=failover` (non-PII). NO new envelope code, NO new REST endpoint, NO migration. |
| 2026-06-03 | Story 6.2 (Dev / Linus) | **§5.1.1 documented-but-unwired routing behaviour REALISED**: `POST /v1/chat/completions` now consults `routing-svc` on every request — a `he-router-{quality,cost,latency}` meta-model in `model` OR the `X-He-Routing-Strategy: quality\|cost\|latency` header resolves (precedence meta-model > header > default, Q-I) to a real model selection. `X-He-Selected-Model` value source MIGRATES `req.Model` → the routing decision and is now a UNIVERSAL success-path invariant (set on mock + adapter non-stream + adapter stream — BR1-2; the prior mock-path-absent behaviour is intentionally retired, Story-6.1 UNIT-013 flipped). A concrete `model` with no strategy routes via STRATEGY_DEFAULT → `selected == req.Model` byte-for-byte (zero regression for the 6-vendor matrix). **§5.2 `RoutingService.SelectModel` REQUEST contract REUSED verbatim**; ONE additive RESPONSE field `SelectModelResponse.score_source=6` (string — `model_pricing`\|`clickhouse`\|`fallback`\|`default`; same additive pattern as `ab_selected_models=4`/`strategy_used=5`; passes `buf breaking: FILE`). FIRST gateway→routing-svc Connect-RPC client (`apps/api-gateway/internal/routingclient/`, 100ms deadline Q-E). **§5.1.2 gRPC→envelope mapping lands gateway-side (Q-H, NO new codes)**: `InvalidArgument→400_invalid_request`; `NotFound` on a `he-router-*` meta-model→`502_upstream_unavailable`, on a concrete-default miss→`400_invalid_request`. **Q-G fail-open/closed**: routing-svc Unavailable/DeadlineExceeded + concrete model → fail-OPEN passthrough of `req.Model` (chat keeps working); + `he-router-*` meta-model → fail-CLOSED `502_upstream_unavailable`. `X-He-AB-Models` IGNORED (A/B is Story 6.4, Q-N). NO new REST endpoint. Observability: slog decision line (non-PII) + Prometheus `he_routing_decisions_total{strategy,selected_model,score_source}` + `he_routing_select_duration_seconds`. |
| 2026-06-03 | Story 6.1 (Dev / Linus) | **§5.2 `RoutingService` proto sketch REALISED** as `he.routing.v1.RoutingService` (Connect-RPC) at `packages/proto/he/routing/v1/routing.proto` + vendored Go at `packages/proto/gen/go/he/routing/v1/`. FIRST `routing-svc` Go service (`apps/routing-svc/`, server-side only — gateway client wiring + real scoring are Story 6.2 per Q-B). The realised contract supersedes the pre-Epic-6 sketch: **Q-D** `strategy` upgraded `string`→`enum Strategy{UNSPECIFIED=0,DEFAULT=1,QUALITY=2,COST=3,LATENCY=4}` (engine maps `UNSPECIFIED(0) ≡ DEFAULT(1)`, never errors on zero-value); **Q-H** NEW `SelectModelRequest.he_request_id=5` (gateway correlation, slog-only echo); **additive** `SelectModelResponse.ab_selected_models=4` (Story 6.4 A/B; empty in 6.1) + **Q-I** `strategy_used=5` (echoes the actually-fired strategy); **Q-K** `adapter_endpoint=2` documented RESERVED/empty in 6.1 (the gateway resolves the endpoint via `adapterclient.Registry`; field number held stable). Field numbers 1/2/3 preserved from the sketch; this is a NEW proto file (`he.routing.v1`, not `he.api.v1`) so `buf breaking: FILE` is not crossed. **NO new §5.1.2 envelope codes** — routing-svc surfaces gRPC codes only (`InvalidArgument`/`NotFound`/`Internal`); the gRPC→§5.1.2 envelope translation lands gateway-side in Story 6.2 (BR1-3). **Q-E** SelectModel deadline budget = 100ms documented as a lifetime contract value (the actual `context.WithTimeout` lands gateway-side in 6.2). Models-catalogue lifted to shared `packages/models-catalogue/` (Q-A option (a)); `BuildPublicModelsSnapshot()` byte-identical (4.7-INT-001 unchanged). |
| 2026-06-11 | Story 8.4 (Dev / Linus) | **§5.1.3 `PATCH /v1/me/keys/{api_key_id}` EXTENDED (additive field, NO new endpoint)**: optional `content_safety_strictness` ∈ {strict, default, loose}, validated at the gateway BEFORE the RPC (bad token → `400_invalid_request` param=`content_safety_strictness`, no DB side-effect); present-only mutation (omit → preserve; a patch with ONLY this field satisfies the ≥1-field gate). Surfaced read-back on the PATCH response + the `GET /v1/me/keys` `ApiKeyEntry`. **§5.2 `AuthService` EXTENDED**: `ValidateApiKeyResponse`/`UpdateApiKeyRequest`/`UpdateApiKeyResponse`/`ApiKeyEntry` each gain an additive `content_safety_strictness` (H-1 pinned field numbers **8/8/10/10**; hand-edited into the generated pb.go per the buf-can't-run limit + a wire-shape round-trip test). `ValidateApiKeyResponse` carries the level to the gateway bearer cache so the bidirectional content-safety filter gates per-Key from cache (no per-request PG round-trip), **fail-closed to strict**. NO new envelope code, NO new endpoint. |

---
