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
  2. Invalidate Redis cache
  3. Reject all subsequent calls
```

### 8.2.1 Revocation Lag Note (Story 3.2)

The Validate-step Redis cache (TTL = 300 s) introduces a documented
**revocation lag**: when an operator UPDATEs `api_keys.revoked_at`, the
api-gateway continues serving the cached positive result until the entry
naturally expires — i.e., the worst-case window between revoke and
client-visible 401 is 5 minutes.

For the MVP this is the accepted design trade-off (Architect Round 1 OQ5
ruling 2026-05-18): no enterprise SOC-2 customer has been onboarded
pre-launch, and the lag bound is well-defined + operator-tunable. The
positives-only cache rule (Story 3.2 BR-1.6) bounds the lag — revoked
keys cannot pile up beyond cache TTL.

**Immediate-revocation alternatives (future Story, not yet tracked):**

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

Both alternatives bound the lag to single-digit seconds; the current
5-minute lag is the MVP-acceptable design per AC4 documentation. No
tracked Story spawned now per Architect — "wait for first enterprise
prospect feedback to size the work".

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

---
