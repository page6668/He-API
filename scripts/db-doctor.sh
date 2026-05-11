#!/usr/bin/env bash
# Story 1.6 — DB Doctor self-check (Q5 ruling: Bash local path).
set -euo pipefail

# Probes PostgreSQL (psql) + Redis Tair (redis-cli) + ClickHouse
# (clickhouse-client) from a developer workstation. The SQL/shell queries live
# in scripts/db-doctor/probes/ as the single source-of-truth shared with the
# cluster-side Helm chart infra/helm/db-doctor/templates/configmap.yaml.
#
# Exit codes (BR-4.1):
#   0  all checks PASS
#   1  any check FAIL
#   2  config or credentials missing
#
# Output (BR-4.2): one grep-able line per check, prefix [PASS] or [FAIL].
#
# Credentials are loaded from ~/.he-api/staging.env (gitignored).

# Portable cwd — resolve repo root via git or dirname walk (matches db-migrate.sh).
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || cd "$SCRIPT_DIR/.." && pwd)"
PROBES_DIR="$REPO_ROOT/scripts/db-doctor/probes"

ENV_FILE="${HE_API_DOCTOR_ENV:-$HOME/.he-api/staging.env}"
if [ ! -r "$ENV_FILE" ]; then
  echo "[FAIL] config: cannot read $ENV_FILE — populate from K8s Secret he-api-db-creds" >&2
  exit 2
fi
# shellcheck source=/dev/null
source "$ENV_FILE"

: "${HE_API_DB_POSTGRES_URI:?HE_API_DB_POSTGRES_URI required in $ENV_FILE}"
: "${HE_API_DB_REDIS_URI:?HE_API_DB_REDIS_URI required in $ENV_FILE}"
: "${HE_API_DB_CLICKHOUSE_URI:?HE_API_DB_CLICKHOUSE_URI required in $ENV_FILE}"

FAIL_COUNT=0
PASS_COUNT=0

elapsed_ms() {
  # Cross-platform ms — uses python3 if available, falls back to date +%s*1000.
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import time; print(int(time.time()*1000))'
  else
    echo "$(($(date +%s) * 1000))"
  fi
}

check() {
  local label="$1"
  shift
  local start end
  start="$(elapsed_ms)"
  if "$@" >/dev/null 2>&1; then
    end="$(elapsed_ms)"
    echo "[PASS] $label (latency=$((end - start))ms)"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    end="$(elapsed_ms)"
    echo "[FAIL] $label (latency=$((end - start))ms)"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# --- PostgreSQL: SELECT 1 + baseline migration check ------------------------
check "postgres: SELECT 1 OK" \
  psql "$HE_API_DB_POSTGRES_URI" -v ON_ERROR_STOP=1 -f "$PROBES_DIR/postgres.sql"

# Atlas baseline version assertion via atlas migrate status (Q1 ruling).
# If atlas binary unavailable, the operator can fall back to a direct
# SELECT against atlas_schema_revisions (table is created by Atlas on first apply).
check "postgres: baseline migration applied" \
  bash -c 'atlas migrate status --url "$HE_API_DB_POSTGRES_URI" --dir "file://'"$REPO_ROOT"'/migrations/postgres" 2>/dev/null | grep -q "Current Version" || psql "$HE_API_DB_POSTGRES_URI" -tAc "SELECT 1 FROM atlas_schema_revisions WHERE version=$$0001$$ LIMIT 1" | grep -q 1'

# --- Redis: PING ------------------------------------------------------------
# Parse rediss://user:pass@host:port/db into positional args for the probe.
parse_redis_uri() {
  python3 - "$HE_API_DB_REDIS_URI" <<'PYEOF'
import sys
from urllib.parse import urlparse
u = urlparse(sys.argv[1])
print(u.hostname or "")
print(u.port or 6379)
print(u.username or "default")
print(u.password or "")
PYEOF
}
readarray -t REDIS_PARTS < <(parse_redis_uri)
# The probe wraps the redis-cli PING call so the local + cluster paths share
# the same script.
check "redis: PING -> PONG" \
  bash "$PROBES_DIR/redis-ping.sh" "${REDIS_PARTS[0]}" "${REDIS_PARTS[1]}" "${REDIS_PARTS[2]}" "${REDIS_PARTS[3]}"

# --- ClickHouse: SELECT 1 + baseline migration check ------------------------
check "clickhouse: SELECT 1 OK" \
  clickhouse-client --secure --host "$(python3 -c 'import sys,urllib.parse as u;p=u.urlparse(sys.argv[1]);print(p.hostname)' "$HE_API_DB_CLICKHOUSE_URI")" \
  --password "$(python3 -c 'import sys,urllib.parse as u;p=u.urlparse(sys.argv[1]);print(p.password or "")' "$HE_API_DB_CLICKHOUSE_URI")" \
  --user "$(python3 -c 'import sys,urllib.parse as u;p=u.urlparse(sys.argv[1]);print(p.username or "default")' "$HE_API_DB_CLICKHOUSE_URI")" \
  --queries-file "$PROBES_DIR/clickhouse.sql"

# golang-migrate baseline version assertion — schema_migrations table or
# `migrate version` CLI. Either signal proves the baseline ran.
check "clickhouse: baseline migration applied" \
  bash -c 'migrate -path "'"$REPO_ROOT"'/migrations/clickhouse" -database "$HE_API_DB_CLICKHOUSE_URI" version 2>/dev/null | grep -qE "^1" || clickhouse-client -q "SELECT version FROM schema_migrations WHERE database=$$he_api$$ AND version=1"'

TOTAL=$((PASS_COUNT + FAIL_COUNT))
if [ "$FAIL_COUNT" -gt 0 ]; then
  echo "$FAIL_COUNT/$TOTAL checks FAILED"
  exit 1
fi
echo "All checks passed ($PASS_COUNT/$TOTAL)"
exit 0
