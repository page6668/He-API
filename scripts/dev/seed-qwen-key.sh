#!/usr/bin/env bash
#
# scripts/dev/seed-qwen-key.sh — Story 4.2 T0.7 dev-mode bootstrap.
#
# Sources QWEN_UPSTREAM_API_KEY from .env.local (untracked; operator-
# managed). Intended for local development of the apps/adapters/qwen/
# service against the real `dashscope.aliyuncs.com` compat-mode endpoint.
# CI never invokes this script (it's gated behind the .env.local
# existence check).
#
# Production / staging key sourcing is via Vault + External Secrets
# Operator per docs/dev/secrets/qwen-upstream.md.
set -euo pipefail

if [ ! -f .env.local ]; then
  echo "scripts/dev/seed-qwen-key.sh: .env.local not found — skipping (CI lane, no real key needed)."
  exit 0
fi

# shellcheck disable=SC1091
source .env.local

if [ -z "${QWEN_UPSTREAM_API_KEY:-}" ]; then
  echo "scripts/dev/seed-qwen-key.sh: QWEN_UPSTREAM_API_KEY not set in .env.local"
  exit 1
fi

export QWEN_UPSTREAM_API_KEY
echo "QWEN_UPSTREAM_API_KEY sourced (length=${#QWEN_UPSTREAM_API_KEY}); ready for adapter-qwen dev run."
