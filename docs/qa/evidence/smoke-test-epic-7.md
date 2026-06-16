# Smoke Test Report: Epic 7

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 7 — 计费、多币种与支付 (Billing, Multi-currency & Payments) |
| **Trigger**      | manual (`QA *smoke-test 7`)        |
| **Executed At**  | 2026-06-16T08:14:36Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 7.1 | 计费引擎（按 token / 按调用 + markup 5-15%） | Done |
| 7.2 | 多币种 + 汇率刷新 | Done |
| 7.3 | Stripe + PayPal 集成 | Done |
| 7.4 | USDC（Coinbase Commerce）集成 | Done |
| 7.5 | 支付宝 Alipay+ 国际版集成 | Done |
| 7.6 | 微信支付 WeChat Pay HK / Cross-border | Done |
| 7.7 | 自动充值 + 余额预警 + 月度账单 PDF | Done |
| 7.8 | 订阅档（Free/Pro/Team/Enterprise）+ Beta 模式开关 | Done |

- **Total Stories**: 8
- **Done**: 8
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

> Epic 7 is a backend payments/billing epic. The regression vehicle is the Go
> service test suite (`billing-svc`, `payment-svc`, `api-gateway` billing/fx/payment
> surfaces) plus the console Beta-mode unit suite — **not** Playwright, which has no
> `7.*` specs. All suites were executed live from source (no `--reporter=json`
> harness; `go test ./...` + `vitest run`).

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | ~295 (120 billing-svc + 62 payment-svc + 102 api-gateway Epic-7 + 11 console beta) |
| **Tests Failed**| 0                |

### Suite Breakdown

| Module | Command | Result |
|--------|---------|--------|
| `apps/billing-svc` | `go test ./...` | ok — pricing, credit, ledger, fx, invoice, recharge, autorecharge, lowbalance, subscription, paymentmethod, consumer, grpc, fx-refresh cmd |
| `apps/payment-svc` | `go test ./...` | ok — providers stripe/paypal/coinbase/alipay/wechat, webhook ingress (coinbase/alipay/wechat/generic), paymentgrpc off-session, payment_completed producer |
| `apps/api-gateway` (Epic-7 pkgs) | `go test ./internal/handlers/... ./internal/fxrate/... ./internal/billingemit/... ./internal/middleware/billinggate/... ./internal/openaierr/... ./tests/...` | ok — billing_write/read/webhook/autorecharge handlers, fxrate convert/snapshot, billingemit emitter, billinggate, payment_metadata, 7.1/7.2/7.8 skeleton tests |
| `apps/console` | `vitest run __tests__/10.8-beta-mode-toggle.test.tsx` | ok — 11/11 (fail-safe-OFF parsing, env-driven Beta badge) |

No failed tests.

## 3. Core User Journeys

> **Verification mode note:** Live browser E2E of payment checkout could **not** be
> exercised in this environment — there is no running multi-service stack
> (docker/live gateway unavailable locally) and each provider journey terminates at
> an **external sandbox** (Stripe/PayPal/Coinbase/Alipay/WeChat). Each journey below
> was therefore verified at the **integration/unit level** against the named test
> evidence. "Actual" cites the passing test surface; live-browser confirmation is
> tracked as `SMOKE-7-001` in §5.

### Journey 1: Usage metering → cost with markup → ledger debit (7.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | API call metered (per-token / per-call / per-char / per-min) | Cost computed at correct unit price | `billing-svc/internal/pricing/*` (pricing, perchar, permin, loader) pass | PASS |
| 2 | Apply 5-15% markup | Markup applied within band | pricing tests pass | PASS |
| 3 | Debit ledger + check balance gate | usage_ledger debited; gateway gate enforces balance | `ledger_test`, `credit_test`, `billinggate_test`, `billingemit/emitter_test` pass | PASS |

### Journey 2: Multi-currency + FX refresh (7.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Refresh FX rates | fx_rates rows updated; fail-closed if missing | `billing-svc/internal/fx/{provider,refresh}_test`, `cmd/fx-refresh` pass | PASS |
| 2 | Convert amount at snapshot rate | Correct conversion + snapshot pinning | gateway `fxrate/{convert,snapshot}_test` pass | PASS |

