# 8. 安全（Security）

## 8.1 安全分层

| 层 | 保护措施 |
|----|---------|
| **边缘** | Cloudflare WAF + DDoS 防护 + Bot Management |
| **传输** | TLS 1.3 only；HSTS preload；HTTP→HTTPS 自动跳转 |
| **认证** | JWT (RS256) for OAuth session；Bearer token (he-xxx) for API |
| **授权** | RBAC + Key scope（model 范围、IP 白名单） |
| **存储** | 密码 bcrypt cost=12；API Key bcrypt；敏感字段 KMS-encrypted at rest |
| **数据库** | TLS 连接；强制最小权限账号；SQL 注入用 parameterized queries（pgx） |
| **依赖** | dependabot + trivy（镜像扫描） + snyk（V1.1） |
| **日志** | PII 脱敏（用户 prompt 只在审计日志保留 30 天后自动删除） |
| **审计** | 所有 admin 操作写入 audit-svc 不可篡改日志 |

## 8.2 API Key 生命周期

```
Generate:
  1. Generate 32 bytes cryptographic random
  2. Format: 'he-' + base62(32 bytes) → ~46 chars total
  3. Hash: bcrypt(secret_key) cost=12
  4. Store: hash + first 12 chars (key_prefix) for display
  5. Return plaintext to user ONCE, never persisted

Validate (on request):
  1. Extract Bearer from Authorization
  2. Lookup by key_prefix (indexed)
  3. bcrypt-compare full key with hash
  4. Cache result in Redis 5min (auth:apikey:{hash})

Revoke:
  1. Set revoked_at timestamp
  2. Invalidate Redis cache via per-key sentinel (Story 5.1 BR-3.8)
  3. Reject all subsequent calls
```

> **Story 5.1 lifecycle realisation** (2026-05-25): The "Generate" + "Revoke"
> steps are now realised by Story 5.1 (auth-svc CreateApiKey + RevokeApiKey
> RPCs at `apps/auth-svc/internal/apikey/{generate,create,revoke}.go`). The
> "Validate" step was realised by Story 3.2. The lag-note below has been
> UPGRADED from the original 5-minute worst-case to a single-digit-second
> bound via the per-key-id Redis sentinel (Architect Q2 ratified option-c).
> The 5-minute fallback remains the worst-case only on sentinel-write failure
> (BR-3.13 fail-open default).

### 8.2.1 Revocation Lag Note (Story 5.1 UPGRADE)

The Validate-step Redis cache (TTL = 300 s) historically introduced a
**revocation lag** worst-case of 5 minutes when an operator UPDATEd
`api_keys.revoked_at`: the api-gateway continued serving the cached
positive result until the entry naturally expired.

**Story 5.1 UPGRADES this to single-digit milliseconds on the happy path**
via the per-key-id Redis sentinel `auth:apikey:revoked:{api_key_id}`
(TTL 300s — matches the positive-cache TTL). The mechanism:

1. **auth-svc revoke path** (Story 5.1 T3.1): after the PG `UPDATE
   SET revoked_at=NOW()` commits, auth-svc writes
   `SET auth:apikey:revoked:{api_key_id} 1 EX 300` to Redis.
2. **api-gateway bearer-auth path** (Story 5.1 T4.1 — additive modification
   of the Story-3.2 `bearer_auth.go`): on a positive cache hit at
   `auth:apikey:{sha256(plaintext)}`, the middleware ADDITIONALLY checks
   `EXISTS auth:apikey:revoked:{cached.api_key_id}` before serving. If the
   sentinel is present, it `DEL`s the stale positive entry and falls
   through to the auth-svc Validate RPC — which returns REVOKED → 401
   per BR-2.4 anti-enumeration parity.
3. **Worst-case lag**: round-trip latency to Redis on the NEXT cache hit
   (typically single-digit milliseconds). The bound is the time from
   `UPDATE revoked_at=NOW()` to the auth-svc `SET` succeeding (~10 ms PG
   commit + Redis round-trip) PLUS the gateway's next cache-hit request
   (request-rate-dependent — for any actively-used key the bound is
   effectively measured in milliseconds).
4. **Fail-open fallback** (Architect Q-Spec-3 ratified): if the sentinel
   write fails (Redis unavailable in auth-svc), the auth-svc revoke RPC
   STILL returns success to the user; the PG `revoked_at=NOW()` is the
   durable source of truth; the lag falls back to the pre-Story-5.1
   5-minute baseline (positive cache natural-expiry). slog records
   `apikey_revoke_sentinel_failed` at WARN.

**Selected mechanism (Architect Q2 ratified — option c)**: per-key-id
sentinel. Rationale: zero new infrastructure (Redis already in place);
TTL bounded by the cache TTL it must outlive; single-write on revoke
(auth-svc only — no subscriber maintenance); zero gateway-pod state
(statelessness preserved across the cluster). Alternatives (a) Redis
pub/sub and (b) Kafka consumer remain documented below as future
multi-region-cache-coherence options should the threat model evolve.

**Immediate-revocation alternatives (documented for future
multi-region work, NOT realised in Story 5.1):**

