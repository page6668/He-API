# 2. 技术栈（Tech Stack）

## 2.1 完整技术清单

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
| **网关框架** | Go stdlib net/http + connectrpc/connect | stdlib (Go 1.22+) / connectrpc 1.16+ | HTTP server (stdlib) + gRPC over HTTP/2 |
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
| **测试 - 网关合约** | pytest + openai SDK + httpx | 8.* / 1.40.* / 0.27.* | OpenAI Python SDK 合约测试 + 原始 HTTP 公共端点测试 (Story 3.3 引入，Story 4.8 扩展为跨厂商防回归矩阵) |
| **压测** | k6 | 0.50+ | 性能基准 |
| **API 文档** | Mintlify 或 Docusaurus | - | docs.he-api.com |

## 2.2 版本选择原则

- **稳定优先**: 选择 LTS 或最近 stable 大版本（如 PostgreSQL 16，避开刚发布的 17）
- **生态成熟**: 优先选择社区活跃、Stack Overflow 答案多的版本
- **安全更新**: 至少 24 个月内仍接收安全补丁

## 2.3 Change Log

| Date | Story | Change |
|------|-------|--------|
| 2026-05-18 | Story 3.1 (SM Phil) | §2.1 row "网关框架" rewritten from "Fiber 2.52+ (基于 fasthttp)" to "Go stdlib net/http + connectrpc/connect (Go 1.22+ / connectrpc 1.16+)" — ratifies the de-facto stack shipped by Stories 2.2 – 2.6. Original choice preserved under "Original (deprecated 2026-05-18, Story 3.1 ratification)" in `high-level-architecture.md §1.3.1 ADR-2 history`. |
| 2026-05-20 | Story 4.8 (SM Phil + Dev Linus) | §2.1 adds row "测试 - 网关合约" (Python toolchain `pytest + openai + httpx` 8.* / 1.40.* / 0.27.*) — closes the pre-existing Story 3.3 documentation oversight (per LOW-2 Architect Round 1) so the OpenAI Python SDK contract-test toolchain is discoverable from the tech-stack canonical reference. |
| 2026-06-09 | Story 7.3 (Dev / Linus) | §2.1 row "支付集成" — Stripe + PayPal channels REALISED in `apps/payment-svc` (first 2 of the 5 documented channels). **Implementation note**: per Architect Q-SDK, the integrations are built on the platform's stdlib (`crypto/hmac` for the Stripe-Signature HMAC-SHA256 scheme — functionally identical to `stripe-go`'s `webhook.ConstructEvent`; `net/http` thin REST clients for checkout/subscription create + PayPal `verify-webhook-signature`) behind the pluggable `PaymentProvider` seam, rather than vendoring the Stripe/PayPal SDKs. Rationale: zero third-party-SDK dependency keeps payment-svc's `go.mod` minimal + offline-testable + free of the workspace otel-pin churn ([[project_otel_version_pin_gotcha]]); the seam isolates a future `stripe-go` swap with no caller changes. PayPal's official Go SDK is unmaintained (Architect ruling). USDC / Alipay+ / WeChat (7.4-7.6) plug into the same seam. |
| 2026-06-10 | Story 7.4 (Dev / Linus) | §2.1 row "支付集成" — **USDC（Coinbase Commerce）channel REALISED** (3rd of the 5 channels; the FIRST to prove the `PaymentProvider` seam extends without seam/handler changes — provider.go:2-6). Thin `net/http` REST client (charge create) + stdlib `crypto/hmac` `X-CC-Webhook-Signature` verify — no third-party SDK (mirrors the PayPal decision, Q-SDK). ⚠️ Two new primitives vs the fiat spine: the Coinbase signature has **NO timestamp** (replay defence is the inherited `recharge_orders` state-machine alone, BR-W-4) and credit fires only on `charge:confirmed` (crypto on-chain finality, BR-C-1). NO new service / proto / migration / Kafka topic / endpoint shape — the producer, credit applier, `recharge_orders`, and envelope codes are REUSED VERBATIM. |

---
