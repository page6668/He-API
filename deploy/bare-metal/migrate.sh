#!/usr/bin/env bash
# He-API 裸机数据库迁移脚本（替代 K8s 的 Atlas 流程）
#
# 设计原则：
#   * 用 psql 直接驱动，不依赖 atlas / golang-migrate CLI
#   * 幂等：已应用的 migration 记录在 he_api._he_api_schema_migrations
#   * 按文件名前缀序号（0001..0022）顺序执行
#   * baseline 的 CREATE ROLE 改为 IF NOT EXISTS，支持重复运行
#
# 前置条件：
#   * 已安装 postgresql-client（提供 psql）
#   * HE_API_DB_POSTGRES_URI 指向目标库
#     （格式：postgres://<user>:<pass>@<host>:5432/<db>?sslmode=disable）
#   * 首次运行建议用超级用户（postgres）连接，以便创建 he_api 角色与 schema
#   * ClickHouse 为可选项：设置 HE_API_DB_CLICKHOUSE_URI 且安装 clickhouse-client
#     才会尝试应用；否则跳过并提示
#
# 用法：
#   HE_API_DB_POSTGRES_URI=postgres://postgres:xxx@localhost:5432/he_api \
#     ./migrate.sh up          # 应用所有未执行的 migration
#   ./migrate.sh status        # 显示已应用 / 待应用
#   ./migrate.sh down N        # 回滚最近 N 个（best-effort，部分不可回滚）
#   ./migrate.sh dry-run       # 只打印将要执行的文件，不实际执行

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." &> /dev/null && pwd)"
PG_DIR="$REPO_ROOT/migrations/postgres"
CH_DIR="$REPO_ROOT/migrations/clickhouse"

PG_URI="${HE_API_DB_POSTGRES_URI:-}"
CH_URI="${HE_API_DB_CLICKHOUSE_URI:-}"

TRACK_TABLE="he_api._he_api_schema_migrations"

# ---------------------------------------------------------------------------
# 工具检查
# ---------------------------------------------------------------------------
require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: 缺少命令 '$1'，请先安装。" >&2
    exit 1
  }
}

# ---------------------------------------------------------------------------
# 从 URI 提取密码（用于 baseline 创建 he_api 角色，使其与连接密码一致）
# ---------------------------------------------------------------------------
uri_password() {
  # postgres://user:pass@host:5432/db → pass
  echo "$1" | sed -E 's#^[a-zA-Z]+://[^:]+:([^@]+)@.*#\1#'
}

