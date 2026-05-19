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

> **注**: Story 4.1 — DeepSeek 适配器（Epic 4 首个真实模型适配器）新增条目：
> - `apps/adapters/deepseek/` — DeepSeek adapter K8s service（Go Connect-RPC server, HTTP/2-forced upstream client per OQ7, strict-RFC SSE decoder per OQ6, BR-3.3 token-usage Normaliser）。模板沿用 Story-3.1 api-gateway Dockerfile multi-stage build pattern；后续 Stories 4.2-4.6 在 `apps/adapters/<vendor>/` 复用此模板。
> - `apps/adapters/deepseek/internal/upstream/` — HTTPS client + translate + sse_decoder + wire-shape types。SSE decoder 严格 RFC 模式（OQ6 Architect Round 2 ratified），CRLF / 缺少 space after `data:` / 非 `data:` 前缀均视为 `ErrMalformedFrame`（no silent coercion）。
> - `apps/adapters/deepseek/internal/usage/` — `Normaliser` interface（BR-3.2）+ DeepSeek identity-impl + `ErrUsageConstraintViolation`。Stories 4.2-4.6 在 sibling 包内提供 per-vendor normaliser。
> - `apps/api-gateway/internal/adapterclient/` — gateway-side model-id → adapter-endpoint resolver（OQ4 ratified seam: in-process map + K8s DNS resolution）。`Registry.Resolve(modelID) → (ClientHandle, ok)` is the abstraction boundary Epic 6 routing-svc grafts onto。
> - `apps/api-gateway/internal/streaming/adapter_chunker.go` — sister to Story-3.4 MockChunker，consumes adapter Connect-RPC server-streaming chunks，emits OpenAI-compatible `data: <json>\n\n` SSE 流；tail-usage chunk 在 `data: [DONE]` 之前发出（BR-2.4 / BR-3.7）。
> - `apps/api-gateway/internal/streaming.Writer.HeadersFlushed() bool` — 新 Writer 方法（BR-2.5 emit-before-flush boundary）。
> - `packages/proto/he/adapter/v1/adapter.proto` — `he.adapter.v1.AdapterService.Chat(ChatRequest) returns (stream ChatChunk)` 服务端流式 RPC（OQ1 + OQ2 ratified）。Buf-generated Go stubs vendored 至 `packages/proto/gen/go/he/adapter/v1/`。Stories 4.2-4.6 inherit the proto verbatim。
> - `packages/go-observability/requestid/` — OQ8 lift: cross-service `he_request_id` accessor（`HeaderName` / `SpanAttributeKey` / `FromContext` / `WithRequestID` / `ContextWith`）。Gateway middleware `apps/api-gateway/internal/middleware/requestid` 转为薄 re-export shim；后续 housekeeping story 删除 shim。
> - `infra/helm/adapter-deepseek/` — Helm chart（Chart + values + deployment + service + serviceaccount + configmap + externalsecret templates）。Namespace `he-api-adapters`（M4 ratified — 六家 adapter 共享 namespace）。Cold-start budget ≤ 2s（M5 ratified — readiness-probe-gated rollout absorbs adapter start time; api-gateway 1s budget unchanged）。
> - `infra/argocd/applications/adapter-deepseek.yaml` — ArgoCD Application manifest。
> - `scripts/dev/seed-deepseek-key.sh` — dev-mode bootstrap script（gated behind `.env.local` presence — CI never touches it）。
> - `docs/dev/secrets/deepseek-upstream.md` — Vault credential runbook（OQ3 ratified path `kv/data/he-api/upstream/deepseek/`，manual rotation — DeepSeek 不支持 API-key auto-rotation）。沿用 Story-1.6 m-4 6-section template（Topology / Migration Workflow / Credentials & Vault Migration Path / Capacity / Operator Runbook / Decision Lineage）。
>
> 条目添加来源：Story 4.1 Architect Round 2（OQ1-OQ8 + M1-M5 + m1-m4），2026-05-19。

