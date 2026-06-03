# 3. 服务拓扑（Service Topology）

## 3.1 服务清单

| 服务名 | 语言 | 职责 | 部署 |
|--------|------|------|------|
| `api-gateway` | Go | 对外统一入口，OpenAI 兼容 API，鉴权，路由 | K8s Deployment + HPA |
| `auth-svc` | Go | 用户注册/登录、OAuth、Session、2FA、Key 管理 | K8s Deployment |
| `billing-svc` | Go | 计费引擎、订单、订阅、退款、余额扣减 | K8s Deployment |
| `payment-svc` | Go | 5 通道支付集成、Webhook 处理、对账 | K8s Deployment |
| `routing-svc` | Go | 智能路由策略（quality/cost/latency）、failover、A/B<br>**REALISED 6.1** (server-side skeleton: `apps/routing-svc/` — SelectModel Connect-RPC + Strategy interface + engine + 4 stubs; gateway wiring + real scoring + failover + A/B land in Stories 6.2-6.4) | K8s Deployment |
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
   - billing-svc 写入 Kafka topic `usage.recorded`
   - billing-svc 消费 Kafka 扣减余额 (PostgreSQL)
   - audit-svc 消费 Kafka 写入 ClickHouse `request_logs`
   - notification-svc 监听余额阈值，触发预警邮件
```

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
