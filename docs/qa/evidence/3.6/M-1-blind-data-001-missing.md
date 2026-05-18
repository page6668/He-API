# M-1 — 3.6-BLIND-DATA-001 Missing INT Sweep

**Severity**: MEDIUM (non-blocking)
**Category**: DATA / cross-cutting test coverage
**Story**: 3.6
**Detected at**: Step 5.5 (Blind Spot Verification)

## Finding

The QA test design (`docs/qa/assessments/3.6-test-design-20260519.md`)
enumerates **12 BLIND-SPOT** scenarios. **11 of 12** are implemented in
code; one is missing:

- **3.6-BLIND-DATA-001** — `Test_All_7_refactor_sites_header_equals_body`
  Cross-site integration sweep asserting `body.Error.HeRequestID ==
  header.Get("X-He-Request-Id")` across all seven refactored writers:
  1. `handlers/auth.go writeError`
  2. `handlers/chat_completions.go writeChatError`
  3. `middleware/bearer_auth.go writeAPIKeyError`
  4. `middleware/jwt_verify.go writeJWTError`
  5. `middleware/csrf.go writeCSRFViolation`
  6. `middleware/oauth_ratelimit.go` 503 site
  7. `middleware/oauth_ratelimit.go` 429 site

## Partial Coverage (Existing)

| Site | Coverage |
|------|----------|
| auth.go writeError | `Test_INT_005_auth_signin_401_emits_5_field_envelope` (cmd/server/requestid_int_test.go:216) |
| chat_completions writeChatError | `Test_INT_004_chat_completions_413_header_equals_body` (cmd/server/requestid_int_test.go:180) + chat_completions_test.go assertHeRequestID flips |
| bearer_auth writeAPIKeyError | `Test_INT_002_models_401_header_equals_body` (cmd/server/requestid_int_test.go:133) + bearer_auth_test.go bearerHeRequestIDRE assertions |
| jwt_verify writeJWTError | Existing `TestRequireAAL_AAL1Rejected` only asserts `403_aal2_required` code presence — does NOT verify full 5-field envelope nor `he_request_id` regex match |
| csrf writeCSRFViolation | No dedicated test for envelope shape post-refactor (Architect Round 2 L2 anchor) |
| oauth_ratelimit 503 / 429 | Existing `TestOAuthRatelimit_*` assert rate-limit BEHAVIOR only; envelope shape not asserted |

## Mitigating Factors

- `openaierr.Write` is the SOLE canonical writer (64 call sites across 15 files,
  verified by grep; ALL divergent writers — `writeError`, `writeChatError`,
  `writeAPIKeyError`, `writeJWTError`, `mapErrorType`, `mapChatErrorType` — DELETED;
  inline `http.Error` JSON literals in csrf/oauth_ratelimit REMOVED).
- 15 `openaierr` unit tests pin the byte-exact 5-field envelope shape and field
  order (BR-1.7 canonical order: `code` → `message` → `type` → `param` → `he_request_id`).
- Table-driven `Test_Write_status_and_type_table_driven` exercises 50+ codes uniformly.
- Joint header==body invariant is verified at 3 representative sites
  (INT-002 / INT-004 / INT-005) and is structurally guaranteed by every call
  routing through `openaierr.Write`, which reads the same context-stamped
  request-id.

## Risk Assessment

The 5-field envelope contract is correct in production — it is verified by the
canonical writer and its unit-test suite. The missing sweep would only catch
**future regressions** where a contributor reintroduces a divergent writer
(bypassing `openaierr.Write`). That regression class is bounded to a single
review/CI lane and can be caught by a static-analysis check (forbid
`http.ResponseWriter.Write` outside `openaierr/`).

## Recommendation

Non-blocking. The shape contract is held. Recommend FOLLOWUP Story to add:

- `INT-009` — `Test_JWT_401_403_emits_5_field_envelope_with_he_request_id` (Architect Round 2 L3)
- `INT-010` — `Test_CSRF_403_emits_5_field_envelope_with_he_request_id` (Architect Round 2 L2)
- `INT-011` — `Test_OAuthRatelimit_429_503_emits_5_field_envelope_with_he_request_id`

These three integration tests would satisfy the BLIND-DATA-001 sweep + the two
Architect Round 2 Low observations.

## Reproduction

```bash
# 1. Show no implementation
grep -rn "Test_All_7_refactor_sites_header_equals_body\|3.6-BLIND-DATA-001" \
  apps/api-gateway/ packages/
# (no matches)

# 2. Show test design specifies it
grep -n "3.6-BLIND-DATA-001" docs/qa/assessments/3.6-test-design-20260519.md
# 173:| **3.6-BLIND-DATA-001** | DATA-002 | I | P0 | `Test_All_7_refactor_sites_header_equals_body` ...

# 3. Show contract is still held via canonical writer
grep -rc "openaierr.Write" apps/api-gateway/internal/{handlers,middleware} | grep -v ":0"
# 64 call sites total across 15 files
```
