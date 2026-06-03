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

> **注**: Story 4.3 — Kimi（Moonshot AI）适配器（Epic 4 第三个真实模型适配器；THREE-model-id-per-vendor 拓扑）新增条目：
>
> - `apps/adapters/kimi/` — Connect-RPC adapter service hosting ALL THREE `moonshot-v1-8k` + `moonshot-v1-32k` + `moonshot-v1-128k` model ids per BR-1.10 multi-model-id-per-service dispatch extended to N=3 (Story-4.2 OQ-4.2-2 cascade ratification). Sibling of `apps/adapters/deepseek/` and `apps/adapters/qwen/`. The directory was pre-allocated since the pre-Epic-4 source-tree pass.
>   - `cmd/server/main.go` — Connect-RPC bootstrap; `defaultBoundModelIDs = []string{"moonshot-v1-8k","moonshot-v1-32k","moonshot-v1-128k"}` per BR-1.10.
>   - `internal/adapter.go` — `AdapterServiceHandler` (copy from qwen template; identical except per-vendor `errors.go` reference and m-1 body-aware classifier wire-up in the HTTP-error path).
>   - `internal/upstream/{client,translate,sse_decoder,types}.go` — REUSE Story-4.2 qwen patterns byte-for-byte (per Story-4.2 L1 SSE-decoder simplification + L2 per-vendor types replica policy).
>   - `internal/upstream/errors.go` — per-vendor replica (Story-4.2 L2). Adds `ErrorKindContextLengthExceeded` (BR-4.5 NEW) + new helper `ClassifyMoonshotErrorBody(status, body) ErrorKind` per Architect Round 1 m-1 (body-aware classification for the Moonshot 400 invalid_request_error shape — status alone cannot disambiguate context-length errors from generic 400s).
>   - `internal/usage/normaliser.go` — identity-mapping `kimiNormaliser` consuming the lifted `packages/adapter-usage` types (NormalisedUsage, ErrUsageConstraintViolation, ValidateInvariants) per Story-4.2 M1 cascade.
>   - `Dockerfile` — multi-stage build copy from `apps/adapters/qwen/Dockerfile`.
>   - `tests/{adapter_test.go,chaos_test.go}` — 3-model-parametrised integration suite + Toxiproxy-style chaos suite (build-tag `chaos`); ADDS Kimi-specific CHAOS-007 (Moonshot 429 rate-limit) + CHAOS-008 (NEW Moonshot 400 context-length-exceeded body classification per BR-4.5).
> - `infra/helm/adapter-kimi/` — Helm chart copy-paste from `infra/helm/adapter-qwen/` with Kimi swaps (image name, env var names including `KIMI_UPSTREAM_API_KEY` per Architect Round 1 OQ-4.3-2 BRAND-name naming, `supportedModels` Helm value listing all three `moonshot-v1-*` sizes per BR-1.10 multi-model-id-per-service N=3). Namespace `he-api-adapters` (M4 inheritance). Cold-start budget ≤ 2s (M5 inheritance).
> - `infra/argocd/applications/adapter-kimi.yaml` — ArgoCD Application manifest.
> - `scripts/dev/seed-kimi-key.sh` — dev-mode bootstrap script (gated behind `.env.local` presence — CI never touches it).
> - `docs/dev/secrets/kimi-upstream.md` — Vault credential runbook (Story-4.1 OQ3 cascade ratified path `kv/data/he-api/upstream/kimi/`, manual rotation — Moonshot does not support API-key auto-rotation). 沿用 Story-1.6 m-4 6-section template.
> - `apps/api-gateway/internal/adapterclient/registry.go` (MODIFIED): NEW constants `KimiV18kModelID = "moonshot-v1-8k"`, `KimiV132kModelID = "moonshot-v1-32k"`, `KimiV1128kModelID = "moonshot-v1-128k"`, `KimiAdapterEndpointEnv = "KIMI_ADAPTER_ENDPOINT"` per Architect Round 1 OQ-4.3-2 (BRAND-name naming). `LoadFromEnv()` EXTENDED to read `KIMI_ADAPTER_ENDPOINT` and populate ALL THREE model-id entries. The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is UNCHANGED and verified to scale monomorphically from N=2 to N=3 via 4.3-UNIT-011 `assert.Same(h_8k, h_32k); assert.Same(h_32k, h_128k)` chain (Architect Round 1 OQ-4.3-5 policy).
>
> 条目添加来源：Story 4.3 Architect Round 1（OQ-4.3-1..6 + m-1 + m-2，cascades from Story-4.2），2026-05-19。

