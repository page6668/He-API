# Story 4.1 — QA Round 1 Evidence: Blind-Spot Coverage Gaps

Generated 2026-05-19 during `*review 4.1` (QA Turing).

## Summary

Test design `docs/qa/assessments/4.1-test-design-20260519.md` defined 13 blind-spot scenarios as part of Comprehensive coverage. After independent inspection of the implemented test suite, **2/13 are fully covered, 1/13 is partial, 1/13 is acceptably deferred, and 9/13 are missing**.

Two of the missing scenarios are **P0**, and the test design explicitly enumerates them in **"Gate Criteria for Dev → Review"**. They are therefore promoted to HIGH-severity blocking-style issues in the QA gate.

## Independent Verification Steps

1. `grep -E "BLIND-|empty_messages|max_messages_array|uppercase_model|extra.*field|429|quota|three_way|byte_equality|connection.*released|sustained_load" apps/adapters/deepseek apps/api-gateway -r --include="*_test.go"`
2. `grep -E "func Test.*\\bConcurrent|EmptyMessages|MaxMessages|UppercaseModel|Malformed|Quota|ThreeWay|ByteEquality|ConnectionReleased|SustainedLoad" apps/adapters/deepseek apps/api-gateway -r --include="*_test.go"`

(commands executed against working tree on 2026-05-19; outputs preserved in step-5.5.yaml checkpoint)

## Coverage Matrix

| ID | Category | Priority | Status | Evidence |
|----|----------|----------|--------|----------|
| BLIND-BOUNDARY-001 | BOUNDARY | P1 | MISSING | empty-messages adapter defence-in-depth not asserted |
| BLIND-BOUNDARY-002 | BOUNDARY | P2 | MISSING | max-256 length not asserted |
| BLIND-BOUNDARY-003 | BOUNDARY | P1 | MISSING | uppercase model-id regex not asserted |
| BLIND-ERROR-001 | ERROR | P1 | COVERED | `chaos_test.go:TestCHAOS_006_DNSResolutionFailure` |
| BLIND-ERROR-002 | ERROR | P1 | COVERED | `sse_decoder_test.go:TestSSEDecoder_MalformedJSONIsMalformed` + `adapter_streaming_test.go:TestChatStreaming_MalformedUpstreamFrame_IsUnavailable` |
| BLIND-ERROR-003 | ERROR | P2 | MISSING | unknown-field tolerance not asserted |
| BLIND-ERROR-004 | ERROR | P1 | MISSING | 401 disambiguation tested (INT-004); **429 quota_exhausted** path absent — directly contradicts Architect Round 2 M2 ruling |
| BLIND-CONCURRENCY-001 | CONCURRENCY | P1 | PARTIAL | `TestChat_NonStreaming_ConcurrentCalls_NoSharedState` + `TestChatStreaming_ConcurrentCalls_NoSharedState` exist; mixed 50/50 concurrent-100 not asserted |
| BLIND-CONCURRENCY-002 | CONCURRENCY | P2 | DEFERRED | Acceptable per test-design — `t.Skip` with Epic-6 routing-svc pointer |
| **BLIND-DATA-001** | DATA | **P0** | **MISSING** | three-way request-id equality (header == body == slog) not asserted — R3 mitigation gap; **explicit gate criterion** |
| **BLIND-DATA-002** | DATA | **P0** | **MISSING** | token-usage byte equality across chain not asserted — R2 mitigation gap; **explicit gate criterion** |
| BLIND-RESOURCE-001 | RESOURCE | P1 | MISSING | connection-released-on-all-exit-paths (table-driven counter) absent |
| BLIND-RESOURCE-002 | RESOURCE | P1 | MISSING | 1000-request sustained-load pool-leak guard absent |

## Reproduction

```bash
cd apps/adapters/deepseek
grep -rE "BLIND-DATA-001|three_way|RequestId_continuity_three_way_equality" .
# (no matches)

cd ../../..
grep -rE "BLIND-DATA-002|byte_equality_across_chain|TokenUsage.*three_way" apps/adapters/deepseek apps/api-gateway
# (no matches)
```

## Recommendation

These gaps do **not** invalidate the implementation (all ACs verified, ~110 unit + 13 integration + 6 chaos pass; race-clean). They do reflect **test-design gate-criteria non-compliance**. The two P0 gaps should be closed before Story 4.1 reaches `Done`; the P1 + P2 gaps may bundle into a Dev fix pass or be tracked as follow-up tickets for Stories 4.2-4.6 (the same blind-spot taxonomy carries forward).
