#!/usr/bin/env bash
#
# scripts/dev/seed-kimi-key.sh — Story 4.3 T0.7 dev-mode bootstrap.
#
# Sources KIMI_UPSTREAM_API_KEY from .env.local (untracked; operator-
# managed). Intended for local development of the apps/adapters/kimi/
# service against the real `api.moonshot.cn` OpenAI-compatible endpoint.
# CI never invokes this script (it's gated behind the .env.local
# existence check).
#
# Production / staging key sourcing is via Vault + External Secrets
# Operator per docs/dev/secrets/kimi-upstream.md.
set -euo pipefail

if [ ! -f .env.local ]; then
  echo "scripts/dev/seed-kimi-key.sh: .env.local not found — skipping (CI lane, no real key needed)."
  exit 0
fi

# shellcheck disable=SC1091
source .env.local

if [ -z "${KIMI_UPSTREAM_API_KEY:-}" ]; then
  echo "scripts/dev/seed-kimi-key.sh: KIMI_UPSTREAM_API_KEY not set in .env.local"
  exit 1
fi

export KIMI_UPSTREAM_API_KEY
echo "KIMI_UPSTREAM_API_KEY sourced (length=${#KIMI_UPSTREAM_API_KEY}); ready for adapter-kimi dev run."
