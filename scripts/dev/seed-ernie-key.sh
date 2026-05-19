#!/usr/bin/env bash
#
# scripts/dev/seed-ernie-key.sh — Story 4.6 T5.3 dev-mode bootstrap.
#
# Sources ERNIE_UPSTREAM_API_KEY from .env.local (untracked; operator-
# managed). Intended for local development of the apps/adapters/ernie/
# service against the real `qianfan.baidubce.com` v2 OpenAI-compatible
# endpoint. CI never invokes this script (it's gated behind the
# .env.local existence check).
#
# Production / staging key sourcing is via Vault + External Secrets
# Operator per docs/dev/secrets/ernie-upstream.md.
set -euo pipefail

if [ ! -f .env.local ]; then
  echo "scripts/dev/seed-ernie-key.sh: .env.local not found — skipping (CI lane, no real key needed)."
  exit 0
fi

# shellcheck disable=SC1091
source .env.local

if [ -z "${ERNIE_UPSTREAM_API_KEY:-}" ]; then
  echo "scripts/dev/seed-ernie-key.sh: ERNIE_UPSTREAM_API_KEY not set in .env.local"
  exit 1
fi

export ERNIE_UPSTREAM_API_KEY
echo "ERNIE_UPSTREAM_API_KEY sourced (length=${#ERNIE_UPSTREAM_API_KEY}); ready for adapter-ernie dev run."
