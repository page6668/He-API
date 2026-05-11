# He-API 全栈架构文档（Architecture）

> **China LLMs for the World** — 全栈架构规范
>
> 文档版本: v1.0 | 状态: Draft | 输入依据: `docs/project-brief.md`、`docs/prd.md`、`docs/front-end-spec.md`
>
> 起草人: Yuri 代笔（原 Architect 智能体因 op-He-API 会话内 cc 实例的本地代理对长流响应支持不佳而无法完成；Yuri 代笔基于 PRD 第 4 节技术假设 + 前端规格 + 4 项 Q&A 决策）。

---

## 1. 架构总览（Architecture Overview）

### 1.1 高层架构图

```
                       ┌────────────────────────────────────────────┐
  Global Developers  →  Edge TLS Layer (Cloudflare)                 │
                       │   - TLS 1.3 termination                    │
                       │   - DDoS protection                        │
                       │   - NO content caching (合规边界)           │
                       └─────────────────┬──────────────────────────┘
                                         ↓ (HTTPS)
                       ┌─────────────────────────────────────────────┐
                       │  China Cloud (阿里云/腾讯云/华为云) VPC      │
                       │  ┌─────────────────────────────────────┐   │
                       │  │  L7 Load Balancer (ALB / SLB)       │   │
                       │  └────────────────┬────────────────────┘   │
                       │                   ↓                         │
                       │  ┌─────────────────────────────────────┐   │
                       │  │     API Gateway (Go, K8s)           │   │
                       │  │  ┌──────┐ ┌──────┐ ┌──────┐         │   │
                       │  │  │ pod1 │ │ pod2 │ │ pod3 │ ...     │   │
                       │  │  └──────┘ └──────┘ └──────┘         │   │
                       │  └─┬───────┬───────┬───────┬───────────┘   │
                       │    │       │       │       │               │
                       │    ↓       ↓       ↓       ↓               │
                       │  ┌────┐ ┌────┐ ┌────┐ ┌────┐               │
                       │  │auth│ │bill│ │route│ │safety│             │
                       │  │ svc│ │ svc│ │ svc │ │ svc │             │
                       │  └────┘ └────┘ └────┘ └────┘               │
                       │    │                                        │
                       │    ↓                                        │
                       │  ┌──────────────────────────────────┐      │
                       │  │  Model Adapters (6 plugins)      │      │
                       │  │  qwen / deepseek / kimi /        │      │
                       │  │  glm / doubao / ernie            │      │
                       │  └──────────────┬───────────────────┘      │
                       │                 ↓                          │
                       │   ┌─────────────────────────────────┐      │
                       │   │ Upstream LLM APIs (各厂商)      │      │
                       │   │ Qwen / DeepSeek / Moonshot /    │      │
                       │   │ Zhipu / Doubao / Baidu          │      │
                       │   └─────────────────────────────────┘      │
                       │                                            │
                       │  Data Layer (all in China):                │
                       │  ┌─────────┐ ┌──────┐ ┌──────────────┐    │
                       │  │Postgres │ │Redis │ │ ClickHouse   │    │
                       │  │ (OLTP)  │ │      │ │ (OLAP/logs)  │    │
                       │  └─────────┘ └──────┘ └──────────────┘    │
                       │  ┌─────────────┐ ┌────────────────────┐   │
                       │  │ Kafka       │ │ Object Storage     │   │
                       │  │ (events)    │ │ (OSS, exports)     │   │
                       │  └─────────────┘ └────────────────────┘   │
                       └────────────────────────────────────────────┘
```

### 1.2 架构风格

- **API Gateway + Microservices**: 统一入口（Go API Gateway）+ 后端独立服务（gRPC 内部通信）
- **Plugin 架构**: 模型适配器作为独立服务部署，可热插拔新增
- **Event-Driven**: 计费事件、用量统计、审计日志通过 Kafka 异步消费，不阻塞主链路
- **Stateless Gateway**: 网关层零状态，K8s HPA 横向扩展
- **Polyglot Storage**: PostgreSQL（OLTP） + Redis（限流/缓存） + ClickHouse（OLAP/日志） + OSS（对象）

### 1.3 关键架构决策（提前抽出，详见 §14）

| ID | 决策 | 理由 |
|----|------|------|
| ADR-1 | 边缘 TLS 终端用 Cloudflare 但**禁用所有缓存** | 合规：数据不出境 |
| ADR-2 | API Gateway 用 Go（Fiber 框架） | 高并发、低 GC、生态成熟 |
| ADR-3 | 内部服务间 gRPC（HTTP/2 + protobuf） | 类型安全、性能、双向流 |
| ADR-4 | 数据库主用 PostgreSQL 16 | 业务关系强、JSONB 灵活、ACID |
| ADR-5 | 日志/用量/账单事件主用 ClickHouse 24+ | OLAP 高吞吐、压缩比高 |
| ADR-6 | K8s + Helm + ArgoCD（GitOps） | 标准化、可复现 |
| ADR-7 | 前端 Next.js 14 (App Router) + next-intl | 服务端渲染 + i18n + 流式 |
| ADR-8 | 主云阿里云 + 腾讯云容灾 | 备案常见组合、价格谈判优势 |
| ADR-9 | 模型适配器 plugin 独立 deployment | 单家故障不影响其他；独立扩缩容 |
| ADR-10 | 计费引擎按"实时扣减 + 异步对账" 双轨 | 实时熔断 + 准确性保障 |

