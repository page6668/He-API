# 4. 数据模型（Data Models）

## 4.1 PostgreSQL Schema 核心表

```sql
-- 用户表
CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email VARCHAR(255) UNIQUE NOT NULL,
  password_hash TEXT,                           -- bcrypt; nullable for OAuth-only users
  email_verified_at TIMESTAMPTZ,
  oauth_provider VARCHAR(50),                   -- google / github / null
  oauth_subject VARCHAR(255),
  locale VARCHAR(10) DEFAULT 'en',              -- en / zh-CN / ja / ...
  timezone VARCHAR(50) DEFAULT 'UTC',
  totp_secret_encrypted TEXT,                   -- KMS-encrypted
  totp_enabled BOOLEAN DEFAULT FALSE,
  status VARCHAR(20) DEFAULT 'active',          -- active / suspended / pending_deletion
  pending_deletion_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_oauth ON users(oauth_provider, oauth_subject);

-- API Key
CREATE TABLE api_keys (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  team_id UUID REFERENCES teams(id) ON DELETE CASCADE,  -- nullable
  name VARCHAR(100) NOT NULL,
  key_prefix VARCHAR(16) NOT NULL,              -- 'he-xxxxx' first 8-12 chars for display
  key_hash TEXT NOT NULL,                       -- bcrypt 哈希
  scope JSONB NOT NULL DEFAULT '{}',            -- {models: [...], ip_whitelist: [...]}
  monthly_cost_cap_usd NUMERIC(10,2),           -- 月度上限
  current_month_cost_usd NUMERIC(10,2) DEFAULT 0,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_api_keys_user_id ON api_keys(user_id);
CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);

-- Team
CREATE TABLE teams (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL REFERENCES users(id),
  name VARCHAR(100) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE team_members (
  team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role VARCHAR(20) NOT NULL,                    -- owner / member
  joined_at TIMESTAMPTZ DEFAULT NOW(),
  PRIMARY KEY (team_id, user_id)
);

-- 订阅
CREATE TABLE subscriptions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id),
  plan VARCHAR(50) NOT NULL,                    -- free / pro / team / enterprise
  status VARCHAR(20) NOT NULL,                  -- active / cancelled / past_due
  current_period_start TIMESTAMPTZ,
  current_period_end TIMESTAMPTZ,
  payment_provider VARCHAR(50),                 -- stripe / paypal
  external_subscription_id VARCHAR(200),
  created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 余额
CREATE TABLE balances (
  user_id UUID PRIMARY KEY REFERENCES users(id),
  current_usd NUMERIC(12,4) NOT NULL DEFAULT 0,
  current_rmb NUMERIC(12,4) NOT NULL DEFAULT 0,
  auto_recharge_enabled BOOLEAN DEFAULT FALSE,
  auto_recharge_threshold_usd NUMERIC(10,2),
  auto_recharge_amount_usd NUMERIC(10,2),
  auto_recharge_payment_method_id UUID,
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 充值订单
CREATE TABLE recharge_orders (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id),
  amount NUMERIC(12,4) NOT NULL,
  currency VARCHAR(3) NOT NULL,                 -- USD / CNY
  payment_provider VARCHAR(50) NOT NULL,        -- stripe / paypal / usdc / alipay / wechat
  external_order_id VARCHAR(200),
  status VARCHAR(20) NOT NULL,                  -- pending / paid / failed / refunded
  paid_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_recharge_orders_user ON recharge_orders(user_id);
CREATE INDEX idx_recharge_orders_external ON recharge_orders(payment_provider, external_order_id);

-- 模型与定价
CREATE TABLE models (
  id VARCHAR(100) PRIMARY KEY,                  -- 'qwen-max' / 'deepseek-v3'
  display_name VARCHAR(100) NOT NULL,
  vendor VARCHAR(50) NOT NULL,                  -- alibaba / deepseek / moonshot / zhipu / bytedance / baidu
  capabilities JSONB NOT NULL,                  -- {chat:true, vision:true, function_calling:true,...}
  upstream_endpoint VARCHAR(500),
  upstream_model_id VARCHAR(100),
  status VARCHAR(20) DEFAULT 'active',          -- active / deprecated
  created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE model_pricing (
  model_id VARCHAR(100) NOT NULL REFERENCES models(id),
  effective_at TIMESTAMPTZ NOT NULL,
  upstream_price_per_1k_input_tokens NUMERIC(10,6) NOT NULL,
  upstream_price_per_1k_output_tokens NUMERIC(10,6) NOT NULL,
  markup_percent NUMERIC(5,2) NOT NULL DEFAULT 10.00,    -- 5-15%
  PRIMARY KEY (model_id, effective_at)
);

-- 内容安全治理日志（合规归档）
CREATE TABLE content_safety_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL,
  api_key_id UUID,
  he_request_id VARCHAR(50) NOT NULL,
  direction VARCHAR(10) NOT NULL,               -- input / output
  matched_rule VARCHAR(100),
  action VARCHAR(20) NOT NULL,                  -- blocked / warned
  excerpt_redacted TEXT,                        -- 脱敏的命中内容片段
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_safety_logs_user_time ON content_safety_logs(user_id, created_at DESC);

-- Beta 模式 / Feature Flag (Unleash 备份；冷启动数据)
CREATE TABLE feature_flags (
  key VARCHAR(100) PRIMARY KEY,
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  variants JSONB,                               -- 灰度配置
  description TEXT,
  updated_at TIMESTAMPTZ DEFAULT NOW()
);
```

