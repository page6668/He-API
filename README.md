# He-API

He-API 多模型聚合网关 — Turborepo monorepo 工程地基。

> **本仓库目前处于 Story 1.1 完成状态:仅包含工程骨架(配置 + 占位包)**,
> 业务代码、API 端点、数据库迁移由后续 Epic / Story 增量引入。

---

## 目录结构

```
he-api/
├── apps/
│   └── api-gateway/          Go service (placeholder main.go)
├── packages/
│   └── shared-types/         TypeScript 共享类型 (placeholder)
├── infra/
│   ├── terraform/            云资源 IaC (placeholder)
│   ├── helm/                 Helm charts (placeholder)
│   ├── argocd/               GitOps 应用定义 (placeholder)
│   └── k8s-base/             K8s 基础资源 (placeholder)
├── scripts/                  开发与运维脚本 (placeholder)
├── docs/                     PRD / 架构 / Story / Dev 日志
├── .github/workflows/        CI 流水线 (placeholder)
├── package.json              根工作区 (private, packageManager 锁 pnpm@9.x)
├── pnpm-workspace.yaml       声明 apps/* 与 packages/*
├── turbo.json                Turborepo 任务定义 (build/lint/test/dev)
├── go.work                   Go 多 module workspace
└── tsconfig.base.json        TS 共享 base 配置 (strict, ES2022)
```

详细架构见 [`docs/architecture/source-tree.md`](docs/architecture/source-tree.md)。

---

## 工具链版本要求

| 工具       | 版本    | 备注 |
|------------|---------|------|
| **Node.js**| 20 LTS  | 通过 `.nvmrc` 锁定 |
| **pnpm**   | 9.x     | 通过 `package.json` `packageManager` 字段锁定 |
| **Go**     | 1.22+   | `go.work` 要求 |

