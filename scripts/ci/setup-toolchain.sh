#!/usr/bin/env bash
# setup-toolchain.sh — install Node 20 / pnpm 9 / Go 1.22 locally so developers
# can reproduce GitHub Actions CI behavior on a workstation.
#
# CI uses `actions/setup-node@v4`, `pnpm/action-setup@v4`, `actions/setup-go@v5`.
# Locally we either rely on Volta / nvm / fnm / asdf / Homebrew (whichever is
# present) or print install instructions so engineers can pick one.
#
# Safe to run multiple times; exits 0 when the required versions are already on
# PATH.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

NODE_REQ="$(cat "$ROOT/.nvmrc")"          # e.g. 20
PNPM_REQ_RAW="$(node -p "require('./package.json').packageManager")"
PNPM_REQ="${PNPM_REQ_RAW#pnpm@}"          # e.g. 9.15.9
GO_REQ="1.22"

have() { command -v "$1" >/dev/null 2>&1; }

note() { printf '[setup-toolchain] %s\n' "$*"; }

check_node() {
  if ! have node; then
    note "node not found — install Node ${NODE_REQ} via nvm/fnm/volta/asdf (CI uses actions/setup-node@v4 with node-version-file=.nvmrc)."
    return 1
  fi
  local cur major
  cur="$(node -v)"; cur="${cur#v}"
  major="${cur%%.*}"
  if [ "$major" != "$NODE_REQ" ]; then
    note "node ${cur} present, but Node ${NODE_REQ}.x expected (per .nvmrc)."
  else
    note "node ${cur} OK"
  fi
}

check_pnpm() {
  if ! have pnpm; then
    note "pnpm not found — install via 'corepack enable && corepack prepare pnpm@${PNPM_REQ} --activate' (CI uses pnpm/action-setup@v4 reading packageManager)."
    return 1
  fi
  local cur
  cur="$(pnpm -v)"
  if [ "$cur" != "$PNPM_REQ" ]; then
    note "pnpm ${cur} present, but pnpm ${PNPM_REQ} expected (per packageManager field). Run 'corepack prepare pnpm@${PNPM_REQ} --activate'."
  else
    note "pnpm ${cur} OK"
  fi
}

check_go() {
  if ! have go; then
    note "go not found — install Go ${GO_REQ}+ from https://go.dev/dl/ (CI uses actions/setup-go@v5 with go-version=${GO_REQ})."
    return 1
  fi
  local cur major minor
  cur="$(go version | awk '{print $3}')"; cur="${cur#go}"
  major="${cur%%.*}"
  minor="$(printf '%s' "$cur" | awk -F. '{print $2}')"
  if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 22 ]; }; then
    note "go ${cur} present, but Go ${GO_REQ}+ expected."
  else
    note "go ${cur} OK"
  fi
}

note "Checking toolchain against CI expectations…"
status=0
check_node || status=1
check_pnpm || status=1
check_go   || status=1

if [ "$status" -ne 0 ]; then
  note "One or more tools missing. See messages above."
  exit 1
fi

note "All required tools present. To exactly mirror CI: 'pnpm install --frozen-lockfile && pnpm lint && pnpm test && CGO_ENABLED=1 go test ./apps/api-gateway/... -race'."
