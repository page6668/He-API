# Smoke Test Report: Epic 2

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 2 — 账户与认证 (Account & Authentication) |
| **Trigger**      | manual (`QA *smoke-test 2`)        |
| **Executed At**  | 2026-06-16T08:36:09Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 2.1 | Next.js 控制台骨架 + i18n 框架（next-intl） | Done |
| 2.2 | 邮箱注册 + 密码登录 + 邮件验证 | Done |
| 2.3 | OAuth — Google / GitHub | Done |
| 2.4 | TOTP 2FA | Done |
| 2.5 | 个人资料管理（昵称 / locale / 时区） | Done |
| 2.6 | GDPR 数据导出（JSON 包） | Done |
| 2.7 | 账号注销与数据删除（30 天宽限期） | Done |

- **Total Stories**: 7
- **Done**: 7
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

> Epic 2 spans the `auth-svc` Go backend (signup/login, OAuth, TOTP, profile,
> deletion) plus the gateway account-data/deletion handlers (GDPR export, deletion
> grace) and the Next.js console (skeleton + i18n, signup/login, OAuth pages).
> Regression vehicles: `go test ./...` (auth-svc + gateway) and `vitest` (console).
> The eight console Playwright e2e specs (incl. security-attack suites) require a
> running app and were **not** driven live — see SMOKE-2-001.

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | ~540 (368 auth-svc + ~170 console + gateway account-data/deletion subset) |
| **Tests Failed**| 0                |
| **Skipped**     | 33 (console, intentional) |

### Suite Breakdown

| Module | Story | Command | Result |
|--------|-------|---------|--------|
| `apps/auth-svc` | 2.2–2.7 | `go test ./...` | ok — 368 test funcs across jwt, token, password(+hibp), oauth(google/github/state/linking), totp(+qr), recovery, apikey, deletion(sweeper/stripe), handlers(me/totp_*/account_deletion/refresh), repository, ratelimit, audit, kms, metrics |
| `apps/api-gateway/internal/handlers` (account) | 2.6/2.7 | `go test -run 'AccountData\|Deletion\|Export'` | ok — RequestDataExport happy/401/429/tamper, GetCurrentExport, CancelDeletion (happy + 410 grace-expired), GetDeletionState, pending-deletion 403 gates on /me + profile |
| `apps/console` `2.1/2.2/2.3` | 2.1/2.2/2.3 | `vitest run` | 170 pass / 33 skip — i18n skeleton (158), signup/login/verify (32), OAuth (13) |

No failed tests.

> **Known build-surface caveats (pre-existing, not Epic-2 regressions):** Prior notes
> flag a few console **build/lint** issues on HEAD (non-async `maskEmail` under
> `next build`, `next lint`). These are build-tool surface, not unit/integration test
> failures, and do not appear in the `go test` / `vitest` runs above. The earlier
> i18n-keys `2fa.*` codegen crash was **fixed** (commit 9479b4a). Verification here
> was done per-file via the test runners, consistent with that guidance.

## 3. Core User Journeys

> **Verification mode note:** No console page was rendered in a live browser and no
> external IdP (Google/GitHub) or real email round-trip was exercised — no running
> stack locally. Journeys are verified at unit/integration level (auth-svc handlers +
> gateway httptest + console vitest). Residual live gaps tracked in §5.

### Journey 1: Console skeleton + i18n (2.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Load console shell across locales | next-intl namespaces resolve, layout renders | console `2.1` suite (158, 33 skip) pass | PASS |

### Journey 2: Email signup + password login + email verification (2.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Sign up with email + password | Password hashed, HIBP-checked, user created | auth-svc `password`(+`hibp`), `repository/users`, `handlers` pass; console `2.2` (32) pass | PASS |
| 2 | Verify email via token | Verification token issued + consumed | auth-svc `token_test` pass | PASS |
| 3 | Password login | Credentials validated, JWT issued | auth-svc `jwt_test`, `handlers/refresh_token_test` pass | PASS |

### Journey 3: OAuth — Google / GitHub (2.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | OAuth authorize + callback | State validated (CSRF), account linked/created | auth-svc `oauth/{google,github,state,linking}_test`, `metrics/oauth_test` pass; console `2.3` (13) pass | PASS |

