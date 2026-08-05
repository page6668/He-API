#!/usr/bin/env bash
# ============================================================
# build-console.sh — 构建 Next.js Console 部署产物
# 供本地（--local）与 CI（release-binaries.yml）共用的单一真源。
#
# 用法:
#   build-console.sh <repo-root> <output-dir> [build-env-file]
#
#   <repo-root>       He-API monorepo 根（含 pnpm-workspace.yaml）
#   <output-dir>      产物输出目录；产物落在 <output-dir>/（即 /opt/he-api/console）
#   [build-env-file]  可选；提供 next build 期的 NEXT_PUBLIC_* 等变量。
#                     不传则用 deploy/bare-metal/env/console.env 模板（含占位符）。
#
# 部署模型：用 `pnpm deploy --prod` 生成扁平（hoisted）node_modules + `next start`。
#   为何不用 Next 的 `output: 'standalone'`？
#   standalone 在 pnpm 符号链接布局下，Next 文件追踪器会漏掉 styled-jsx 等
#   传递依赖的顶层符号链接，导致运行时 `require('styled-jsx')` 失败。
#   `pnpm deploy` 由 pnpm 自身生成正确的扁平 node_modules，彻底规避该坑。
#
# 产物布局（可直接作为 /opt/he-api/console）:
#   <output-dir>/
#     .next/              <- next build 产物（含 server）
#     node_modules/       <- pnpm deploy --prod 扁平依赖（含 next, react...）
#     public/ app/ lib/ next.config.mjs package.json ...
# 部署时整目录复制到 /opt/he-api/console，由 he-api-console.service 拉起：
#   node /opt/he-api/console/node_modules/.bin/next start -p 3000
# ============================================================
set -euo pipefail

REPO_ROOT="${1:?usage: build-console.sh <repo-root> <output-dir> [build-env-file]}"
OUT="${2:?usage: build-console.sh <repo-root> <output-dir> [build-env-file]}"
ENV_FILE="${3:-}"

cd "$REPO_ROOT"

# ---------- 依赖检查 ----------
command -v node >/dev/null 2>&1 || { echo "✗ 需要 node 20+"; exit 1; }
command -v pnpm >/dev/null 2>&1 || { echo "✗ 需要 pnpm 9.x"; exit 1; }
NODE_MAJOR=$(node -v | sed -E 's/v([0-9]+).*/\1/')
if [[ "$NODE_MAJOR" -lt 18 ]]; then echo "✗ node 版本过低: $(node -v)"; exit 1; fi

# ---------- 注入构建期变量（NEXT_PUBLIC_* 等）----------
if [[ -n "$ENV_FILE" && -f "$ENV_FILE" ]]; then
    echo "==> 从 $ENV_FILE 注入构建期变量"
    set -a; source "$ENV_FILE"; set +a
elif [[ -f deploy/bare-metal/env/console.env ]]; then
    echo "==> 从 deploy/bare-metal/env/console.env 注入构建期变量（占位符）"
    set -a; source deploy/bare-metal/env/console.env; set +a
fi
export NEXT_TELEMETRY_DISABLED=1

# ---------- 安装依赖（增量）----------
if [[ ! -d node_modules/@he-api ]]; then
    echo "==> pnpm install（首次）"
    pnpm install --frozen-lockfile
fi

# ---------- 构建 console ----------
# 裸机部署走 next start（非 standalone），避免 next start 与 output:standalone 不兼容。
export BARE_METAL_BUILD=1
echo "==> next build --filter=@he-api/console"
pnpm exec turbo run build --filter=@he-api/console

# ---------- pnpm deploy 生成扁平 prod node_modules ----------
echo "==> pnpm deploy --filter=@he-api/console --prod -> $OUT"
rm -rf "$OUT"
pnpm deploy --filter @he-api/console --prod "$OUT"

# ---------- 拷贝 .next 构建产物（deploy 不自动包含）----------
echo "==> 拷贝 .next 到 $OUT/.next"
cp -r apps/console/.next "$OUT/.next"

echo "✓ console 部署产物已生成: $OUT"
echo "  启动: node $OUT/node_modules/.bin/next start -p 3000  (WorkingDirectory=$OUT)"
