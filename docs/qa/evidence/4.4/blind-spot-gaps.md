# Story 4.4 — Blind-Spot Test-Coverage Gaps (Non-Blocking)

**Round:** 1 | **QA:** Turing | **Date:** 2026-05-19

## Summary

All 13 blind-spot scenarios from `docs/qa/assessments/4.4-test-design-20260519.md` have
implementation code paths present in the adapter. 7 scenarios have explicit test
coverage; **6 scenarios have CODE-only coverage with no dedicated test**. Following the
Story-4.3 precedent (Round 1 passed 96/100 with a similar fold-into-per-package pattern),
these are flagged as MEDIUM follow-up items rather than gate blockers.

## Gap Detail

| ID | Severity | Code Location | Recommended Test |
|----|----------|---------------|------------------|
| 4.4-BLIND-BOUNDARY-002 | MEDIUM | `apps/adapters/glm/internal/upstream/translate.go` (passthrough; no content validation) | 1-line unit test asserting empty `content` survives `buildRequestBody` to body bytes. |
| 4.4-BLIND-ERROR-003 | MEDIUM | `apps/adapters/glm/internal/adapter.go:171-177` (`ErrorKindEmptyChoices` branch) | `TestChat_NonStreaming_EmptyChoices_IsHardFailure` mirroring `TestChat_NonStreaming_MissingUsage_HardFailure`. |
| 4.4-BLIND-CONCURRENCY-002 | MEDIUM | `defer httpResp.Body.Close()` at `adapter.go:143, 220`; transport idle-conn config in `upstream/client.go` | Sustained-load test: 1000 sequential `glm-4` non-streaming calls; `runtime.NumGoroutine()` returns to baseline ± 5. `-short`-skippable. |
| 4.4-BLIND-DATA-002 | MEDIUM | `packages/adapter-usage.ValidateInvariants` (M1 lift) | `func FuzzValidateInvariants(f *testing.F)` in `packages/adapter-usage/usage_test.go` — single-source property test rather than per-vendor duplication. |
| 4.4-BLIND-RESOURCE-001 | MEDIUM | `defer httpResp.Body.Close()` exit-path pattern in `adapter.go` | Exit-path matrix test using `http.Transport.IdleConnCountFor` over success / 5xx / 4xx-auth / 4xx-rate-limit / timeout / DNS / TLS / client-cancel. |
| 4.4-BLIND-RESOURCE-002 | MEDIUM | `apps/adapters/glm/internal/upstream/sse_decoder.go:21` (`sseMaxLineBytes = 1<<20`) | Two-case boundary test: 1<<20-byte payload succeeds; 1<<20+1-byte payload returns `ErrMalformedFrame`. |

## Decision Rationale

These six gaps do NOT change the gate outcome because:

1. **Code paths exist** for every scenario — the adapter handles them correctly; what's missing is
   regression hardening (test surfaces), not functionality.
2. **Indirect coverage** is present: e.g., empty-choices shares its error-path infrastructure
   with missing-usage (which is tested by `TestChat_NonStreaming_MissingUsage_HardFailure`).
3. **Story-4.3 precedent**: Round 1 passed at 96/100 with the same fold-into-per-package
   pattern; QA was tolerant of the cascade-story posture where the dominant risk surfaces
   (R1-R9) all have explicit COVERED tests, and blind-spot regression hardening is
   incremental.
4. **All dominant risks** (R1 = upstream-failure resilience; R2 = token-usage drift;
   R3 = request-id propagation; R6 = mid-stream resource leak) have explicit COVERED
   coverage via integration / chaos / unit tests.

**Recommendation:** Schedule a follow-up task to add the six tests above before Story
4.5 (Doubao) is started — this hardens the blind-spot regression net for the remaining
two Epic-4 adapters. Non-blocking for Story 4.4 itself.