---

## 2. 技术栈（Tech Stack）

### 2.1 完整技术清单

| 层 | 技术 | 版本 | 用途 |
|----|------|------|------|
| **前端框架** | Next.js | 14.2+ (App Router) | 控制台、文档站 |
| **前端语言** | TypeScript | 5.4+ | 类型安全 |
| **UI 库** | shadcn-ui + Tailwind CSS | 最新 / 3.4+ | 组件 + 样式 |
| **i18n** | next-intl | 3.x | 多语言运行时切换 |
| **状态管理** | TanStack Query + Zustand | 5.x / 4.x | 数据请求 + 全局状态 |
| **表单** | React Hook Form + Zod | 7.x / 3.x | 表单 + 校验 |
| **图表** | Recharts + ECharts | 2.x / 5.x | Dashboard / Benchmark |
| **代码高亮** | Shiki | 1.x | 文档 / Playground |
| **网关语言** | Go | 1.22+ | API Gateway |
| **网关框架** | Fiber | 2.52+ | HTTP server (基于 fasthttp) |
| **内部 RPC** | gRPC + Buf | 1.x | 服务间通信 + protobuf 管理 |
| **后端 OLTP DB** | PostgreSQL | 16+ | 用户、订单、Key、订阅、配额 |
| **缓存/限流** | Redis | 7.2+ | 限流计数、Session、热数据缓存 |
| **OLAP DB** | ClickHouse | 24+ | 调用日志、用量聚合、Benchmark |
| **消息队列** | Apache Kafka | 3.7+ | 计费事件、审计流、异步任务 |
| **对象存储** | 阿里云 OSS / 腾讯云 COS | - | 用户数据导出包、PDF 账单、静态资产 |
| **API 协议** | OpenAPI 3.1 + protobuf | - | 对外/对内合约 |
| **认证** | JWT (RS256) + OAuth2 | - | API Key / OAuth 登录 |
| **支付集成** | Stripe SDK / PayPal SDK / Coinbase Commerce / Alipay+ SDK / WeChat Pay SDK | - | 5 通道支付 |
| **邮件** | SendGrid (主) + 阿里云邮推 (备) | - | 验证码、账单、通知 |
| **DNS** | Cloudflare DNS | - | 全球 DNS 解析 |
| **CDN/Edge** | Cloudflare Workers | - | TLS 终端（不缓存内容） |
| **容器** | Docker | 25+ | 镜像 |
| **编排** | Kubernetes | 1.29+ | 容器编排 |
| **包管理** | Helm | 3.14+ | K8s 部署模板 |
| **部署** | ArgoCD | 2.10+ | GitOps |
| **CI/CD** | GitHub Actions / GitLab CI | - | 流水线 |
| **可观测** | OpenTelemetry SDK | 1.x | 全链路追踪 |
| **指标** | Prometheus + Grafana | 2.50+ / 10+ | metrics + dashboard |
| **日志** | Loki + Promtail | 3.x | 日志聚合 |
| **告警** | Alertmanager + 飞书机器人 + PagerDuty | - | 多通道告警 |
| **错误追踪** | Sentry | 自托管 | 前端 + 后端异常 |
| **特性开关** | Unleash (自托管) | 5.x | A/B / 灰度 / Beta 模式 |
| **密钥管理** | HashiCorp Vault | 1.16+ | 密钥、证书、动态密码 |
| **基础设施即代码** | Terraform | 1.7+ | 云资源 |
| **测试 - 后端** | Go testing + testify + ginkgo | - | 单元 + 集成 |
| **测试 - 前端** | Vitest + Playwright | - | 单元 + E2E |
| **压测** | k6 | 0.50+ | 性能基准 |
| **API 文档** | Mintlify 或 Docusaurus | - | docs.he-api.com |

### 2.2 版本选择原则

- **稳定优先**: 选择 LTS 或最近 stable 大版本（如 PostgreSQL 16，避开刚发布的 17）
- **生态成熟**: 优先选择社区活跃、Stack Overflow 答案多的版本
- **安全更新**: 至少 24 个月内仍接收安全补丁

---

## 3. 服务拓扑（Service Topology）

### 3.1 服务清单