> **注**: Story 4.2 — Qwen（通义千问）适配器（Epic 4 第二个真实模型适配器）新增条目：
> - `apps/adapters/qwen/` — Qwen adapter K8s service（Go Connect-RPC server, HTTP/2-preferred upstream client per OQ-4.2-5（`http.Transport{ForceAttemptHTTP2: true}` + ALPN HTTP/1.1 fallback — Qwen-specific divergence from Story-4.1 OQ7 forced-HTTP/2 to accommodate Aliyun gateway variability）, strict-RFC SSE decoder per OQ6（REUSE Story-4.1 pattern verbatim per Round 1 L1 simplification — DashScope compat-mode SSE matches OpenAI byte-for-byte）, identity-mapping `qwenNormaliser` per OQ-4.2-3 cascade）。
> - `apps/adapters/qwen/internal/upstream/` — HTTPS client + translate + sse_decoder + wire-shape types + errors (per-vendor classifier replica per Round 1 L2 ratification — `internal/` packages not cross-importable across `apps/adapters/<vendor>/` module boundaries; acceptable duplication)。BR-4.4 addition: `ErrorKindRateLimitThrottle` replaces DeepSeek's `quota_exhausted` for upstream 429 (Round 1 OQ-4.2-4 ratification — distinct slog disambiguation for oncall paging).
> - `apps/adapters/qwen/internal/usage/` — Qwen identity-mapping Normaliser implementation consuming the lifted `packages/adapter-usage` types (Round 1 OQ-4.2-3a partial-lift ratification — `NormalisedUsage` + `ErrUsageConstraintViolation` + `ValidateInvariants` lifted; the `Normaliser` interface stays vendor-local).
> - `packages/adapter-usage/` — **NEW top-level Go module** (sibling to `packages/go-observability/`). Hosts `NormalisedUsage` struct + `ErrUsageConstraintViolation` sentinel + `ValidateInvariants(prompt, completion, total) error` helper. Story-4.1 `apps/adapters/deepseek/internal/usage/normaliser.go` is back-compat-retrofit (type-aliases + variable re-exports preserve existing call-sites unchanged). Stories 4.3-4.6 inherit this shared shape.
> - `infra/helm/adapter-qwen/` — Helm chart copy-paste from `infra/helm/adapter-deepseek/` with Qwen swaps (image name, env var names including `QWEN_UPSTREAM_API_KEY` per Round 1 OQ-4.2-6 model-family naming, `supportedModels` Helm value listing `[qwen-max, qwen-plus]` per BR-1.10 single-service-per-vendor + Round 1 OQ-4.2-2 ratification). Namespace `he-api-adapters` (M4 inheritance). Cold-start budget ≤ 2s (M5 inheritance).
> - `infra/argocd/applications/adapter-qwen.yaml` — ArgoCD Application manifest.
> - `scripts/dev/seed-qwen-key.sh` — dev-mode bootstrap script (gated behind `.env.local` presence — CI never touches it).
> - `docs/dev/secrets/qwen-upstream.md` — Vault credential runbook (Round 1 OQ3 inheritance ratified path `kv/data/he-api/upstream/qwen/`, manual rotation — DashScope 不支持 API-key auto-rotation). 沿用 Story-1.6 m-4 6-section template.
> - `apps/api-gateway/internal/adapterclient/registry.go` (MODIFIED): NEW constants `QwenMaxModelID = "qwen-max"`, `QwenPlusModelID = "qwen-plus"`, `QwenAdapterEndpointEnv = "QWEN_ADAPTER_ENDPOINT"` per Round 1 OQ-4.2-6. `LoadFromEnv()` EXTENDED to read `QWEN_ADAPTER_ENDPOINT` and populate BOTH model-id entries. `NewRegistry` REFACTORED per Round 1 M2 endpoint-dedup — model ids sharing one endpoint URL share one underlying `connectClientHandle` (preserves Story-4.1 single-endpoint behaviour bit-for-bit).
>
> 条目添加来源：Story 4.2 Architect Round 1（OQ-4.2-1..6 + M1-M2 + m1 + L1-L2），2026-05-19。

---
