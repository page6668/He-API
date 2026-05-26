# 10. 性能与可扩展性（Performance & Scalability）

## 10.1 性能预算

| 指标 | 目标 | 实现 |
|------|------|------|
| 网关 P95 叠加延迟 | ≤ 100ms | Go stdlib net/http: P95 ≤ 100 ms observed in Story 3.1 cold-start; sustained-latency benchmark deferred to Epic 9 k6 baseline |
| 流式 TTFB | ≤ 300ms | 上游连接预热 + 早期 SSE 心跳 |
| 单实例 QPS | ≥ 5,000 | Go stdlib net/http: sustained-QPS benchmark deferred to Epic 9 k6 baseline (Story 3.1 ratified the stdlib stack; the legacy Fiber 5 k QPS claim is no longer load-bearing); K8s HPA |
| 并发连接 | ≥ 50,000 | 多 pod + LB 长连接 |
| Token 计费精度 | 误差 < 1% | 实时 Redis + Kafka 异步对账 PG |

## 10.2 缓存层

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

## 10.3 限流分层

```
Edge 层（Cloudflare Rate Limiting）:
  - 全局每 IP 1000 RPM 上限（防 DDoS）
  - 异常流量自动 challenge

应用层（gateway middleware in MVP; quota-svc reserved for future multi-caller）:
  - QPS / RPM / TPM per Key
  - 月度消费上限
  - 固定窗口计数（Redis INCR + EXPIRE NX, 1s/60s TTL）—— ZSET 滑动窗口预留为未来精度升级路径
```

**Q1 ratification (Story 5.3 / Architect Round 1):** MVP 选择 **in-gateway middleware**（`apps/api-gateway/internal/middleware/ratelimit/`），不引入独立 `quota-svc`。理由：Story 5.3 唯一调用方是网关热路径；独立服务多一次 RPC 跳转，无相称收益。`quota-svc` 预留给未来多调用方场景（如 billing-svc + routing-svc 同时消费同一配额状态）。

**Q2 ratification (Story 5.3 / Architect Round 1):** MVP 选择 **固定窗口 INCR + EXPIRE NX**（go-redis EVALSHA-cached Lua 原子脚本），不使用 ZSET 滑动窗口。理由：边界 artifact 有界（窗口边界 ≤ 2x ceiling，永不持续），符合滥用防护威胁模型；ZSET 滑动窗口预留为未来精度升级（付费层需要严格按秒预算时）。

## 10.4 扩展瓶颈

| 瓶颈 | 阈值 | 应对 |
|------|------|------|
| Gateway pod | 5000 QPS | HPA + 横向扩展 |
| Postgres 连接数 | ~1000 | PgBouncer 连接池 + 读写分离 + 只读副本 |
| Redis QPS | ~50k/instance | 集群分片（key hash） |
| ClickHouse 写入 | ~1M rows/s | 已超出预期上限；批量写入 + 分布式表 |
| Kafka 吞吐 | ~10k msg/s/partition | 多 partition + replication factor 3 |
| 上游 LLM API 限流 | 厂商不同（Qwen/DeepSeek 单 key 1000+ QPS） | 多 key 池 + 智能 failover |

## 10.5 Change Log

| Date | Story | Change |
|------|-------|--------|
| 2026-05-18 | Story 3.1 (SM Phil) | §10.1 网关延迟 / 单实例 QPS 表格的"实现"列由 "Go fasthttp" 改写为 "Go stdlib net/http"；5 k QPS 5,000 QPS 单实例上限以 stdlib 基线在 Epic 9 k6 baseline 重新认证。Original Fiber fasthttp benchmark claim is no longer load-bearing per Story 3.1 ratification (ADR-2). |
| 2026-05-26 | Story 5.3 (Architect Wright Round 1, M-2 + R-5) | §10.3 限流分层段落更新：Q1 ratification（in-gateway middleware for MVP, quota-svc reserved）+ Q2 ratification（fixed-window INCR + EXPIRE NX for MVP, ZSET sliding-window reserved）。原 nominal `应用层（quota-svc）` + `滑动窗口计数（Redis ZSET 实现）` 文案保留为未来精度升级路径的预留态，divergence rulings 显式记录。 |

---