| 服务名 | 语言 | 职责 | 部署 |
|--------|------|------|------|
| `api-gateway` | Go | 对外统一入口，OpenAI 兼容 API，鉴权，路由 | K8s Deployment + HPA |
| `auth-svc` | Go | 用户注册/登录、OAuth、Session、2FA、Key 管理 | K8s Deployment |
| `billing-svc` | Go | 计费引擎、订单、订阅、退款、余额扣减 | K8s Deployment |
| `payment-svc` | Go | 5 通道支付集成、Webhook 处理、对账 | K8s Deployment |
| `routing-svc` | Go | 智能路由策略（quality/cost/latency）、failover、A/B | K8s Deployment |
| `safety-svc` | Go | 内容安全过滤（敏感词 + 模型分类器） | K8s Deployment |
| `quota-svc` | Go | 限流（QPS/RPM/TPM）、配额、月度上限熔断 | K8s Deployment |
| `adapter-qwen` | Go | Qwen 模型适配器 | K8s Deployment（独立 pod 池） |
| `adapter-deepseek` | Go | DeepSeek 适配器 | K8s Deployment |
| `adapter-kimi` | Go | Kimi 适配器 | K8s Deployment |
| `adapter-glm` | Go | GLM 适配器 | K8s Deployment |
| `adapter-doubao` | Go | Doubao 适配器 | K8s Deployment |
| `adapter-ernie` | Go | 文心适配器 | K8s Deployment |
| `analytics-svc` | Go | 用量聚合、Dashboard 数据查询、Benchmark 计算 | K8s Deployment |
| `audit-svc` | Go | 审计日志写入、合规归档、敏感词治理日志 | K8s Deployment |
| `notification-svc` | Go | 邮件、SMS、Webhook 推送 | K8s Deployment |
| `console` | Next.js | Web 控制台前端（SSR + CSR） | K8s Deployment |
| `docs-site` | Next.js / Mintlify | 公开文档站 | K8s Deployment / 静态站 |

### 3.2 数据流（典型 LLM 调用）

```
1. Client → Edge TLS (Cloudflare) → 仅 TLS 终端，不缓存
2. Edge → L7 LB (ALB) → 上海/北京/深圳 region (多 AZ)
3. L7 LB → api-gateway pod
4. api-gateway:
   a. JWT/Bearer Token 校验 (本地缓存) → 失败返回 401
   b. → quota-svc (gRPC) 配额校验 → 失败返回 429
   c. → safety-svc (gRPC) 入参敏感词过滤 → 命中返回 400
   d. → routing-svc (gRPC) 选择 model adapter
5. api-gateway → adapter-{model} (gRPC, 流式)
6. adapter → 上游 LLM 厂商 API (HTTPS, 流式 SSE)
7. 流式响应反向链路:
   adapter → api-gateway → safety-svc 出参过滤 (流式 chunk-by-chunk) → client
8. 异步事件:
   - billing-svc 写入 Kafka topic `usage.recorded`
   - billing-svc 消费 Kafka 扣减余额 (PostgreSQL)
   - audit-svc 消费 Kafka 写入 ClickHouse `request_logs`
   - notification-svc 监听余额阈值，触发预警邮件
```

### 3.3 同步 vs 异步边界

| 操作 | 模式 | 理由 |
|------|------|------|
| Token 用量统计 | 异步 (Kafka) | 不阻塞调用链路；ClickHouse 批量写入 |
| 余额扣减 | 半同步：Redis 实时扣减 + Kafka 异步落库 | 实时熔断 + 数据库不被高 QPS 打爆 |
| 限流计数 | 同步 (Redis) | 必须实时 |
| 鉴权 | 同步 (本地缓存 + Redis) | 必须实时 |
| 内容安全 | 同步 | 阻塞链路必须 |
| 审计日志 | 异步 (Kafka → ClickHouse) | 高吞吐 |
| 邮件通知 | 异步 (Kafka → notification-svc) | 不阻塞用户操作 |
| 退款处理 | 异步任务 | 涉及外部支付通道，可能需重试 |

---

## 4. 数据模型（Data Models）

### 4.1 PostgreSQL Schema 核心表

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

### 4.2 ClickHouse Schema (OLAP)

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

### 4.3 Redis Key 规范

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

### 4.4 Kafka Topics

| Topic | Schema | 消费者 | 保留 |
|-------|--------|-------|------|
| `usage.recorded` | UsageEvent (proto) | billing-svc, audit-svc, analytics-svc | 7 天 |
| `payment.completed` | PaymentEvent | billing-svc, notification-svc | 30 天 |
| `audit.event` | AuditEvent | audit-svc | 30 天 |
| `notification.queued` | NotificationEvent | notification-svc | 7 天 |
| `safety.violation` | SafetyEvent | audit-svc, ops 告警 | 90 天 |

---

## 5. API 规范（API Specification）

### 5.1 对外 API（OpenAI 兼容）

#### 5.1.1 Chat Completions

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

