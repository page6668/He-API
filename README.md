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

### PR 必需 status checks（6 个）

合并到 `main` 的 PR 必须等待以下 6 个 check 全部绿色：

| Check 名称         | Workflow                          | 含义 |
|--------------------|-----------------------------------|------|
| `lint-ts`          | `.github/workflows/lint.yml`      | TS 端 `pnpm lint`（Turborepo 编排） |
| `lint-go`          | `.github/workflows/lint.yml`      | `golangci-lint` + `gofumpt`（作用域 `./apps/...`） |
| `unit-ts`          | `.github/workflows/test.yml`      | `pnpm test`（Vitest） |
| `unit-go`          | `.github/workflows/test.yml`      | `go test -race`（`CGO_ENABLED=1`） |
| `integration`      | `.github/workflows/test.yml`      | 占位（Story 1.5/1.6 接入真实场景） |
| `build-image-pr`   | `.github/workflows/build-images.yml` | `docker build` 验证（无 push） |

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

- **Require status checks before merging**：勾选上方 6 个 check 全部为必需。
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

## 文档

- [PRD](docs/prd.md)
- [架构总览](docs/architecture.md)
- [Story 列表](docs/stories/)

---

## License

(待补)