### Journey 4: TOTP 2FA (2.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Enroll TOTP (QR + secret) | Secret provisioned, QR generated | auth-svc `totp_test`, `qr_test`, `handlers/totp_enroll_test` pass | PASS |
| 2 | Challenge at login + recovery codes | MFA token enforced, recovery codes usable | auth-svc `jwt/mfa_token_test`, `handlers/totp_challenge_test`, `recovery/codes_test`, `repository/mfa_recovery_codes_test` pass | PASS |
| 3 | Disable TOTP | Re-auth required to disable | auth-svc `handlers/totp_disable_test` pass | PASS |

### Journey 5: Profile management — nickname / locale / timezone (2.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Read + update profile | /me returns + updates nickname/locale/tz | auth-svc `handlers/me_test`, `repository/users_test` pass | PASS |

### Journey 6: GDPR data export — JSON package (2.6)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Request data export | Export job created (rate-limited, JWT-scoped, tamper-rejected) | gw `TestRequestDataExport_HappyPath_200`, `_RaceLimit_429`, `_BodyTampering_400`, `_MissingJWT_401`, `GetCurrentExport_*` pass | PASS |

### Journey 7: Account deletion + 30-day grace (2.7)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Request deletion | Pending-deletion state set; /me + profile return 403 | gw `GetMeRoute_PendingDeletion_Returns403`, `UpdateProfileRoute_PendingDeletion_Returns403` pass | PASS |
| 2 | Cancel within grace / expired | Cancel OK in window; 410 after grace expires | gw `CancelDeletion_HappyPath`, `CancelDeletion_GraceExpired410`, `GetDeletionState` pass | PASS |
| 3 | Sweeper purges after grace | Data deleted (Stripe + ClickHouse + PG) | auth-svc `deletion/sweeper_test`, `stripe_test` pass | PASS |

**Summary**: 7 / 7 journeys passed (unit + integration)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live browser run; console vitest suites clean |
| Network Failures     | N_A    | No live HTTP / IdP / email round-trip; auth flows verified via httptest + mocks |
| Visual Consistency   | N_A    | No live render; i18n locale coverage asserted in 2.1 unit suite |
| Performance          | N_A    | No live page-load measured |
| Auth Flow            | PASS   | This epic IS the auth flow — signup→verify→login→2FA→profile→deletion all unit/integration-verified; rate-limit, CSRF state, HIBP, body-tamper, MFA-token, grace-expiry edge cases covered |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-2-001 | MEDIUM | The eight console Playwright e2e specs (2.1/2.2/2.2-security-attacks/2.3/2.3-oauth-attacks/2.4/2.5/2.6) were not executed against a live app — no running stack locally. Login/OAuth/2FA/export/deletion journeys verified only at unit + integration level. | All | Run console Playwright e2e (incl. the two security-attack suites) against staging before GA. |
| SMOKE-2-002 | MEDIUM | OAuth (2.3) and email verification (2.2) were not exercised against **real** Google/GitHub IdPs or a real email round-trip — verified via mocks/httptest only. | J2, J3 | Run one live OAuth round-trip per provider + one real email-verification flow against staging before GA. |
| SMOKE-2-003 | LOW | Pre-existing console **build-surface** issues noted on HEAD (non-async `maskEmail` under `next build`, `next lint`) are unrelated to Epic-2 test verdict but should be cleared before a production build. | N/A (build) | Confirm `next build` / `next lint` clean in CI as part of the launch checklist (Epic 10.7). |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline, this report §2) | `go test ./...` (auth-svc) + targeted gateway account-data/deletion run + `vitest run` (console 2.1/2.2/2.3) — all green |

No screenshots captured (no live browser session — see SMOKE-2-001).

## 7. Recommendation

**Result**: PASS

Epic 2 is production-ready at the unit + integration level: all 7 stories are Done and
the full regression suite is green (~540 tests, 0 failures, 33 intentional console
skips), covering every core journey — console skeleton + i18n, email signup/login/
verification (HIBP + tokens), Google/GitHub OAuth (CSRF state + linking), TOTP 2FA
(enroll/challenge/recovery/disable), profile management, GDPR JSON export, and account
deletion with 30-day grace (cancel, grace-expiry 410, sweeper purge).

Confidence is **MEDIUM** because the security- and identity-sensitive surfaces were not
exercised live: console Playwright e2e incl. attack suites (SMOKE-2-001) and real
IdP/email round-trips (SMOKE-2-002). Given this is the authentication epic, recommend
running both against staging before the GA cutover. The pre-existing build-surface
items (SMOKE-2-003) fold into the Epic-10.7 launch checklist.
