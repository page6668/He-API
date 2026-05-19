#!/usr/bin/env bash
#
# scripts/dev/seed-doubao-key.sh — Story 4.5 T5.3 dev-mode bootstrap.
#
# Sources DOUBAO_UPSTREAM_API_KEY + DOUBAO_PRO_ENDPOINT_ID + DOUBAO_LITE_ENDPOINT_ID
# from .env.local (untracked; operator-managed). Intended for local development
# of the apps/adapters/doubao/ service against the real
# `ark.cn-beijing.volces.com` v3 OpenAI-compatible endpoint. CI never invokes
# this script (it's gated behind the .env.local existence check).
#
# Production / staging key sourcing is via Vault + External Secrets Operator
# per docs/dev/secrets/doubao-upstream.md. Endpoint-id sourcing is via the
# `doubao-endpoint-ids` ConfigMap per Architect Round 1 OQ-4.5-4 ratification
# (endpoint ids identify resources, not authorise access).
set -euo pipefail

if [ ! -f .env.local ]; then
  echo "scripts/dev/seed-doubao-key.sh: .env.local not found — skipping (CI lane, no real key needed)."
  exit 0
fi

# shellcheck disable=SC1091
source .env.local

if [ -z "${DOUBAO_UPSTREAM_API_KEY:-}" ]; then
  echo "scripts/dev/seed-doubao-key.sh: DOUBAO_UPSTREAM_API_KEY not set in .env.local"
  exit 1
fi
if [ -z "${DOUBAO_PRO_ENDPOINT_ID:-}" ]; then
  echo "scripts/dev/seed-doubao-key.sh: DOUBAO_PRO_ENDPOINT_ID not set in .env.local (BR-1.12 fail-fast trigger)"
  exit 1
fi
if [ -z "${DOUBAO_LITE_ENDPOINT_ID:-}" ]; then
  echo "scripts/dev/seed-doubao-key.sh: DOUBAO_LITE_ENDPOINT_ID not set in .env.local (BR-1.12 fail-fast trigger)"
  exit 1
fi

export DOUBAO_UPSTREAM_API_KEY DOUBAO_PRO_ENDPOINT_ID DOUBAO_LITE_ENDPOINT_ID
echo "DOUBAO_UPSTREAM_API_KEY sourced (length=${#DOUBAO_UPSTREAM_API_KEY})"
echo "DOUBAO_PRO_ENDPOINT_ID  sourced (${DOUBAO_PRO_ENDPOINT_ID})"
echo "DOUBAO_LITE_ENDPOINT_ID sourced (${DOUBAO_LITE_ENDPOINT_ID})"
echo "ready for adapter-doubao dev run."
