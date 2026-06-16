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
  default_routing_strategy VARCHAR(20),         -- Story 6.5 (0019): quality/cost/latency or NULL=no default (account-level routing preference; app-validated, no CHECK)
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
  created_at TIMESTAMPTZ DEFAULT NOW(),
  content_safety_strictness VARCHAR(10) NOT NULL DEFAULT 'strict'  -- Story 8.4 per-Key 内容安全严格度
    CHECK (content_safety_strictness IN ('strict','default','loose'))
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
-- NOTE (Story 7.2): current_rmb stays reserved/DEFAULT 0 (Q-RMBCOL — RMB is a
-- compute-on-read display conversion over the single USD SoT, NOT a stored
-- mirror). Supersedes 7.1's "until Story 7.2" comment: a stored-RMB column is
-- deferred to a future story, not activated by 7.2.

-- 汇率快照 (Story 7.2) — append-only, effective-dated USD-base FX rates. The
-- active rate per (base,quote) pair is the latest fetched_at row
-- (ORDER BY fetched_at DESC LIMIT 1, Architect L-1; model_pricing parity). The
-- daily fx-refresh cron INSERTs a new row on success; a provider failure inserts
-- nothing (STALE-SERVE, BR-C-3). rate is NUMERIC(18,8) read as ::text →
-- shopspring/decimal (NEVER float64, M-1). Migration 0009 seeds one
-- source='bootstrap' USD→CNY row so cold-start display reads convert before the
-- first cron (BR-C-6).
CREATE TABLE fx_rates (
  base_currency  CHAR(3) NOT NULL,            -- ISO-4217, e.g. USD
  quote_currency CHAR(3) NOT NULL,            -- ISO-4217, e.g. CNY
  rate           NUMERIC(18,8) NOT NULL,      -- units of quote per 1 base; > 0
  source         VARCHAR(50) NOT NULL,        -- provider id, or 'bootstrap'
  fetched_at     TIMESTAMPTZ NOT NULL,        -- effective ts; exposed as fx_as_of
  PRIMARY KEY (base_currency, quote_currency, fetched_at)
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

-- 内容安全治理日志（合规归档）— IMPLEMENTED in Story 8.5 (migration 0014).
-- The persisting contentsafety Recorder (apps/api-gateway/internal/safetylog)
-- writes ONE row per acted-on §9.3 block (8.2 入参 reject / 8.3 出参 redact /
-- stream terminate), fire-and-forget. NOTE the deliberate ABSENCE of a
-- `REFERENCES users(id)` FK on user_id: this is the §9.3 GDPR-erasure exemption
-- ("合规优先于个人删除请求") — account deletion must NOT cascade-purge the
-- compliance log (contrast data_export_requests' ON DELETE CASCADE). A 6-month
-- retention CronJob (cmd/safety-log-retention) prunes by created_at; the 备案
-- PDF (cmd/safety-filing-report) renders an aggregate summary.
CREATE TABLE content_safety_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL,                         -- NO users FK (GDPR-exemption, Story 8.5 BR-2.2)
  api_key_id UUID,
  he_request_id VARCHAR(50) NOT NULL,
  direction VARCHAR(10) NOT NULL,               -- input / output
  matched_rule VARCHAR(100),                    -- canonical rule id (NOT surface 敏感词)
  action VARCHAR(20) NOT NULL,                  -- blocked / warned ('warned' RESERVED; v1 logs only 'blocked')
  strictness VARCHAR(10) NOT NULL DEFAULT '',   -- 8.4 effective level the block acted under (OQ-8.5-7)
  excerpt_redacted TEXT,                        -- 脱敏的命中内容片段 (masked span; never raw text / literal term)
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_safety_logs_user_time ON content_safety_logs(user_id, created_at DESC);
CREATE INDEX idx_safety_logs_created_at ON content_safety_logs(created_at);  -- 6-month retention sweep (Story 8.5)

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

-- 用量小时聚合 (Story 9.1 H-2 amendment: +success_count, −avg/p95 — see §4.5)
CREATE MATERIALIZED VIEW request_logs_hourly_agg
ENGINE = SummingMergeTree()
ORDER BY (user_id, model, hour)
AS SELECT
  user_id,
  model,
  toStartOfHour(ts) AS hour,
  count() AS request_count,
  countIf(status_code < 400) AS success_count,  -- 成功率 = sum(success_count)/sum(request_count)
  sum(prompt_tokens) AS prompt_tokens,
  sum(completion_tokens) AS completion_tokens,
  sum(total_tokens) AS total_tokens,
  sum(cost_usd) AS cost_usd
FROM request_logs
GROUP BY user_id, model, hour;
-- NOTE (Story 9.1 / Q-AGG H-2): avg_latency_ms / p95_latency_ms were REMOVED.
-- avg() and quantile() stored as plain SummingMergeTree columns are SUMMED on
-- background merge → statistically invalid. A future latency story re-adds them
-- as avgState()/quantileState(0.95)() AggregateFunction columns read with -Merge.

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
ratelimit:key:{api_key_id}:qps                  INCR + EXPIRE 1 NX            # Story 5.3 (Architect Q4 per-key MVP)
ratelimit:key:{api_key_id}:rpm                  INCR + EXPIRE 60 NX           # Story 5.3 (Architect Q4 per-key MVP)
ratelimit:key:{api_key_id}:tpm                  INCRBY + EXPIRE 60 NX         # Story 5.3 post-deduction (Architect Q3 + Q4)
ratelimit:apikey:create:{user_id}               INCR + EXPIRE 3600, 上限 10  # Story 5.1 (BR-1.10 anti-abuse on POST /v1/me/keys)
session:{session_id}                            JSON, TTL 7 days
auth:apikey:{sha256_hex(plaintext_key)}         缓存 user_id + scope, TTL 5min  # Story 3.2 (T6.5 §4.5 Change Log)
auth:apikey:revoked:{api_key_id}                SET value="1" TTL 300s         # Story 5.1 BR-3.8 cross-pod cache-invalidation sentinel; gateway bearer_auth.go EXISTS-checks on positive cache hit
balance:user:{user_id}:realtime                 实时余额, 跟 PG 对账
flag:beta_mode                                  bool, 实时 Feature Flag  # Story 7.8 LIVE (gateway read; PG feature_flags cold-start; Unleash push)
entitlement:user:{user_id}                      JSON {plan,status}, TTL ≤ 60s   # Story 7.8 — gateway hot-path tier snapshot; billing-svc SOLE writer (BR-E-3); miss → free (fail-safe-LOW)
entitlement:user:{user_id}:invalidated          SET "1" TTL ≤ 60s               # Story 7.8 — cross-pod invalidation sentinel on downgrade/cancel (5.1 precedent)
```

## 4.4 Kafka Topics

| Topic | Schema | 消费者 | 保留 |
|-------|--------|-------|------|
| `usage.recorded` | UsageEvent (proto) | billing-svc, audit-svc, analytics-svc | 7 天 |
| `request.logged` | UsageLogEvent (`he.analytics.v1`, protojson) | analytics-svc | 7 天 |
| `request.logged.dlq` | UsageLogEvent (malformed/poison) | analytics-svc (DLQ) | 7 天 |
| `payment.completed` | PaymentEvent | billing-svc, notification-svc | 30 天 |
| `audit.event` | AuditEvent (Story 5.1 adds `event_type ∈ {api_key.created, api_key.revoked}`) | audit-svc | 30 天 |
| `notification.queued` | NotificationEvent | notification-svc | 7 天 |
| `safety.violation` | SafetyEvent | audit-svc, ops 告警 | 90 天 |

## 4.5 Change Log

| Date | Story | Author | Change |
| 2026-06-16 | 6.5 | Dev (Linus) | **§4.1 `he_api.users` EXTENDED** via `migrations/postgres/0019_add_default_routing_strategy_to_users.sql` — `ADD COLUMN default_routing_strategy VARCHAR(20) NULL` (additive, nullable, reversible `DROP COLUMN`, non-destructive). Lives alongside `locale`/`timezone` as a per-user preference; NULL = "no default" = today's `STRATEGY_DEFAULT` passthrough, so existing rows need NO backfill (Q-B/Q-D). NO `CHECK` constraint — the enum `{quality,cost,latency}` is app-validated in auth-svc `UpdateProfile` (single validation home, consistent with locale/timezone). **§4.3 NEW Redis keys** (auth-svc-OWNED — NOT the billing-svc `entitlement:user:{id}`): `user:routing_pref:{user_id}` (the cached strategy string or "" for none; gateway hot-path single GET, lazy-populated via auth-svc `GetMe` on miss, SETEX TTL 1h) + `user:pref_updated:{user_id}` cross-pod invalidation sentinel (Story-5.1/5.2 `config_updated` idiom; SET on profile save, gateway checks EXISTS on a cache hit → re-resolve; fail-OPEN to none on any Redis/RPC error — Q-A Option B, the JWT-`drs`-claim Option A OVERRULED per BR-3.6). `atlas.sum` row added. |
|------|-------|--------|--------|
| 2026-06-11 | 9.1 | Dev (Linus) | **§4.2 ClickHouse `request_logs` + `request_logs_hourly_agg` REALISED** (FIRST ClickHouse business tables) via `migrations/clickhouse/002_create_request_logs.{up,down}.sql` — `request_logs` lands **verbatim** (MergeTree, `PARTITION BY toYYYYMM(ts)`, `ORDER BY (user_id, ts, he_request_id)`, `TTL ts + INTERVAL 90 DAY`; Q-DEDUP keeps MergeTree — at-least-once dups tolerated for the approximate dashboard, exact dedup deferred to 9.3). Migration runs under the migration-admin credential, NOT the `he_api` app user (`SELECT, INSERT` only — M-1). **§4.2 MV CORRECTED (Q-AGG / H-2)**: `request_logs_hourly_agg` ADDS `success_count UInt64 = countIf(status_code < 400)` (makes month/quarter 成功率 = `sum(success_count)/sum(request_count)` computable from the MV — a count is sum-compatible) and **DROPS `avg_latency_ms`/`p95_latency_ms`** (avg()/quantile() stored as plain SummingMergeTree columns are SUMMED on merge → statistically invalid; 9.1 consumes no latency aggregate; a future latency story re-adds them as `avgState()`/`quantileState(0.95)()` AggregateFunction state read with `-Merge`). **§4.4 NEW topics**: `request.logged` (`UsageLogEvent`, `he.analytics.v1` protojson; 消费者 analytics-svc; 保留 7 天 — Kafka is transport-only, ClickHouse's 90-day TTL is the durable SoT) + `request.logged.dlq` (Q-EVT — NEW topic, NOT a reuse of success-only `usage.recorded`). The gateway emits `request.logged` fire-and-forget on EVERY terminal /v1/chat/completions + /v1/embeddings outcome (success AND failure — the 成功率 rows `usage.recorded` misses). `cost_usd` is gateway-emitted, NON-NULL, string-decimal (Q-COST / H-1 — no PG `usage_ledger` cross-read; the dashboard 消费 reads ClickHouse ONLY). `selected_by_strategy` write is now realised (closes the 6.2 Epic-9-scope deferral — H-3); the writer uses a named-column INSERT leaving `team_id`/`user_agent`/`error_message` to their column defaults (teams unrealized; `error_message` omitted for PII discipline, BR-ING-5). |
| 2026-06-10 | 7.8 | Dev (Linus) | **§4.1 `feature_flags` REALISED** (migration `0012_create_feature_flags.sql`, Q-BETA-MIGRATION): the pre-defined-but-never-migrated table is created verbatim + an idempotent `('beta_mode', false) ON CONFLICT (key) DO NOTHING` seed. PG = global Beta-mode cold-start SoT; Redis `flag:beta_mode` = runtime mirror; Unleash = live push. **§4.1 `subscriptions` (0010) EXTENDED, no schema change**: the opaque `plan` string now carries TIER semantics (`packages/plan-catalogue`); `credit.applySubscription` flips `plan` on the confirmed webhook (BR-S-3). NO `plan_entitlements` table (Q-PLAN-CATALOG = code-catalogue). **§4.3 NEW Redis key** `entitlement:user:{id}` (JSON `{plan,status}`, billing-svc SOLE writer, TTL ≤ 60s + `entitlement:user:{id}:invalidated` cross-pod sentinel — 5.1 precedent; the gateway hot-path read for tier enforcement, fail-safe-LOW on miss). The pre-listed `flag:beta_mode` is now LIVE (gateway `internal/featureflag` read; booting-no-signal→OFF, running-loses-Redis→last-known). |
| 2026-05-18 | 3.2 | Dev (Linus) | **§4.3 Redis Key 规范**: Updated `auth:apikey:{key_hash}` → `auth:apikey:{sha256_hex(plaintext_key)}`. Rationale: the bcrypt `key_hash` cannot be derived from incoming plaintext without a pre-cache DB lookup (bcrypt is one-way; you'd need to bcrypt-compare against candidate hashes — defeating the cache the entry is meant to serve). SHA-256 of plaintext is the only design achieving O(1) cache-key derivation while preserving defence in depth (a Redis-dump compromise cannot reverse to plaintext). Architect Round 1 M4 (2026-05-18) ruled IN FAVOUR of the Story's design. |
| 2026-05-18 | 3.2 | Dev (Linus) | **§4.1 `api_keys.team_id` FK deferral**: Story 3.2's migration `0006_create_api_keys.sql` lands `team_id UUID` (nullable, no REFERENCES clause) because `he_api.teams` table does not exist yet (created in Epic 5). `ALTER TABLE he_api.api_keys ADD CONSTRAINT fk_api_keys_team FOREIGN KEY (team_id) REFERENCES he_api.teams(id) ON DELETE CASCADE` to land in Epic 5 alongside the `he_api.teams` table creation (Architect Round 1 OQ4 ruling, 2026-05-18). |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.1 `he_api.api_keys` is now WRITTEN** (Story 3.2 was read-only — `LookupAPIKeysByPrefix` + fire-and-forget `last_used_at` UPDATE). Story 5.1 adds INSERT (CreateApiKey RPC) + UPDATE `revoked_at=NOW()` (RevokeApiKey RPC) + omit-`key_hash` SELECT (ListApiKeys RPC, BR-2.5 defence-in-depth). No DDL change (`cumulative_context_impact.db_schema=false`). |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.1 `api_keys.team_id` FK — confirmed defer (Architect Q3 ratified)**: Story 5.1 issues USER-scoped keys only (`team_id=NULL`); `he_api.teams` table creation + FK ALTER both DEFERRED to Epic 6+ when team-collaboration becomes a deliverable. The pre-flag from 2026-05-18 row above remains accurate. |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.3 NEW Redis keys**: `ratelimit:apikey:create:{user_id}` (TTL 3600s, ceiling 10 per BR-1.10 anti-abuse on POST /v1/me/keys); `auth:apikey:revoked:{api_key_id}` (TTL 300s, BR-3.8 cross-pod cache-invalidation sentinel — Architect Q2 ratified option-c; gateway bearer_auth.go EXISTS-checks on positive cache hit). The 300s TTL is chosen to outlive the Story-3.2 positive-cache TTL (300s) so stale positives can't survive sentinel-driven invalidation. |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.4 NEW `audit.event` event_type values**: `api_key.created` (BR-3.5 — payload `{api_key_id, user_id, key_prefix, name, client_ip_hash, user_agent_hash, ts}`); `api_key.revoked` (BR-3.6 — adds `revoked_at`). PII-safe: NEVER plaintext, NEVER bcrypt hash. The audit-svc consumer routes by `event_type` and requires NO code changes for Story 5.1. |
| 2026-05-25 | 5.1 | Dev (Linus) | **§4.4 NEW canonical money-field serialization** (Architect Q-Spec-4 ratified — Stripe precedent): NUMERIC(10,2) + NUMERIC(12,4) columns surface on the He-API OpenAPI surface as **string-decimals** (e.g., `"50.00"`) — NOT JSON numbers. Rationale: a JSON number is IEEE 754 binary float; round-trip would lose NUMERIC precision (`0.1 + 0.2 != 0.3`). FIRST money field on the API surface (`monthly_cost_cap_usd` + `current_month_cost_usd` via Story 5.1 GET /v1/me/keys). Future money-field stories (5.4 monthly-cap, billing-svc) MUST cascade-lock. |
| 2026-05-26 | 5.3 | Dev (Linus) | **§4.3 per-key rate-limit siblings realised**: `ratelimit:key:{api_key_id}:rpm` (TTL 60s NX) and `ratelimit:key:{api_key_id}:tpm` (TTL 60s NX, post-deduction model) — previously implied by `同上` on the `:qps` row. Atomic operations via two Lua scripts: `check_and_incr.lua` (3-axis CHECK + INCR(QPS,RPM) + EXPIRE NX; TPM check-only, no INCR) and `tpm_deduct.lua` (INCRBY + EXPIRE 60 NX) — strict NX semantics matching BR-3.2 / Architect H-2. Per Architect Q4 ratification, MVP ships per-key only; per-user counters (`ratelimit:user:*`) remain reserved for future team-scoped Story. |
| 2026-06-03 | 5.2 | Dev (Linus) | **§4.1 `api_keys` UPDATE writers** — `UPDATE scope, monthly_cost_cap_usd` (UpdateApiKey RPC); NO `updated_at` write (Architect Q-K — column does not exist; the Kafka `api_key.config_updated` `ts` is the last-modified SoT). No DDL change. **§4.3 NEW Redis keys**: `auth:apikey:config_updated:{api_key_id}` (TTL 300s — BR-1.9 config-update sentinel mirroring the Story-5.1 revoke sentinel; gateway EXISTS-checks it alongside the revoke sentinel in ONE round-trip) + `usage:apikey:{api_key_id}:month_cost_usd` (Q-D realtime cost counter, NO TTL — cron-reset by Story 5.4; gateway `GET` READ-only on every bearer request; billing-svc `INCRBYFLOAT` WRITE in Epic 6+). **§4.3 EXTENDED** `auth:apikey:{sha256(plaintext)}` cache value shape (Q-A): ADDS `scope_models[]`, `scope_ip_whitelist[]`, `monthly_cost_cap_usd` (all `omitempty`); existing `{api_key_id, user_id, team_id, scope}` PRESERVED for legacy back-compat; TTL UNCHANGED (300s). **§4.4 NEW `audit.event` event_type**: `api_key.config_updated` (PII-safe; payload adds `changed_fields[]`). |
| 2026-06-03 | 6.2 | Dev (Linus) | **§4.1 `he_api.models` + `he_api.model_pricing` REALISED** via `migrations/postgres/0007_create_models_and_pricing.sql` (Story-6.1 Q-C carry-over) — additive (CREATE TABLE only, no ALTER/DROP), REVERSIBLE (Atlas dynamic down drops `model_pricing` then `models`), NON-DESTRUCTIVE; `atlas.sum` row added. Tables match §4.1 lines 101-119. Idempotent seed (ON CONFLICT DO NOTHING) for the 8 concrete catalogue models (DefaultRegistry MINUS the 3 `he-router-*` virtual entries, Q-D) + one `effective_at` pricing row each. **NEW READ source** (routing-svc `internal/pricing`, Q-K read-only pgx pool, mirrors auth-svc): the `cost` strategy ranks by `(upstream_price_per_1k_input_tokens + upstream_price_per_1k_output_tokens)` ascending using the latest-`effective_at` row per model (Q-J); loaded as a boot snapshot + 60s refresh (Q-E), NEVER a per-request query (BR2-2). Stale pricing → suboptimal routing only, never a wrong charge (billing reads pricing independently — Epic 7). §4.2 ClickHouse `benchmark_results` / `request_logs_hourly_agg` remain REFERENCED-not-created (quality/latency degrade behind the `Scorer` seam until Epic 9 — Q-A Option A); the `request_logs.routing_strategy`/`selected_by_strategy` write stays Epic-9 scope (Q-B). |
| 2026-06-09 | 7.2 | Dev (Linus) | **§4.1 NEW table `he_api.fx_rates`** via `migrations/postgres/0009_create_fx_rates.sql` — append-only, effective-dated USD-base FX rates (`base_currency CHAR(3)`, `quote_currency CHAR(3)`, `rate NUMERIC(18,8)`, `source VARCHAR(50)`, `fetched_at TIMESTAMPTZ`, PK `(base,quote,fetched_at)` — Q-FXTABLE). Active rate = latest `ORDER BY fetched_at DESC LIMIT 1` per pair (Architect L-1). Additive (CREATE TABLE + idempotent `source='bootstrap'` USD→CNY seed via `ON CONFLICT DO NOTHING` with a fixed `fetched_at`), REVERSIBLE (Atlas dynamic down drops `fx_rates`), NON-DESTRUCTIVE; `atlas.sum` row added (BR-C-6). **§4.1 `balances.current_rmb` clarification** (Architect Q-RMBCOL): the column STAYS reserved/DEFAULT 0 — RMB is a compute-on-read display conversion over the single USD SoT, NOT a stored mirror; this supersedes 7.1's "until Story 7.2" note (a stored-RMB column is deferred to a future story). **WRITE** by the `fx-refresh` cron (`billing-svc/cmd/fx-refresh` → `internal/fx.Refresh`; STALE-SERVE on provider failure = no insert, BR-C-3). **READ** (display-only, Q-SOT) by gateway `internal/fxrate` boot+60s snapshot for `GET /v1/balance\|usage ?currency=rmb` — NEVER on the deduction/reconciliation path; the 7.1 USD reconciliation invariant is preserved verbatim. NO new Redis key (Q-FXCACHE — in-proc snapshot, no Redis). NO new Kafka topic. |
| 2026-06-09 | 7.3 | Dev (Linus) | **§4.1 `recharge_orders` + `subscriptions` REALISED** via `migrations/postgres/0010_create_recharge_orders_and_subscriptions.sql` — both pre-defined in §4.1 (recharge_orders L107-119, subscriptions L61-71) and explicitly NOT created by 7.1 (deferred to "7.8 / 7.3+"). `recharge_orders` carries a **UNIQUE(payment_provider, external_order_id)** constraint (Q-CREDIT-IDEMPOTENCY — UPGRADED from the §4.1 plain idx; the exactly-once credit fence, NULLs distinct so pending rows coexist) + idx(user_id); `subscriptions` carries idx(payment_provider, external_subscription_id). Additive (2× CREATE TABLE + indexes — NO ALTER/DROP; `balances.auto_recharge_*` UNTOUCHED, Story 7.7), REVERSIBLE (Atlas dynamic down drops subscriptions then recharge_orders), NON-DESTRUCTIVE; `atlas.sum` row added. **§4.4 `payment.completed` REALISED**: payment-svc PRODUCES it on a signature-verified webhook (`PaymentEvent` protojson — settled_amount is the provider TRUTH, Q-AMOUNT); billing-svc consumes it for the exactly-once balance CREDIT (`internal/credit`: pending→paid fence + `balances.current_usd` credit in ONE tx — the inverse of the 7.1 debit) + subscription lifecycle; notification-svc receipt email (no code change). **FIRST balance CREDIT path** (money-IN counterpart to 7.1's money-OUT); USD single SoT preserved (a non-USD settled amount converts at the 7.2 fx_rate — Q-CURRENCY, built+tested, USD-only enabled at the endpoint). |
| 2026-06-10 | 7.7 | Dev (Linus) | **§4.1 NEW tables `he_api.payment_methods` + `he_api.invoices`** via `migrations/postgres/0011_create_payment_methods_and_invoices.sql` (ordinal **0011** — Architect Medium #1 corrected SM's 0014; 7.4-7.6 were additive-no-migration). `payment_methods` (Q-PAYMENT-METHODS): FIRST stored off-session-chargeable token — `provider_pm_token` is the opaque Stripe PaymentMethod id ONLY (PCI §8.4 SAQ-A, NEVER a PAN) + display-safe `brand`/`last4`; billing-svc single-writer. `invoices` (Q-INVOICE-TABLE): monthly statement SoT, figures FROZEN at generation (BR-I-2), **UNIQUE(user_id, period)** + `ON CONFLICT DO NOTHING` = exactly-one-per-user-month fence (BR-I-1). **§4.1 `balances.auto_recharge_*` REALISED**: the 4 columns pre-declared nullable by 0008 are wired (config via `SetAutoRecharge`) + the additive FK `auto_recharge_payment_method_id → payment_methods(id) ON DELETE SET NULL`. **§4.1 `recharge_orders` ALTER**: ADD `is_auto_recharge BOOLEAN` + a **partial-UNIQUE** index `(user_id) WHERE status='pending' AND is_auto_recharge` = the DURABLE auto-recharge storm fence (Q-TRIGGER — at most ONE in-flight auto-recharge per user, survives Redis loss). **§4.4 `payment.completed` REUSED** verbatim — the off-session auto-recharge charge settles via this topic → the 7.3 exactly-once credit applier UNCHANGED (NO new credit path, BR-R-5); the credit carries OUR order id via `ChargeOffSessionRequest.order_id` → Stripe metadata. NEW `low_balance`/`low_balance_failed` notification templates (5.4 cap_threshold SETNX-dedupe precedent). Additive / REVERSIBLE / NON-DESTRUCTIVE; `atlas.sum` row added. NEW Redis keys (no TTL): `autorecharge:lock:user:{id}`, `autorecharge:fail:user:{id}`, `balancestate:user:{id}:low_balance_notified`. |
| 2026-06-03 | 5.4 | Dev (Linus) | No DDL change. **§4.1 `api_keys` cron-reset writer** — `UPDATE current_month_cost_usd = 0 WHERE revoked_at IS NULL` (`repository.ResetMonthlyCosts`, run by the `monthly-cost-reset` CronJob at `0 0 1 * *` UTC; column-level idempotent). **§4.3 NEW Redis keys** (no TTL — cron-cleared): `keystate:apikey:cap_tripped:{api_key_id}` (sticky-trip breaker — presence ⇒ 402 fast-path; SET NX on first cross), `keystate:apikey:cap_warning_80_notified:{api_key_id}` + `keystate:apikey:cap_tripped_notified:{api_key_id}` (once-per-month email-dedupe sentinels SET NX inside notification-svc). The cron SCAN+DELs these three families PLUS the Story-5.2 `usage:apikey:*:month_cost_usd` counter family (cluster-mode-aware via `ClusterClient.ForEachMaster`, Architect m-1). **§4.4 NEW `audit.event` event_types**: `monthly_cost_reset.completed` + `monthly_cost_reset.redis_only.completed` (+ `.failed` variants; 3-retry best-effort emit per BR-3.9; audit-svc routes by type, no consumer change). |
| 2026-06-11 | 8.4 | Dev (Linus) | **§4.1 `api_keys` ADD COLUMN `content_safety_strictness VARCHAR(10) NOT NULL DEFAULT 'strict' CHECK ∈ {strict,default,loose}`** via migration `0013_add_content_safety_strictness_to_api_keys.sql` (additive / reversible / NON-DESTRUCTIVE — NOT NULL+DEFAULT backfills existing rows atomically, no data migration; `atlas.sum` row added with the placeholder hash per the local atlas-can't-run limit). Per-Key 内容安全严格度 gating the bidirectional content-safety filter (Story 8.4); DEFAULT `'strict'` = the shipped 8.2/8.3 block-all posture (zero regression, OQ-8.4-1; fail-closed). Read on the Validate hot path (carried to the gateway bearer cache) + the List/Update read-back. **No new table, no FK, no index.** |

---
