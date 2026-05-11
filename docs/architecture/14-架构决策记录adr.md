# 14. 架构决策记录（ADR）

完整 ADR 列表（提前 1.3 节有摘要）。每个 ADR 在 `docs/adr/` 下独立文件存档（标准 ADR 模板：Context / Decision / Consequences）。

## ADR-1: 边缘 TLS 终端用 Cloudflare 但禁用所有缓存

**Context**: 海外用户访问中国境内服务延迟高（150-300ms RTT）。需边缘加速，但合规要求数据不出境。

**Decision**: 使用 Cloudflare 作为全球 anycast TLS 终端 + DDoS 防护，但所有 routes 配置 `Cache-Control: no-store`，且通过 Workers 强制移除任何上游 Cache-Control 头。Edge 不持久化任何用户内容。

**Consequences**:
- ✅ DDoS 防护 + TLS 性能提升（更近的边缘）
- ✅ 合规边界守住（Cloudflare 仅做 TLS 终端）
- ⚠️ 不能利用 CDN 加速 GET 数据
- ⚠️ 需法律意见书确认"TLS 终端经过境外 PoP 但内容不持久化"是否构成数据出境（强烈建议在 Beta 上线前完成）

## ADR-2: API Gateway 用 Go (Fiber 框架)

**Context**: 网关需高并发、低延迟、易于运维。

**Decision**: Go 1.22 + Fiber 2.x（基于 fasthttp）。

**Consequences**:
- ✅ 高并发 5000+ QPS / pod
- ✅ 单二进制部署简单
- ✅ 团队 Go 经验成熟
- ❌ Fiber 不如 net/http 标准库通用（依赖 fasthttp）— 接受此 trade-off

## ADR-9: 模型适配器 plugin 独立 deployment

**Context**: 6 家模型 API 稳定性、限流、版本各异。

**Decision**: 每个适配器独立 K8s Deployment + 独立 pod 池 + 独立 HPA。Gateway 通过 service-mesh 或 K8s Service 路由到对应 adapter。

**Consequences**:
- ✅ 单家故障不影响其他
- ✅ 每家可独立扩缩
- ✅ 新增模型仅需新 adapter（不动 gateway 核心代码）
- ⚠️ 服务数量增加（6 个额外 deployment）；运维成本上升
- ⚠️ 内部 gRPC 跳数增加（gateway → adapter）；用 sidecar mesh 优化

## ADR-10: 计费引擎"实时扣减 + 异步对账"双轨

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
