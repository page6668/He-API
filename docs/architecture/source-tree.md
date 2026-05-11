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
│   │   ├── he/api/v1/*.proto
│   │   └── buf.yaml
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

---
