# 6. 源代码组织（Source Tree）

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
│   ├── sample-otel-app/                Epic 1 reference impl (OTel SDK / Prom exporter / 结构化 JSON 日志 教科书示例) [非生产服务]
│   ├── sample-grpc-app/                Epic 1 gRPC reference impl (Story 1.5 — connect-go + Buf 教科书示例) [非生产服务]
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
│   │   ├── he/<domain>/v1/<service>.proto    (Buf module 主路径, domain-based versioning)
│   │   ├── gen/go/he/<domain>/v1/*.pb.go     (vendored generated Go code, Buf 生成)
│   │   ├── buf.yaml
│   │   └── buf.gen.yaml
│   ├── go-observability/                Go observability 共享包 (Story 1.5 — TracerProvider / slog JSON / otelhttp wrap)
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

> **注**: `apps/sample-otel-app/` 是 **Epic 1 reference impl**（非生产服务）— 由 Story 1.4 引入，作为 Epic 2-10 业务服务集成 OTel SDK + Prometheus exporter + 结构化 JSON 日志的"教科书示例"。其 `/hello` 与 `/metrics` 端点不进 API registry。条目添加来源：Story 1.4 Architect Round 1 minor m-1，2026-05-11。

> **注**: `apps/sample-grpc-app/` 是 **Epic 1 gRPC reference impl**（非生产服务）— 由 Story 1.5 引入，作为 Epic 2-10 业务 gRPC 服务集成 connect-go + Buf + observability 共享包的"教科书示例"，与 `sample-otel-app` 形成 HTTP + gRPC 双 reference 对位。其 Ping RPC 不进 API registry。条目添加来源：Story 1.5 Architect Round 1 minor m-2，2026-05-11。

> **注**: `packages/proto/he/<domain>/v1/<service>.proto` proto 布局（Story 1.5 Architect Q4 ruling，2026-05-11）— 由 `he/api/v1/*.proto` 占位符细化为 domain-based `he/<domain>/v1/<service>.proto`（Buf-recommended）。生成的 Go 代码 vendored 至 `packages/proto/gen/go/he/<domain>/v1/*.pb.go`（commit 入库；CI `buf generate` + `git diff --exit-code packages/proto/gen/` 检测 drift）。条目修订来源：Story 1.5 Architect Round 1 minor m-1，2026-05-11。

> **注**: `packages/go-observability/` 是 Story 1.5 引入的 Go observability 共享包（OTel TracerProvider + slog JSON handler + otelhttp wrap），统一 Epic 2-10 全部 14 个 Go 服务的 OTel/Prom/log 接入。`apps/sample-otel-app/cmd/server/main.go` 重构以消费此包列为 post-1.5 followup（Epic 2 Story 2.1 auth-svc 落地时同期完成）。条目添加来源：Story 1.5 Architect Round 1 Q5 ruling，2026-05-11。

> **注**: Story 1.6 — 数据库基础新增条目：
> - `infra/terraform/modules/rds-postgres/` — Aliyun RDS PostgreSQL 16 Terraform 模块（Q4 ruling：三独立 modules）。
> - `infra/terraform/modules/redis-tair/` — Aliyun Tair (Redis 7.2 兼容) Terraform 模块（Q4 ruling）。
> - `infra/terraform/modules/clickhouse/` — Aliyun ClickHouse 24+ Terraform 模块（Q4 ruling）。
> - `migrations/postgres/` — Atlas versioned-mode migration directory（Q1 ruling）；包含 `atlas.hcl` + `0001_baseline.sql` + `atlas.sum`。
> - `migrations/clickhouse/` — golang-migrate paired up/down migration directory（Q2 ruling）；包含 `001_baseline.up.sql` + `001_baseline.down.sql`（m-2 空 stub）。
> - `scripts/db-doctor/probes/` — SQL/shell probe 单一可信源（Q5 ruling），由 `scripts/db-doctor.sh` 与 `infra/helm/db-doctor/templates/configmap.yaml` 共享引用，零查询重复。
> - `infra/helm/db-doctor/` — DB Doctor Helm CronJob chart（Q5 ruling cluster path + M-2 ruling: direct helm install, NOT ArgoCD）。
> - `scripts/db-migrate.sh` — 统一 migration 入口（up/down/status/diff）；委托 Atlas + golang-migrate 二进制。
> - `scripts/db-doctor.sh` — 本地 DB 自检脚本；5 分钟 SLO；exit 0/1/2 per BR-4.1。
> - `docs/architecture/database-bootstrap.md` — 6-section operator 指南（m-4 ruling: Topology / Migration Workflow / Credentials & Vault Migration Path / Capacity / Operator Runbook / Decision Lineage）。
>
> 条目添加来源：Story 1.6 Architect Round 1 (Q1-Q5 + M-1/M-2 + m-1..m-5)，2026-05-11。

> **注**: Story 2.1 — `apps/console/messages/` 采用 namespace-split：`messages/{locale}/{namespace}.json`。本 Story 落地 `common.json` × 10 locale（`en/zh-CN/ja/ko/es/fr/de/pt/ru/ar` MVP set）；后续 Story 按需新增 namespace（`auth.json` / `billing.json` / `dashboard.json` / ...）。`packages/i18n-keys/src/` 通过 `scripts/gen-i18n-keys.ts` 从 `messages/en/*.json` 自动生成 union literal types（每个 namespace 一个 `{Namespace}Keys`），CI `console / i18n-keys-completeness` job 跑 `git diff --exit-code packages/i18n-keys/src/` 做 drift 检测 + `scripts/check-i18n-keys.ts` 做 key 集完整性 + ICU plural 分支检测。`apps/console/` Next.js 14 App Router 脚手架（Story 2.1 落地）暴露 `[locale]` 段路由 + `middleware.ts` next-intl cookie-first 协商 + `lib/i18n.ts` (isRtlLocale/resolveLocale/buildLocaleCookieOptions) + `components/LocaleSwitch.tsx`。条目添加来源：Story 2.1 Architect Round 1 Q2 ruling + m-1，2026-05-12。

---
