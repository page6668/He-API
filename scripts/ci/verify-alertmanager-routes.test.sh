#!/usr/bin/env bash
# verify-alertmanager-routes.test.sh — Story 10.7 AC1 gold-gate tests.
#
# Exercises the gate against the real staging values (must PASS) and against
# drift fixtures (must FAIL), covering the QA scenarios:
#   10.7-UNIT-001  critical→pagerduty mapping present; route-drift simulation FAILS
#   10.7-UNIT-002  explicit non-blackhole catch-all; absent → FAILS
#   10.7-UNIT-003  secret-scan; inline routing key → FAILS
#   10.7-L-001     critical dual-channel (pagerduty + 飞书 mirror, continue:true)
#
# No bats dependency — plain bash, runs locally and in CI.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="$DIR/verify-alertmanager-routes.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; failc=0
ok()   { printf '  ✓ %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  ✗ %s\n' "$1"; failc=$((failc + 1)); }

# expect_pass <label>            — gate against the real staging file, exit 0
expect_pass() {
  if "$GATE" >/dev/null 2>&1; then ok "$1"; else bad "$1 (gate failed on the real values)"; fi
}
# expect_fail <label> <fixture>  — gate against a fixture, exit != 0
expect_fail() {
  if "$GATE" "$2" >/dev/null 2>&1; then bad "$1 (gate PASSED a bad fixture — drift undetected)"; else ok "$1"; fi
}

# --- the real staging values must pass every invariant ----------------------
expect_pass "UNIT-001/002/003/L-001 — real staging route tree passes the gate"

# --- UNIT-001 route drift: critical → slack instead of pagerduty -------------
cat > "$TMP/drift.yaml" <<'YAML'
alertmanager:
  config:
    route:
      receiver: catch-all
      routes:
        - matchers: ['severity = "critical"']
          receiver: slack
          continue: true
        - matchers: ['severity = "critical"']
          receiver: feishu
    receivers:
      - name: catch-all
        webhook_configs:
          - url_file: /etc/alertmanager/secrets/x/feishu_webhook_url
      - name: slack
        slack_configs:
          - api_url_file: /etc/alertmanager/secrets/x/slack_webhook_url
YAML
expect_fail "UNIT-001 — critical→slack drift is rejected" "$TMP/drift.yaml"

# --- UNIT-002 missing catch-all ---------------------------------------------
cat > "$TMP/no-catchall.yaml" <<'YAML'
alertmanager:
  config:
    route:
      receiver: pagerduty
      routes:
        - matchers: ['severity = "critical"']
          receiver: pagerduty
          continue: true
        - matchers: ['severity = "critical"']
          receiver: feishu
    receivers:
      - name: pagerduty
        pagerduty_configs:
          - routing_key_file: /etc/alertmanager/secrets/x/pagerduty_routing_key
      - name: feishu
        webhook_configs:
          - url_file: /etc/alertmanager/secrets/x/feishu_webhook_url
YAML
expect_fail "UNIT-002 — missing non-blackhole catch-all is rejected" "$TMP/no-catchall.yaml"

# --- UNIT-003 inline plaintext secret ---------------------------------------
cat > "$TMP/inline-secret.yaml" <<'YAML'
alertmanager:
  config:
    route:
      receiver: catch-all
      routes:
        - matchers: ['severity = "critical"']
          receiver: pagerduty
          continue: true
        - matchers: ['severity = "critical"']
          receiver: feishu
    receivers:
      - name: catch-all
        webhook_configs:
          - url_file: /etc/alertmanager/secrets/x/feishu_webhook_url
      - name: pagerduty
        pagerduty_configs:
          - routing_key: R0ABCD1234PLAINTEXTKEY
      - name: feishu
        webhook_configs:
          - url_file: /etc/alertmanager/secrets/x/feishu_webhook_url
YAML
expect_fail "UNIT-003 — inline plaintext routing_key is rejected" "$TMP/inline-secret.yaml"

printf '\n[verify-alertmanager-routes.test] %d passed, %d failed\n' "$pass" "$failc"
[ "$failc" -eq 0 ]
