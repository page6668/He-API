#!/usr/bin/env bash
# verify-cron-schedule.sh — single-source-of-truth schedule gate for He-API's
# money-adjacent CronJobs. A drifted schedule on either cron is a cost-control
# compromise (monthly-cost-reset resets cap breakers; fx-refresh refreshes the
# display rate), so this golden-file check fails CI on any change to a literal.
#
#   - Story 5.4 BR-3.1: monthly-cost-reset MUST be EXACTLY "0 0 1 * *".
#   - Story 7.2 H-2:    fx-refresh         MUST be EXACTLY "0 0 * * *".
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

note() { printf '[verify-cron-schedule] %s\n' "$*"; }
fail() { note "FAIL: $*"; exit 1; }

# verify_schedule <name> <template> <values> <values-key> <expected-literal>
# Asserts the expected literal is bound to the `schedule:` key in values.yaml
# (anchored so a stray comment cannot satisfy the gate) and that the template
# does not hard-code a different literal.
verify_schedule() {
  local name="$1" template="$2" values="$3" key="$4" expected="$5"
  [ -f "$template" ] || fail "$name: CronJob template not found: $template"
  [ -f "$values" ]   || fail "$name: values.yaml not found: $values"

  # Escape the cron literal's regex-special chars (* -> \*) for an anchored match.
  local esc
  esc="$(printf '%s' "$expected" | sed 's/[*]/\\*/g')"
  if ! grep -Eq "^[[:space:]]*schedule:[[:space:]]*\"${esc}\"[[:space:]]*(#.*)?$" "$values"; then
    fail "$name: schedule key in $values is not exactly \"$expected\""
  fi

  # Defence-in-depth: the template must reference the values key, not a literal.
  if grep -E 'schedule:\s*"[0-9*/, ]+"' "$template" | grep -qv "Values.${key}.schedule"; then
    fail "$name: template hard-codes a schedule literal other than \"$expected\""
  fi

  note "OK — $name schedule is \"$expected\""
}

verify_schedule "monthly-cost-reset" \
  "$ROOT/infra/helm/auth-svc/templates/cronjob-monthly-cost-reset.yaml" \
  "$ROOT/infra/helm/auth-svc/values.yaml" \
  "monthlyCostReset" \
  "0 0 1 * *"

verify_schedule "fx-refresh" \
  "$ROOT/infra/helm/billing-svc/templates/cronjob-fx-refresh.yaml" \
  "$ROOT/infra/helm/billing-svc/values.yaml" \
  "fxRefresh" \
  "0 0 * * *"

note "OK — all money-adjacent cron schedules locked (BR-3.1 + H-2)"