## 4.2 ClickHouse Schema (OLAP)

```sql
-- 调用日志
CREATE TABLE request_logs (
  he_request_id String,
  user_id UUID,
  api_key_id UUID,
  team_id UUID,
  model String,
  upstream_model String,
  routing_strategy String,
  selected_by_strategy String,                  -- 策略实际选中模型
  status_code UInt16,
  prompt_tokens UInt32,
  completion_tokens UInt32,
  total_tokens UInt32,
  cost_usd Decimal64(6),
  latency_ms_total UInt32,
  latency_ms_gateway UInt32,
  latency_ms_upstream UInt32,
  ttfb_ms UInt32,                               -- 流式首字符
  is_streaming UInt8,
  client_ip IPv4,
  client_country FixedString(2),
  user_agent String,
  error_code String,
  error_message String,
  ts DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(ts)
ORDER BY (user_id, ts, he_request_id)
TTL ts + INTERVAL 90 DAY;                       -- 90 天保留

-- 用量小时聚合
CREATE MATERIALIZED VIEW request_logs_hourly_agg
ENGINE = SummingMergeTree()
ORDER BY (user_id, model, hour)
AS SELECT
  user_id,
  model,
  toStartOfHour(ts) AS hour,
  count() AS request_count,
  sum(prompt_tokens) AS prompt_tokens,
  sum(completion_tokens) AS completion_tokens,
  sum(total_tokens) AS total_tokens,
  sum(cost_usd) AS cost_usd,
  avg(latency_ms_total) AS avg_latency_ms,
  quantile(0.95)(latency_ms_total) AS p95_latency_ms
FROM request_logs
GROUP BY user_id, model, hour;

-- Benchmark 跑分结果
CREATE TABLE benchmark_results (
  task_id String,
  model String,
  task_type String,
  language String,
  quality_score Float32,
  latency_p50 UInt32,
  latency_p95 UInt32,
  cost_per_1k_tokens Decimal64(6),
  ts DateTime DEFAULT now()
) ENGINE = MergeTree() ORDER BY (model, task_type, ts);
```

## 4.3 Redis Key 规范

```
ratelimit:user:{user_id}:qps                    INCR + EXPIRE 1
ratelimit:user:{user_id}:rpm                    INCR + EXPIRE 60
ratelimit:user:{user_id}:tpm                    INCRBY + EXPIRE 60
ratelimit:key:{api_key_id}:qps                  同上
ratelimit:apikey:create:{user_id}               INCR + EXPIRE 3600, 上限 10  # Story 5.1 (BR-1.10 anti-abuse on POST /v1/me/keys)
session:{session_id}                            JSON, TTL 7 days
auth:apikey:{sha256_hex(plaintext_key)}         缓存 user_id + scope, TTL 5min  # Story 3.2 (T6.5 §4.5 Change Log)
auth:apikey:revoked:{api_key_id}                SET value="1" TTL 300s         # Story 5.1 BR-3.8 cross-pod cache-invalidation sentinel; gateway bearer_auth.go EXISTS-checks on positive cache hit
balance:user:{user_id}:realtime                 实时余额, 跟 PG 对账
flag:beta_mode                                  bool, 实时 Feature Flag
```

## 4.4 Kafka Topics

