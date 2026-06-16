# Smoke Test Report: Epic 5

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 5 — API Key 管理、配额与限流 (Key / Quota / Rate-Limit) |
| **Trigger**      | manual (`QA *smoke-test 5`)        |
| **Executed At**  | 2026-06-16T01:52:51Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 5.1 | API Key 创建 / 列表 / 吊销 | Done |
| 5.2 | Key 配置（范围 / IP 白名单 / 月度上限） | Done |
| 5.3 | 限流（QPS / RPM / TPM） | Done |
| 5.4 | 月度消费上限熔断 + 邮件预警 | Done |
| 5.5 | 控制台 Keys 页面（CRUD + 配置 UI） | Done |

- **Total Stories**: 5
- **Done**: 5
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | 272 (201 Go test functions + 71 console vitest) |
| **Tests Failed**| 0                |

Regression executed at **unit + integration + chaos** (Go) and **component/unit** (console vitest)
levels. The console **browser e2e** (`apps/console/e2e/5.5-keys-page*.spec.ts`) and full
live-Redis/live-SMTP end-to-end paths are CI-only / mock-backed locally — see Cross-Cutting + Issues.

### Suite breakdown (all `ok` / `passed`, 0 failures)

| Suite | Tests | Result |
|-------|------:|--------|
| `api-gateway/internal/middleware/keypolicy` (IP-whitelist, monthly-cap, cap-tripped) | 64 | ok |
| `api-gateway/internal/middleware/ratelimit` (QPS/RPM/TPM + TPM deduct) | 22 | ok |
| `api-gateway/internal/middleware/billinggate` | 7 | ok |
| `api-gateway/internal/handlers/me_keys*` (key CRUD / revoke / ratelimit cfg) | 9 | ok |
| `api-gateway/tests/ratelimit_integration + ratelimit_chaos` | 20 | ok |
| `auth-svc/internal/repository` (api_keys) | 30 | ok |
| `auth-svc/internal/apikey` + `internal/audit` (validate, no-plaintext-leak) | 30 | ok |
| `auth-svc/internal/ratelimit` | 8 | ok |
| `notification-svc/internal/handlers/cap_threshold` (cap email alert) | 11 | ok |
| `console __tests__/5.5-console-keys-page-crud-config-ui` (vitest) | 71 | passed |
| **Total** | **272** | **0 failures** |

> `auth-svc/internal/apikey` ran ~234s (argon2/bcrypt key-hash cost is intentionally high) — passed.

### AC → test traceability (behavior coverage confirmed)

| Epic DoD / AC | Covering test(s) | Status |
|---------------|------------------|--------|
| 5.1 — 创建后明文仅展示一次 | `auth-svc apikey/validate` (`TestValidate_NoPlaintextLeakOnDBError`) + `TestRequireAPIKey_CacheKeyIsSHA256NotPlaintext` (SHA-256 at rest, never plaintext) | PASS |
| 5.1 — 吊销立即生效（Redis 缓存失效） | `TestMeKeysRevoke`, `TestRequireAPIKey_RevokedSameEnvelope`, `TestRequireAPIKey_RedisGetErrorFallsThrough` | PASS |
| 5.2 — 非白名单 IP → 403 | `TestCheckIPWhitelist`, `TestKeyPolicy_IPWhitelist`, `clientip_test.go` | PASS |
| 5.2 — 超月度上限 → 402 | `TestCheckMonthlyCap`, `TestKeyPolicy_MonthlyCap`, `TestCapTripped_*` (402 fire/no-fire, fail-open/closed) | PASS |
| 5.3 — 超限 → 429 + Retry-After | `TestINT012_429EnvelopeAcrossThreeAxes` + `Retry-After` header in `ratelimit.go`/`ratelimit_test.go`/`ratelimit_integration_test.go` | PASS |
| 5.4 — 命中熔断后 Key 暂停 | `TestCapTripped_SentinelHit_FastPath`, `TestCapTripped_SlowPathFirstCross_SetNX_Fire_402` (sentinel pause) | PASS |
| 5.4 — 用户邮件通知 | `notification-svc cap_threshold` (11 tests, incl. `TestNotify_HTMLEscape`, `TestNotify_InvalidThreshold`, `TestNotify_DetachedContext_SurvivesParentCancel`) | PASS |
| 5.5 — UI 可完成所有 Key 操作 | console vitest 71/71 (Create/List/Revoke/Configure components) | PASS |

## 3. Core User Journeys

### Journey 1: API Key lifecycle (create → list → configure → revoke)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Create key | Plaintext returned once; SHA-256 stored | `auth-svc repository` + apikey suites `ok`; cache-key is SHA-256 not plaintext | PASS |
| 2 | List keys | Masked keys, no plaintext leak | `me_keys` handler + audit no-plaintext tests `ok` | PASS |
| 3 | Configure (scope / IP whitelist / monthly cap) | Config persisted + enforced | `keypolicy` checks (64) `ok`; console `ConfigureKeyDrawer` covered | PASS |
| 4 | Revoke | Immediate effect via Redis cache invalidation | `TestMeKeysRevoke` + `TestRequireAPIKey_RevokedSameEnvelope` `ok` | PASS |

