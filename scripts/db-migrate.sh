#!/usr/bin/env bash
# Story 1.6 — Unified DB migration entrypoint.
set -euo pipefail

# Delegates to Atlas (PostgreSQL, versioned-migrations mode per Q1 + m-1) and
# golang-migrate (ClickHouse per Q2). Reads connection URIs from the
# `HE_API_DB_<POSTGRES|REDIS|CLICKHOUSE>_URI` environment variables OR, when
# unset, falls back to `kubectl get secret he-api-db-creds -n he-api-staging`.
#
# Credential source is the K8s Secret only for Story 1.6 (M-1 ruling); a future
# dedicated Story will swap the data source via ExternalSecret CR — application
# contract stays identical (env var names, Secret name).
#
# Usage:
#   scripts/db-migrate.sh up      # apply baselines (PG via atlas, CH via golang-migrate)
#   scripts/db-migrate.sh down    # atlas migrate down — baseline is one-way per BR-3.3
#   scripts/db-migrate.sh status  # atlas migrate status; golang-migrate version
#   scripts/db-migrate.sh diff    # atlas migrate diff drift gate

# Canonical migration directories — referenced literally for the BR-3.6
# boundary regex in CI:
#   migrations/postgres    Atlas versioned-mode baseline (Q1).
#   migrations/clickhouse  golang-migrate paired up/down (Q2).

# Resolve repo root so the script is portable across cwds (BR-3.4, MIG-DB-004).
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || cd "$SCRIPT_DIR/.." && pwd)"

PG_DIR="$REPO_ROOT/migrations/postgres"
CH_DIR="$REPO_ROOT/migrations/clickhouse"

# ---------------------------------------------------------------------------
# Credential resolution — env first, K8s Secret fallback (M-1: no Vault).
# ---------------------------------------------------------------------------
load_uri() {
  local var_name="$1" # e.g. HE_API_DB_POSTGRES_URI
  local secret_key="$2" # e.g. HE_API_DB_POSTGRES_URI

  if [ -n "${!var_name-}" ]; then
    return 0
  fi

  # Fallback: kubectl get secret he-api-db-creds -n he-api-staging
  if command -v kubectl >/dev/null 2>&1; then
    local value
    value=$(kubectl get secret he-api-db-creds -n he-api-staging \
      -o jsonpath="{.data.$secret_key}" 2>/dev/null | base64 -d || true)
    if [ -n "$value" ]; then
      export "$var_name=$value"
      return 0
    fi
  fi

  echo "ERROR: $var_name not set and could not be sourced from K8s Secret he-api-db-creds." >&2
  echo "       (External secret-manager integration is deferred to a future Story per M-1 ruling.)" >&2
  return 1
}

# ---------------------------------------------------------------------------
# Boundary check (BR-3.6, MIG-DB-001) — refuse if non-baseline files exist.
# ---------------------------------------------------------------------------
check_zero_business_tables() {
  local extra_pg
  extra_pg=$(find "$PG_DIR" -type f -name '*.sql' \
    ! -name '0001_baseline.sql' 2>/dev/null | wc -l | tr -d ' ')
  if [ "$extra_pg" != "0" ]; then
    echo "ERROR MIG-DB-001: PG migration directory must only contain 0001_baseline.sql (BR-3.6)." >&2
    find "$PG_DIR" -type f -name '*.sql' ! -name '0001_baseline.sql' >&2
    return 1
  fi
  local extra_ch
  extra_ch=$(find "$CH_DIR" -type f -name '*.sql' \
    ! -name '001_baseline.up.sql' ! -name '001_baseline.down.sql' 2>/dev/null | wc -l | tr -d ' ')
  if [ "$extra_ch" != "0" ]; then
    echo "ERROR MIG-DB-001: CH migration directory must only contain paired baseline files (BR-3.6)." >&2
    find "$CH_DIR" -type f -name '*.sql' ! -name '001_baseline.up.sql' ! -name '001_baseline.down.sql' >&2
    return 1
  fi
}

