#!/usr/bin/env bash
# verify-no-money-path-10.8.test.sh — Story 10.8 boundary-guard tests.
#
# The real tree must PASS (the (b) boundary is intact at HEAD); drift fixtures
# that reintroduce the parked (a) money-path or a beta_mode write surface must
# FAIL. Also asserts the guard does NOT false-fail on tokens that appear only in
# test files (which legitimately reference the parked design).
#
#   10.8-UNIT-005  GrantTrialCredit / trial_grants / 0018 reintroduction → FAIL
#   10.8-INT-004   trial/signup-triggered balances top-up → FAIL
#   10.8-UNIT-012  beta_mode write helper → FAIL
#
# No bats dependency — plain bash, runs locally and in CI.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="$DIR/verify-no-money-path-10.8.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; failc=0
ok()  { printf '  ✓ %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  ✗ %s\n' "$1"; failc=$((failc + 1)); }

expect_pass() { # <label> <root>
  if "$GATE" "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1 (gate failed unexpectedly)"; fi
}
expect_fail() { # <label> <root>
  if "$GATE" "$2" >/dev/null 2>&1; then bad "$1 (gate PASSED a bad fixture — boundary breach undetected)"; else ok "$1"; fi
}

# --- the real repo tree must pass (boundary intact at HEAD) ------------------
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
expect_pass "real tree passes — (b) boundary intact at HEAD" "$REPO_ROOT"

mkfix() { mkdir -p "$1/apps/svc" "$1/packages/p" "$1/migrations/postgres"; }

# --- UNIT-005: GrantTrialCredit reintroduced --------------------------------
F1="$TMP/grant"; mkfix "$F1"
printf 'package svc\nfunc GrantTrialCredit() {}\n' > "$F1/apps/svc/grant.go"
expect_fail "UNIT-005 — GrantTrialCredit in source is rejected" "$F1"

# --- UNIT-005: trial_grants table reintroduced ------------------------------
F2="$TMP/table"; mkfix "$F2"
printf 'CREATE TABLE he_api.trial_grants (id uuid);\n' > "$F2/migrations/postgres/0099_x.sql"
# trial_grants is also checked in apps/packages; put it in source too.
printf 'package p\nconst T = "trial_grants"\n' > "$F2/packages/p/x.go"
expect_fail "UNIT-005 — trial_grants in source is rejected" "$F2"

# --- UNIT-005: migration 0018 reintroduced ----------------------------------
F3="$TMP/mig"; mkfix "$F3"
printf -- '-- additive\n' > "$F3/migrations/postgres/0018_trial_grants.sql"
expect_fail "UNIT-005 — migration 0018+ is rejected" "$F3"

# --- INT-004: trial-triggered balances top-up -------------------------------
F4="$TMP/topup"; mkfix "$F4"
printf 'package svc\n// on trial signup credit the balance\nfunc f(){ creditBalanceSQL() }\n' > "$F4/apps/svc/topup.go"
expect_fail "INT-004 — trial/signup balances top-up is rejected" "$F4"

# --- UNIT-012: beta_mode write helper ---------------------------------------
F5="$TMP/write"; mkfix "$F5"
printf 'package svc\nfunc SetBetaMode(on bool) {}\n' > "$F5/apps/svc/flip.go"
expect_fail "UNIT-012 — SetBetaMode write helper is rejected" "$F5"

# --- negative control: tokens ONLY in a *_test.go file must NOT fail --------
F6="$TMP/testonly"; mkfix "$F6"
printf 'package svc\n// guards absence of GrantTrialCredit / trial_grants\nfunc TestGuard(){}\n' > "$F6/apps/svc/x_test.go"
expect_pass "test-only mention of parked tokens does NOT false-fail" "$F6"

printf '\n[verify-no-money-path-10.8.test] %d passed, %d failed\n' "$pass" "$failc"
[ "$failc" -eq 0 ]