> **注**: Story 4.4 — GLM（智谱）适配器（Epic 4 第四个真实模型适配器；SINGLE-model-id-per-vendor N=1 degenerate case — relaxes back to Story-4.1 pattern while the Story-4.2 M2 endpoint-dedup branch is a clean no-op at N=1）新增条目：
>
> - `apps/adapters/glm/` — Connect-RPC adapter service hosting ONLY `glm-4` per BR-1.10 N=1 degenerate case (Story-4.2 OQ-4.2-2 multi-model-id pattern collapsed back to Story-4.1 single-id pattern; future GLM sizes would extend `boundModelIDs` without code changes). Sibling of `apps/adapters/deepseek/`, `apps/adapters/qwen/`, and `apps/adapters/kimi/`. The directory was pre-allocated since the pre-Epic-4 source-tree pass.
>   - `cmd/server/main.go` — Connect-RPC bootstrap; `defaultBoundModelIDs = []string{"glm-4"}` per BR-1.10.
>   - `internal/adapter.go` — `AdapterServiceHandler` (copy from kimi template; identical except status-only classification on the HTTP-error path — Story-4.3 m-1 body-aware classifier is NOT cascaded per Architect Round 1 OQ-4.4-6 verbatim REUSE).
>   - `internal/upstream/{client,translate,sse_decoder,types}.go` — REUSE Story-4.3 kimi patterns byte-for-byte (per Story-4.2 L1 SSE-decoder simplification + L2 per-vendor types replica policy). URL path `/api/paas/v4/chat/completions` per OQ-4.4-1.
>   - `internal/upstream/errors.go` — per-vendor replica (Story-4.2 L2). REUSE Story-4.2 enum set verbatim — Story-4.3 `ErrorKindContextLengthExceeded` + `ClassifyMoonshotErrorBody` are NOT cascaded per OQ-4.4-6. Additive entries permitted without Architect Round 2 should Zhipu-specific failure shapes surface (Story-4.3 m-1 precedent).
>   - `internal/usage/normaliser.go` — identity-mapping `glmNormaliser` consuming the lifted `packages/adapter-usage` types (NormalisedUsage, ErrUsageConstraintViolation, ValidateInvariants) per Story-4.2 M1 cascade.
>   - `Dockerfile` — multi-stage build copy from `apps/adapters/kimi/Dockerfile`.
>   - `tests/{adapter_test.go,chaos_test.go}` — N=1 integration suite + Toxiproxy-style chaos suite (build-tag `chaos`); seven CHAOS scenarios (5xx burst / slow loris / mid-stream RST / full timeout / TLS / DNS / Zhipu 429 rate-limit). NO CHAOS-008 — Story-4.3 body-aware context-length test is NOT cascaded per OQ-4.4-6.
> - `infra/helm/adapter-glm/` — Helm chart copy-paste from `infra/helm/adapter-kimi/` with GLM swaps (image name, env var names including `GLM_UPSTREAM_API_KEY` per Architect Round 1 OQ-4.4-2 BRAND-name naming cascade, `supportedModels: [glm-4]` Helm value per BR-1.10 N=1). Namespace `he-api-adapters` (M4 inheritance). Cold-start budget ≤ 2s (M5 inheritance).
> - `infra/argocd/applications/adapter-glm.yaml` — ArgoCD Application manifest.
> - `scripts/dev/seed-glm-key.sh` — dev-mode bootstrap script (gated behind `.env.local` presence — CI never touches it).
> - `docs/dev/secrets/glm-upstream.md` — Vault credential runbook (Story-4.1 OQ3 cascade ratified path `kv/data/he-api/upstream/glm/`, manual rotation — Zhipu does not support API-key auto-rotation). 沿用 Story-1.6 m-4 6-section template.
> - `apps/api-gateway/internal/adapterclient/registry.go` (MODIFIED): NEW constants `GLMModelID = "glm-4"`, `GLMAdapterEndpointEnv = "GLM_ADAPTER_ENDPOINT"` per Architect Round 1 OQ-4.4-2 (BRAND-name naming per Story-4.2 OQ-4.2-6 cascade). `LoadFromEnv()` EXTENDED to read `GLM_ADAPTER_ENDPOINT` and populate the SINGLE `glm-4` entry. The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is UNCHANGED — at N=1 the byEndpoint map has one entry and the dedup branch is a clean NO-OP (4.4-UNIT-011 SKIPPED-branch documentation test per Architect Round 1 R9 ratification).
>
> 条目添加来源：Story 4.4 Architect Round 1（OQ-4.4-1..6 + cascade-locked confirmations，cascades from Stories 4.2/4.3），2026-05-19。