#### 5.1.2 标准错误码

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
| 502 | `502_upstream_unavailable` | 上游模型不可用（即将 failover） |
| 504 | `504_upstream_timeout` | 上游模型超时（即将 failover） |
| 500 | `500_internal_error` | 系统异常 |

#### 5.1.3 其他端点

```
GET  /v1/models                    返回模型列表 + 能力矩阵
POST /v1/embeddings                文本 embedding
POST /v1/audio/transcriptions      ASR (Whisper 兼容)
POST /v1/audio/speech              TTS
POST /v1/images/generations        V1.1
GET  /v1/usage                     查询本月用量（自定义端点）
GET  /v1/balance                   查询余额
```

### 5.2 内部 gRPC 服务（protobuf 节选）

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

---

## 6. 源代码组织（Source Tree）

```
he-api/                                  (Monorepo, Turborepo)
├── apps/
│   ├── api-gateway/                     Go service
│   │   ├── cmd/server/main.go
│   │   ├── internal/
│   │   │   ├── handlers/                HTTP handlers (OpenAI 兼容)
│   │   │   ├── middleware/              auth / quota / safety / cors / logging
│   │   │   ├── routing/                 路由策略
│   │   │   └── streaming/               SSE 处理
│   │   ├── pkg/                         可共享 internal libs
│   │   ├── go.mod
│   │   └── Dockerfile
│   ├── auth-svc/                        gRPC service
│   ├── billing-svc/
│   ├── payment-svc/
│   ├── routing-svc/
│   ├── safety-svc/
│   ├── quota-svc/
│   ├── analytics-svc/
│   ├── audit-svc/
│   ├── notification-svc/
│   ├── adapters/
│   │   ├── qwen/
│   │   ├── deepseek/
│   │   ├── kimi/
│   │   ├── glm/
│   │   ├── doubao/
│   │   └── ernie/
│   │       ├── cmd/server/main.go
│   │       ├── internal/adapter.go
│   │       └── go.mod
│   ├── console/                         Next.js Web 控制台
│   │   ├── app/                         App Router
│   │   │   ├── [locale]/
│   │   │   │   ├── (marketing)/         landing / pricing / models / benchmark
│   │   │   │   ├── (auth)/              signin / signup / onboarding
│   │   │   │   ├── (console)/           dashboard / keys / billing / ...
│   │   │   │   └── playground/
│   │   │   └── api/                     Next.js API routes (BFF for console)
│   │   ├── components/                  React components
│   │   │   ├── ui/                      shadcn-ui base
│   │   │   └── business/                ModelChip / RoutingStrategyCard / ...
│   │   ├── lib/                         hooks / clients / utils
│   │   ├── messages/                    i18n JSON files (en.json, zh-CN.json, ...)
│   │   ├── public/
│   │   ├── package.json
│   │   └── next.config.mjs
│   └── docs/                            Mintlify 或 Docusaurus
├── packages/
│   ├── proto/                           protobuf definitions (用 Buf 管理)
│   │   ├── he/api/v1/*.proto
│   │   └── buf.yaml
│   ├── sdk-python/                      Python SDK
│   │   ├── he_api/
│   │   ├── tests/
│   │   ├── pyproject.toml
│   │   └── README.md
│   ├── sdk-typescript/                  TS SDK (npm: @he-api/sdk)
│   │   ├── src/
│   │   ├── tests/
│   │   └── package.json
│   ├── sdk-go/                          Go SDK
│   ├── shared-types/                    TS shared types (前端 ↔ console)
│   ├── i18n-keys/                       共享 i18n key 定义（避免拼写错误）
│   └── eslint-config/
├── infra/
│   ├── terraform/                       云资源（VPC / K8s / DB / OSS / Kafka）
│   │   ├── modules/
│   │   ├── envs/
│   │   │   ├── dev/
│   │   │   ├── staging/
│   │   │   └── prod/
│   │   └── main.tf
│   ├── helm/                            Helm charts (per-service)
│   │   ├── api-gateway/
│   │   ├── auth-svc/
│   │   └── ...
│   ├── argocd/                          GitOps app definitions
│   │   ├── applications/
│   │   └── projects/
│   └── k8s-base/                        K8s 基础资源 (namespace / network policy / RBAC)
├── scripts/                             开发与运维脚本
│   ├── db-migrate.sh
│   ├── seed-test-data.sh
│   └── ci/
├── docs/                                文档
│   ├── project-brief.md
│   ├── prd.md
│   ├── front-end-spec.md
│   └── architecture.md (本文)
├── .github/workflows/                   CI 流水线
│   ├── lint.yml
│   ├── test.yml
│   ├── build-images.yml
│   └── deploy-staging.yml
├── turbo.json                           Turborepo 配置
├── pnpm-workspace.yaml
├── go.work                              Go workspace
└── README.md
```

---

## 7. 基础设施与部署（Infrastructure & Deployment）

### 7.1 云架构

