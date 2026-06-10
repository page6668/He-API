# 3. 服务拓扑（Service Topology）

## 3.1 服务清单

| 服务名 | 语言 | 职责 | 部署 |
|--------|------|------|------|
| `api-gateway` | Go | 对外统一入口，OpenAI 兼容 API，鉴权，路由 | K8s Deployment + HPA |
| `auth-svc` | Go | 用户注册/登录、OAuth、Session、2FA、Key 管理 | K8s Deployment |
| `billing-svc` | Go | 计费引擎、订单、订阅、退款、余额扣减 | K8s Deployment |
| `payment-svc` | Go | 5 通道支付集成、Webhook 处理、对账 | K8s Deployment |
| `routing-svc` | Go | 智能路由策略（quality/cost/latency）、failover、A/B<br>**REALISED 6.1** (server-side skeleton) · **6.2** (real scoring + gateway wiring) · **6.3 failover REALISED** (gateway sequential failover over the ranked `failover_chain` on 502/504 — max 3 attempts / 30s; NEW optional `RankedStrategy` engine capability; pre-flush-only on the streaming path) · **6.4 A/B REALISED** (gateway parses `X-He-AB-Models`, routing-svc validates the 2 concrete legs → `is_ab_test`/`ab_selected_models`; gateway dispatches BOTH legs in PARALLEL via `sync.WaitGroup` + merges into one OpenAI `chat.completion` with per-choice `x_he_model`; non-streaming-only; dual-billing; no per-leg failover) | K8s Deployment |
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

## 3.2 数据流（典型 LLM 调用）

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
   - api-gateway 写入 Kafka topic `usage.recorded` (Story 7.1 Q-PRODUCER — 仅网关在响应时持有 per-request token 计数 + he_request_id + selected_model；billing-svc 是消费者，详见 data-models §4.4)
   - billing-svc 消费 Kafka 扣减余额 (PostgreSQL)
   - audit-svc 消费 Kafka 写入 ClickHouse `request_logs`
   - notification-svc 监听余额阈值，触发预警邮件
```

**套餐权益执行 + 计费档管理 (Story 7.8 — 订阅档 + Beta 模式)**：步骤 4b 的配额校验在热路径上叠加了**套餐权益 (entitlement) 执行**。`api-gateway` 在 `internal/entitlement/` 读取**缓存快照** `entitlement:user:{id}`（plan + 解析后的 rpm/tpm/qps/quota 限额，TTL≤60s + 跨 pod 失效哨兵——复用 5.1 `auth:apikey:revoked` 模式），**绝不在 chat 热路径做同步 PG/billing 调用**（BR-E-6）。快照缺失或解析不确定时**失败降级到 free 下限 (fail-safe-LOW，绝不 fail-open-high)**（BR-E-2）；plan 来自服务端按已鉴权 user_id 解析，客户端断言的 plan 被结构性忽略（防越权）。当 `flag:beta_mode` ON 时（`internal/featureflag/`：Redis 运行时读 + PG `feature_flags` 冷启动回落 + Unleash live push；冷启动无信号→OFF，运行中失 Redis→last-known），网关对每轴叠加全局 Sandbox 上限 `effective_limit = min(plan_limit, sandbox_limit)`（无 Enterprise 豁免——Beta 即收敛），再喂入既有 5.3 Lua 限流器 / 5.4 月度上限（仅改限额值，不新增节流信封）。写侧：**`billing-svc` 是该快照的唯一写者**——在每次 `subscriptions` 状态变更（升级/降级/取消/7.3 provider webhook 确认）时写入/失效快照（升级即时、降级与取消顺延至 `current_period_end`；plan 流转以 7.3 provider webhook 为准，绝不乐观改库；档位限额来自版本化的 `packages/plan-catalogue/` 代码目录，无运行时漂移）。Beta 开关的**写面由 Unleash 控制台 RBAC 守护**（He-API 无 `beta_mode` 写端点——Q-ADMIN-BETA），网关侧仅为内部只读。

## 3.3 同步 vs 异步边界

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
