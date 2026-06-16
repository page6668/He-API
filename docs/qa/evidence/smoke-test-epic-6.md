# Smoke Test Report: Epic 6

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 6 — 智能路由与故障转移 (Smart Routing & Failover) |
| **Trigger**      | manual (`QA *smoke-test 6`)        |
| **Executed At**  | 2026-06-16T01:56:56Z               |
| **Overall**      | PASS *(implemented scope)* — ⚠️ EPIC-COMPLETE SIGN-OFF WITHHELD |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 6.1 | 路由器核心（策略接口 + 决策引擎） | Done |
| 6.2 | quality / cost / latency 策略实现 | Done |
| 6.3 | 自动 failover（3 次失败 / 30s 超时） | Done |
| 6.4 | A/B 模式（X-He-AB-Models） | Done |
| **6.5** | **路由策略配置 UI（控制台）** | **MISSING — no story file, no implementation** |

- **Stories enumerated in PRD** (`epic-6-smart-routing.yaml`, `estimated_stories: 5`): **5**
- **Story files present**: 4 (6.1–6.4)
- **Done**: 4 / 5
- **Not present / Not Done**: 1 (6.5)
- **Completeness**: 80%

> ⚠️ **Completeness gap (SMOKE-6-01, HIGH).** The Epic-6 PRD enumerates **Story 6.5 — 路由策略配置 UI（控制台）** with AC "用户可在 UI 选择默认策略". No `docs/stories/6.5*.md` file exists, and no console routing-strategy UI exists under `apps/console/`. Stories 6.1 and 6.3 repeatedly reference 6.5 as a planned future story (e.g. 6.1: "Console UI for strategy selection (Story 6.5)"; 6.3: "Stories 6.4 / 6.5 (Console default-strategy UI) build on the failover semantics"). 6.5 has been neither built nor formally descoped. **Epic 6 cannot be signed off as complete until 6.5 is dispositioned.**

## 2. Regression Suite

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | 112 routing-specific Go test functions (+ gateway `TestFailover_*` / `TestAB_*` / `TestChaos_Failover_*` in `handlers`) |
| **Tests Failed**| 0                |

Regression executed at **unit + integration + chaos** level against the implemented backend scope
(6.1–6.4). No console suite ran — Story 6.5 (console UI) does not exist (see §1).

### Suite breakdown (all `ok`, 0 failures)

| Suite | Tests | Result |
|-------|------:|--------|
| `routing-svc/...` (engine, strategy×3, scoring, pricing, catalogue, handler, server, tests) | 78 | ok |
| `api-gateway/internal/routingclient` (decider + A/B) | 34 | ok |
| `api-gateway/internal/handlers` (`TestFailover_*`, `TestAB_*`, `TestChaos_Failover_*`) | — | ok |
| **Total (routing-specific)** | **112** | **0 failures** |

### Epic DoD → test traceability

The Epic's **3 functional DoD bullets are all met** by the implemented backend (strategy choice is
available programmatically via the `X-He-Routing-Strategy` request header; 6.5 would add a console
UI surface for the *default*, which is a UX convenience, not one of the 3 DoD bullets):

| Epic DoD bullet | Covering test(s) | Status |
|-----------------|------------------|--------|
| 用户可选 quality / cost / latency 三种策略 | `TestCost_CheapestSelected`, `TestCost_MissingPricingRankedLast`, `TestCost_PriceTieFirstAlphabetical`, quality/latency strategy suites, `Test6_1_AC2_StrategyEngineDispatch` | PASS |
| 上游故障自动 failover | `TestFailover_NonStream_502/504_AdvancesToRank2`, `TestFailover_NonStream_AttemptCapAtThree`, `TestFailover_NonStream_BudgetExhaustion` (30s), `TestChaos_Failover_Upstream502/Timeout_SwitchesToRank2`, `TestFailover_NonStream_ExactlyOneDeduct_TwoFailThenSucceed` | PASS |
| A/B 模式可同时调用 2 个模型对比 | `TestAB_BothSucceed_Merge`, `TestAB_ParallelDispatch_Barrier`, `TestAB_PartialFailure_OneLegDown`, `TestAB_BadHeaderCount_400`, `TestAB_OneRequest_OneTokenDeduction`, `TestAB_LegOutOfScope_403` | PASS |

## 3. Core User Journeys

