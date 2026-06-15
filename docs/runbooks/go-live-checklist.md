# Runbook — Go-Live Checklist (Story 10.7 AC3)

> **Purpose**: a single, operator-runnable launch-readiness gate that statically
> unifies the Epic 1–10 DoD across six domains. This is **not** a new set of
> requirements — every item references an existing story DoD / architecture
> section / runbook (`[Source: …]`, BR-10.7.16). It is the go-live gate the
> operator + PO walk **before Story 10.8 flips the Beta 公测 switch**; this
> runbook does **not** flip that switch.
>
> **Form (OQ-10.7-3)**: doc-primary (this file) + a lightweight machine verifier
> (`scripts/ci/verify-go-live-checklist.sh`) that checks the mechanically-checkable
> items (dashboards / PrometheusRules / fx-refresh CronJob / release pipelines
> present). Items marked **[manual]** are operator judgement; **[verifier]** are
> machine-checked; **[PO-gated]** require Product-Owner sign-off and are NOT
> auto-executed by this story.

## 0. How to use

1. Run the verifier: `bash scripts/ci/verify-go-live-checklist.sh` — all **[verifier]** items must pass (non-zero exit = a red item; never a false-green, BLIND-ERROR-020).
2. Walk the six domain sections below; tick every box. A `[manual]` item needs the named operator/owner to confirm.
3. Complete the **synthetic-page drill** (§7) — proves the alert→PagerDuty wiring end-to-end, not just config presence.
4. Record sign-off (§8). Any unchecked **[PO-gated]** item stays "PO-blocked" — the pipeline is ready but the irreversible action is not executed.

## 1. 可观测就绪 (Observability)