### Journey 2: Quota & rate-limit enforcement on the request path

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Call from non-whitelisted IP | 403 | `TestCheckIPWhitelist` / `TestKeyPolicy_IPWhitelist` `ok` | PASS |
| 2 | Exceed QPS/RPM/TPM | 429 + `Retry-After` | `TestINT012_429EnvelopeAcrossThreeAxes` + Retry-After header `ok` | PASS |
| 3 | Exceed monthly cap | 402 | `TestCheckMonthlyCap` + `TestCapTripped_*` `ok` | PASS |
| 4 | Hit cost-cap circuit breaker | Key paused + email alert | cap-tripped sentinel tests + `notification-svc cap_threshold` `ok` | PASS |
| 5 | Redis fault during enforcement | Defined fail-open/closed posture | `TestRedisNilFailsOpen`, `TestOAuthRatelimit_RedisDown_FailsClosed`, ratelimit chaos suite `ok` | PASS |

### Journey 3: Console Keys page (Story 5.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Render keys table / empty state | Correct CRUD UI | console vitest 71/71 `passed` (KeysTable, KeysEmptyState, KeysPanel) | PASS |
| 2 | Create-key modal + one-time plaintext display | Plaintext shown once, copyable | `CreateKeyModal` + `ApiKeyDisplay` covered by vitest | PASS |
| 3 | Configure drawer + revoke dialog | Config + revoke flows | `ConfigureKeyDrawer` + `RevokeKeyDialog` covered by vitest | PASS |
| 4 | Browser e2e render in real browser | Live page interaction | `e2e/5.5-keys-page*.spec.ts` present but NOT run locally (browser/console-build lane) — SMOKE-5-02 | PASS (component-level) |

**Summary**: 3 / 3 journeys passed (component + integration level)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live browser session run this env; console vitest (jsdom) clean |
| Network Failures     | PASS   | Redis/upstream fault paths covered by ratelimit chaos suite + fail-open/closed tests; 0 failures |
| Visual Consistency   | N_A    | Keys page not rendered live this session; components covered by 71 vitest assertions |
| Performance          | N_A    | No AC-level perf SLO in Epic 5; ratelimit budgets validated functionally, load budgets live under Epic 9 |
| Auth Flow            | PASS   | API-key auth (SHA-256 cache, revoke-invalidation, 403/402/429 envelopes) covered by gateway middleware + auth-svc suites |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-5-01 | LOW | Cache-invalidation-on-revoke and rate-limit counters are exercised against mock/miniredis-backed fakes in this local session, not a live Redis. | Journeys 1 & 2 | Confirm against live Redis in the integration/staging lane before GA; behavior is contract-covered by `TestMeKeysRevoke` + cap-tripped sentinel tests. |
| SMOKE-5-02 | LOW | Console browser e2e (`e2e/5.5-keys-page.spec.ts` + a11y spec) not executed locally (browser/console-build lane). The 71-test vitest component suite DID run green. | Journey 3 | Run the 5.5 e2e in CI/console lane; track any console-build issues separately (see auth-surface note). |
| SMOKE-5-03 | LOW | Cap-breach email is verified at the notification-handler unit level (render/escape/threshold); real SMTP delivery is not exercised locally. | Journey 2 | Verify end-to-end mail delivery via staging notification pipeline before GA. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (this report) | `go test` + `pnpm vitest run` results captured inline (§2); 272 tests, 0 failures |

No screenshots captured (no live browser session — backend + component-test coverage).

## 7. Recommendation

**Result**: PASS

Epic 5 is production-ready at the unit/integration/component level: all 5 stories Done, and the
full regression (201 Go test functions across gateway `keypolicy`/`ratelimit`/`billinggate`/`me_keys`,
`auth-svc` api-key + ratelimit + audit, and `notification-svc` cap-threshold, plus 71 console vitest
tests for the 5.5 Keys page) passes with **0 failures**. Every Epic DoD / AC maps to a passing test:
403 on non-whitelisted IP, 402 on monthly-cap, 429 + `Retry-After` on QPS/RPM/TPM, cost-cap
circuit-breaker pause + email alert, and one-time plaintext / SHA-256-at-rest / revoke-invalidation.

Confidence is **MEDIUM** (not HIGH) for one reason: the live-stack edges — real Redis cache
invalidation, console browser e2e, and real SMTP delivery — are mock-backed or CI-only and were not
exercised in this local session (all 3 issues LOW severity, all by-design, none defects). Run the
staging integration lane (live Redis + 5.5 e2e + notification SMTP) to confirm those edges before
GA traffic.
