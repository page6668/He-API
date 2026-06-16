# Smoke Test Report: Epic 10

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 10 — SDK、文档站、Playground 与上线 (SDKs, Docs, Playground & Launch) |
| **Trigger**      | manual (`QA *smoke-test 10`)       |
| **Executed At**  | 2026-06-16T08:30:06Z               |
| **Overall**      | PASS (with concerns)               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 10.1 | 控制台 10 种语言翻译落地 | Done |
| 10.2 | Python SDK（drop-in OpenAI 替代） | Done |
| 10.3 | TypeScript SDK | Done |
| 10.4 | Go SDK | Done |
| 10.5 | 文档站（Quickstart + API Reference + Cookbook，10 种语言） | Done |
| 10.6 | 在线 Playground + Benchmark 页 | Done |
| 10.7 | 上线检查表 + 监控告警 + 客服接入 | Done |
| 10.8 | Beta 公测开关 + 限额免费试用配置 | Done |

- **Total Stories**: 8
- **Done**: 8
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

> Epic 10 is the launch/distribution epic: three SDKs (Python/TypeScript/Go), a
> Docusaurus docs site, console i18n rollout + Playground/Benchmark + Intercom, and
> the gateway Beta toggle + trial-credit entitlement. Regression vehicles: `go test`
> (sdk-go + gateway), `pytest` (sdk-python), `vitest` (sdk-typescript + console).
> Live e2e (console Playwright, docs-site Playwright) require a running app and were
> **not** driven live — see SMOKE-10-001/004.

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true (1 environment-only failure, see below) |
| **Tests Total** | ~240 (53 sdk-go + 41 sdk-python + 40 sdk-ts + 27 gateway + 79 console) |
| **Tests Failed**| 1 (environment-only — `pip` cannot execute locally; **not** an SDK defect) |
| **Skipped/Todo**| 5 (intentional) |

### Suite Breakdown

| Module | Story | Command | Result |
|--------|-------|---------|--------|
| `packages/sdk-go` | 10.4 | `go test ./...` | ok — 53 tests |
| `packages/sdk-python` | 10.2 | `pytest -q` | **39 pass / 1 skip / 1 FAIL** — failure is `test_10_2_int_001_build_and_clean_install` (env, see note) |
| `packages/sdk-typescript` | 10.3 | `vitest run` | ok — 39 pass / 1 skip |
| `apps/api-gateway` (playground/beta/trial) | 10.6/10.8 | `go test ./internal/handlers/... ./internal/featureflag/... ./internal/entitlement/...` | ok — playground_chat (18), featureflag betamode (5), entitlement beta_trial_1008 (4) |
| `apps/console` `10.1/10.6/10.7/10.8` | 10.1/10.6/10.7/10.8 | `vitest run` | 76 pass / 3 todo — i18n rollout, playground+benchmark, intercom, beta toggle |
| `apps/docs` | 10.5 | build artifacts | Built — `apps/docs/build/` present with 15 locale/section dirs (Quickstart/API-reference/Cookbook). Live e2e not run (SMOKE-10-004) |

> **Environment-only failure — `test_10_2_int_001_build_and_clean_install`:**
> This P0 test spawns `pip install <built wheel>` into a fresh target. The subprocess
> returned non-zero because **pip itself crashed** importing `xml.parsers.expat`:
> `Symbol not found: _XML_SetAllocTrackerActivationThreshold` — the local Homebrew
> Python 3.12 `pyexpat` dylib is incompatible with the system `libexpat`. This is the
> same class of local-toolchain limit as buf/atlas/docker; it is **not** a defect in
> the SDK wheel. Corroborating evidence: the wheel **built** successfully (the
> `built_dist` fixture), and `test_10_2_int_002_twine_check_metadata` and
> `_003_dist_vs_import_name_split` (which consume the same built wheel) **passed**.
> The clean-room install path simply could not be exercised on this machine. **Must be
> confirmed in CI / a clean environment** (SMOKE-10-001).

