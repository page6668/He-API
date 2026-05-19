#!/usr/bin/env bash
#
# scripts/dev/seed-deepseek-key.sh — Story 4.1 T0.7 dev-mode bootstrap.
#
# Sources DEEPSEEK_UPSTREAM_API_KEY from .env.local (untracked; operator-
# managed). Intended for local development of the apps/adapters/deepseek/
# service against the real api.deepseek.com endpoint. CI never invokes
# this script (it's gated behind the .env.local existence check).
#
# Production / staging key sourcing is via Vault + External Secrets
# Operator per docs/dev/secrets/deepseek-upstream.md.
set -euo pipefail

if [ ! -f .env.local ]; then
  echo "scripts/dev/seed-deepseek-key.sh: .env.local not found — skipping (CI lane, no real key needed)."
  exit 0
fi

# shellcheck disable=SC1091
source .env.local

if [ -z "${DEEPSEEK_UPSTREAM_API_KEY:-}" ]; then
  echo "scripts/dev/seed-deepseek-key.sh: DEEPSEEK_UPSTREAM_API_KEY not set in .env.local"
  exit 1
fi

export DEEPSEEK_UPSTREAM_API_KEY
echo "DEEPSEEK_UPSTREAM_API_KEY sourced (length=${#DEEPSEEK_UPSTREAM_API_KEY}); ready for adapter-deepseek dev run."
