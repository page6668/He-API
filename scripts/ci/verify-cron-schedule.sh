#!/usr/bin/env bash
# verify-cron-schedule.sh — Story 5.4 BR-3.1 single-source-of-truth gate.
#
# Asserts the monthly-cost-reset CronJob schedule is EXACTLY "0 0 1 * *" (00:00
# UTC on day 1 of each month). A drifted schedule could reset the cost-cap
# breakers mid-month (a cost-control compromise), so this golden-file check
# fails CI on any change to the literal.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMPLATE="$ROOT/infra/helm/auth-svc/templates/cronjob-monthly-cost-reset.yaml"
VALUES="$ROOT/infra/helm/auth-svc/values.yaml"
EXPECTED='0 0 1 * *'

note() { printf '[verify-cron-schedule] %s\n' "$*"; }

fail() { note "FAIL: $*"; exit 1; }

[ -f "$TEMPLATE" ] || fail "CronJob template not found: $TEMPLATE"
[ -f "$VALUES" ]   || fail "values.yaml not found: $VALUES"

# The template references the schedule via .Values.monthlyCostReset.schedule;
# the literal lives in values.yaml. Assert the exact string is bound to the
# `schedule:` key (anchored so a stray comment containing the literal cannot
# satisfy the gate).
if ! grep -Eq "^[[:space:]]*schedule:[[:space:]]*\"0 0 1 \* \*\"[[:space:]]*(#.*)?$" "$VALUES"; then
  fail "schedule key in $VALUES is not exactly \"$EXPECTED\""
fi

# Defence-in-depth: ensure no rogue alternate schedule literal slipped into the
# template (it must NOT hard-code a different cron expression).
if grep -E 'schedule:\s*"[0-9*/, ]+"' "$TEMPLATE" | grep -qvF "$EXPECTED" ; then
  # Only flag if a quoted literal schedule that ISN'T the expected one exists.
  if grep -E 'schedule:\s*"[0-9*/, ]+"' "$TEMPLATE" | grep -qv 'Values.monthlyCostReset.schedule'; then
    fail "template hard-codes a schedule literal other than \"$EXPECTED\""
  fi
fi

note "OK — monthly-cost-reset schedule is \"$EXPECTED\" (BR-3.1)"