### Journey 1: Strategy-based model selection (6.1 / 6.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Request with cost strategy | Cheapest in-scope model selected | `TestCost_CheapestSelected` + scoring suite `ok` | PASS |
| 2 | Request with quality / latency strategy | Highest-quality / lowest-latency selected | quality + latency strategy suites `ok` | PASS |
| 3 | Decision observability | `X-He-Selected-Model` reflects final model | `routingclient/decider` + engine dispatch tests `ok` | PASS |
| 4 | Strategy boundary cases (empty/nil/tie) | Deterministic, safe defaults | `TestCost_EmptyCandidates`, `TestCost_NilPriceSource`, `TestCost_PriceTieFirstAlphabetical` `ok` | PASS |

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

### Journey 4: Console strategy-config UI (6.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | User selects default strategy in console | UI persists per-user default | **Not implemented — no story file, no UI** | **NOT BUILT** |

**Summary**: 3 / 4 journeys passed; Journey 4 (6.5) not built.

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No console surface for this epic (6.5 not built); routing is a backend service |
| Network Failures     | PASS   | Upstream-fault paths are the *subject* of this epic — failover + chaos suites cover 502/504/timeout; 0 failures |
| Visual Consistency   | N_A    | No UI delivered (6.5 missing) |
| Performance          | N_A    | No AC-level perf SLO; 30s failover budget validated functionally |
| Auth Flow            | PASS   | A/B leg scope enforcement (`TestAB_LegOutOfScope_403`) + per-request billing invariants covered |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-6-01 | **HIGH** | Epic-6 PRD enumerates **Story 6.5 — 路由策略配置 UI（控制台）** (`estimated_stories: 5`, AC "用户可在 UI 选择默认策略"), referenced as planned in 6.1/6.3, but it has **no story file and no implementation**. The epic is 80% complete by story count. | Journey 4 | **PO/SM disposition required**: either (a) draft + build 6.5, or (b) formally descope 6.5 and update `epic-6-smart-routing.yaml` (`estimated_stories: 4`, remove the 6.5 entry). Until then, do NOT mark Epic 6 complete. |
| SMOKE-6-02 | LOW | Cross-request circuit-breaker reading of "3 次失败 / 30s" was DEFERRED (6.3 Q-B ruling); only per-request failover (≤3 attempts / 30s budget) is implemented. This is a ratified scope decision, not a defect. | Journey 2 | Track the circuit-breaker as a future story once upstream-health telemetry exists; per-request failover satisfies the current Epic DoD. |
| SMOKE-6-03 | LOW | End-to-end failover/A/B verified against in-test fake upstreams (chaos suite is httptest/Toxiproxy-style), not a live multi-vendor mesh. | Journeys 2 & 3 | Confirm against staging multi-vendor mesh before GA traffic. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (this report) | `go test ./...` results captured inline (§2); 112 routing tests, 0 failures |

No screenshots captured (no UI delivered for this epic).

## 7. Recommendation

**Result**: PASS for the implemented backend scope (6.1–6.4) — **but epic-complete sign-off is WITHHELD.**

The routing backend is solid: all 4 implemented stories Done, the full routing regression (112
test functions across `routing-svc` + gateway `routingclient`/failover/A/B) passes with **0
failures**, and all **3 functional Epic DoD bullets** (strategy choice / auto-failover / A/B mode)
map to passing tests, including chaos coverage and the billing/scope invariants.

**However, Epic 6 is NOT complete.** The PRD enumerates **5 stories**; only 4 exist. **Story 6.5
(路由策略配置 UI · console)** has been neither built nor formally descoped (SMOKE-6-01, HIGH). Its AC
("用户可在 UI 选择默认策略") is a console surface; strategy selection is currently only available
programmatically via the `X-He-Routing-Strategy` header.

Confidence is **MEDIUM** because of this completeness gap (the implemented code itself would be
HIGH).

### ⚠️ Blocking question for PO/SM (STOP & ASK)

Per the elite-engineering ambiguity protocol, I am **not** auto-handing off `*epic-complete 6`.
Please disposition Story 6.5:

1. **Build it** — draft + implement the console routing-strategy UI, then re-run `*smoke-test 6`; or
2. **Descope it** — formally remove 6.5 from `epic-6-smart-routing.yaml` (set `estimated_stories: 4`), at which point Epic 6's DoD is met and this report upgrades to PASS / HIGH.

Which path?