```
主云：阿里云
  Region: cn-shanghai (主) + cn-shenzhen (灾备)
  - VPC + 多 AZ 子网
  - SLB (Server Load Balancer) 7 层
  - ACK (K8s) 集群
  - RDS PostgreSQL 16 (主从)
  - Redis (Tair 集群版)
  - 阿里云 ClickHouse 或自建
  - 阿里云 Kafka (or 自建)
  - OSS (对象存储)
  - SLS (日志服务，备选)
  - KMS (密钥管理)

容灾云：腾讯云
  Region: ap-shanghai
  - 镜像复制（OSS → COS 同步）
  - 主云灾难时手动切换 DNS

边缘：Cloudflare
  - 全球 anycast TLS 终端
  - DDoS 防护
  - WAF
  - DNS
  - 配置：proxied 但 Cache-Control: no-store, all routes
```

### 7.2 K8s 资源拓扑

```
namespace: he-api-prod
  - api-gateway (Deployment, replicas=10, HPA 5-50)
  - auth-svc, billing-svc, ... (各 3 replicas, HPA)
  - adapter-* (各 3 replicas, HPA per model 流量)
  - kafka, postgres, redis, clickhouse 用云托管

namespace: he-api-staging
  - 同 prod 但 replicas=2

namespace: monitoring
  - prometheus, grafana, alertmanager, loki

namespace: cert-manager
namespace: ingress-nginx
namespace: argocd
```

### 7.3 部署流程（GitOps）

```
开发者 push to main
  ↓
GitHub Actions:
  1. lint + unit test (per-service 并行)
  2. integration test
  3. build Docker images, push to registry (阿里云 ACR)
  4. update Helm values (image tag)
  5. commit to infra repo (separate repo or same)
  ↓
ArgoCD detect change
  ↓
ArgoCD sync to staging cluster
  ↓
自动 e2e test on staging
  ↓ (passing)
ArgoCD prompts manual promote to prod
  ↓
ArgoCD sync to prod (Blue-Green or Canary)
```

### 7.4 灾备策略

| 故障级别 | RTO | RPO | 处理 |
|---------|-----|-----|------|
| 单 pod 故障 | < 1min | 0 | K8s 自动重启 |
| 单 AZ 故障 | < 5min | 0 | 多 AZ 部署，流量切到健康 AZ |
| 主 region 故障 | < 30min | < 5min | DNS 切换至腾讯云灾备 |
| 数据库主节点故障 | < 2min | 0 | RDS 自动主从切换 |
| 上游模型 API 故障 | 0（用户透明） | 0 | 智能路由 failover 到其他模型 |

---

## 8. 安全（Security）

### 8.1 安全分层

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

### 8.2 API Key 生命周期

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

### 8.3 GDPR / CCPA 实现

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

### 8.4 PCI-DSS 边界

He-API **不存储任何信用卡数据**：
- Stripe / PayPal 自有 PCI-DSS Level 1 合规
- 用户卡信息直接发送至 Stripe/PayPal（client-side tokenization）
- 我方仅存储 token 与订单 ID

---

## 9. 合规架构（Compliance Architecture）

### 9.1 数据不出境的架构强制

| 合规边界 | 强制手段 |
|---------|---------|
| 数据 100% 境内存储 | PostgreSQL / Redis / ClickHouse / OSS 全部部署在阿里云境内 region |
| Edge 不缓存内容 | Cloudflare 配置 `Cache-Control: no-store` 全路由 + Workers 强制移除任何缓存指令 |
| 海外 SaaS 不接触请求/响应内容 | Sentry 自托管在境内（不用 sentry.io）；PostHog 自托管；不发任何用户内容到 SaaS |
| 备份不出境 | OSS 跨 region 复制限定境内（cn-shanghai → cn-shenzhen） |
| 监控告警平台 | Grafana 自托管；告警通过飞书机器人（境内） + PagerDuty（仅传 metric 标识，不传内容） |

### 9.2 三件套备案

| 备案 | 申请主体 | 时间线 | 阻塞行为 |
|------|---------|-------|---------|
| ICP 备案 | 中国境内法人公司 | 2-4 周 | 无 ICP 备案不能在境内提供网站服务 |
| 算法备案（互联网算法） | 同上 | 4-8 周 | 提供算法推荐服务必须 |
| 生成式 AI 服务备案 | 同上 | 6-12 周 | 提供 LLM 服务必须（核心阻塞） |

**Beta 模式应对**: 生成式 AI 备案完成前，平台以 Sandbox / 限额免费 / 对外标识 "Beta" 运行，规避正式商业化运营。`feature_flags.beta_mode = true` 全局开关在网关层强制：
- 余额扣减跳过（Free credit only）
- Stripe / PayPal 通道隐藏，仅 USDC / Alipay+ / WeChat 小额开放
- 文档与控制台明显标注 "Beta"

### 9.3 内容安全合规

