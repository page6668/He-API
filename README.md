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

## 文档

- [PRD](docs/prd.md)
- [架构总览](docs/architecture.md)
- [Story 列表](docs/stories/)

---

## License

(待补)
