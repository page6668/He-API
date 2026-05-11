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
session:{session_id}                            JSON, TTL 7 days
auth:apikey:{key_hash}                          缓存 user_id + scope, TTL 5min
balance:user:{user_id}:realtime                 实时余额, 跟 PG 对账
flag:beta_mode                                  bool, 实时 Feature Flag
```

## 4.4 Kafka Topics

| Topic | Schema | 消费者 | 保留 |
|-------|--------|-------|------|
| `usage.recorded` | UsageEvent (proto) | billing-svc, audit-svc, analytics-svc | 7 天 |
| `payment.completed` | PaymentEvent | billing-svc, notification-svc | 30 天 |
| `audit.event` | AuditEvent | audit-svc | 30 天 |
| `notification.queued` | NotificationEvent | notification-svc | 7 天 |
| `safety.violation` | SafetyEvent | audit-svc, ops 告警 | 90 天 |

---
