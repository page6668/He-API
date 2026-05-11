# 10. 性能与可扩展性（Performance & Scalability）

## 10.1 性能预算

| 指标 | 目标 | 实现 |
|------|------|------|
| 网关 P95 叠加延迟 | ≤ 100ms | Go fasthttp + 同 region gRPC + Redis 缓存 |
| 流式 TTFB | ≤ 300ms | 上游连接预热 + 早期 SSE 心跳 |
| 单实例 QPS | ≥ 5,000 | Go fasthttp benchmark 已达；K8s HPA |
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

应用层（quota-svc）:
  - QPS / RPM / TPM per Key
  - 月度消费上限
  - 滑动窗口计数（Redis ZSET 实现）
```

## 10.4 扩展瓶颈

| 瓶颈 | 阈值 | 应对 |
|------|------|------|
| Gateway pod | 5000 QPS | HPA + 横向扩展 |
| Postgres 连接数 | ~1000 | PgBouncer 连接池 + 读写分离 + 只读副本 |
| Redis QPS | ~50k/instance | 集群分片（key hash） |
| ClickHouse 写入 | ~1M rows/s | 已超出预期上限；批量写入 + 分布式表 |
| Kafka 吞吐 | ~10k msg/s/partition | 多 partition + replication factor 3 |
| 上游 LLM API 限流 | 厂商不同（Qwen/DeepSeek 单 key 1000+ QPS） | 多 key 池 + 智能 failover |

---
