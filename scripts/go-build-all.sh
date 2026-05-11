#!/usr/bin/env bash
# go-build-all.sh — build every Go module declared in go.work.
#
# Why: Go workspace mode does not support `go build ./...` from the workspace
# root when there is no go.mod at the root. This script enumerates `use`
# entries from go.work and runs `go build ./...` inside each one, which is
# the equivalent of "build everything in the workspace".
#
# Exit non-zero on first module that fails to build.
# Compatible with bash 3.2+ (macOS default).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ ! -f go.work ]]; then
  echo "go.work not found at $ROOT" >&2
  exit 1
fi

MODULES=$(go work edit -json | python3 -c '
import json, sys
data = json.load(sys.stdin)
for entry in data.get("Use", []) or []:
    print(entry["DiskPath"])
')

if [[ -z "$MODULES" ]]; then
  echo "No modules declared in go.work" >&2
  exit 0
fi

count=0
while IFS= read -r mod; do
  [[ -z "$mod" ]] && continue
  echo ">>> go build ./... in $mod"
  ( cd "$mod" && go build ./... )
  count=$((count + 1))
done <<EOF
$MODULES
EOF

echo "OK: built $count Go module(s)"
