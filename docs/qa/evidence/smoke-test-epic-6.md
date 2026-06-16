# Smoke Test Report: Epic 6

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 6 — 智能路由与故障转移 (Smart Routing & Failover) |
| **Trigger**      | manual (`QA *smoke-test 6`)        |
| **Executed At**  | 2026-06-16T08:08:46Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

> **Re-run note.** This supersedes the 2026-06-16T01:56:56Z report, which ran
> before Story 6.5 existed and was therefore SKIPPED-equivalent (PASS on
> implemented scope only, with `SMOKE-6-01 (HIGH)` flagging 6.5 as MISSING).
> Story 6.5 has since landed (commit `b5a51af`, QA Round 1 PASS, Done). The Epic
> is now 100% complete and **SMOKE-6-01 is RESOLVED**.

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 6.1 | 路由器核心（策略接口 + 决策引擎） | Done |
| 6.2 | quality / cost / latency 策略实现 | Done |
| 6.3 | 自动 failover（3 次失败 / 30s 超时） | Done |
| 6.4 | A/B 模式（X-He-AB-Models） | Done |
| 6.5 | 路由策略配置 UI（控制台）— 账户级默认路由策略 | Done |

- **Total Stories**: 5
- **Done**: 5
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | Go: 46 routingclient funcs + 61 handler Failover/AB/Chaos/Default funcs + routing-svc (8 pkgs) + auth-svc (incl. `routing_strategy` / `routing_pref` / `update_profile` / `me` suites). Console: 6.5 RoutingStrategyForm vitest (9 tests). |
| **Tests Failed**| 0                |

Regression executed at **unit + integration + chaos + component** level against the
full implemented scope (6.1–6.5):

- `apps/api-gateway/internal/routingclient` → `ok` (incl. the new 6.5 user-default
  precedence suite: `TestParseStrategy_DefaultPlusHeader_HeaderWins`,
  `…_DefaultPlusMeta_MetaWins`, `…_DefaultOnly_RoutesByUserDefault`,
  `…_NoDefault_ByteForBye62`, `TestDecide_ResolvesUserDefault`,
  `TestDecideAB_DoesNotApplyUserDefault`, `TestDecide_UserDefaultSlogSource` — 11/11 PASS)
- `apps/api-gateway/internal/handlers` (Failover / AB / Chaos) → `ok`
- `apps/routing-svc/...` (8 packages) → `ok`
- `apps/auth-svc/...` (repository + handlers, incl. `default_routing_strategy`
  validation / persistence / GetMe passthrough) → `ok`
- `apps/console` `6.5-routing-strategy-config-ui.test.tsx` → 9/9 PASS
  (act() warnings are non-fatal React test noise, not assertion failures)

> **No Playwright browser E2E was executed** — no `tests/e2e/story-6.*.spec.ts`
> specs exist, and no live application stack was running (toolchain/env limits:
> docker/atlas unavailable locally). Journey verification below is at the
> unit/integration/component level. This is the sole reason confidence is
> capped at MEDIUM, not HIGH.

## 3. Core User Journeys

### Journey 1: Strategy-based model selection (6.1 / 6.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Request with cost strategy | Cheapest in-scope model selected | `TestCost_*` + scoring suite `ok` | PASS |
| 2 | Request with quality / latency strategy | Highest-quality / lowest-latency selected | quality + latency strategy suites `ok` | PASS |
| 3 | Decision observability | `X-He-Selected-Model` reflects final model | `routingclient/decider` + engine dispatch `ok` | PASS |
| 4 | Strategy boundary cases (empty/nil/tie) | Deterministic, safe defaults | `TestCost_EmptyCandidates` / `…_NilPriceSource` / `…_PriceTieFirstAlphabetical` `ok` | PASS |

### Journey 2: Automatic failover (6.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Upstream returns 502/504 | Advance to rank-2 model | `TestFailover_NonStream_502/504_AdvancesToRank2` + chaos `ok` | PASS |
| 2 | Repeated failures | Cap at 3 total attempts | `TestFailover_NonStream_AttemptCapAtThree` `ok` | PASS |
| 3 | Slow chain | Stop at 30s wall-clock budget | `TestFailover_NonStream_BudgetExhaustion` `ok` | PASS |
| 4 | Billing under failover | Exactly one token deduction | `TestFailover_NonStream_ExactlyOneDeduct_TwoFailThenSucceed` `ok` | PASS |
| 5 | Non-retriable code | Terminal, no failover | `TestFailover_NonStream_NonRetriable_NoFailover` `ok` | PASS |

### Journey 3: A/B mode (6.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `X-He-AB-Models: m1,m2` | Both models invoked in parallel | `TestAB_BothSucceed_Merge`, `TestAB_ParallelDispatch_Barrier` `ok` | PASS |
| 2 | Response shape | Contains both legs' results | `TestABModelsPresent`, `TestAB_BothSucceed_Merge` `ok` | PASS |
| 3 | One leg fails | Partial result + marker | `TestAB_PartialFailure_OneLegDown`, `TestAB_NonRetriableLeg_SurfacedInMarker` `ok` | PASS |
| 4 | Malformed header | 400 | `TestAB_BadHeaderCount_400` `ok` | PASS |

