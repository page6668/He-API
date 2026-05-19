# Story 4.4 — Test Execution Evidence

**Round:** 1 | **QA:** Turing | **Date:** 2026-05-19

## Independent Test Execution

All commands were run by QA from a clean working tree:

```
$ go test -race -count=1 ./apps/adapters/glm/...
ok  	github.com/he-api/he-api/apps/adapters/glm/internal              3.476s
ok  	github.com/he-api/he-api/apps/adapters/glm/internal/upstream     3.829s
ok  	github.com/he-api/he-api/apps/adapters/glm/internal/usage        2.567s
ok  	github.com/he-api/he-api/apps/adapters/glm/tests                 4.734s

$ go test -race -count=1 ./apps/api-gateway/internal/adapterclient/...
ok  	github.com/he-api/he-api/apps/api-gateway/internal/adapterclient 1.885s

$ go test -race -tags chaos -count=1 ./apps/adapters/glm/tests/...
ok  	github.com/he-api/he-api/apps/adapters/glm/tests                 2.943s

$ go test -race -count=1 ./apps/api-gateway/tests/...
ok  	github.com/he-api/he-api/apps/api-gateway/tests                  2.522s
```

**Total passing tests across all Story-4.4 surfaces:** 72
- `internal/`: 12 tests (Chat handler, BoundModelIDs, race-clean, PII-safe logs)
- `internal/upstream`: 14 tests (translate, client ALPN, classify, SSE decoder)
- `internal/usage`: 6 tests (Normaliser identity + 3 constraint paths + zero-completion edge)
- `tests/`: 9 integration (END-to-END via httptest.NewTLSServer)
- `tests/` (chaos build-tag): 7 chaos tests (5xx burst, slow loris, mid-stream RST, full timeout, TLS handshake, DNS, 429 rate-limit)
- `adapterclient/`: 4 new GLM-specific tests (out of 24 total registry tests, all PASS)

**No discrepancy** with Dev's claim in the Story Dev Agent Record:
> "All Go tests pass under `-race`; chaos suite passes under `-tags chaos`."

## Cross-Vendor Regression

`TestRegistry_AllFourVendorsCoRegistered_NoCrossVendorShadowing` (4.4-INT-009) confirms
that DeepSeek (Story 4.1), Qwen (Story 4.2), Kimi (Story 4.3), AND GLM (Story 4.4)
entries all resolve to their correct endpoints in a single Registry instance with NO
cross-vendor entry shadowing.

## Skeleton / Skipped Tests (Acknowledged Posture)

The following tests are intentional `t.Skip("…moved to…")` placeholders per the Story-4.4
File Locations table — they preserve QA's scenario inventory while delegating actual
execution to per-package tests:

```
=== RUN   Test4_4_AC1
--- SKIP: Test4_4_AC1 (0.00s)
=== RUN   Test4_4_AC2
--- SKIP: Test4_4_AC2 (0.00s)
=== RUN   Test4_4_AC3
--- SKIP: Test4_4_AC3 (0.00s)
=== RUN   Test4_4_BlindSpot
--- SKIP: Test4_4_BlindSpot (0.00s)
```

This is **not** a regression — actual execution is in
`apps/adapters/glm/{internal,internal/upstream,internal/usage,tests}` (all PASS) +
`apps/api-gateway/internal/adapterclient/` (all PASS).

## Live-Exercise Posture (E2E + CONTRACT)

`apps/api-gateway/tests/openai_sdk_glm_contract_test.py` and `openai_sdk_glm_live_test.py`
are intentionally skeleton-only per Dev posture (cascade from Stories 4.1-4.3):

- CONTRACT-001/002 gated by `HE_API_TEST_GATEWAY_URL` env var (unset on dev workstation)
- E2E-001/002 gated by `HE_API_GLM_LIVE=1` env var (unset; requires Zhipu account access)

This is documented in Dev Agent Record Open Issues and is the same posture ratified by
the Story-4.1/4.2/4.3 precedents.
