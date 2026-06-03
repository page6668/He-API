# 13. 测试策略（Test Strategy）

## 13.1 测试金字塔

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

## 13.2 测试类型

| 类型 | 目标 | 工具 |
|------|------|------|
| 单元测试 | 业务逻辑、工具函数 | Go testing / Vitest |
| 集成测试 | 服务 + DB / Redis 联动 | testcontainers-go |
| 契约测试 | Adapter 协议合规 | 自建 fixtures + golden files + 共享协议不变量库 `apps/api-gateway/tests/_protocol_invariants.py`（Story 4.8）+ adapter-fake CI lane（Story 4.8） |
| E2E | 核心用户旅程（注册→调用→计费） | Playwright |
| 压测 | 网关性能 | k6 |
| 混沌测试 | 上游模型故障注入 | Toxiproxy<br>**Story 6.3 REALISED**：上游 502 / 连接超时注入 → 网关 failover 切到下一候选模型（`go test -tags chaos`；Toxiproxy 为 nightly 环境超集，门控 lane 用 fault-handle 注入同一不变量） |
| 安全测试 | OWASP Top 10 | OWASP ZAP |

## 13.3 关键测试场景

- 高并发流式响应不串流（race condition）
- 余额扣减并发安全（同一用户多并发请求）
- 上游 502 时 failover 正确切换
- 内容过滤流式拦截不漏检
- 多语言文案完整性（CI 检测缺失 i18n key）
- 5 个支付通道沙箱集成测试
- Adapter contract anti-regression matrix runs on every PR (`.github/workflows/test.yml::gateway-openai-sdk-contract` — 20-cell matrix × 10 vendor model ids × stream={false, true}; live-vendor verification runs nightly via `.github/workflows/contract-tests-live.yml`; Story 4.8 closes Epic 4)

## 13.4 变更记录

| 日期 | Story / 作者 | 变更摘要 |
|------|-------------|---------|
| 2026-05-20 | Story 4.8 (SM Phil + Dev Linus) | §13.2 契约测试工具列扩展为「自建 fixtures + golden files + 共享协议不变量库 + adapter-fake CI lane」；§13.3 新增 anti-regression matrix 关键场景项。 |

---