```
入参 Filter (safety-svc):
  1. Bloom filter 快速排除非命中（98%+ 流量）
  2. 词典匹配（基础关键词列表）
  3. 模型分类器（轻量 BERT 微调，TopP 95% 准确）
  4. 命中 → 拒绝 + 写入 content_safety_logs

出参 Filter:
  - 流式 chunk 缓冲：最近 200 tokens 滑窗检测
  - 命中后立即终止流，返回脱敏内容
  - 已发出的 chunk 客户端处理（无法收回，但日志记录）

治理日志:
  - 保留 6 个月（合规要求）
  - 备案审计可调取
  - 用户 GDPR 删除时不删除（合规优先于个人删除请求）
```

---

## 10. 性能与可扩展性（Performance & Scalability）

### 10.1 性能预算

| 指标 | 目标 | 实现 |
|------|------|------|
| 网关 P95 叠加延迟 | ≤ 100ms | Go fasthttp + 同 region gRPC + Redis 缓存 |
| 流式 TTFB | ≤ 300ms | 上游连接预热 + 早期 SSE 心跳 |
| 单实例 QPS | ≥ 5,000 | Go fasthttp benchmark 已达；K8s HPA |
| 并发连接 | ≥ 50,000 | 多 pod + LB 长连接 |
| Token 计费精度 | 误差 < 1% | 实时 Redis + Kafka 异步对账 PG |

### 10.2 缓存层

```
L1: 应用内存缓存（Gateway pod LRU, TTL 30s）
  - 模型路由元数据
  - 模型定价
  - Feature flag

L2: Redis（共享）
  - API Key 鉴权结果（TTL 5min）
  - 限流计数器
  - 余额（写穿到 PG）

L3: PostgreSQL
  - 持久化数据
```

### 10.3 限流分层

```
Edge 层（Cloudflare Rate Limiting）:
  - 全局每 IP 1000 RPM 上限（防 DDoS）
  - 异常流量自动 challenge

应用层（quota-svc）:
  - QPS / RPM / TPM per Key
  - 月度消费上限
  - 滑动窗口计数（Redis ZSET 实现）
```

### 10.4 扩展瓶颈

| 瓶颈 | 阈值 | 应对 |
|------|------|------|
| Gateway pod | 5000 QPS | HPA + 横向扩展 |
| Postgres 连接数 | ~1000 | PgBouncer 连接池 + 读写分离 + 只读副本 |
| Redis QPS | ~50k/instance | 集群分片（key hash） |
| ClickHouse 写入 | ~1M rows/s | 已超出预期上限；批量写入 + 分布式表 |
| Kafka 吞吐 | ~10k msg/s/partition | 多 partition + replication factor 3 |
| 上游 LLM API 限流 | 厂商不同（Qwen/DeepSeek 单 key 1000+ QPS） | 多 key 池 + 智能 failover |

---

## 11. 可观测性（Observability）

### 11.1 三大支柱

| 类型 | 工具 | 内容 |
|------|------|------|
| **Metrics** | Prometheus + Grafana | RED + USE 指标 + 业务指标 |
| **Logs** | Loki + Promtail | 结构化 JSON 日志 |
| **Traces** | OpenTelemetry + Jaeger（后端） | 全链路 trace |

### 11.2 关键 Dashboard

1. **Gateway 大盘**: QPS / 错误率 / 延迟 P50/P95/P99 / Top error codes
2. **业务大盘**: 注册/天 / DAU / 充值金额 / Token 消费 / 月 GMV
3. **模型大盘**: 各模型 QPS / 延迟 / 错误率 / 成本 / 路由命中率
4. **支付大盘**: 各通道成功率 / 平均时间 / 退款率 / 异常告警
5. **合规大盘**: 内容过滤命中率 / 误杀率（需人工标注） / 备案进度 / 数据出境检测（应永远 0）

### 11.3 告警规则（核心）

```yaml
- alert: GatewayP95LatencyHigh
  expr: histogram_quantile(0.95, rate(http_request_duration_seconds_bucket[5m])) > 0.5
  for: 5m
  severity: warning
  
- alert: GatewayErrorRateHigh
  expr: rate(http_requests_total{status=~"5.."}[5m]) / rate(http_requests_total[5m]) > 0.01
  for: 5m
  severity: critical
  
- alert: UpstreamModelDown
  expr: rate(adapter_upstream_errors_total[5m]) > 0.1
  for: 3m
  severity: warning
  labels:
    routing_action: failover
    
- alert: DataExportSuspect
  expr: increase(network_egress_to_overseas_bytes[10m]) > 0
  for: 1m
  severity: critical
  description: "WARNING: 检测到向境外网络的数据传输（不应发生）"
```

### 11.4 通知通道

- **P0 (critical)**: PagerDuty → 值班手机（含技术 + 合规人员）
- **P1 (high)**: 飞书机器人 + 邮件
- **P2 (medium)**: 飞书 + Slack
- **P3 (info)**: Slack

---

