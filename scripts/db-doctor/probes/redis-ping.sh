#!/usr/bin/env bash
# Story 1.6 — Redis health probe (Q5 single-source-of-truth).
# Shared by scripts/db-doctor.sh AND infra/helm/db-doctor/templates/configmap.yaml.
# Expects redis-cli on PATH; reads connection params from positional args ($1=host $2=port $3=user $4=pwd).
set -euo pipefail

host="${1:?host required}"
port="${2:-6379}"
user="${3:-default}"
pwd="${4:-}"

response=$(redis-cli -h "$host" -p "$port" --user "$user" --pass "$pwd" --tls --no-auth-warning PING)

if [ "$response" = "PONG" ]; then
  exit 0
fi
echo "expected PONG, got: $response" >&2
exit 1