> **注**: Story 4.5 — Doubao（豆包 / Volcengine Ark v3）适配器（Epic 4 第五个真实模型适配器；BR-1.10 N=2 RESTORATION 回到 Story-4.2 qwen 模式；FIRST Epic-4 non-identity translate — 引入 `model` 字段双向 rewrite 机制打破 OQ-4.2-3 identity-Normaliser cascade）新增条目：
>
> - `apps/adapters/doubao/` — Connect-RPC adapter service hosting BOTH `doubao-pro` AND `doubao-lite` per BR-1.10 N=2 case (RESTORES Story-4.2 qwen pattern after Story-4.4's N=1 detour; M2 endpoint-dedup branch returns to actually-executing form). Sibling of `apps/adapters/deepseek/`, `apps/adapters/qwen/`, `apps/adapters/kimi/`, and `apps/adapters/glm/`. The directory was pre-allocated since the pre-Epic-4 source-tree pass.
>   - `cmd/server/main.go` — Connect-RPC bootstrap; `defaultBoundModelIDs = []string{"doubao-pro", "doubao-lite"}` per BR-1.10 N=2. PLUS startup-validation slog (`event=adapter_startup_validation`) for the NEW `DOUBAO_PRO_ENDPOINT_ID` + `DOUBAO_LITE_ENDPOINT_ID` env vars per Architect Round 1 OQ-4.5-4 rollover note (helps catch ConfigMap-edit-without-pod-restart drift).
>   - `internal/adapter.go` — `AdapterServiceHandler` (copy from glm template) PLUS Story-4.5-specific additions: (a) injected `*upstream.EndpointMap` per Architect Round 1 m-1; (b) BR-1.12 fail-fast probe at the top of `ChatInto` (short-circuits with `connect.CodeFailedPrecondition` BEFORE the upstream HTTPS call if `endpoint_map.Lookup` returns `ErrUnsupportedModel`); (c) BR-1.11 per-request closure capture of `req.Model` for inbound back-translate (NO `ReverseLookup` per Architect Round 1 l-1); (d) `connectCodeForKind` mapper EXTENDED with `ErrorKindEndpointIDNotConfigured → CodeFailedPrecondition` case.
>   - `internal/upstream/{client,sse_decoder,types}.go` — REUSE Story-4.4 glm patterns byte-for-byte (per Story-4.2 L1 SSE-decoder simplification + L2 per-vendor types replica policy). URL path `/api/v3/chat/completions` per OQ-4.5-1.
>   - `internal/upstream/translate.go` — NEW `TranslateChatRequest` (outbound rewrite friendly id → endpoint id per BR-1.7.f + BR-3.8a), `TranslateChatResponse` (inbound non-streaming rewrite endpoint id → friendly id per BR-1.11 + BR-3.8b), `TranslateChatChunk` (inbound streaming per-chunk rewrite per BR-1.11). Identity-mapping cascade per OQ-4.5-3 for ALL non-`model` fields.
>   - `internal/upstream/endpoint_map.go` — **NEW Story-4.5-specific module**: friendly-id → Volcengine endpoint-id static lookup (`Lookup`, `ErrUnsupportedModel`). Test-injectable `New(envProvider)` constructor per Architect Round 1 m-1 refactor (replaces the brittle package-level `var x = map{...os.Getenv}` pattern). Env-var-sourced from ConfigMap `doubao-endpoint-ids` per OQ-4.5-4. NO `ReverseLookup` per Architect Round 1 l-1 (closure pattern is rotation-safe).
>   - `internal/upstream/errors.go` — per-vendor replica (Story-4.2 L2). REUSE Story-4.4 enum set verbatim per OQ-4.5-6 — Story-4.3 body-aware classifier NOT cascaded. NEW `ErrorKindEndpointIDNotConfigured` per BR-4.6 — surfaced via `ClassifyError` when err wraps `ErrUnsupportedModel`, NOT from an HTTP status code. Additive entries permitted without Architect Round 2 should Volcengine-specific failure shapes surface (Story-4.3 m-1 precedent).
>   - `internal/usage/normaliser.go` — identity-mapping `doubaoNormaliser` consuming the lifted `packages/adapter-usage` types (NormalisedUsage, ErrUsageConstraintViolation, ValidateInvariants) per Story-4.2 M1 cascade.
>   - `Dockerfile` — multi-stage build copy from `apps/adapters/glm/Dockerfile`.
>   - `tests/{adapter_test.go,chaos_test.go}` — 2-model-parametrised integration suite (BR-1.11 round-trip rewrite verified at every cell; 20-cell oracle invariant test) + Toxiproxy-style chaos suite (build-tag `chaos`); EIGHT CHAOS scenarios (5xx burst / slow loris / mid-stream RST / full timeout / TLS / DNS / Volcengine 429 rate-limit / **NEW CHAOS-008 endpoint-id-not-configured fail-fast** per BR-1.12 + BR-4.6 — spawn adapter with env vars unset, verify CodeFailedPrecondition + upstream HTTPS NEVER initiated).
> - `infra/helm/adapter-doubao/` — Helm chart copy-paste from `infra/helm/adapter-glm/` with Doubao swaps (image name, env var names including `DOUBAO_UPSTREAM_API_KEY` per Architect Round 1 OQ-4.5-2 BRAND-name naming cascade, `supportedModels: [doubao-pro, doubao-lite]` Helm value per BR-1.10 N=2). NEW: also mounts ConfigMap `doubao-endpoint-ids` via `envFrom: configMapRef` per OQ-4.5-4 (endpoint ids identify resources, not authorise access — API key remains in Vault). Namespace `he-api-adapters` (M4 inheritance). Cold-start budget ≤ 2s (M5 inheritance).
> - `infra/helm/adapter-doubao/templates/configmap.yaml` — NEW Story-4.5 ConfigMap `doubao-endpoint-ids` declaring `DOUBAO_PRO_ENDPOINT_ID` + `DOUBAO_LITE_ENDPOINT_ID` keys (values templated from `values.yaml` `.endpointIDs.pro` + `.endpointIDs.lite`).
> - `infra/argocd/applications/adapter-doubao.yaml` — ArgoCD Application manifest.
> - `scripts/dev/seed-doubao-key.sh` — dev-mode bootstrap script (gated behind `.env.local` presence — CI never touches it); seeds API key + BOTH endpoint ids (BR-1.12 fail-fast trigger if any are missing).
> - `docs/dev/secrets/doubao-upstream.md` — Vault credential runbook (Story-4.1 OQ3 cascade ratified path `kv/data/he-api/upstream/doubao/`, manual rotation — Volcengine does not support API-key auto-rotation) PLUS NEW "Endpoint ID Configuration" section documenting the ConfigMap pattern + OQ-4.5-4 ratification rationale + endpoint-id rotation runbook (ConfigMap edit + `kubectl rollout restart`). 沿用 Story-1.6 m-4 6-section template.
> - `apps/api-gateway/internal/adapterclient/registry.go` (MODIFIED): NEW constants `DoubaoProModelID = "doubao-pro"`, `DoubaoLiteModelID = "doubao-lite"`, `DoubaoAdapterEndpointEnv = "DOUBAO_ADAPTER_ENDPOINT"` per Architect Round 1 OQ-4.5-2 (BRAND-name naming per Story-4.2 OQ-4.2-6 cascade — brand wins over platform `volcengine` + company `bytedance`). `LoadFromEnv()` EXTENDED to read `DOUBAO_ADAPTER_ENDPOINT` and populate BOTH `doubao-pro` AND `doubao-lite` entries. The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is UNCHANGED — at N=2 (RESTORATION) the byEndpoint map dedups both entries to ONE underlying `ClientHandle` via 4.5-UNIT-013 `assert.Same(h_pro, h_lite)` chain (REUSE Story-4.2 4.2-UNIT-013 pattern verbatim per OQ-4.3-5 cascade-locked policy).
>
> 条目添加来源：Story 4.5 Architect Round 1（OQ-4.5-1..6 + cascade-locked confirmations + m-1/l-1/m-2 refinements，cascades from Stories 4.2/4.3/4.4），2026-05-19。

> **注**: Story 4.6 — Ernie（文心 / Baidu Qianfan v2 OpenAI-compat）适配器（Epic 4 第六个且最后一个真实模型适配器；关闭 six-vendor cross-vendor regression matrix；BR-1.10 N=1 RESTORATION 回到 Story-4.4 glm 模式；架构 `models-registry.md §4.5 Promotion rule` URL-path-segment hint 被 Architect Round 1 OQ-4.6-1 option (a) Qianfan v2 OpenAI-compat ruling 取代，标记为 HISTORICAL — Qianfan v2 GA 发生在原架构 pass 之后，旧 hint 已过时）新增条目：
>
> - `apps/adapters/ernie/` — Connect-RPC adapter service hosting ONLY `ernie-4.0` per BR-1.10 N=1 degenerate case (RESTORES Story-4.4 glm pattern after Story-4.5's N=2 RESTORATION; M2 endpoint-dedup branch is a clean no-op at N=1 — 4.6-UNIT-011 SKIPPED-branch documentation test per Story-4.4 R9 ratification cascade). Sibling of `apps/adapters/deepseek/`, `apps/adapters/qwen/`, `apps/adapters/kimi/`, `apps/adapters/glm/`, and `apps/adapters/doubao/`. The directory was pre-allocated since the pre-Epic-4 source-tree pass.
>   - `cmd/server/main.go` — Connect-RPC bootstrap; `defaultBoundModelIDs = []string{"ernie-4.0"}` per BR-1.10. Default base URL `https://qianfan.baidubce.com` per OQ-4.6-1 option (a) ratification.
>   - `internal/adapter.go` — `AdapterServiceHandler` (copy from glm template — byte-for-byte with `glm`→`ernie` substitution + URL path `/v2/chat/completions` per OQ-4.6-1). NO Story-4.5-specific additions (no `EndpointMap`, no BR-1.11 back-translate, no BR-1.12 fail-fast — all option-(b)-scoped and dropped per OQ-4.6-1 option (a) ratification). `upstreamRequestIDHeader = "X-Bce-Request-Id"` constant (SM lean per m-2; pending Phase 0 empirical-verify at first staging deploy).
>   - `internal/upstream/{client,sse_decoder,translate,types,errors}.go` — REUSE Story-4.4 glm patterns byte-for-byte (per Story-4.2 L1 SSE-decoder simplification + L2 per-vendor types replica policy). URL path `/v2/chat/completions` per OQ-4.6-1. Plain `Authorization: Bearer $ERNIE_UPSTREAM_API_KEY` (Qianfan IAM key). Identity-mapping translate per OQ-4.6-3 cascade verbatim.
>   - `internal/usage/normaliser.go` — identity-mapping `ernieNormaliser` consuming the lifted `packages/adapter-usage` types (NormalisedUsage, ErrUsageConstraintViolation, ValidateInvariants) per Story-4.2 M1 cascade.
>   - `Dockerfile` — multi-stage build copy from `apps/adapters/glm/Dockerfile`.
>   - `tests/{adapter_test.go,chaos_test.go}` — N=1 integration suite (4.6-INT-001..008 + 4.6-INT-012 — non-streaming + streaming + 429 rate-limit + token-usage oracle + missing-usage HARD failure + 25-concurrent race-clean) + Toxiproxy-style chaos suite (build-tag `chaos`); SEVEN CHAOS scenarios (5xx burst / slow loris / mid-stream RST / full timeout / TLS / DNS / Baidu 429 rate-limit). NO CHAOS-008 — Story-4.5 endpoint-id-not-configured + option-(b) access-token-refresh-failed scenarios both N/A under OQ-4.6-1 option (a).
> - `infra/helm/adapter-ernie/` — Helm chart copy-paste from `infra/helm/adapter-glm/` with Ernie swaps (image name, env var names including `ERNIE_UPSTREAM_API_KEY` per Architect Round 1 OQ-4.6-2 BRAND-name naming cascade — brand wins over family `wenxin` + company `baidu`; final Epic-4 cascade closure, `supportedModels: [ernie-4.0]` Helm value per BR-1.10 N=1). NO new ConfigMap (Story-4.5 `doubao-endpoint-ids` pattern is N/A — no endpoint-id ConfigMap mount). Namespace `he-api-adapters` (M4 inheritance). Cold-start budget ≤ 2s (M5 inheritance).
> - `infra/argocd/applications/adapter-ernie.yaml` — ArgoCD Application manifest.
> - `scripts/dev/seed-ernie-key.sh` — dev-mode bootstrap script (gated behind `.env.local` presence — CI never touches it).
> - `docs/dev/secrets/ernie-upstream.md` — Vault credential runbook (Story-4.1 OQ3 cascade ratified path `kv/data/he-api/upstream/ernie/`, manual rotation — Baidu does not support API-key auto-rotation; same operational posture as DeepSeek + Qwen + Kimi + GLM + Doubao). 沿用 Story-1.6 m-4 6-section template.
> - `docs/dev/logs/4.6-dev-log.md` — dev log (Phase 0 empirical-curl verification deferred to first staging deploy + Phase 1 implementation summary + Phase 2 test surface).
> - `apps/api-gateway/internal/adapterclient/registry.go` (MODIFIED): NEW constants `ErnieModelID = "ernie-4.0"`, `ErnieAdapterEndpointEnv = "ERNIE_ADAPTER_ENDPOINT"` per Architect Round 1 OQ-4.6-2 (BRAND-name naming per Story-4.2 OQ-4.2-6 cascade text — brand wins over family `wenxin` / 文心 and company `baidu`; final Epic-4 cascade closure). `LoadFromEnv()` EXTENDED to read `ERNIE_ADAPTER_ENDPOINT` and populate the SINGLE `ernie-4.0` entry. The Story-4.2 M2 `NewRegistry` endpoint-dedup branch is UNCHANGED — at N=1 (RESTORATION) the byEndpoint map has one entry and the dedup branch is a clean NO-OP (4.6-UNIT-011 SKIPPED-branch documentation test per Story-4.4 R9 ratification cascade). SIX-vendor cross-vendor regression verified at 4.6-INT-009 (`TestRegistry_AllSixVendorsCoRegistered_NoCrossVendorShadowing` — closes the Epic-4 vendor matrix).
> - `apps/api-gateway/tests/openai_sdk_ernie_contract_test.py` + `openai_sdk_ernie_live_test.py` — SDK contract + live test skeletons (4.6-CONTRACT-001..002 + 4.6-E2E-001..002, gated by `HE_API_ERNIE_LIVE=1` — separate from sibling vendor live gates; final Epic-4 vendor-live-gate cohort closure).
>
> 条目添加来源：Story 4.6 Architect Round 1（OQ-4.6-1..6 + cascade-locked confirmations + m-1/m-2/l-1 refinements，cascades from Stories 4.2/4.3/4.4/4.5），2026-05-19。**Six-vendor cross-vendor regression matrix COMPLETE** — `apps/api-gateway/internal/adapterclient/registry.go` now ends Epic 4 with TEN model-id → endpoint entries: `deepseek-v3` (Story 4.1), `qwen-max` + `qwen-plus` (Story 4.2), `moonshot-v1-8k` + `moonshot-v1-32k` + `moonshot-v1-128k` (Story 4.3), `glm-4` (Story 4.4), `doubao-pro` + `doubao-lite` (Story 4.5), `ernie-4.0` (Story 4.6).

> **注**: Story 4.7 — 能力矩阵接口 + 公开页面（关闭 Epic-4 DoD 第三行 "能力矩阵公布"；FIRST realised `(marketing)` route group — pre-flagged at line 40 of original source-tree pass; FIRST unauthenticated gateway endpoint pre-Story-5.x rate-limit Stories）新增条目：
>
> - `apps/api-gateway/internal/handlers/models_public.go` — NEW unauthenticated handler `PublicModelsHandler` + `NewPublicModelsHandler(logger, snapshot)` + `BuildPublicModelsSnapshot(startedAt int64)` (OQ-4.7-5 constructor injection ratified — bearer + public handlers share a snapshot built ONCE at boot). Method gate: GET/HEAD allowed; other methods emit `405_method_not_allowed` envelope + `Allow: GET` per RFC 7231 §6.5.5 (OQ-4.7-6 NEW envelope code).
> - `apps/api-gateway/internal/handlers/models.go` (MODIFIED): NEW types `ModelCapabilities` (7 fields per BR-1.2) + extended `ModelEntry` with `Capabilities` appended LAST per BR-1.4; NEW package-private map `capabilitiesByModelID` (11 rows verbatim per OQ-4.7-3); NEW `init()` panic enforces 1:1 invariant with `modelsCatalogue` (4.7-UNIT-010 sibling assertion in tests); slog event renamed `models_list` → `models_list_v1` per BR-1.9; NEW exported method `StartedAt()` so the public-handler snapshot shares the bearer handler's `created` timestamp.
> - `apps/api-gateway/internal/middleware/cors/` — NEW middleware package `cors.PublicCORS(next)` + exported constants (`PublicPathPrefix`, `PublicOriginWildcard`, `PublicAllowedMethods`). OQ-4.7-7 hard constraints: `*` Origin scoped to `/public/*` only; `Access-Control-Allow-Credentials` NEVER emitted per Architect m-1 anti-credential guard. OPTIONS preflight short-circuits with 204 + CORS headers. Wraps mux innermost in `cmd/server/main.go` so non-`/public/*` requests flow through untouched.
> - `apps/api-gateway/internal/openaierr/codes.go` (MODIFIED): NEW entry `"405_method_not_allowed": {405, "invalid_request_error"}` per OQ-4.7-6 — fills the §5.1.2 method-mismatch hole (Story-3.6 "one envelope code per HTTP scenario" precedent).
> - `apps/api-gateway/tests/models_integration_test.go` — NEW Go integration suite (4.7-INT-001 byte-identity / INT-002 no-bearer 200 / INT-003 BR-1.7 defence-in-depth regression / INT-007 anti-credential CORS guard / BLIND-BOUNDARY-002 OPTIONS preflight / BLIND-BOUNDARY-003 query-string ignored). Lives at `apps/api-gateway/tests/` per Story-4.x convention; package `story_4_1_skeleton_test` (shared with sibling skeletons).
> - `apps/api-gateway/tests/public_models_contract_test.py` — NEW Python httpx-based contract suite (4.7-CONTRACT-003/004/005). The OpenAI SDK has no key-less ModelList client; raw httpx covers the unauthenticated wire contract + 405 envelope shape + cross-endpoint parse-equality.
> - `apps/api-gateway/internal/handlers/models_public_test.go` — NEW unit suite (4.7-UNIT-006 method permutations / UNIT-007 PII discipline / BLIND-BOUNDARY-001 HEAD / BLIND-CONCURRENCY-001/002 race-clean / BLIND-DATA-001 mutation isolation).
> - `apps/api-gateway/internal/handlers/models_bench_test.go` — NEW benchmark + assertion suite for BR-1.10 < 5ms P95 warm-path regression marker (4.7-UNIT-013; soft-gate via `t.Log`).
> - `apps/console/app/[locale]/(marketing)/layout.tsx` — FIRST realisation of the pre-flagged `(marketing)` route group. Minimal nav stub + footer; `aria-current="page"` on the Models link.
> - `apps/console/app/[locale]/(marketing)/models/page.tsx` — Server Component for `/{locale}/models`. SSR-fetches `/public/models` via `fetchPublicModels`; renders `<CapabilityMatrix>` or the fallback banner; exports `generateMetadata()` per BR-2.5 (title + description + og:* + canonical).
> - `apps/console/app/[locale]/(marketing)/models/error.tsx` — Next.js error boundary client component (i18n key `models.error.generic`).
> - `apps/console/lib/api/public-models.ts` — NEW data client + Zod schemas (`ModelCapabilitiesSchema` / `ModelEntrySchema` / `PublicModelsResponseSchema`); `fetchPublicModels()` never throws — failures collapse to empty matrix for the fallback banner; default `next: { revalidate: 300 }` per OQ-4.7-8 with m-3 dev-mode 60s refinement.
> - `apps/console/components/business/CapabilityMatrix.tsx` — Responsive matrix; desktop `<table>` with caption + scoped headers; mobile `<article role="region">` card stack via Tailwind `md:` breakpoint (BR-2.6).
> - `apps/console/components/business/CapabilityBadge.tsx` — ✓/✗ pill with sibling `sr-only` text per BR-2.7 (WCAG 2.1 AA).
> - `apps/console/messages/{en,zh-CN,ja,ko,es,fr,de,pt,ru,ar}/models.json` — NEW i18n namespace × 10 locales. `en` + `zh-CN` fully translated; the other 8 carry `[en-pending]` prefixed values per Story-2.1 m-1 cascade (key SET completeness required for CI `console / i18n-keys-completeness`; key values fully translate in Epic-10 marketing-launch).
> - `packages/i18n-keys/src/models.ts` — NEW generated `ModelsKeys` union (36 keys); `index.ts` re-exports the namespace.
> - `apps/console/__tests__/components/business/CapabilityMatrix.test.tsx` — NEW Vitest suite (4.7-UNIT-008/009/011/012 — row count, ✓/✗ badges, Intl.NumberFormat locale formatting under en + zh-CN).
> - `apps/console/e2e/marketing-models.spec.ts` — NEW Playwright E2E covering E2E-001..004 + INT-004/006 + BLIND-ERROR-001 + BLIND-FLOW-001/002/003. `/public/models` is intercepted via `page.route` so the suite is self-contained.
> - `apps/console/e2e/marketing-models-a11y.spec.ts` — axe-core scaffolding for INT-005 (currently `test.skip` — `@axe-core/playwright` is not yet a console devDependency; the harness lands in a follow-up Story).
> - `docs/dev/logs/4.7-dev-log.md` — Dev log per Story-4.6 precedent.
>
> 条目添加来源：Story 4.7 Architect Round 1（OQ-4.7-1..10 + m-1/m-2/m-3 advisories），2026-05-20. **Epic-4 DoD line 3 ("能力矩阵公布") CLOSED**; Story 4.8 contract-tests anti-regression remains as the final Epic-4 closure.

> **注**: Story 4.8 — adapter-fake CI-only binary annotation. Each of the six
> per-vendor `apps/adapters/{vendor}/cmd/fake-upstream/main.go` binaries is
> a TEST-ONLY HTTP server that replaces the real vendor upstream in the
> `gateway-openai-sdk-contract` CI lane (`.github/workflows/test.yml`). Per
> OQ-4.8-3 m-1 ratification each `main.go` MUST carry the top-of-file
> comment `// CI-only adapter-fake upstream — DO NOT package in production
> images`. The production Dockerfile copies ONLY the `cmd/server/` output;
> fake-upstream binaries are NEVER deployed. The pattern carries forward
> to Stories 4.9+ adding vendors — append `apps/adapters/<new-vendor>/cmd/
> fake-upstream/main.go` alongside the production `cmd/server/main.go`.
> Sibling pattern precedent: Story 1.4 `apps/sample-otel-app/cmd/server/`
> ratifies the "test-only reference implementation under apps/" convention.
> Entry added by Story 4.8 (LOW-1 Architect Round 1), 2026-05-20.

> **注**: Story 4.8 — shared protocol invariants library + matrix test.
> `apps/api-gateway/tests/_protocol_invariants.py` (single module, leading-
> underscore pytest "not a test module" convention per OQ-4.8-1) is the
> FIRST shared pytest helper module in the gateway test root. Five exported
> assertion helpers (`assert_chat_completion_shape` / `_chunk_shape` /
> `assert_model_entry_shape` / `_embedding_shape` / `_error_envelope_shape`)
> codify the §5.1.1/5.1.1.1/5.1.2 spec invariants. Imported by EVERY
> per-vendor + per-endpoint `_contract_test.py`. Companion meta-tests live
> at `_protocol_invariants_test.py` (4.8-UNIT-001..020). The matrix test
> `openai_sdk_protocol_completeness_test.py` parametrises across 10 vendor
> model-ids × stream={False, True} = 20 cells per BR-2.6. Stories 4.9+
> adding vendors append one entry to `MATRIX_MODELS` (BR-2.8 single-source-
> of-truth) and the matrix auto-grows. Entry added by Story 4.8, 2026-05-20.

---

> **注**: Story 5.2 — key-policy enforcement + monthly-cost counter packages.
> - `apps/api-gateway/internal/middleware/keypolicy/` — NEW. The three
>   per-request enforcement gates (AC2 IP whitelist / AC3 model scope / AC4
>   monthly cap) running AFTER bearer-auth, BEFORE the chat/embeddings
>   handlers. Files: `keypolicy.go` (middleware), `checks.go` (pure
>   predicates), `clientip.go` (Q-E trusted-proxy XFF walker), `bodypeek.go`
>   (model tee), `metrics.go` (OTel instruments).
> - `apps/api-gateway/internal/usage/` — NEW. Redis-backed monthly-cost
>   counter: `counter_reader.go` (production `ReadMonthlyCostUSD`, Q-D Redis
>   SoT READ) + `counter_publisher.go` (STUB `PublishCostIncrement` — WRITE
>   contract pending billing-svc Epic 6+).
> - `infra/helm/api-gateway/templates/configmap-trusted-proxies.yaml` — NEW
>   ConfigMap `api-gateway-trusted-proxies` (Q-E) exposing `CLOUDFLARE_CIDRS`
>   + `INGRESS_CIDRS` env vars for the XFF walker; empty default = failsafe.
> Entry added by Story 5.2 (Architect Round 1 m2), 2026-06-03.

> **注**: Story 5.5 — Console Keys page (CRUD + config UI; frontend-only).
> - `apps/console/app/[locale]/(console)/keys/page.tsx` — list Server Component
>   (SSR `listMyKeys` + `fetchPublicModels`; `<KeysPanel>` or error banner).
> - `apps/console/app/[locale]/(console)/keys/[api_key_id]/created/page.tsx` —
>   one-time plaintext display SC (`force-dynamic`, noindex/no-referrer; BR-PD-4
>   shape guard → `notFound()`). Cache-Control no-store added in `middleware.ts`.
> - `apps/console/components/business/` — NEW: `KeysPanel` (client orchestrator,
>   router.refresh coherence), `KeysTable`, `KeysTableSkeleton`, `KeysEmptyState`,
>   `CapBudgetBar` (client cap heuristic, Q-E5 overrule), `ScopeChips`,
>   `CreateKeyModal`, `ApiKeyDisplay` (FIRST realization), `ConfigureKeyDrawer`,
>   `IpWhitelistEditor`, `RevokeKeyDialog`.
> - `apps/console/components/ui/{dialog,toast}.tsx` — NEW shared a11y primitives
>   (focus-trap Dialog/Drawer + Toast); `Button` gains a `destructive` variant.
> - `apps/console/components/ConsoleSidebarNav.tsx` — NEW client nav (aria-current).
> - `apps/console/lib/api/{money,ip}.ts` — NEW pure helpers; `me-keys.ts` EXTENDED
>   (`readScope`, `PLAINTEXT_RE`, completed `KeyNameSchema`, tightened cap regex).
> - `apps/console/messages/{10}/account.json` — `account.keys.*` extended;
>   `packages/i18n-keys/src/account.ts` regenerated (163 keys).
> - Tests: `apps/console/__tests__/5.5-console-keys-page-crud-config-ui.test.tsx`
>   (71 Vitest blocks, all green); `apps/console/e2e/5.5-keys-page{,-a11y}.spec.ts`
>   (authored, skip-gated pending the integrated stack + `@axe-core/playwright`).
> Entry added by Story 5.5 (Dev / Linus), 2026-06-03.

---

> **注 (Story 6.1, 2026-06-03)**: `apps/routing-svc/`（source-tree 第 19 行预分配）由 Story 6.1 **REALISED**（server-side skeleton）：`cmd/server/` (main + Dockerfile) + `internal/{engine,strategy,handler,server,catalogue}/` + `tests/`。FIRST `routing-svc` Go 服务（Connect-RPC `he.routing.v1.RoutingService/SelectModel`）。Gateway 客户端接线 + 真实打分 + failover + A/B 延后至 Stories 6.2-6.4。
> - `packages/models-catalogue/` — **NEW top-level Go module**（sibling to `packages/adapter-usage/` / `packages/go-observability/`，Q-A option (a) lift）。承载 Story-4.7 `modelsCatalogue` + `capabilitiesByModelID`（11 行）+ BR-1.3 1:1 invariant（panic-at-construction，双向）。`ModelEntry`/`Capabilities`/`Catalogue`/`Registry`/`NewFromRegistry`/`DefaultRegistry`/`DefaultCatalogue`。被 `apps/api-gateway/internal/handlers`（重接线为 `buildGatewayCatalogue`，wire shape byte-identical，4.7-INT-001 不变）+ `apps/routing-svc/internal/catalogue` 共同消费（single SoT，no drift）。
> - `packages/proto/he/routing/v1/routing.proto` + vendored `packages/proto/gen/go/he/routing/v1/` — 实现 rest-api-spec.md §5.2 `RoutingService` sketch（enum Q-D / `he_request_id` Q-H / `ab_selected_models`+`strategy_used` additive / `adapter_endpoint` reserved Q-K）。
> - `infra/helm/routing-svc/`（Q-J sizing，stateless：deployment/service/sa/configmap/servicemonitor/prometheusrule/networkpolicy）+ `infra/argocd/applications/routing-svc.yaml`。
>
> **注 (Story 6.2, 2026-06-03)**: 真实打分 + gateway↔routing-svc 接线落地。NEW packages/files：
> - `apps/routing-svc/internal/pricing/`（NEW）— `Snapshot`/`PriceOf`/`Load`（pgx 读 `he_api.model_pricing`，latest-`effective_at` per model）+ `Provider`（boot snapshot + 60s refresh，atomic-pointer swap，last-good on error；Q-E/Q-K）。consumer: `internal/strategy/cost.go`。
> - `apps/routing-svc/internal/scoring/`（NEW）— `Scorer` seam（`Score(ctx,candidates)→(map,hasData)`）+ `NoData`（默认，degraded-until-Epic-9）+ `Map`（seeded/test scorer）。consumer: `internal/strategy/{quality,latency}.go`。BR3-1 Epic-9 thin drop-in。
> - `apps/routing-svc/internal/strategy/{cost,quality,latency}.go`（REPLACE 6.1 stubs）+ `candidates.go`（`concreteCandidates` he-router-* 排除 Q-D/BR2-5 + `cheapest` Q-J）+ `scored.go`（`scoredOrDegrade` AC3）。`DefaultStrategies(Deps)` 注入 Prices/Scorers。
> - `apps/routing-svc/internal/engine/`（EXTEND）— `Decide` 增加 `score_source` 返回；NEW optional `SourcedStrategy` capability（保持 cascade-locked `Strategy.Select` 不变）。
> - `apps/routing-svc/cmd/server/main.go`（EXTEND）— resilient pgx pool（`HE_API_DB_POSTGRES_URI` 可选；unset/unreachable → degraded，不 fail boot，BLIND-ERROR-003）+ pricing Provider 接入。`go.mod` adds `jackc/pgx/v5` + `pashagolub/pgxmock/v3`（test）。NO otelpgx（repo 无先例；plain pgxpool mirrors auth-svc）。
> - `apps/api-gateway/internal/routingclient/`（NEW）— `ClientHandle`/`LoadFromEnv`（`ROUTING_SVC_ENDPOINT`，unset → nil → passthrough）+ `ParseStrategy`（Q-I 优先级）+ `Decider`（100ms deadline Q-E + gRPC→§5.1.2 Q-H + fail-open/closed Q-G + metrics Q-M）。consumer: `internal/handlers/chat_completions.go`。
> - `apps/api-gateway/internal/handlers/chat_completions{,_stream}.go`（MODIFIED）— 路由决策插入 adapter-resolve fork 之前；`selected` 替代 `req.Model` 用于 resolve/adapter-model/`X-He-Selected-Model`/response-model；header 成为 universal success-path invariant（含 mock + mock-stream，BR1-2）；UNIT-013 flip。
> - `migrations/postgres/0007_create_models_and_pricing.sql`（NEW，additive+reversible）+ `atlas.sum` row + seed。
> - `packages/proto/he/routing/v1/routing.proto`（EXTEND）— additive `SelectModelResponse.score_source=6`（High-1）+ vendored gen 重新生成。
> - `infra/helm/routing-svc/`（EXTEND，可选 `db.enabled` → PG secretRef + 5432 egress）+ `infra/helm/api-gateway/`（EXTEND，可选 `routing.endpoint` → `ROUTING_SVC_ENDPOINT`）。
