# 4. 技术假设（Technical Assumptions）

> Architect 阶段会做最终选型；以下为业务方建议方向。

## 4.1 仓库结构（Repository Structure）

- **Monorepo**（推荐 Turborepo / Nx）
  - `apps/gateway` — 网关核心（Go）
  - `apps/console` — Web 控制台（Next.js）
  - `apps/docs` — 文档站（Docusaurus / Mintlify）
  - `apps/admin` — 内部管理后台（Next.js）
  - `packages/sdk-python` — Python SDK
  - `packages/sdk-typescript` — TypeScript SDK
  - `packages/sdk-go` — Go SDK
  - `packages/adapters/*` — 各模型适配器
  - `packages/proto` — 共享类型与协议
  - `infra/` — Terraform / Helm / K8s manifests

## 4.2 服务架构（Service Architecture）

- **网关层**: 无状态 Go 服务，K8s 横向扩展
- **账户/计费/监控/Key 管理**: 独立服务，gRPC 内部通信
- **模型适配器**: 每家一个独立 deployment，便于独立发版与扩缩
- **数据流**: API 请求 → API Gateway (Go) → Adapter Service → 上游 LLM；账单/日志通过 Kafka 异步消费 → ClickHouse / Postgres 持久化

## 4.3 测试策略（Testing Strategy）

- **单元测试**: 覆盖率 ≥ 70%（业务逻辑模块），关键路径 ≥ 90%
- **集成测试**: 适配器 vs 真实上游（Mock 不替代）；契约测试（OpenAI 协议兼容性）
- **E2E**: Playwright（关键控制台流程）+ k6（API 压测）
- **混沌测试**: 注入上游 timeout / error，验证 failover 行为
- **CI**: GitHub Actions / GitLab CI；PR 强制跑 lint + unit + integration

## 4.4 部署与基础设施

- **云**: 阿里云 / 腾讯云 / 华为云境内 VPC（满足备案要求）
- **容器**: Docker；K8s + Helm
- **CI/CD**: ArgoCD / Flux（GitOps）
- **可观测**: OpenTelemetry + Prometheus + Loki + Grafana
- **CDN**: Cloudflare 仅做 TLS 终端 + 静态资产分发，**不缓存 API 请求/响应内容**（合规边界）
- **DNS**: 国际域 `he-api.com`；地区子域 `us.he-api.com` / `eu.he-api.com`（V1.1 多 region）

## 4.5 数据存储

- **PostgreSQL**: 用户、订单、Key、订阅、配额（OLTP）
- **Redis**: 限流计数、缓存、Session（in-memory）
- **ClickHouse / TimescaleDB**: 请求日志、用量聚合（OLAP）
- **对象存储**（OSS/COS）: 用户数据导出包、PDF 账单

## 4.6 第三方集成

- **OAuth**: Google / GitHub / Microsoft
- **邮件**: SendGrid（海外）/ 阿里云邮推（国内备份）
- **支付**: Stripe / PayPal / Coinbase Commerce / 支付宝开放平台 / 微信支付商户平台
- **客服**: Intercom 或 Crisp（多语言聊天）
- **监控告警**: PagerDuty / 飞书机器人 / Slack
- **分析**: PostHog（产品分析）+ Plausible（隐私友好的网站分析）

---
