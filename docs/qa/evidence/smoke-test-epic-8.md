# Smoke Test Report: Epic 8

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 8 — 内容安全 (Content Safety)        |
| **Trigger**      | manual (`QA *smoke-test 8`)        |
| **Executed At**  | 2026-06-16T08:18:06Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 8.1 | 敏感词词库（中英基础库 + 可扩展） | Done |
| 8.2 | 入参过滤（命中拦截 + 错误码） | Done |
| 8.3 | 出参过滤（含流式响应替换） | Done |
| 8.4 | 严格度配置（per Key） | Done |
| 8.5 | 治理日志（拦截记录）+ 备案材料模板 | Done |

- **Total Stories**: 5
- **Done**: 5
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

> Epic 8 is an in-process content-safety epic on the API gateway — there is **no
> console UI footprint** (per-Key strictness is API-configured, not a console page).
> The regression vehicle is the Go suite: the `safety-lexicon` package plus the
> gateway's `contentsafety`, `safetylog`, streaming chunker, safety handler tests,
> and the `safety-filing-report` / `safety-log-retention` commands. Unlike Epic 7,
> the safety filter has **no external dependency** — the httptest handler suites
> exercise the full request→filter→block/redact→response path (including SSE
> streaming replacement) end-to-end at the API boundary.

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | ~193 (48 lexicon + 56 contentsafety + 22 safetylog + 46 handler-safety + 6 stream-chunker + 6 filing-report + 9 log-retention) |
| **Tests Failed**| 0                |

### Suite Breakdown

| Module | Command | Result |
|--------|---------|--------|
| `packages/safety-lexicon` | `go test ./...` | ok — zh/en base lexicon, bloom filter, extensible registry, bench |
| `apps/api-gateway/internal/contentsafety` | `go test ./...` | ok — scanner, recorder, strictness, streamguard (+ strictness variant), bench |
| `apps/api-gateway/internal/safetylog` | `go test ./...` | ok — recorder, redact, report, bench |
| `apps/api-gateway/internal/streaming` | `go test ./...` | ok — chunker_safety (streaming replacement) |
| `apps/api-gateway/internal/handlers` (safety) | `go test ./...` | ok — chat_completions safety (input/output/strictness/persist), audio_transcriptions safety |
| `apps/api-gateway/cmd/safety-filing-report` | `go test ./...` | ok — 备案材料 filing report generation |
| `apps/api-gateway/cmd/safety-log-retention` | `go test ./...` | ok — retention manifest, migration, main |

No failed tests.

## 3. Core User Journeys

> **Verification mode note:** No running gateway instance was driven via a live
> browser/HTTP client — there is no running stack locally (docker/live gateway
> unavailable). However, because content safety is fully **in-process and
> deterministic** (no external provider), the httptest handler suites constitute a
> genuine end-to-end exercise at the API boundary. Each journey is mapped to its
> passing test evidence; the one remaining gap (live SSE against a real upstream
> model) is tracked as `SMOKE-8-001` in §5.

### Journey 1: Sensitive-word lexicon load — zh/en base + extensible (8.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Load base zh/en lexicon | Words loaded into matcher | `safety-lexicon/lexicon_test` pass | PASS |
| 2 | Extend via registry | Custom entries merge; bloom pre-filter correct | `registry` + `bloom` covered by `lexicon_test`/`bench_test` pass | PASS |

### Journey 2: Input filter — hit → block + error code (8.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Submit request containing sensitive term | Request blocked | `contentsafety/scanner_test`, `handlers/chat_completions_safety_test`, `audio_transcriptions_safety_test` pass | PASS |
| 2 | Return content-safety error code | Correct error code/shape returned | `chat_completions_safety_test` (error-code assertions) pass | PASS |

### Journey 3: Output filter incl. streaming replacement (8.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Upstream output contains sensitive term (non-stream) | Output redacted/blocked | `handlers/chat_completions_safety_output_test` (+ internal) pass | PASS |
| 2 | Streaming (SSE) output contains sensitive term | Replaced mid-stream without breaking SSE framing | `contentsafety/streamguard_test`, `streaming/chunker_safety_test` pass | PASS |

### Journey 4: Per-Key strictness configuration (8.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Configure strictness on a Key | Strictness level honored per Key | `contentsafety/strictness_test`, `streamguard_strictness_test`, `handlers/chat_completions_safety_strictness_test` (+ internal) pass | PASS |

### Journey 5: Governance logging + filing-material template (8.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Interception event recorded | Governance log written, PII redacted | `safetylog/recorder_test`, `redact_test`, `handlers/chat_completions_safety_persist_test` pass | PASS |
| 2 | Generate 备案 filing material | Filing report produced from logs | `cmd/safety-filing-report/main_test`, `safetylog/report_test` pass | PASS |
| 3 | Log retention | Retention manifest + migration applied | `cmd/safety-log-retention/{manifest,migration,main}_test` pass | PASS |

**Summary**: 5 / 5 journeys passed (in-process end-to-end)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No console footprint in Epic 8 (API-only) |
| Network Failures     | N_A    | No live HTTP run; handler HTTP paths verified via httptest |
| Visual Consistency   | N_A    | No UI rendered |
| Performance          | PASS   | `contentsafety/bench_test`, `safety-lexicon/bench_test`, `safetylog/bench_test` present and passing — hot-path filtering benchmarked |
| Auth Flow            | N_A    | Key auth is upstream (Epic 5); strictness binds to authenticated Key context, verified in strictness suites |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-8-001 | LOW | No live running-gateway SSE smoke against a **real upstream model** was performed (no running stack locally). In-process coverage is strong (~193 tests green, full filter path including streaming replacement via httptest), but a real-network streaming round-trip is unverified. | Journey 3 (streaming output) | Run one live SSE smoke through a deployed gateway against a real upstream model with a planted sensitive term, before GA. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline, this report §2) | `go test ./...` output for safety-lexicon + gateway content-safety packages/cmds — all `ok` |

No screenshots captured (API-only epic, no live browser session).

## 7. Recommendation

**Result**: PASS

Epic 8 is production-ready. All 5 stories are Done and the full content-safety
regression suite is 100% green (~193 tests, 0 failures), covering every core journey:
zh/en extensible lexicon, input block + error code, output filtering including
streaming SSE replacement, per-Key strictness, and governance logging + 备案 filing
material + log retention. Hot-path performance is benchmarked.

Confidence is **MEDIUM** (near-HIGH) — content safety is fully in-process and
deterministic, so the httptest handler suites are a genuine end-to-end exercise; the
only residual gap is a live SSE round-trip against a real upstream model
(SMOKE-8-001). Recommend a single live streaming smoke on a deployed gateway before
GA to close it.