| Topic | Schema | 消费者 | 保留 |
|-------|--------|-------|------|
| `usage.recorded` | UsageEvent (proto) | billing-svc, audit-svc, analytics-svc | 7 天 |
| `payment.completed` | PaymentEvent | billing-svc, notification-svc | 30 天 |
| `audit.event` | AuditEvent (Story 5.1 adds `event_type ∈ {api_key.created, api_key.revoked}`) | audit-svc | 30 天 |
| `notification.queued` | NotificationEvent | notification-svc | 7 天 |
| `safety.violation` | SafetyEvent | audit-svc, ops 告警 | 90 天 |

## 4.5 Change Log

| Date | Story | Author | Change |
|------|-------|--------|--------|
| 2026-05-18 | 3.2 | Dev (Linus) | **§4.3 Redis Key 规范**: Updated `auth:apikey:{key_hash}` → `auth:apikey:{sha256_hex(plaintext_key)}`. Rationale: the bcrypt `key_hash` cannot be derived from incoming plaintext without a pre-cache DB lookup (bcrypt is one-way; you'd need to bcrypt-compare against candidate hashes — defeating the cache the entry is meant to serve). SHA-256 of plaintext is the only design achieving O(1) cache-key derivation while preserving defence in depth (a Redis-dump compromise cannot reverse to plaintext). Architect Round 1 M4 (2026-05-18) ruled IN FAVOUR of the Story's design. |
| 2026-05-18 | 3.2 | Dev (Linus) | **§4.1 `api_keys.team_id` FK deferral**: Story 3.2's migration `0006_create_api_keys.sql` lands `team_id UUID` (nullable, no REFERENCES clause) because `he_api.teams` table does not exist yet (created in Epic 5). `ALTER TABLE he_api.api_keys ADD CONSTRAINT fk_api_keys_team FOREIGN KEY (team_id) REFERENCES he_api.teams(id) ON DELETE CASCADE` to land in Epic 5 alongside the `he_api.teams` table creation (Architect Round 1 OQ4 ruling, 2026-05-18). |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.1 `he_api.api_keys` is now WRITTEN** (Story 3.2 was read-only — `LookupAPIKeysByPrefix` + fire-and-forget `last_used_at` UPDATE). Story 5.1 adds INSERT (CreateApiKey RPC) + UPDATE `revoked_at=NOW()` (RevokeApiKey RPC) + omit-`key_hash` SELECT (ListApiKeys RPC, BR-2.5 defence-in-depth). No DDL change (`cumulative_context_impact.db_schema=false`). |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.1 `api_keys.team_id` FK — confirmed defer (Architect Q3 ratified)**: Story 5.1 issues USER-scoped keys only (`team_id=NULL`); `he_api.teams` table creation + FK ALTER both DEFERRED to Epic 6+ when team-collaboration becomes a deliverable. The pre-flag from 2026-05-18 row above remains accurate. |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.3 NEW Redis keys**: `ratelimit:apikey:create:{user_id}` (TTL 3600s, ceiling 10 per BR-1.10 anti-abuse on POST /v1/me/keys); `auth:apikey:revoked:{api_key_id}` (TTL 300s, BR-3.8 cross-pod cache-invalidation sentinel — Architect Q2 ratified option-c; gateway bearer_auth.go EXISTS-checks on positive cache hit). The 300s TTL is chosen to outlive the Story-3.2 positive-cache TTL (300s) so stale positives can't survive sentinel-driven invalidation. |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.4 NEW `audit.event` event_type values**: `api_key.created` (BR-3.5 — payload `{api_key_id, user_id, key_prefix, name, client_ip_hash, user_agent_hash, ts}`); `api_key.revoked` (BR-3.6 — adds `revoked_at`). PII-safe: NEVER plaintext, NEVER bcrypt hash. The audit-svc consumer routes by `event_type` and requires NO code changes for Story 5.1. |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.4 NEW canonical money-field serialization** (Architect Q-Spec-4 ratified — Stripe precedent): NUMERIC(10,2) + NUMERIC(12,4) columns surface on the He-API OpenAPI surface as **string-decimals** (e.g., `"50.00"`) — NOT JSON numbers. Rationale: a JSON number is IEEE 754 binary float; round-trip would lose NUMERIC precision (`0.1 + 0.2 != 0.3`). FIRST money field on the API surface (`monthly_cost_cap_usd` + `current_month_cost_usd` via Story 5.1 GET /v1/me/keys). Future money-field stories (5.4 monthly-cap, billing-svc) MUST cascade-lock. |

---