- **(a) Redis pub/sub channel invalidation** — auth-svc publishes
  `auth.apikey.revoked:{id}` on revoke; gateway-side subscribers `DEL`
  matching `auth:apikey:<sha256_hex(plaintext)>` keys. Bounds the lag to
  single-digit seconds; cost is a long-lived gateway↔Redis subscriber per
  pod. Note: the gateway does not store the plaintext (cache key is
  derived from sha256(plaintext) — BR-1.5), so this design needs the
  auth-svc revoke path to publish ALL hashed key entries to delete (or
  the gateway-side subscriber to maintain a parallel id→hash mapping).

- **(b) Kafka audit.event consumer** — auth-svc emits an
  `audit.apikey.revoked` event on revoke; a gateway-side consumer
  applies the same cache-invalidation. Bounds the lag to the Kafka
  consumer-lag (single-digit seconds at MVP scale). Reuses the existing
  Story 1.6 `audit.event` topic + 30-day retention so the audit-trail
  doubles as the invalidation queue.

Both alternatives bound the lag to single-digit seconds and ADD operational
complexity (per-pod subscribers or consumer-groups) over the Story-5.1
sentinel; they remain documented for future multi-region cache-coherence
work when the gateway is sharded across regions and a single Redis cluster
no longer serves all pods.

---

### 8.x Change Log

| Date | Story | Change |
|------|-------|--------|
| 2026-05-25 | 5.1 | **§8.2 Generate + Revoke steps REALISED** (auth-svc CreateApiKey + RevokeApiKey RPCs). **§8.2.1 lag-note UPGRADED**: 5-min worst-case → single-digit-millisecond happy path via per-key-id Redis sentinel (Architect Q2 ratified option-c). 5-min fallback preserved on sentinel-write failure (BR-3.13 fail-open per Q-Spec-3). |
| 2026-06-03 | 5.2 | **§8.1 layer 4 "授权 RBAC + Key scope (model 范围, IP 白名单)" REALISED** — the architecture promise that stood as a forward-declaration since the initial pass is now enforced by the gateway `keypolicy` middleware: per-request IP-whitelist (CIDR-aware, IPv4+IPv6), model-scope, and monthly-cap gates on the bearer hot path. **§8.2 "Configure" step ADDED** to the key lifecycle (PATCH /v1/me/keys/{id} → UpdateApiKey RPC). Client-IP discovery hardened against X-Forwarded-For spoofing via the Q-E trusted-proxy walker (XFF trusted ONLY when the direct caller is in the `api-gateway-trusted-proxies` CIDR set; empty default = RemoteAddr-only failsafe). PII discipline preserved: all logged IPs are `/24`+`/64`-masked SHA-256 hashes (SECURITY-006). Cap is a soft throttle — fail-OPEN on Redis-counter unavailability (Q-F). |
| 2026-06-03 | 5.4 | **§8.1 layer 4 monthly-cap enforcement CLOSED end-to-end** (Epic-5 DoD line 3) — Story 5.2 ratified the per-request 402 path; Story 5.4 adds the **sticky-trip circuit breaker** (`keystate:apikey:cap_tripped:{id}` Redis sentinel, no TTL — survives mid-month counter mutation; cron-cleared at the UTC month boundary) + the **threshold email path** (80% warning + 100% tripped, once-per-month SETNX dedupe). Money-related kill-switch posture: fail-OPEN on Redis (cap stays a soft throttle, Q-F) but the breaker is fail-CLOSED on clear (only the cron unsets it). **PII discipline (§8.3 cascade)**: the outbound email carries `key.name` + the user's own `cap_usd` ONLY — NEVER plaintext key, bcrypt hash, or precise current-month spend (tripped template omits the threshold figure entirely). Template variables are HTML-escaped via `html/template` (defence against an injection-named key, BR-2.8). The `monthly-cost-reset` CronJob touches every non-revoked row + Redis cap-state family; `concurrencyPolicy: Forbid` + `backoffLimit: 0` (one cron, one outcome, human-verified on failure). slog events carry only `api_key_id` + `he_request_id` (no plaintext/hash). |

## 8.3 GDPR / CCPA 实现

```
Data Export:
  1. User clicks "Export My Data"
  2. notification-svc enqueues task
  3. analytics-svc generates JSON dump:
     - users row
     - api_keys (metadata only, no plaintext key)
     - subscriptions
     - balances
     - recharge_orders
     - ClickHouse: request_logs (last 90 days)
     - content_safety_logs related
  4. Compress to ZIP, store in OSS (per-user bucket)
  5. Generate signed URL (24h)
  6. Email user

Data Deletion:
  1. User clicks "Delete My Account" + password verify
  2. Set users.status = 'pending_deletion', pending_deletion_at = now+30d
  3. User can login during 30d to cancel
  4. After 30d:
     - Soft-delete users row (keep for legal retention)
     - Anonymize PII (email → hash, name → null)
     - Delete API Keys (cascade)
     - Delete content_safety_logs
     - ClickHouse: anonymize user_id in request_logs (legal retention)
     - OSS: delete personal files
```

