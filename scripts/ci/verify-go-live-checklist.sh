#!/usr/bin/env bash
# verify-go-live-checklist.sh — Story 10.7 AC3 lightweight go-live verifier.
#
# Machine-checks the mechanically-checkable launch-readiness items (OQ-10.7-3):
# the six checklist sections exist, every checklist item is traceable to a
# [Source: …], and the repo artifacts the checklist points at are present
# (dashboards / PrometheusRules incl. DataExportSuspect / fx-refresh CronJob /
# the three SDK release pipelines). A missing artifact is a non-zero exit with a
# clear message — never a false-green (BLIND-ERROR-020). Runtime items (live
# dashboards, DR drill, real DataExportSuspect==0) stay [manual] in the runbook.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNBOOK="$ROOT/docs/runbooks/go-live-checklist.md"

fails=0
note() { printf '[verify-go-live-checklist] %s\n' "$*"; }
ok()   { note "OK — $*"; }
bad()  { note "FAIL: $*"; fails=$((fails + 1)); }

need_file() { [ -f "$ROOT/$1" ] && ok "$2" || bad "$2 (missing: $1)"; }

# --- runbook structure -------------------------------------------------------
[ -f "$RUNBOOK" ] || { note "FAIL: runbook not found: $RUNBOOK"; exit 1; }

for section in "可观测就绪" "支付就绪" "合规就绪" "容灾就绪" "客户面就绪" "SDK 首发"; do
  if grep -q "$section" "$RUNBOOK"; then ok "section present: $section"; else bad "section missing: $section"; fi
done

# Every checklist item line ("- [ ]") must carry a [Source: …] (no invented gate).
unsourced="$(grep -nE '^- \[ \]' "$RUNBOOK" | grep -vF '[Source:' || true)"
if [ -n "$unsourced" ]; then
  bad "unsourced checklist item(s):"
  printf '%s\n' "$unsourced"
else
  ok "every checklist item is traceable to a [Source: …]"
fi

# SDK first-release items must be explicitly PO-gated (BR-10.7.14 / DATA-020).
if grep -q '\[PO-gated\]' "$RUNBOOK"; then ok "SDK first-release marked PO-gated"; else bad "SDK first-release not marked PO-gated"; fi

# --- machine-checkable artifacts exist --------------------------------------
for dash in auth-svc business compliance gateway model payment; do
  need_file "infra/grafana-dashboards/${dash}.json" "dashboard ${dash}.json"
done
need_file "infra/helm/api-gateway/templates/prometheusrule.yaml" "gateway PrometheusRule"
if grep -q "DataExportSuspect" "$ROOT/infra/helm/api-gateway/templates/prometheusrule.yaml" 2>/dev/null; then
  ok "DataExportSuspect §9.1 egress sentinel rule present"
else
  bad "DataExportSuspect rule missing from the gateway PrometheusRule"
fi
need_file "infra/helm/billing-svc/templates/cronjob-fx-refresh.yaml" "fx-refresh CronJob"
need_file "infra/helm/observability/kube-prometheus-stack/values-staging.yaml" "Alertmanager route config"
need_file ".github/workflows/release-sdk-python.yml" "SDK release pipeline (python)"
need_file ".github/workflows/release-sdk-typescript.yml" "SDK release pipeline (typescript)"
need_file ".github/workflows/release-sdk-go.yml" "SDK release pipeline (go)"

if [ "$fails" -eq 0 ]; then
  note "OK — go-live checklist verifier passed (all machine-checkable items present)"
  exit 0
fi
note "FAIL: $fails check(s) failed — go-live not ready"
exit 1
