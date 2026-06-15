#!/usr/bin/env bash
# verify-no-money-path-10.8.sh — Story 10.8 boundary guard (AC1, ratified OQ-10.8-1 = (b)).
#
# Story 10.8 ships NO backend money code: the "$5 试用额度" is the EXISTING Free-tier
# monthly entitlement (7.8), auto-resolved via default-to-free — not a new credit
# grant. The parked (a) money-path (GrantTrialCredit RPC / trial_grants table /
# migration 0018 / accumulative credit SQL) is OUT-OF-SCOPE unless PO overturns
# OQ-1 + 7.8:246 (a separate future story). This gate is a static, false-green-proof
# guard that the boundary holds in source (NOT in story/QA docs, which legitimately
# name the parked design).
#
# It also guards Q-ADMIN-BETA (BR-10.8.8): there is NO He-API beta_mode write
# surface — the flip is operated through Unleash only; this repo is read-only.
#
# QA scenario trace -> docs/qa/assessments/10.8-test-design-20260616.md:
#   10.8-UNIT-005  no GrantTrialCredit / trial_grants / 0018 / balances top-up / new credit SQL
#   10.8-INT-004   zero money-write on the new-verified Beta read path
#   10.8-UNIT-012  no He-API beta_mode write surface/endpoint (Q-ADMIN-BETA)
#
# Usage: verify-no-money-path-10.8.sh [ROOT]   (ROOT default: repo root)
# Exit: 0 = boundary intact; 1 = a forbidden money-path / write-surface token found.
set -uo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

fails=0
note() { printf '[verify-no-money-path-10.8] %s\n' "$*"; }
ok()   { note "OK — $*"; }
bad()  { note "FAIL: $*"; fails=$((fails + 1)); }

# Source code only: apps/ + packages/ (+ migrations/), excluding tests and the
# story/QA markdown (which legitimately name the parked (a) design). The guard
# script itself lives under scripts/ and is excluded by the dir scope.
CODE_DIRS=()
for d in apps packages; do [ -d "$ROOT/$d" ] && CODE_DIRS+=("$ROOT/$d"); done

# grep over source, skipping test files / __tests__ / tests dirs / node_modules / .next.
src_grep() {
  [ ${#CODE_DIRS[@]} -eq 0 ] && return 1
  grep -rIlE "$1" "${CODE_DIRS[@]}" \
    --exclude='*_test.go' \
    --exclude='*.test.ts' \
    --exclude='*.test.tsx' \
    --exclude-dir='__tests__' \
    --exclude-dir='tests' \
    --exclude-dir='node_modules' \
    --exclude-dir='.next' \
    --exclude-dir='dist' \
    2>/dev/null
}

check_absent() { # <regex> <label>
  local hits
  hits="$(src_grep "$1" || true)"
  if [ -n "$hits" ]; then
    bad "$2 — found in source:"; printf '    %s\n' $hits
  else
    ok "$2"
  fi
}

# 10.8-UNIT-005 — the parked (a) money-path must NOT exist in source.
check_absent 'GrantTrialCredit'                 'no GrantTrialCredit RPC (parked (a))'
check_absent 'trial_grants'                     'no trial_grants table reference (parked (a))'

# migration 0018+ (the parked (a) migration). The catalogue/flag work needs no new
# migration; the latest legitimate migration is 0017.
if ls "$ROOT"/migrations/postgres/0018_* >/dev/null 2>&1; then
  bad "no migration 0018+ (parked (a) trial-grant migration)"
else
  ok "no migration 0018+ (latest legitimate migration is 0017)"
fi

# 10.8-INT-004 — no NEW credit/top-up write keyed to trial/beta signup. A balances
# credit triggered by a trial/signup is the (a) path; legitimate 7.x credit-on-
# payment is unrelated. File-level co-occurrence (not line-scoped): a source file
# that mentions BOTH a trial/signup trigger AND a balances-credit-write primitive
# is the (a) shape — flag it.
trigger_files="$(src_grep '(trial|signup)' || true)"
topup_label='no trial/signup-triggered balances top-up (parked (a))'
if [ -n "$trigger_files" ]; then
  offenders=""
  for f in $trigger_files; do
    if grep -IqE '(creditBalanceSQL|top.?up|trial_credit|current_usd[[:space:]]*\+)' "$f" 2>/dev/null; then
      offenders="$offenders $f"
    fi
  done
  if [ -n "$offenders" ]; then
    bad "$topup_label — found in source:"; printf '    %s\n' $offenders
  else
    ok "$topup_label"
  fi
else
  ok "$topup_label"
fi

# 10.8-UNIT-012 — no He-API beta_mode WRITE surface (Q-ADMIN-BETA): the flip is
# Unleash-only; this repo reads beta_mode (featureflag.Reader) but never writes it.
check_absent '(Set|Update|Flip|Toggle|Write|Enable|Disable)BetaMode' \
  'no beta_mode write helper (Q-ADMIN-BETA — Unleash-only flip)'
check_absent 'UPDATE[[:space:]].*feature_flags' \
  'no SQL UPDATE of feature_flags (no He-API flag write surface)'

if [ "$fails" -eq 0 ]; then
  note "OK — (b) boundary intact: no money-path, no beta_mode write surface"
  exit 0
fi
note "FAIL: $fails boundary violation(s) — (b) scope breached (see OQ-10.8-1 + 7.8:246)"
exit 1