> **Out-of-scope failures (NOT Epic 10):** `packages/shared-types` has 4 failing
> tests, all in **Epic 1** files — `1.2-UNIT-004`, `1.2-UNIT-009` (CI/CD pipeline
> shape), `1.6-UNIT-128`, `1.6-UNIT-147` (DB migration file counts). These are stale
> repo-shape snapshot assertions that froze early-project invariants the 10-epic
> codebase outgrew (e.g. "exactly 1 postgres migration" — there are now 19; "no Go
> lint scope in ./packages" — `sdk-go` now exists). They are tracked as SMOKE-10-003
> and do not affect Epic 10's verdict.

## 3. Core User Journeys

> **Verification mode note:** SDK journeys verified at unit/integration level; console
> and docs journeys verified at vitest unit level (live Playwright not run — no running
> stack). Gaps tracked in §5.

### Journey 1: Console 10-language rollout (10.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Load console in each of 10 languages | Namespaces resolve, RTL handled, no missing keys | console `10.1-language-translation-rollout.test` (20, 3 skip) pass | PASS |

### Journey 2: Python SDK — drop-in OpenAI replacement (10.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `Client`/`AsyncClient` subclass openai, base_url→gateway | Drop-in parity, version single-source, openai pin 1.40.* guard | `sdk_design_10_2` 39 pass (drop-in, version, pin, twine metadata, dist/import split) | PASS |
| 2 | Install built wheel into clean target | Self-contained import | **NOT VERIFIED LOCALLY** — pip cannot run (env); wheel built + twine-check OK | CONCERN (SMOKE-10-001) |

### Journey 3: TypeScript SDK (10.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Typed npm package, base_url→gateway, SSE streaming, malformed-SSE no-hang | Publishable, typed, robust | `10.3-typescript-sdk.test` 39 pass / 1 skip (incl. npm pack contains only dist) | PASS |

### Journey 4: Go SDK (10.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `NewClient` returns upstream openai client on gateway, balance/usage fns | Drop-in Go client | `sdk-go` 53 tests pass | PASS |

### Journey 5: Docs site — Quickstart / API Reference / Cookbook, 10 langs (10.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Build docs site | Static site builds across locales | `apps/docs/build/` present, 15 locale/section dirs incl. api-reference + cookbook | PASS (build) |
| 2 | Render pages in browser | Pages load, nav works | **NOT VERIFIED LOCALLY** — docs Playwright e2e not run | CONCERN (SMOKE-10-004) |

### Journey 6: Online Playground + Benchmark (10.6)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Playground chat via JWT proxy → ChatCompletionsHandler | Proxy delegates, no new RPC | gw `playground_chat_test` (18) pass; console `10.6-playground-benchmark.test` (19) pass | PASS |
| 2 | Benchmark page (shape-drift graceful degrade) | Degrades gracefully on bad seed | console benchmark blind-spot tests pass | PASS |

### Journey 7: Launch checklist + monitoring/alerting + customer support (10.7)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Intercom customer-support wiring | Identity hash + boot correct | console `10.7-intercom.test` (29) pass | PASS |

### Journey 8: Beta toggle + trial-credit config (10.8)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Beta-mode console badge (env-driven, fail-safe OFF) | Badge reflects NEXT_PUBLIC_BETA_MODE | console `10.8-beta-mode-toggle.test` (11) pass | PASS |
| 2 | Gateway beta gate + trial-credit entitlement | Lenient parse, fail-safe OFF; trial credit applied | gw `featureflag/betamode_test` (5), `entitlement/beta_trial_1008_test` (4) pass | PASS |

**Summary**: 8 / 8 journeys passed; 2 carry an environment-only verification gap (J2 step 2, J5 step 2)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live browser run; console + benchmark stderr (shape-drift / malformed-SSE) are intentional blind-spot test logs, not errors |
| Network Failures     | N_A    | No live HTTP run; SDK/gateway HTTP verified via httptest/mocks |
| Visual Consistency   | N_A    | No live render; i18n RTL + 10-lang coverage asserted in 10.1 unit suite |
| Performance          | N_A    | No live page-load; benchmark page logic unit-tested |
| Auth Flow            | PASS   | Playground JWT proxy delegation verified in `playground_chat_test`; beta gate / trial entitlement bind to auth context |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-10-001 | MEDIUM | Python SDK clean-room wheel install (`test_10_2_int_001`) could not be verified locally — `pip` crashes on this machine (Homebrew `pyexpat` ↔ system `libexpat` symbol mismatch). The wheel builds and passes twine-check, so this is an environment limit, not a defect — but the fresh-install path is unconfirmed locally. | J2 step 2 | Confirm in CI / a clean Python env (the test already exists; run it in GitHub Actions). |
| SMOKE-10-002 | LOW | Console (10.1/10.6/10.7) and docs-site Playwright e2e were not executed against a live app — no running stack locally. | J1, J5, J6, J7 | Run console + docs Playwright e2e against staging before GA. |
| SMOKE-10-003 | LOW | `packages/shared-types` has 4 failing tests — all in **Epic 1** files (1.2-UNIT-004/009, 1.6-UNIT-128/147). Stale repo-shape snapshot assertions the 10-epic codebase outgrew (migration count, package lint scope, placeholder main_test). Out of Epic-10 scope; not a product defect. | N/A (Epic 1) | Hand to SM/Dev to refresh the Epic-1 snapshot assertions to current repo shape (separate housekeeping story). |
| SMOKE-10-004 | LOW | Docs site verified by build artifacts only (`apps/docs/build/`, 15 locale/section dirs); pages not rendered in a live browser. | J5 step 2 | Serve the built site and run the `10.5` docs Playwright e2e before GA. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline, this report §2) | `go test` (sdk-go + gateway), `pytest` (sdk-python), `vitest` (sdk-ts + console) outputs |
| artifact | `apps/docs/build/` | Built Docusaurus site, 15 locale/section dirs (Quickstart + API Reference + Cookbook) |

No screenshots captured (no live browser session — see SMOKE-10-002/004).

## 7. Recommendation

**Result**: PASS (with concerns)

Epic 10 is production-ready at the unit + integration level: all 8 stories are Done and
the regression suite is green except for a single **environment-only** failure (Python
clean-room install — `pip` cannot execute on this machine; the wheel itself builds and
passes twine-check). All three SDKs (Python drop-in, TypeScript, Go), the gateway
Playground proxy, the Beta toggle + trial-credit entitlement, console i18n / benchmark /
Intercom, and the docs-site build are verified.

Confidence is **MEDIUM** with three pre-GA actions:
1. **SMOKE-10-001 (MEDIUM)** — re-run `test_10_2_int_001_build_and_clean_install` in CI / a clean Python env to confirm the wheel installs cleanly.
2. **SMOKE-10-002 / 004 (LOW)** — run console + docs-site Playwright e2e against staging.
3. **SMOKE-10-003 (LOW)** — Epic-1 `shared-types` snapshot drift is unrelated to Epic 10; route to SM/Dev as housekeeping (does not block this epic).