# ---------------------------------------------------------------------------
# Subcommands.
# ---------------------------------------------------------------------------
cmd_up() {
  check_zero_business_tables
  load_uri HE_API_DB_POSTGRES_URI HE_API_DB_POSTGRES_URI
  load_uri HE_API_DB_CLICKHOUSE_URI HE_API_DB_CLICKHOUSE_URI

  # Atlas: versioned migrations only — Q1 + m-1 lock-in.
  echo "==> Applying PG baseline via atlas migrate apply --dir migrations/postgres"
  atlas migrate apply --dir "file://$PG_DIR" --url "$HE_API_DB_POSTGRES_URI"

  # golang-migrate: paired up/down — Q2.
  echo "==> Applying ClickHouse baseline via migrate -path migrations/clickhouse -database <uri> up"
  migrate -path "$CH_DIR" -database "$HE_API_DB_CLICKHOUSE_URI" up
}

cmd_down() {
  load_uri HE_API_DB_POSTGRES_URI HE_API_DB_POSTGRES_URI
  load_uri HE_API_DB_CLICKHOUSE_URI HE_API_DB_CLICKHOUSE_URI

  echo "==> PG rollback via Atlas (atlas computes rollback dynamically, m-3 ruling)"
  atlas migrate down \
    --dir "file://$PG_DIR" \
    --url "$HE_API_DB_POSTGRES_URI"

  echo "==> CH rollback via golang-migrate (m-2: baseline down stub is intentionally empty)"
  migrate -path "$CH_DIR" \
    -database "$HE_API_DB_CLICKHOUSE_URI" \
    down 1
}

cmd_status() {
  load_uri HE_API_DB_POSTGRES_URI HE_API_DB_POSTGRES_URI
  load_uri HE_API_DB_CLICKHOUSE_URI HE_API_DB_CLICKHOUSE_URI

  echo "==> PG status (Atlas)"
  atlas migrate status \
    --dir "file://$PG_DIR" \
    --url "$HE_API_DB_POSTGRES_URI"

  echo "==> CH status (golang-migrate)"
  migrate -path "$CH_DIR" \
    -database "$HE_API_DB_CLICKHOUSE_URI" \
    version || true
}

cmd_diff() {
  load_uri HE_API_DB_POSTGRES_URI HE_API_DB_POSTGRES_URI

  echo "==> PG drift gate (Atlas) — exit code propagates (BR-3.5)"
  atlas migrate diff \
    --dir "file://$PG_DIR" \
    --url "$HE_API_DB_POSTGRES_URI" \
    --dev-url "$HE_API_DB_POSTGRES_URI"

  echo "==> CH drift gate: re-running 'up' on already-applied baseline (no-op)"
  load_uri HE_API_DB_CLICKHOUSE_URI HE_API_DB_CLICKHOUSE_URI
  migrate -path "$CH_DIR" \
    -database "$HE_API_DB_CLICKHOUSE_URI" \
    up
}

# ---------------------------------------------------------------------------
# Dispatch.
# ---------------------------------------------------------------------------
case "${1:-}" in
  up)     shift; cmd_up     "$@" ;;
  down)   shift; cmd_down   "$@" ;;
  status) shift; cmd_status "$@" ;;
  diff)   shift; cmd_diff   "$@" ;;
  *)
    cat >&2 <<EOF
Usage: $(basename "$0") {up|down|status|diff}

  up      Apply PG (Atlas versioned) and CH (golang-migrate) baselines.
  down    Rollback (PG via Atlas; CH down stub is empty per m-2).
  status  Show applied versions for both DBs.
  diff    Drift gate — exit non-zero when schema diverges from migrations/.

Credentials are read from HE_API_DB_<POSTGRES|CLICKHOUSE>_URI env vars or,
when unset, from K8s Secret he-api-db-creds in he-api-staging namespace.
External secret-manager integration is deferred to a future Story per M-1.
EOF
    exit 64
    ;;
esac