# ---------------------------------------------------------------------------
# 创建追踪表（在 he_api schema 下）
# ---------------------------------------------------------------------------
ensure_track_table() {
  psql "$PG_URI" -v ON_ERROR_STOP=1 -X -q <<'SQL'
CREATE SCHEMA IF NOT EXISTS he_api;
CREATE TABLE IF NOT EXISTS he_api._he_api_schema_migrations (
    version     TEXT        PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
SQL
}

# ---------------------------------------------------------------------------
# 列出待应用的 migration 文件（按序号排序）
# ---------------------------------------------------------------------------
list_pending() {
  local applied
  applied="$(psql "$PG_URI" -X -t -A -c \
    "SELECT version FROM he_api._he_api_schema_migrations;" 2>/dev/null || true)"
  for f in $(ls "$PG_DIR"/*.sql 2>/dev/null | sort); do
    local base; base="$(basename "$f")"
    # 跳过 atlas 元数据文件
    [[ "$base" == "atlas.hcl" || "$base" == "atlas.sum" ]] && continue
    local ver; ver="$(echo "$base" | grep -oE '^[0-9]+' | head -1)"
    [[ -z "$ver" ]] && continue
    if ! echo "$applied" | grep -qxF "$ver"; then
      echo "$f"
    fi
  done
}

list_applied() {
  psql "$PG_URI" -X -t -A -c \
    "SELECT version FROM he_api._he_api_schema_migrations ORDER BY version;" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# 应用单个 PG migration（baseline 特殊处理角色创建）
# ---------------------------------------------------------------------------
apply_pg_file() {
  local f="$1"
  local base; base="$(basename "$f")"
  local ver; ver="$(echo "$base" | grep -oE '^[0-9]+' | head -1)"
  local target="$f"

  if [[ "$base" == "0001_baseline.sql" ]]; then
    local app_pw; app_pw="$(uri_password "$PG_URI")"
    # 将 CREATE ROLE he_api ... :'app_password' 改为 IF NOT EXISTS + 真实密码，
    # 使脚本可重复运行且角色密码与连接 URI 一致。
    target="$(mktemp /tmp/he_api_mig.XXXXXX.sql)"
    sed -E \
      "s/CREATE ROLE he_api LOGIN PASSWORD :'app_password';/CREATE ROLE IF NOT EXISTS he_api LOGIN PASSWORD '${app_pw}';/" \
      "$f" > "$target"
  fi

  echo "==> 应用 PG migration: $base"
  psql "$PG_URI" -v ON_ERROR_STOP=1 -X -f "$target" \
    -v app_password="$(uri_password "$PG_URI")"

  psql "$PG_URI" -X -q -c \
    "INSERT INTO he_api._he_api_schema_migrations(version) VALUES ('$ver') ON CONFLICT DO NOTHING;"

  [[ "$target" != "$f" ]] && rm -f "$target"
}

# ---------------------------------------------------------------------------
# ClickHouse（可选）
# ---------------------------------------------------------------------------
apply_clickhouse() {
  [[ -z "$CH_URI" ]] && { echo "⚠  HE_API_DB_CLICKHOUSE_URI 未设置，跳过 ClickHouse migration。"; return 0; }
  command -v clickhouse-client >/dev/null 2>&1 || {
    echo "⚠  未安装 clickhouse-client，跳过 ClickHouse migration（analytics-svc 将无表可用）。"; return 0
  }
  echo "==> 应用 ClickHouse migration"
  for f in $(ls "$CH_DIR"/*.up.sql 2>/dev/null | sort); do
    echo "    - $(basename "$f")"
    clickhouse-client --url "$CH_URI" --multiquery < "$f" || {
      echo "ERROR: ClickHouse migration 失败: $(basename "$f")" >&2
      return 1
    }
  done
}

# ---------------------------------------------------------------------------
# 子命令
# ---------------------------------------------------------------------------
cmd_up() {
  require psql
  [[ -z "$PG_URI" ]] && { echo "ERROR: HE_API_DB_POSTGRES_URI 未设置。" >&2; exit 1; }
  ensure_track_table
  local pending; pending="$(list_pending)"
  if [[ -z "$pending" ]]; then
    echo "✓ 无待应用的 PG migration。"
  else
    echo "待应用 PG migration："
    echo "$pending" | while read -r f; do echo "  - $(basename "$f")"; done
    echo "$pending" | while read -r f; do apply_pg_file "$f"; done
    echo "✓ PG migration 完成。"
  fi
  apply_clickhouse
}

cmd_status() {
  require psql
  [[ -z "$PG_URI" ]] && { echo "ERROR: HE_API_DB_POSTGRES_URI 未设置。" >&2; exit 1; }
  echo "已应用："
  list_applied | sed 's/^/  [x] /'
  echo "待应用："
  list_pending | while read -r f; do echo "  [ ] $(basename "$f")"; done
}

cmd_down() {
  require psql
  [[ -z "$PG_URI" ]] && { echo "ERROR: HE_API_DB_POSTGRES_URI 未设置。" >&2; exit 1; }
  local n="${1:-1}"
  echo "⚠  回滚为 best-effort：多数 He-API migration 为 ADDITIVE，无对应 DOWN 脚本。"
  echo "    仅从追踪表移除最近 $n 条记录（不反向执行 SQL）。"
  psql "$PG_URI" -X -q -c "
    DELETE FROM he_api._he_api_schema_migrations
    WHERE version IN (
      SELECT version FROM he_api._he_api_schema_migrations
      ORDER BY version DESC LIMIT $n
    );"
  echo "✓ 已从追踪表移除 $n 条记录。"
}

cmd_dry_run() {
  require psql
  [[ -z "$PG_URI" ]] && { echo "ERROR: HE_API_DB_POSTGRES_URI 未设置。" >&2; exit 1; }
  ensure_track_table
  echo "将应用以下 PG migration："
  list_pending | while read -r f; do echo "  - $(basename "$f")"; done
  echo "ClickHouse: $([[ -n "$CH_URI" ]] && echo "将尝试应用" || echo "跳过（未配置）")"
}

# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------
case "${1:-up}" in
  up)       cmd_up ;;
  status)   cmd_status ;;
  down)     cmd_down "${2:-1}" ;;
  dry-run)  cmd_dry_run ;;
  *)
    echo "用法: $0 {up|status|down N|dry-run}" >&2
    exit 1
    ;;
esac