推荐启用 [corepack](https://nodejs.org/api/corepack.html) 让 `pnpm` 自动遵循
`packageManager` 字段:

```bash
corepack enable
```

---

## 本地启动

最快启动(TypeScript 端):

```bash
pnpm i && pnpm build
```

完整启动(含 Go workspace 构建):

```bash
# 1. 安装 TS 依赖
pnpm i

# 2. 构建所有 TS workspace 包
pnpm build

# 3. 构建所有 Go module(workspace 模式下从根目录批量构建)
bash scripts/go-build-all.sh
```

二次执行 `pnpm build` 应命中 Turborepo 缓存(输出 `>>> FULL TURBO`)。

> **关于 Go 构建命令**:Go workspace 模式下,`go build ./...` 在仓库根目录无效
> (根没有 `go.mod`)。因此提供 `scripts/go-build-all.sh` 脚本统一构建所有
> `go.work` 中声明的 module。等价的单 module 命令:
> `go build ./apps/api-gateway/...` 或 `cd apps/api-gateway && go build ./...`。

---

## 常用命令

| 命令                | 说明 |
|---------------------|------|
| `pnpm i`            | 安装所有 workspace 依赖 |
| `pnpm build`        | Turborepo 构建所有 TS 包 |
| `pnpm lint`         | Turborepo lint 所有 TS 包 |
| `pnpm test`         | Turborepo 运行所有测试 |
| `pnpm dev`          | Turborepo dev 模式(persistent) |
| `bash scripts/go-build-all.sh` | 构建 `go.work` 中声明的所有 Go module |

---

## CI/CD

GitHub Actions 提供 PR 检查与 merge 触发的 staging 部署链路。Story 1.2 落地基线工作流，
Story 1.3/1.4 接入真实 ACR / ArgoCD 后即可端到端运行。

### PR 必需 status checks（10 个）

合并到 `main` 的 PR 必须等待以下 10 个 check 全部绿色（6 个由 Story 1.2 提供，4 个由 Story 1.3 新增）：

| Check 名称                       | Workflow                              | 含义 |
|----------------------------------|---------------------------------------|------|
| `lint-ts`                        | `.github/workflows/lint.yml`          | TS 端 `pnpm lint`（Turborepo 编排） |
| `lint-go`                        | `.github/workflows/lint.yml`          | `golangci-lint` + `gofumpt`（作用域 `./apps/...`） |
| `unit-ts`                        | `.github/workflows/test.yml`          | `pnpm test`（Vitest） |
| `unit-go`                        | `.github/workflows/test.yml`          | `go test -race`（`CGO_ENABLED=1`） |
| `integration`                    | `.github/workflows/test.yml`          | 占位（Story 1.5/1.6 接入真实场景） |
| `build-image-pr`                 | `.github/workflows/build-images.yml`  | `docker build` 验证（无 push） |
| `terraform-validate (staging)`   | `.github/workflows/infra-lint.yml`    | `terraform fmt -check` + `init -backend=false` + `validate` + `tflint --recursive`（staging matrix） |
| `terraform-validate (prod)`      | `.github/workflows/infra-lint.yml`    | 同上（prod matrix） |
| `helm-lint`                      | `.github/workflows/infra-lint.yml`    | `helm lint` + `helm template \| kubeconform -strict -summary` |
| `k8s-manifest-validate`          | `.github/workflows/infra-lint.yml`    | `kubeconform -strict -summary` 校验 `infra/k8s-base/` |

### 本地复现 CI

```bash
# 一次性工具链体检
bash scripts/ci/setup-toolchain.sh

# TS 端（与 lint-ts / unit-ts 对齐）
pnpm install --frozen-lockfile
pnpm lint
pnpm test

# Go 端（与 lint-go / unit-go 对齐；作用域必须 ./apps/...，与 go.work 一致）
golangci-lint run --timeout=5m ./apps/...
gofumpt -l -d ./apps                       # 无 diff 表示通过
CGO_ENABLED=1 go test ./apps/api-gateway/... -count=1 -race
```

### 需运维配置的 secrets / vars

| 类型      | 名称                       | 何时配置 | 用途 |
|-----------|----------------------------|----------|------|
| `secrets` | `ACR_REGISTRY`             | Story 1.3 | 阿里云 ACR 域名 |
| `secrets` | `ACR_USERNAME`             | Story 1.3 | ACR 登录用户 |
| `secrets` | `ACR_PASSWORD`             | Story 1.3 | ACR 登录密码 |
| `vars`    | `ARGOCD_ENDPOINT`          | Story 1.4 | ArgoCD server 地址 |
| `vars`    | `STAGING_NAMESPACE`        | Story 1.4 | staging 命名空间 |

> `GITOPS_BOT_TOKEN` 已 **不再需要**：`deploy-staging.yml` 改用内置
> `${{ secrets.GITHUB_TOKEN }}` 配合 job-level `permissions: contents: write`。
> GitHub 官方约定：`GITHUB_TOKEN` 推送的 commit/push 事件 **不会** 再次触发同
> 仓库 workflow，机制层面切断 push → deploy-staging → bot-commit → push 的无限循环。

### Branch protection 推荐配置（仓库 admin 手工）

- **Require status checks before merging**：勾选上方 **10 个 check** 全部为必需。
  Story 1.3 新增的 4 个 check 名称：`terraform-validate (staging)` /
  `terraform-validate (prod)` / `helm-lint` / `k8s-manifest-validate`。
- **禁止 admin 强推**（Include administrators in restrictions）。
- **`github-actions[bot]` 加入 Bypass list**：在 `main` 分支保护规则的
  *Bypass list* 中添加 `github-actions[bot]`（或在 "Require a pull request
  before merging" 规则中显式排除该 bot）。

  **未配置时的后果**：`deploy-staging.yml` 的 `update-helm-values` job 使用
  `GITHUB_TOKEN` 直接 `git push` 回 `main`，会绕过 PR Review 流程；若未为 bot
  设置 bypass，该 push 会被静默拒绝，Helm values 永远不更新——GitOps 流水线
  静默断链。**必须** 在 Story 1.3 配置真实 `values-staging.yaml` 之前确保该
  bypass 已就位。

### 并发控制

- `build-images.yml`：`concurrency.group = build-images-${{ github.ref }}`；
  `cancel-in-progress = ${{ github.event_name == 'pull_request' }}`。
  PR 同 ref 后续 push 会取消上一次 run（节省 runner），`main` push 不取消，
  保证每次 merge 的 `build/push/image-sha artifact` 链路完整，与下游
  `deploy-staging` 1:1 对齐。
- `deploy-staging.yml`：`concurrency.group = deploy-staging`（全仓库串行化）；
  `cancel-in-progress = false`，保证每次 merge 的 helm-values 改写都完整完成，
  避免连续 merge 时 `git push` 回 main 的 fast-forward 竞态。

### Workflow 串联拓扑

```
┌─────────────────────┐
│   PR opened/sync    │
└──────────┬──────────┘
           ▼
 ┌─────────────────┬──────────────────┬──────────────────────┐
 │   lint.yml      │     test.yml     │  build-images.yml    │
 │ (lint-ts/-go)   │ (unit-ts/-go/    │  (build-image-pr,    │
 │                 │  integration)    │   push: false)       │
 └─────────────────┴──────────────────┴──────────────────────┘
                       (6 个 PR check 并行)
                                 │
                                 ▼ merge to main
 ┌──────────────────────────────────────────────────────────┐
 │  build-images.yml (build-image-main: push ACR + upload   │
 │                    image-sha artifact)                    │
 └──────────────────────────┬───────────────────────────────┘
                            │ workflow_run: completed/success
                            ▼
 ┌──────────────────────────────────────────────────────────┐
 │  deploy-staging.yml                                       │
 │   1. download image-sha artifact                          │
 │   2. update-helm-values (yq + GITHUB_TOKEN push)          │
 │   3. trigger-argocd-sync (gated by ARGOCD_ENDPOINT)       │
 └──────────────────────────┬───────────────────────────────┘
                            ▼
                      ArgoCD sync → K8s (Story 1.3/1.4 接入)
```

### 镜像 tag 策略

仅 push immutable `:${git_sha}` tag。**不**使用 `:staging-latest` 等可变 tag——
ArgoCD 通过 Helm values 中的 `image.tag = ${git_sha}` 拉取唯一镜像，保证 GitOps
镜像不可变原则。

---

## Infrastructure

Story 1.3 交付的阿里云基础设施 IaC（Terraform + Helm + Kustomize）。本节是
运维与 Dev 的 onboarding 入口；详细模块说明见
`docs/architecture/infrastructure-deployment.md`。

### Bootstrap Sequence（运维一次性执行）

> **必须** 由运维使用阿里云管理员凭证执行 **一次**；CI / Dev / `tfstate-operator`
> 子账号 **均无权限** 执行此步骤。脚本本身具备幂等性，重跑安全。

```bash
# Required env:
#   ALICLOUD_ACCESS_KEY  (admin, one-shot)
#   ALICLOUD_SECRET_KEY  (admin, one-shot)
#   ALICLOUD_REGION      (default cn-shanghai)
bash scripts/infra/bootstrap-state-backend.sh staging
```

脚本创建 Terraform state 后端的三件套：
- OSS bucket `he-api-tfstate-staging-sh`（versioning + SSE-KMS + bucket policy）
- KMS CMK `alias/he-api-tfstate-staging`（ENCRYPT_DECRYPT + 365d 轮换）
- TableStore 实例 + `terraform-lock` 表（PK = `LockID:string`）

### Apply 顺序（4 步）

1. **`bash scripts/infra/bootstrap-state-backend.sh staging`** — 运维一次性
   bootstrap state 后端（见上节，**仅运维**）。
2. **`cd infra/terraform/envs/staging && terraform init && terraform plan && terraform apply`** —
   Dev / 运维（持有 `tfstate-operator` 凭证）创建 VPC / ACK / ACR 真实资源。
   本 Story 阶段仅在 staging 执行；prod 留 Story 1.7+ 上线评审后执行。
3. **`kubectl apply -k infra/k8s-base/`** — 应用 6 个 namespace + RBAC +
   NetworkPolicy + ResourceQuota（顺序由 kustomization.yaml 保证）。
4. **`helm install api-gateway infra/helm/api-gateway/ -n he-api-staging -f infra/helm/api-gateway/values-staging.yaml`** —
   首次部署 api-gateway chart。预期 Pod 进入 `ImagePullBackOff`（真实 image
   在 Story 1.5 之后才有），用 `helm uninstall` 清理即可。

### 必备阿里云 RAM 权限

`tfstate-operator` RAM 子账号所需的 **6 条 policy**（最小权限原则；
**严禁** 使用 `AdministratorAccess` 或 root 账号执行 `terraform apply`）：

| Policy                       | 用途 |
|------------------------------|------|
| `AliyunECSFullAccess`        | ACK worker node ECS 实例生命周期 |
| `AliyunVPCFullAccess`        | VPC + vSwitch + NAT + EIP |
| `AliyunCSFullAccess`         | ACK 托管集群 |
| `AliyunCRFullAccess`         | ACR Enterprise Edition 实例 + namespace + repo |
| `AliyunOSSFullAccess`        | state bucket 读写（被 bucket policy 二次收紧到 `tfstate-operator`） |
| `custom: kms+ots`            | 自定义 policy: `kms:Encrypt/Decrypt` on `alias/he-api-tfstate-*` + `ots:*` on `he-api-tfstate-*/terraform-lock` |

> **No Admin policy** — 任何带 `AdministratorAccess` 或 `*:*` 的策略都被禁止；
> 一旦 Terraform state 被泄漏，攻击面以最小权限收敛。

### Cost estimate

| 资源                            | 月度估算（按量计费） |
|---------------------------------|----------------------|
| ACK 控制面                       | 免费 |
| 3× `ecs.c7.large` worker        | ~¥600 |
| NAT Gateway + EIP 流量          | ~¥100 |
| ACR Enterprise Basic（预付费）  | ~¥100 |
| OSS state bucket                | ~¥1 |
| KMS CMK                         | ~¥10 |
| TableStore（按量）              | ~¥1 |
| **staging 月度合计**            | **约 ¥800/月** |

**触达 ¥1500/月** 须 **运维 + Tech Lead 联合审批**；超过 ¥2000/月触发自动停机
评审（Story 1.7+ 落地）。

### Story 1.2 dependency 闭环对照表

Story 1.3 落地后会关闭 Story 1.2 deploy-staging.yml 的 fallback 路径。
GitHub Secrets / Variables 由运维在 `terraform apply` 完成后人工填入：

| Story 1.2 引用                              | Story 1.3 交付                          | 操作 |
|---------------------------------------------|-----------------------------------------|------|
| `secrets.ACR_REGISTRY`                      | `module.acr.acr_endpoint` 的 `terraform output` | 运维填入 GitHub Secrets |
| `secrets.ACR_USERNAME`                      | T3 README "Secrets Bootstrap"（`cr-pusher` 子账号） | 运维填入 GitHub Secrets |
| `secrets.ACR_PASSWORD`                      | T3 README "Secrets Bootstrap"（90 天临时 token，60 天前轮换） | 运维填入 GitHub Secrets |
| `infra/helm/api-gateway/values-staging.yaml`| T8 真实文件，关闭 `probe_helm` fallback | 自动随仓库 PR 落地 |
| K8s namespace `he-api-staging`              | T7 `namespace.yaml`                     | `kubectl apply -k infra/k8s-base/` |

### 集群生命周期 + 回滚预案（Rollback predicate）

**staging 集群默认保留至 Story 1.4 启动** — Story 1.4 ArgoCD / 可观测紧随 1.3，
需要 K8s 集群与 ACR 已就位。本 Story 完成后立即 `terraform destroy` 会导致
1.4 启动时重复成本、重复风险，且 Cluster ID 变化会让下游 ArgoCD endpoint /
ACR push 凭证全部失效。

- **不主动 destroy 条件**：Story 1.4 启动 ≤ 2 周内。
- **运维评估 destroy 条件**：Story 1.4 启动延期 > 2 周。
- **执行顺序**（当且仅当满足上一条）：

  ```bash
  # 1. 卸载 Helm release (如有残留)
  helm uninstall api-gateway -n he-api-staging

  # 2. 卸载 K8s base 资源
  kubectl delete -k infra/k8s-base/

  # 3. 销毁 Terraform 管理的资源（ACK → VPC，NAT/EIP 在 VPC 前 detach）
  cd infra/terraform/envs/staging
  terraform destroy
  ```

- **必保留资源（Epic 级共享 — 严禁进入 `terraform destroy` 范围）**：

  | 资源                          | 后果（若被删除） |
  |-------------------------------|------------------|
  | **ACR Enterprise Basic 实例** | 已推镜像全部丢失；Story 1.5+ build 链路断裂 |
  | **OSS state bucket**          | 所有环境的 Terraform state 文件丢失 → Epic 整体重建 |
  | **KMS CMK**                   | state 文件解密能力丢失 → 同上 |
  | **TableStore `terraform-lock` 表** | 并发 apply 安全失效 → 状态文件腐败风险 |

- prod 因 `terraform_data.{vpc,ack}_destroy_guard` 上的
  `lifecycle.prevent_destroy = true` 不可 destroy；如需销毁需人工去除 guard
  + Tech Lead 显式 sign-off。

### 未交付项（等待 Story 1.4 / 1.6）

| 内容                                  | 交付 Story |
|---------------------------------------|------------|
| ArgoCD 安装                            | Story 1.4  |
| OpenTelemetry / Prometheus / Loki / Grafana | Story 1.4  |
| RDS / Redis / ClickHouse / Kafka       | Story 1.6  |
| `vars.ARGOCD_ENDPOINT` / `vars.STAGING_NAMESPACE` 填值 | Story 1.4 |

---

## Observability

Story 1.4 lands the staging observability stack on the ACK cluster retained by Story 1.3. Five upstream charts compose the stack per Architect Round 1 **Q1** ruling (hybrid distribution); one reference-implementation service (`apps/sample-otel-app/`) exercises the full pyramid (trace + metrics + structured logs).

### 5 dashboards

| Dashboard | Status | Path | Notes |
|-----------|--------|------|-------|
| Gateway 大盘 | ✓ Implemented | `infra/grafana-dashboards/gateway.json` | 4 panels (Request Rate / Error Rate / Latency P95 / Top error codes) — PromQL uses OpenTelemetry Semantic Conventions 1.x (Round 1 **M-1**: `http_server_request_duration_seconds_*`, label `service_name`, label `http_response_status_code`). |
| 业务大盘 | □ Placeholder | `infra/grafana-dashboards/business.json` | Pending Epic 2-8 data integration. |
| 模型大盘 | □ Placeholder | `infra/grafana-dashboards/model.json` | Pending Epic 2-8 data integration. |
| 支付大盘 | □ Placeholder | `infra/grafana-dashboards/payment.json` | Pending Epic 2-8 data integration. |
| 合规大盘 | □ Placeholder | `infra/grafana-dashboards/compliance.json` | Pending Epic 2-8 data integration. |

### Install order (BOOTSTRAP MODE)

`scripts/observability/install.sh` is a **BOOTSTRAP MODE** script (the file header tags it so explicitly per Round 1 **m-2**). It is the staging-only entry point until **Story 1.7+** lands ArgoCD; after that, every release migrates into an ArgoCD `Application`.

```bash
# Prereqs: ACK staging kubeconfig active, helm 3.14+, kubectl 1.29+, jq.
./scripts/observability/install.sh
```

Install order is locked by Round 1 Q1 ruling — `kube-prometheus-stack` → `kubectl wait` for the ServiceMonitor CRD (m-3) → `loki` → `promtail` → `jaeger` → `otel-collector` → `sample-otel-app`. The CRD wait is mandatory: dependents emit ServiceMonitors at install-time and admission fails if the CRD has not finished registering.

### Operator workflows

**Grafana** — port-forward only (Round 1 **Q5**: public exposure, TLS, auth deferred to Story 1.7+ alongside the ingress-nginx / cert-manager bootstrap):

```bash
kubectl port-forward svc/kube-prometheus-stack-grafana 3000:80 -n monitoring
# Browse http://localhost:3000
kubectl get secret -n monitoring kube-prometheus-stack-grafana -o jsonpath='{.data.admin-password}' | base64 -d
```

The admin password is auto-generated by the chart (Round 1 **Q6**) into the secret named above; OIDC is Epic 9.

**Jaeger Query** — `kubectl port-forward svc/jaeger-query 16686:16686 -n monitoring`.

**Prometheus** — `kubectl port-forward svc/kube-prometheus-stack-prometheus 9090:9090 -n monitoring`.

### Cost (vs Story 1.3 baseline)

| Component | Storage | Retention | Extra ¥/月 |
|-----------|---------|-----------|------------|
| Prometheus | emptyDir | 24h (Q4) | ≈ +¥0 |
| Loki | filesystem in emptyDir (cap 2Gi) | 48h | ≈ +¥0 |
| Jaeger | in-memory | ~2h (natural eviction) | ≈ +¥0 |
| Grafana | none (sidecar discovery) | — | ≈ +¥0 |
| Alertmanager | emptyDir | — | ≈ +¥0 |

Net incremental: **≈ +¥0** — the stack consumes spare CPU/Mem on the 1.3 staging pool (3× ecs.c7.large). Prod PVC / ESSD / OSS strategy is a Story 1.7+ ADR.

### Architect Round 1 ruling traceability

| Ruling | Decision | Anchor |
|--------|----------|--------|
| **Q1** Helm distribution | Hybrid — kube-prometheus-stack umbrella + 4 independent charts. ArgoCD = Story 1.7+. | `infra/helm/observability/README.md` + install.sh BOOTSTRAP MODE header |
| **Q2** Trace backend | Jaeger preserved per architecture §11.1. Tempo branch fully removed (M-5). | `infra/helm/observability/jaeger/` + otel-collector exports to `jaeger-collector:4317` |
| **Q3** Sample service | `apps/sample-otel-app/` (api-gateway stays single-responsibility) | `apps/sample-otel-app/cmd/server/main.go` |
| **Q4** Persistence | Staging full emptyDir; Prom 24h / Loki 48h / Jaeger ~2h. Prod = Story 1.7+ ADR. | values-staging.yaml per chart |
| **Q5** Grafana exposure | port-forward only; ingress / TLS / OIDC = Story 1.7+ | `kubectl port-forward` workflow above |
| **Q6** Grafana auth | K8s Secret auto-gen; OIDC = Epic 9. | `kubectl get secret … admin-password` workflow above |
| **M-1** PromQL semconv | OTel Semantic Conventions 1.x; never legacy `http_server_requests_total`. | `infra/grafana-dashboards/gateway.json` PromQL |
| **M-2** Promtail explicit | Promtail is its own release; CI matrix=6. | `infra/helm/observability/promtail/` |
| **M-3** node-exporter off | `nodeExporter.enabled: false` (baseline PSA). | `kube-prometheus-stack/values-staging.yaml` |
| **M-4** install.sh SHA inject | Story 1.2 deploy-staging.yml untouched; install.sh injects git SHA. | install.sh `--set image.tag=$(git rev-parse --short HEAD)` |
| **M-5** Tempo removed | Jaeger only. | values + datasources + install.sh negative test |

`apps/sample-otel-app/` exercises the full pyramid at install time — see `docs/dev/logs/1.4-dev-log.md` §E2E for the 5 screenshots (`helm list` + Grafana 3-datasource green + Jaeger trace tree + Gateway dashboard RED curves + Loki LogQL `trace_id`).

---

## 文档

- [PRD](docs/prd.md)
- [架构总览](docs/architecture.md)
- [Story 列表](docs/stories/)

---

## License

(待补)