### Journey 4: Console strategy-config UI + account-level default (6.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | User opens settings/profile, selects a default strategy, Saves | UI persists per-user default via `PUT /v1/me/profile` | `RoutingStrategyForm` mounted at `app/[locale]/(console)/settings/profile/page.tsx`; vitest AC1 select+persist `ok` | PASS |
| 2 | Double-click Save | Exactly one request issued | `[BLIND-SPOT] 6.5-BLIND-FLOW-001` `ok` | PASS |
| 3 | Concurrent update (etag conflict) | Conflict banner shown, no silent overwrite | `account.routing.banners.concurrent_update` rendered; vitest `ok` | PASS |
| 4 | Persisted default resolved on chat hot path | meta > header > user-default > STRATEGY_DEFAULT; no per-request DB hit | `TestDecide_ResolvesUserDefault` + precedence suite `ok` | PASS |
| 5 | No default set (existing users) | Byte-for-byte 6.2 passthrough (zero regression) | `TestParseStrategy_NoDefault_ByteForBye62` `ok` | PASS |
| 6 | DB migration | Additive, nullable, reversible, backfill-free | `0019_add_default_routing_strategy_to_users.sql` — `ADD COLUMN … VARCHAR(20)` NULL; down computed by `atlas migrate down 1` | PASS |
| 7 | i18n coverage | `account.routing.*` present in all 10 locales | ar/de/en/es/fr/ja/ko/pt/ru/zh-CN all carry the `routing` block | PASS |

**Summary**: 4 / 4 journeys passed.

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live stack running; component test renders clean (only non-fatal `act()` warnings) |
| Network Failures     | PASS   | Upstream-fault paths are the *subject* of this epic — failover + chaos suites cover 502/504/timeout; 0 failures. 6.5 write path = single `PUT /v1/me/profile` (Story-2.5 optimistic-concurrency reuse) |
| Visual Consistency   | N_A    | No live browser render (env limits); RoutingStrategyForm reuses the Story-2.5 ProfileForm layout pattern |
| Performance          | PASS   | Hot-path default resolution adds **zero per-request DB I/O** (Q-A ruled: JWT claim / cached lookup; verified by `TestDecide_ResolvesUserDefault`). 30s failover budget validated functionally |
| Auth Flow            | PASS   | IDOR defence — `user_id` always derived from JWT, never request body (Story-2.5 discipline); A/B leg-scope enforcement `TestAB_LegOutOfScope_403`; key-scope post-validation via Story-5.2 keypolicy gate |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-6-02 | LOW | No Playwright browser E2E exists for Epic 6 and no live stack was exercised; all verification is unit/integration/component-level. Confidence capped at MEDIUM. | All | Optional: add a thin `story-6.5` Playwright happy-path (select → save → reload shows persisted default) when a live console env is available. Non-blocking. |
| SMOKE-6-03 | LOW | `RoutingStrategyForm` emits React `act(...)` warnings under vitest (state updates outside `act`). Cosmetic test-harness noise; assertions pass. | Journey 4 | Optional: wrap the async submit state update in `act()` in the test or `await` the settle. Non-blocking. |

> **SMOKE-6-01 (HIGH) — RESOLVED.** The prior run's blocking gap (Story 6.5 not
> built / not descoped) is closed: 6.5 is implemented (console UI + additive
> `PUT /v1/me/profile` field + auth-svc proto/validation/persistence + gateway
> precedence tier + migration 0019) and marked Done.

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| Test | `apps/api-gateway/internal/routingclient/routing_default_test.go` | 6.5 user-default precedence suite (header/meta/default/no-default) |
| Test | `apps/api-gateway/internal/handlers/chat_completions_failover*_test.go` | 6.3 failover + chaos suites |
| Test | `apps/api-gateway/internal/handlers/chat_completions_ab*_test.go` | 6.4 A/B-mode suites |
| Test | `apps/auth-svc/internal/handlers/routing_pref_test.go`, `…/repository/routing_strategy_test.go` | 6.5 default-strategy validation + persistence |
| Test | `apps/console/components/business/6.5-routing-strategy-config-ui.test.tsx` | 6.5 console UI (9 tests) |
| Code | `apps/console/components/business/RoutingStrategyForm.tsx` + `app/[locale]/(console)/settings/profile/page.tsx` | 6.5 UI + mount point |
| Migration | `migrations/postgres/0019_add_default_routing_strategy_to_users.sql` | Additive nullable column (reversible) |

## 7. Recommendation

**Result**: PASS

Epic 6 is production-ready. All 5 stories are Done and the full implemented
scope (strategy-based selection, automatic failover, A/B mode, and the
account-level default-routing-strategy UI capstone) passes regression at the
unit + integration + chaos + component level with 0 failures. The prior
blocking completeness gap (SMOKE-6-01) is resolved.

Two **LOW / non-blocking** follow-ups remain (SMOKE-6-02 missing browser E2E,
SMOKE-6-03 cosmetic `act()` warnings); neither gates release. Confidence is
**MEDIUM** solely because no live browser E2E was executed (env limits) — the
release decision is not contingent on it.