- [ ] **[verifier]** 5 dashboards present (Gateway / 业务 / 模型 / 支付 / 合规). [Source: docs/architecture/11-可观测性observability.md §11.2; infra/grafana-dashboards/*.json]
- [ ] **[verifier]** Alert wiring live — Alertmanager route + receivers consume the emit-side PrometheusRules. [Source: Story 10.7 AC1; infra/helm/observability/kube-prometheus-stack/values-staging.yaml]
- [ ] **[verifier]** Gateway §11.3 RED rules present (`GatewayP95LatencyHigh` / `GatewayErrorRateHigh` / `UpstreamModelDown` / `DataExportSuspect`). [Source: docs/architecture/11-可观测性observability.md §11.3; infra/helm/api-gateway/templates/prometheusrule.yaml]
- [ ] **[verifier]** Route integrity gold gate green (`critical→pagerduty` + dual-channel + non-blackhole catch-all). [Source: Story 10.7 BR-10.7.6; scripts/ci/verify-alertmanager-routes.sh]
- [ ] **[manual]** Synthetic-page drill passed (see §7). [Source: Story 10.7 BR-10.7.15 / OQ-10.7-3]

## 2. 支付就绪 (Payments)

- [ ] **[manual]** 5 payment channels healthy (Stripe / PayPal / Coinbase USDC / Alipay+ / WeChat). [Source: Stories 7.3–7.6; docs/architecture/tech-stack.md §2.1 "支付集成"]
- [ ] **[verifier]** fx-refresh CronJob present + schedule locked (`0 0 * * *`). [Source: Story 7.2 H-2; infra/helm/billing-svc/templates/cronjob-fx-refresh.yaml; scripts/ci/verify-cron-schedule.sh]
- [ ] **[manual]** monthly-cost-reset CronJob healthy (`0 0 1 * *`). [Source: Story 5.4 BR-3.1; scripts/ci/verify-cron-schedule.sh]

## 3. 合规就绪 (Compliance)

- [ ] **[manual]** §9.1 数据不出境 verified — `DataExportSuspect` == 0 (no overseas egress). [Source: docs/architecture/9-合规架构compliance-architecture.md §9.1; infra/helm/api-gateway/templates/prometheusrule.yaml `DataExportSuspect`]
- [ ] **[manual]** Content-safety (内容安全) filters live. [Source: Epic 8.x]
- [ ] **[manual]** 三件套备案 progress (ICP / 算法 / 生成式 AI). [Source: docs/architecture/9-合规架构compliance-architecture.md §9.2]
- [ ] **[verifier]** Customer-support egress posture = ZERO-PII to Intercom (only `he_request_id`; HMAC dormant; PRC region-gated). [Source: Story 10.7 AC2 / OQ-10.7-2]

## 4. 容灾就绪 (Disaster Recovery)

- [ ] **[manual]** §7.4 DR drill — DNS cutover to the 腾讯云 灾备 at least table-top rehearsed. [Source: docs/architecture/infrastructure-deployment.md §灾备]
- [ ] **[manual]** Backups in-region only (cn-shanghai → cn-shenzhen). [Source: docs/architecture/9-合规架构compliance-architecture.md §9.1]

## 5. 客户面就绪 (Customer-facing)

- [ ] **[manual]** Docs site live (docs.he-api.com) + production DNS cutover done. [Source: Story 10.5; apps/docs]
- [ ] **[verifier]** Console 10-locale i18n complete (en/zh-CN/ja/ko/es/fr/de/pt/ru/ar). [Source: Story 10.1; apps/console/messages/*]
- [ ] **[verifier]** Customer support (Intercom) wired on the authed console face. [Source: Story 10.7 AC2; apps/console/components/business/IntercomMessenger.tsx]

## 6. SDK 首发 (SDK first-release) —承接 R-OQ-10.2-2 / 10.3 / 10.4

- [ ] **[verifier]** OIDC trusted-publishing pipelines present (Python / TypeScript / Go), dry-run only in CI. [Source: Story 10.7 AC3 / OQ-10.7-4; .github/workflows/release-sdk-{python,typescript,go}.yml]
- [ ] **[PO-gated]** PyPI `he-api` name owned + PyPI OIDC publisher configured → trigger real publish. [Source: Story 10.2 R-OQ-2; BR-10.7.14]
- [ ] **[PO-gated]** npm `@he-api` scope owned → trigger real publish. [Source: Story 10.3 R-OQ-10.3-2; BR-10.7.14]
- [ ] **[PO-gated]** Go `github.com/he-api/sdk-go` repo/org created + first `v0.x` tag pushed. [Source: Story 10.4 [[project_story_10_4_go_sdk_precedents]]; BR-10.7.14]

> **PO note**: real publish is a single, irreversible, externally-visible action.
> It is **NOT** executed in Story 10.7 — the pipelines run dry-run only (`twine
> check` / `npm pack` / tag-validation). PO confirms name ownership + provisions
> the publish credentials, then triggers the gated `publish` job manually.

## 7. Synthetic-page drill (manual operator step, OQ-10.7-3)

> Validates AC1 wiring **end-to-end** (not just config presence, BR-10.7.15).

1. Fire a heartbeat / test critical alert (e.g. a temporary always-firing rule, or `amtool` against the staging Alertmanager).
2. Confirm the **on-call phone receives a PagerDuty incident** carrying only operational metadata (alertname / severity / service / runbook_url — **no** user_id/email/IP).
3. Confirm the **飞书 mirror** also fired (P0 dual-channel).
4. Resolve the test alert; confirm the resolved notification.
5. Remove the test rule.

## 8. Sign-off

| Domain | Owner | Status | Date |
|--------|-------|--------|------|
| 可观测 | SRE | ☐ | |
| 支付 | Payments | ☐ | |
| 合规 | Compliance/Legal | ☐ | |
| 容灾 | SRE | ☐ | |
| 客户面 | Frontend/Docs | ☐ | |
| SDK 首发 | PO | ☐ (PO-gated) | |

> Decision lineage: this checklist is the launch-readiness gate that precedes
> Story 10.8 (Beta 公测 switch). It does not flip that switch.