## 8.4 PCI-DSS 边界

He-API **不存储任何信用卡数据**：
- Stripe / PayPal 自有 PCI-DSS Level 1 合规
- 用户卡信息直接发送至 Stripe/PayPal（client-side tokenization）
- 我方仅存储 token 与订单 ID

**Story 7.3 REALISED (SAQ-A)** — payment-svc 集成 Stripe + PayPal：用户在 provider 托管的 checkout 页面付款，卡数据 browser→provider 直达，**永不经过 He-API**；我方仅持久化 `recharge_orders.external_order_id`（provider token/id）与内部订单 id；无任何端点接受 PAN/CVV（7.3-UNIT-009 断言）。**Webhook 认证模型（NEW）**：入站 `POST /v1/billing/webhooks/{stripe,paypal}` 挂在 bearer + CSRF 链**之外**——provider 的**签名即凭证**（Stripe-Signature HMAC-SHA256，constant-time + timestamp tolerance；PayPal verify-webhook-signature）；验签**先于任何 body 解析**，伪造/重放/篡改 → 400，body 永不入账（BR-W-1/W-4）。webhook 签名密钥与 API 密钥**分离**（独立爆炸半径，Q-SECRETS）；全部 env 注入、slog 脱敏、永不落日志。详见 `docs/dev/secrets/payment-provider.md`。

**Story 7.4 REALISED — USDC（Coinbase Commerce）crypto 通道**：加密通道**无任何卡 PAN**（crypto 无卡，强于 SAQ-A 卡边界）；我方仅持久化 Coinbase charge id（`external_order_id`）+ 内部订单 id。入站 `POST /v1/billing/webhooks/coinbase` 同样挂在 bearer + CSRF 链**之外**——`X-CC-Webhook-Signature` = lowercase-hex `HMAC-SHA256(rawBody, COINBASE_COMMERCE_WEBHOOK_SECRET)`，`hmac.Equal` constant-time，验签**先于任何 body 解析**（BR-W-1/W-2）。⚠️ **与 Stripe 的差异**：Coinbase 签名**无 timestamp 段** → 无签名新鲜度窗口 → 防重放完全依赖继承的 `recharge_orders` pending→paid 状态机（BR-W-4，重放的 valid `charge:confirmed` = 零行 UPDATE = 不二次入账）。入账**仅在 `charge:confirmed`**（链上确认 = Coinbase finality；`charge:pending` 未确认 → 200 ACK 不入账；underpaid/delayed → 继承的金额完整性守卫 park，BR-C-1/C-3）。`COINBASE_COMMERCE_WEBHOOK_SECRET` 与 `COINBASE_COMMERCE_API_KEY` 分离、env 注入、永不落日志。

**Story 7.5 REALISED — Alipay+（Antom international）钱包通道**：钱包/账户通道**无任何卡 PAN**（用户在 Antom 托管 cashier 的钱包内完成认证，provider-hosted，SAQ-A 边界成立）；我方仅持久化 Antom `paymentId`（`external_order_id`）+ 内部订单 id。入站 `POST /v1/billing/webhooks/alipay` 同样挂在 bearer + CSRF 链**之外**。⚠️ **平台首个非对称签名方案**：Antom 用**其私钥**签名通知，我方用 **Alipay+ 公钥**验签（`rsa.VerifyPKCS1v15`, `crypto.SHA256`）——我方**不持有共享密钥**，配置泄露**也无法伪造**通知（公钥非密钥）。⚠️ **首个 constructed-string 签名**：被签内容是 `POST <publicNotifyPath>\n<Client-Id>.<Request-Time>.<rawBody>`（method+URI+Client-Id+Request-Time+body，**非** raw body 单独）；URI 必须用**配置的公网 notify path**（Q-NOTIFY-PATH）重建，**非** `r.URL.Path`（网关代理到内部路径）；任一组件被篡改 → 验签失败 → 400（fail-closed）。验签**先于任何 body 解析**（BR-W-1/W-2）。⚠️ **防重放优于 7.4**：`Request-Time` 给出新鲜度窗口 → 超窗 stale 通知被拒（恢复 Coinbase 缺失的签名时间戳层，与 Stripe 持平，BR-W-4 layer-1）+ 继承的 `recharge_orders` 状态机（layer-2）= 双层防御。入账**仅在 `resultStatus=S`**（Antom finality；`U` 进行中 → 200 ACK 不入账；`F` → 失败不入账；settled≠intent → 继承金额完整性守卫 park，BR-C-1/C-3）。⚠️ **密钥爆炸半径**：`ALIPAY_PLUS_MERCHANT_PRIVATE_KEY`（签我方**所有出站** Antom 调用）爆炸半径**大于** HMAC webhook 密钥——泄露可冒充我方 create/refund payments；与 `ALIPAY_PLUS_ALIPAY_PUBLIC_KEY`（验签，公钥）+ `ALIPAY_PLUS_CLIENT_ID` 三者分离、env 注入、**私钥永不落日志**。minor-unit 金额 → string-decimal，scale loss **fail-closed**（永不 100× 误入账，BR-W-7）。

---
