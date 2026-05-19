#!/usr/bin/env bash
#
# scripts/dev/seed-glm-key.sh — Story 4.4 T5.3 dev-mode bootstrap.
#
# Sources GLM_UPSTREAM_API_KEY from .env.local (untracked; operator-
# managed). Intended for local development of the apps/adapters/glm/
# service against the real `open.bigmodel.cn` v4 OpenAI-compatible
# endpoint. CI never invokes this script (it's gated behind the
# .env.local existence check).
#
# Production / staging key sourcing is via Vault + External Secrets
# Operator per docs/dev/secrets/glm-upstream.md.
set -euo pipefail

if [ ! -f .env.local ]; then
  echo "scripts/dev/seed-glm-key.sh: .env.local not found — skipping (CI lane, no real key needed)."
  exit 0
fi

# shellcheck disable=SC1091
source .env.local

if [ -z "${GLM_UPSTREAM_API_KEY:-}" ]; then
  echo "scripts/dev/seed-glm-key.sh: GLM_UPSTREAM_API_KEY not set in .env.local"
  exit 1
fi

export GLM_UPSTREAM_API_KEY
echo "GLM_UPSTREAM_API_KEY sourced (length=${#GLM_UPSTREAM_API_KEY}); ready for adapter-glm dev run."