### Journey 3: Card top-up via Stripe / PayPal (7.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Create recharge order | Order created at provider | `payment-svc/provider/{stripe,paypal}`, `recharge/order_test` pass | PASS |
| 2 | Provider webhook → payment_completed | Balance credited idempotently | `webhook/handler_test`, `producer/payment_completed_test`, gateway `billing_webhook_test` pass | PASS |

### Journey 4: USDC via Coinbase Commerce (7.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Crypto charge + ingress webhook | Signature verified; balance credited | `provider/coinbase`, `webhook/coinbase_ingress_test` pass | PASS |

### Journey 5: Alipay+ international (7.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Alipay+ charge + ingress webhook | Signature verified; balance credited | `provider/alipay`, `webhook/alipay_ingress_test` pass | PASS |

### Journey 6: WeChat Pay HK / cross-border (7.6)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | WeChat Pay HK charge + ingress webhook | Signature verified; balance credited | `provider/wechat`, `webhook/wechat_ingress_test` pass | PASS |

### Journey 7: Auto-recharge + low-balance alert + monthly invoice PDF (7.7)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Balance crosses threshold | Auto-recharge triggered (off-session) | `autorecharge/trigger_test`, `paymentgrpc/offsession_test`, gateway `billing_autorecharge_test` pass | PASS |
| 2 | Low-balance alert | Alert emitted at threshold | `lowbalance/alerter_test` pass | PASS |
| 3 | Monthly invoice PDF | PDF generated from ledger | `invoice/{store,pdf}_test`, `cmd/monthly-invoice` build pass | PASS |

### Journey 8: Subscription tiers + Beta switch (7.8)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Change plan (Free/Pro/Team/Enterprise) | Plan transition validated | `subscription/changeplan_test` pass | PASS |
| 2 | Beta-mode console face | Env-driven Beta badge, fail-safe OFF | console `10.8-beta-mode-toggle` 11/11 pass | PASS |

**Summary**: 8 / 8 journeys passed (integration-level)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live browser run (no running stack); console unit suite clean |
| Network Failures     | N_A    | No live HTTP traffic exercised; webhook/provider HTTP verified via httptest in suites |
| Visual Consistency   | N_A    | No dedicated Epic-7 console routes rendered; UI footprint = env-driven Beta badge |
| Performance          | N_A    | No live page-load measured |
| Auth Flow            | PASS   | Balance/billing gate auth verified in `billinggate_test`; off-session payment auth in `offsession_test` |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-7-001 | LOW | Live browser/end-to-end payment checkout against external provider **sandboxes** was not exercised — no running multi-service environment locally (docker/live gateway unavailable) and flows terminate at external Stripe/PayPal/Coinbase/Alipay/WeChat sandboxes. Integration coverage is strong (~295 tests green) but the full real-network round-trip is unverified. | All payment journeys (3–7) | Run a staging smoke against each provider's sandbox (real webhook round-trip + balance credit) before GA. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline, this report §2) | `go test ./...` output for billing-svc, payment-svc, api-gateway Epic-7 packages — all `ok` |
| log | (inline, this report §2) | `vitest run` output for console Beta-mode — 11/11 pass |

No screenshots captured (no live browser session — see SMOKE-7-001).

## 7. Recommendation

**Result**: PASS

Epic 7 is production-ready at the integration level: all 8 stories are Done and the
full backend regression suite (billing-svc, payment-svc, api-gateway billing/fx/payment
surfaces) plus the console Beta-mode suite are 100% green (~295 tests, 0 failures),
covering every core journey — metering+markup, FX, all five payment providers,
auto-recharge, low-balance alert, monthly invoice PDF, subscription tiers, and the
Beta switch.

Confidence is **MEDIUM** rather than HIGH because live browser/real-network checkout
against external provider sandboxes could not be exercised in this environment
(SMOKE-7-001). Recommend a staging sandbox smoke per provider before the GA cutover to
close that gap.
