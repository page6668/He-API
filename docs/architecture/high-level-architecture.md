# 1. 架构总览（Architecture Overview）

## 1.1 高层架构图

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

## 1.2 架构风格

- **API Gateway + Microservices**: 统一入口（Go API Gateway）+ 后端独立服务（gRPC 内部通信）
- **Plugin 架构**: 模型适配器作为独立服务部署，可热插拔新增
- **Event-Driven**: 计费事件、用量统计、审计日志通过 Kafka 异步消费，不阻塞主链路
- **Stateless Gateway**: 网关层零状态，K8s HPA 横向扩展
- **Polyglot Storage**: PostgreSQL（OLTP） + Redis（限流/缓存） + ClickHouse（OLAP/日志） + OSS（对象）

## 1.3 关键架构决策（提前抽出，详见 §14）

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
