#!/usr/bin/env bash
# verify-alertmanager-routes.sh — Story 10.7 AC1 gold gate (BR-10.7.6).
#
# The Alertmanager route tree is the last mile of the alerting pipeline. A
# drifted route (e.g. a critical alert silently mapped to Slack instead of
# PagerDuty), a missing non-blackhole catch-all, or a plaintext secret in the
# committed values is a launch-grade incident — a P0 page that never reaches the
# on-call phone, or a leaked credential. This gate locks those invariants and
# fails CI on any regression. Pure grep/awk — no helm/yq dependency (mirrors the
# Story 5.4 / 7.2 verify-cron-schedule.sh convention).
#
# Invariants (Architect-ratified OQ-10.7-1):
#   1. severity=critical → pagerduty                  (the gold mapping)
#   2. severity=critical ALSO → 飞书 mirror + continue  (P0 dual-channel, OQ-1.3)
#   3. an explicit non-blackhole catch-all receiver    (OQ-1.4)
#   4. receiver secrets via *_file only — never inline (OQ-1.2 / BR-10.7.3)
#
# Usage: verify-alertmanager-routes.sh [values-file]
#   (defaults to the kube-prometheus-stack staging values; the optional arg lets
#    the test harness point it at drift fixtures.)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VALUES="${1:-$ROOT/infra/helm/observability/kube-prometheus-stack/values-staging.yaml}"

note() { printf '[verify-alertmanager-routes] %s\n' "$*"; }
fail() { note "FAIL: $*"; exit 1; }

[ -f "$VALUES" ] || fail "values file not found: $VALUES"

# --- collect every receiver a `severity = "critical"` matcher routes to -------
# awk: on a matchers line containing severity="critical", read the NEXT
# `receiver:` line's value.
critical_receivers="$(awk '
  /matchers:.*severity[[:space:]]*=[[:space:]]*"critical"/ { want = 1; next }
  want && /receiver:/ { print $2; want = 0 }
' "$VALUES")"

# 1. critical → pagerduty (route-drift gate)
if ! printf '%s\n' "$critical_receivers" | grep -qx "pagerduty"; then
  fail "no route maps severity=critical → pagerduty (route drift = silent P0 loss)"
fi
note "OK — severity=critical → pagerduty present"

# 2. P0 dual-channel: critical also mirrored to 飞书 with continue:true
if ! printf '%s\n' "$critical_receivers" | grep -qx "feishu"; then
  fail "P0 dual-channel missing: critical is not mirrored to 飞书"
fi
if ! grep -Eq "^[[:space:]]*continue:[[:space:]]*true[[:space:]]*$" "$VALUES"; then
  fail "P0 dual-channel missing: no 'continue: true' on the critical route"
fi
note "OK — P0 dual-channel (PagerDuty + 飞书 mirror, continue:true)"

# 3. explicit non-blackhole catch-all
if ! grep -Eq "^[[:space:]]*receiver:[[:space:]]*catch-all[[:space:]]*$" "$VALUES"; then
  fail "route has no explicit catch-all default receiver (unmatched alert would vanish)"
fi
if ! grep -Eq "^[[:space:]]*-[[:space:]]*name:[[:space:]]*catch-all[[:space:]]*$" "$VALUES"; then
  fail "no receiver named 'catch-all' is defined"
fi
# the catch-all must actually notify (have a *_configs notifier) — not a blackhole
if ! awk '
  /^[[:space:]]*-[[:space:]]*name:[[:space:]]*catch-all[[:space:]]*$/ { inblock = 1; next }
  inblock && /^[[:space:]]*-[[:space:]]*name:/ { inblock = 0 }
  inblock && /_configs:/ { g = 1 }
  END { exit (g ? 0 : 1) }
' "$VALUES"; then
  fail "catch-all receiver is a blackhole (no *_configs notifier)"
fi
note "OK — explicit non-blackhole catch-all receiver"

# 4. secret hygiene — only *_file references, never inline secrets.
# Match the `<key>:` form anywhere on the line (covers the `- routing_key:` list
# item); the trailing colon distinguishes it from the safe `<key>_file:` variant.
grep -Eq "(^|[[:space:]-])routing_key:[[:space:]]"        "$VALUES" && fail "inline PagerDuty routing_key (use routing_key_file)" || true
grep -Eq "(^|[[:space:]-])api_url:[[:space:]]"            "$VALUES" && fail "inline Slack api_url (use api_url_file)"          || true
grep -Eq "(^|[[:space:]-])smtp_auth_password:[[:space:]]" "$VALUES" && fail "inline SMTP password (use smtp_auth_password_file)" || true
if grep -Eq "hooks\.slack\.com|open\.feishu\.cn/open-apis|events\.pagerduty\.com/v2|integration_key" "$VALUES"; then
  fail "plaintext webhook / integration secret detected in committed values"
fi
note "OK — no inline secrets (receivers reference *_file only)"

note "OK — Alertmanager route integrity locked (critical→pagerduty + dual-channel + catch-all + secret-ref)"