## 12. 编码规范（Coding Standards）

### 12.1 Go 后端

- **目录布局**: 遵循 [Standard Go Project Layout](https://github.com/golang-standards/project-layout)
- **错误处理**: 显式返回 `error`；用 `errors.Wrap` / `fmt.Errorf("...%w", err)` 添加上下文；禁用 `panic` 在业务路径
- **并发**: context 传递；context.Done() 检查；errgroup 替代 sync.WaitGroup
- **依赖注入**: wire 或手动 constructor
- **测试**: testify + ginkgo（BDD 风格）；每文件配套 _test.go；覆盖率 ≥ 70%
- **Linter**: golangci-lint (含 govet, gosimple, staticcheck, ineffassign, errcheck)
- **格式**: gofumpt（gofmt 严格版）
- **gRPC**: 全部用 connect-go（HTTP/2 + gRPC 双协议，调试友好）

### 12.2 TypeScript 前端

- **风格**: ESLint + Prettier；strict mode TS
- **组件**: 函数组件 + hooks；避免 class
- **命名**: PascalCase 组件 / camelCase 函数 / SCREAMING_SNAKE 常量
- **错误处理**: ErrorBoundary 包裹关键区；Toast 通知用户
- **测试**: Vitest 单元 + Playwright E2E
- **依赖原则**: 慎用大型 lib（每个 dep 评估 bundle 影响）

### 12.3 通用规则

- 所有用户面文案外置 i18n
- 所有 PII 在日志输出前脱敏
- 所有外部输入校验（Zod / proto schema）
- API 字段命名遵循 OpenAI 协议（snake_case）；内部 gRPC 用 camelCase（proto 默认）
- Git commit 用 Conventional Commits + Orchestrix Co-Authored-By 签名

---

## 13. 测试策略（Test Strategy）

### 13.1 测试金字塔

```
                /\
               /E2E\          5%   Playwright (关键用户旅程)
              /─────\
             /集成测试\        25%  服务间 gRPC + DB
            /─────────\
           /  契约测试  \     20%  OpenAI 协议合规 + Adapter contract
          /─────────────\
         /   单元测试    \    50%  纯函数 + handler 隔离
        /─────────────────\
```

### 13.2 测试类型

| 类型 | 目标 | 工具 |
|------|------|------|
| 单元测试 | 业务逻辑、工具函数 | Go testing / Vitest |
| 集成测试 | 服务 + DB / Redis 联动 | testcontainers-go |
| 契约测试 | Adapter 协议合规 | 自建 fixtures + golden files |
| E2E | 核心用户旅程（注册→调用→计费） | Playwright |
| 压测 | 网关性能 | k6 |
| 混沌测试 | 上游模型故障注入 | Toxiproxy |
| 安全测试 | OWASP Top 10 | OWASP ZAP |

### 13.3 关键测试场景

- 高并发流式响应不串流（race condition）
- 余额扣减并发安全（同一用户多并发请求）
- 上游 502 时 failover 正确切换
- 内容过滤流式拦截不漏检
- 多语言文案完整性（CI 检测缺失 i18n key）
- 5 个支付通道沙箱集成测试

---

## 14. 架构决策记录（ADR）

完整 ADR 列表（提前 1.3 节有摘要）。每个 ADR 在 `docs/adr/` 下独立文件存档（标准 ADR 模板：Context / Decision / Consequences）。

### ADR-1: 边缘 TLS 终端用 Cloudflare 但禁用所有缓存

**Context**: 海外用户访问中国境内服务延迟高（150-300ms RTT）。需边缘加速，但合规要求数据不出境。

**Decision**: 使用 Cloudflare 作为全球 anycast TLS 终端 + DDoS 防护，但所有 routes 配置 `Cache-Control: no-store`，且通过 Workers 强制移除任何上游 Cache-Control 头。Edge 不持久化任何用户内容。

**Consequences**:
- ✅ DDoS 防护 + TLS 性能提升（更近的边缘）
- ✅ 合规边界守住（Cloudflare 仅做 TLS 终端）
- ⚠️ 不能利用 CDN 加速 GET 数据
- ⚠️ 需法律意见书确认"TLS 终端经过境外 PoP 但内容不持久化"是否构成数据出境（强烈建议在 Beta 上线前完成）

### ADR-2: API Gateway 用 Go (Fiber 框架)

**Context**: 网关需高并发、低延迟、易于运维。

**Decision**: Go 1.22 + Fiber 2.x（基于 fasthttp）。

**Consequences**:
- ✅ 高并发 5000+ QPS / pod
- ✅ 单二进制部署简单
- ✅ 团队 Go 经验成熟
- ❌ Fiber 不如 net/http 标准库通用（依赖 fasthttp）— 接受此 trade-off

### ADR-9: 模型适配器 plugin 独立 deployment

**Context**: 6 家模型 API 稳定性、限流、版本各异。

**Decision**: 每个适配器独立 K8s Deployment + 独立 pod 池 + 独立 HPA。Gateway 通过 service-mesh 或 K8s Service 路由到对应 adapter。

**Consequences**:
- ✅ 单家故障不影响其他
- ✅ 每家可独立扩缩
- ✅ 新增模型仅需新 adapter（不动 gateway 核心代码）
- ⚠️ 服务数量增加（6 个额外 deployment）；运维成本上升
- ⚠️ 内部 gRPC 跳数增加（gateway → adapter）；用 sidecar mesh 优化

### ADR-10: 计费引擎"实时扣减 + 异步对账"双轨

**Context**: 高并发下 PostgreSQL 直接扣减成为瓶颈，但又必须实时熔断超额。

**Decision**:
1. 调用前：读取 Redis `balance:user:{id}:realtime` 检查 + 原子 DECRBY 实时扣减
2. 调用后：发 Kafka `usage.recorded` event
3. billing-svc 消费 event 批量写 PostgreSQL（5 秒 / 1000 条触发批写）
4. 每 1 小时跑对账脚本：Redis vs PostgreSQL 差异 → 调整 Redis（PG 为准）
5. PostgreSQL 故障时 Redis 仍可独立运行（最终一致性 SLA: 5 分钟内对账）

**Consequences**:
- ✅ 高并发性能（Redis 50k QPS）
- ✅ 实时熔断保护
- ⚠️ 短窗口可能多扣 / 少扣（接受）
- ⚠️ Redis 故障时降级（fallback 到 PG，性能下降但功能可用）

---

## 15. 实施路线图（Implementation Roadmap）

### 15.1 8 周激进 Beta 计划

| Week | 重点 | 关键交付 |
|------|------|---------|
| 1 | 项目地基 (Epic 1) | Monorepo + CI + K8s + 基础数据库 |
| 1-2 | 账户与 i18n 框架 (Epic 2) | 注册/登录/OAuth 可用 |
| 2-3 | 网关核心 (Epic 3) | OpenAI 兼容协议跑通 |
| 3-4 | 6 家模型适配器 (Epic 4) | 6 家全部接通 |
| 5 | Key 管理 + 限流 (Epic 5) + 智能路由 (Epic 6) | 配额可控；路由策略生效 |
| 6 | 计费 + 5 通道支付 (Epic 7) + 内容安全 (Epic 8) | 支付走通；过滤上线 |
| 7 | 多模态 + 监控 (Epic 9) + 多语言 + 文档 + SDK (Epic 10) | 体验完整 |
| 8 | 上线检查 + 性能调优 + Beta 公测开放 | 平台对外可用 |

### 15.2 风险缓解锚点

- **Week 4**: 必须完成至少 3 家模型适配器（DeepSeek + Qwen + Kimi），否则触发 scope 调整警告
- **Week 6**: 必须有任一支付通道走通；如 Stripe 卡顿则切换到 USDC + Alipay+ 优先
- **Week 7**: 备案任一项需有明确进度（材料提交确认）
- **持续**: 每周一次合规专家 sync，确认数据不出境断言

---

## 16. 下一步（Next Steps）

### 16.1 立即行动

1. **业务方审阅本架构**，标注需要修订之处
2. **Architect 实地评估**：
   - 阿里云 / 腾讯云 region 与 instance 类型选型
   - PostgreSQL / Redis / ClickHouse 容量规划（基于 MVP 1k 用户假设）
   - 月度基础设施成本估算（建议 ≤ ¥30K / $4K 起）
3. **Tech Lead 拆分**：将本文 Epic 1-10 进一步拆分为 Sprint Backlog（每 2 周一个 Sprint）
4. **PO 接力**：执行 `*execute-checklist po-master-validation` 验证 PRD/Spec/Architecture 三方一致性
5. **法务对接**：基于 9.1 节合规架构，请法律意见书针对：
   - Cloudflare TLS 终端是否构成数据出境
   - 5 个支付通道分别的合规边界
   - 三件套备案时间线确认

### 16.2 与 PO 的接口

> PO 在 *execute-checklist 时需特别检查：
> - PRD 的 44 条 FR 是否全部对应到 Architecture 的服务/模块？
> - PRD 的 9 条 NFR 是否在 Architecture 中有具体实现方案？
> - Front-end Spec 的 15 个页面是否在 Architecture 的 console 模块内规划？
> - 4 项 Q&A 决策是否在三份文档中保持一致？

### 16.3 与 SM (Phase B) 的接口

> 进入开发阶段后，SM 基于本架构 + PRD 拆分故事时需要：
> - 每个故事关联到 Architecture 中的具体服务（apps/xxx）
> - 复杂度评分参考本架构的"服务边界" + "测试策略"
> - 验收标准引用本架构定义的 NFR 指标

---

> **架构 v1.0 终版**。等待业务方审阅。审阅通过后转交 PO 做 master validation 与 shard。
