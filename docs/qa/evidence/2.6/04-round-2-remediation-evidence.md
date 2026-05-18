# Story 2.6 — Round 2 Remediation Evidence

**Date**: 2026-05-18
**Round**: 2
**Reviewer**: Turing
**Mode**: incremental (Round 1 gate = FAIL → Round 2 review of fixes)

## Round 1 Issue Closure Map

| Issue | Round 1 Severity | Round 2 Status | Evidence |
|-------|------------------|----------------|----------|
| ISSUE-1 | CRITICAL — production NoOp wiring | CLOSED (Option-C per gate's own recommendation) | `apps/analytics-svc/cmd/server/main.go:98-110` env-gated; default `HE_API_ANALYTICS_GDPR_WORKER_ENABLED=false`; binary refuses Kafka subscription while NoOps wired |
| ISSUE-2 | HIGH — AC1 BR-1.9 a11y | CLOSED | `apps/console/components/business/ExportDataDialog.tsx:209-286` — focus trap (Tab/Shift+Tab), focus restore via `restoreFocusTo` + `requestAnimationFrame`, document-level ESC, initial-focus, overlay-click; misleading comment removed |
| ISSUE-3 | HIGH — 5 zero-test packages | CLOSED | 5 new test files / 34 top-level Test funcs: `audit_test.go` (6), `events/gdpr_publisher_test.go` (5), `ratelimit/gdpr_export_test.go` (8), `repository/data_export_requests_test.go` (6), `workers/gdpr_export_test.go` (9) |
| ISSUE-4 | HIGH — TS-CONS-004 runtime breach | CLOSED (formally — subset of ISSUE-1 remediation) | analytics-svc audit emits gated by Option-C; partial-emit state now operator-controlled, not silent |
| ISSUE-5 | MEDIUM — BR-2.5 doc/code drift | CLOSED | `repository/data_export_requests.go:55-70` rewritten — "as-built design delegates atomicity to the Redis Lua INCR safety-net"; `handlers/data_export.go:92-110` flow comment acknowledges BR-2.5 deviation explicitly |
| ISSUE-6 | MEDIUM — commit-before-publish ordering | CARRYOVER | Gated by Option-C; mitigation = T8.6 operator runbook (still in §Deferred) |
| ISSUE-7 | MEDIUM — BR-6.5 PII guard untested | CLOSED | `audit_test.go:59-113` `TestBR65_PIIGuard_GDPREvents` covers all 3 GDPR event types; asserts no email/display_name/IP/UA in payload or metadata |
| ISSUE-8 | MEDIUM — BR-4.6 ZIP size cap | CARRYOVER | Gated by Option-C; worker not subscribed → no in-memory ZIP build active |
| ISSUE-9 | MEDIUM — BR-5.6 retry policy | CARRYOVER | Gated by Option-C; worker not subscribed → no Send() invocation |
| ISSUE-10 | LOW — BR-6.3 cta-key count drift | CARRYOVER | SM correction (non-blocking) |

## Independent Test Execution Results

- `go test -count=1 ./apps/api-gateway/internal/handlers/... ./apps/notification-svc/... ./apps/analytics-svc/... ./apps/auth-svc/...` — ALL PASS (~150s total)
- `tsx scripts/check-i18n-keys.ts` — PASS (3 namespaces × 10 locales)
- `pnpm --filter @he-api/console typecheck` — PASS (0 diagnostics)

## Blind Spot Coverage Improvement

3 of 6 previously-missing blind spots now COVERED (CONCURRENCY-RACE-2.5, FLOW-DOUBLE-CONSUME, BOUNDARY-A11Y-FOCUS). 3 still MISSING_CODE (DATA-PII-DUMP, RESOURCE-ZIP-MEM, ERROR-EMAIL-RETRY) but all behind Option-C flag — will require attention when the worker is enabled in a follow-up Story.

## Disposition Note

The Story closes the contract surface honestly:
- All compile, all unit-tested, all docs aligned with as-built code
- Production binary is operator-safe (Option-C flag prevents claiming Kafka messages while NoOps wired)
- End-to-end fulfillment (real OSS + ClickHouse SDK vendoring) is the natural Phase-2 follow-up

This matches the Story's stated scope as the "contract surface + request path" deliverable.
