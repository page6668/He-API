# Smoke Test Report: Epic 9

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 9 — 可观测性与多模态 (Observability, Analytics & Multimodal) |
| **Trigger**      | manual (`QA *smoke-test 9`)        |
| **Executed At**  | 2026-06-16T08:25:22Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 9.1 | 用量大盘（实时 + 历史聚合） | Done |
| 9.1b | 用量趋势图（<UsageChart> + /v1/me/usage/series） | Done |
| 9.2 | 实时调用日志（最近 1000 条） | Done |
| 9.3 | 历史日志下载（90 天 + JSON/CSV） | Done |
| 9.4 | 全链路 trace（OpenTelemetry） | Done |
| 9.5 | Vision API（兼容 OpenAI）+ Qwen-VL/GLM-4V | Done |
| 9.6 | ASR API（兼容 Whisper）+ Doubao | Done |
| 9.7 | TTS API + Doubao | Done |

- **Total Stories**: 8
- **Done**: 8
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

> Epic 9 is a **mixed** epic: a Go backend (analytics ingestion + ClickHouse, query
> APIs, OpenTelemetry tracing, multimodal vision/ASR/TTS handlers and adapters) **and**
> a console analytics UI (dashboard, trend chart, logs, export). Regression vehicles:
> Go `go test ./...` across all Epic-9 modules **and** the console `vitest` unit
> suites. The four console Playwright e2e specs (9.1/9.1b/9.2/9.3) require a running
> app and were **not** driven live (no running stack locally) — see SMOKE-9-001.

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | ~285 (227 Go + 58 console) |
| **Tests Failed**| 0                |
| **Skipped**     | 1 (intentional — see note) |

> **Intentional skip:** `9.3-UNIT-050` (range picker — not exposed; fixed 90-day
> window) is `test.skip` by design, documented in the story skeleton. Not a gap.

### Suite Breakdown

| Module | Story | Command | Result |
|--------|-------|---------|--------|
| `apps/analytics-svc` | 9.1/9.2/9.3 | `go test ./...` | ok — clickhouse writer/row, dumps + request_logs_export, workers (37) |
| `packages/go-observability` | 9.4 | `go test ./...` | ok — tracer, http, meter, logger, requestid, trace_propagation_9.4, trace_integration_9.4 (49) |
| `packages/models-catalogue` | 9.6/9.7 | `go test ./...` | ok — doubao asr/tts catalogue entries (3) |
| `apps/api-gateway/internal/analyticsquery` | 9.1/9.1b/9.2/9.3 | `go test ./...` | ok — series handler, logs handler, integration (49) |
| `apps/api-gateway/internal/analyticslog` + `internal/usage` | 9.1/9.2 | `go test ./...` | ok — analytics log + counter reader (7) |
| `apps/api-gateway/internal/handlers` (vision/audio/export) | 9.5/9.6/9.7/9.3 | `go test ./...` | ok — chat_completions_vision, audio_transcriptions, audio_speech, usage_log_export, ab_observ (65) |
| `apps/adapters/{qwen,glm}` | 9.5 | `go test ./...` | ok — vision-capable chat adapters |
| `apps/adapters/doubao` | 9.6/9.7 | `go test ./...` | ok — asr_transcribe, tts_synthesize (+ chaos/adapter) |
| `apps/notification-svc` | 9.3 | `go test ./...` | ok — usage_log_export repository/template/handler (export-ready email) |
| `apps/console` `__tests__/9.1,9.1b,9.2,9.3` | 9.1–9.3 | `vitest run` | 58 pass / 1 skip — dashboard, UsageChart, request logs, log export |

No failed tests.

## 3. Core User Journeys

> **Verification mode note:** No console page was rendered in a live browser (no
> running stack locally). UI journeys are verified at the **vitest unit level**
> (component + action behavior); the gateway read APIs they call are verified by
> `analyticsquery` integration tests. Multimodal journeys are verified via
> **fake-upstream** adapters, not live provider endpoints. Residual gaps tracked in §5.

### Journey 1: Usage dashboard — realtime + historical aggregation (9.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Open dashboard | Realtime + historical aggregates render | console `9.1-usage-dashboard.test` pass; gw `analyticsquery/handler_test` pass | PASS |

### Journey 2: Usage trend chart (9.1b)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Render `<UsageChart>` from `/v1/me/usage/series` | Series chart renders, a11y table, RTL, rapid-toggle latest-wins | console `9.1b-usage-trend-chart.test` (25) pass; gw `analyticsquery/series_*` pass | PASS |

### Journey 3: Realtime call logs — last 1000 (9.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Open logs page | Most-recent 1000 entries listed | console `9.2-request-logs.test` pass; gw `analyticsquery/logs_*` pass | PASS |

### Journey 4: Historical log download — 90d + JSON/CSV (9.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Request export (JSON/CSV, 90-day window) | Export job created, email when ready | console `9.3-log-export.test` pass; gw `usage_log_export_test`; `analytics-svc/dumps/request_logs_export_test`; `notification-svc` export suites pass | PASS |

### Journey 5: Full-chain trace — OpenTelemetry (9.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Request flows gateway→adapter | Trace context propagated, spans emitted | `go-observability/trace_propagation_9.4_test`, `trace_integration_9.4_test`, `tracer_test` pass | PASS |

### Journey 6: Vision API — OpenAI-compatible + Qwen-VL/GLM-4V (9.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Chat completion with image parts | Routed to Qwen-VL/GLM-4V; multimodal passthrough | gw `chat_completions_vision_test` (+ internal); qwen/glm adapter tests pass | PASS |

### Journey 7: ASR API — Whisper-compatible + Doubao (9.6)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Audio transcription request | Whisper-compatible response via Doubao | gw `audio_transcriptions_*_test`; doubao `asr_transcribe_test` pass | PASS |

### Journey 8: TTS API + Doubao (9.7)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Speech synthesis request | Audio returned via Doubao | gw `audio_speech_internal_test`; doubao `tts_synthesize_test` pass | PASS |

**Summary**: 8 / 8 journeys passed (unit + integration / fake-upstream)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live browser run; console unit suites clean (Recharts width/height stderr is jsdom ResizeObserver noise, not an error) |
| Network Failures     | N_A    | No live HTTP run; gateway query/export HTTP verified via httptest |
| Visual Consistency   | N_A    | No live render; chart a11y (radiogroup + hidden table) + RTL covered in 9.1b unit suite |
| Performance          | N_A    | No live page-load measured |
| Auth Flow            | N_A    | Analytics read APIs bind to authenticated user context (Epic 2/5); not re-exercised here |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-9-001 | MEDIUM | The four console Playwright e2e specs (9.1 dashboard, 9.1b trend-chart, 9.2 logs, 9.3 export) were **not** executed against a live app — no running stack locally. Console journeys are verified only at the vitest unit level + gateway integration tests; full browser render + real network round-trip is unverified. | Journeys 1–4 | Run `apps/console` Playwright e2e (9.1/9.1b/9.2/9.3) against a deployed/staging console before GA. |
| SMOKE-9-002 | LOW | Multimodal journeys (vision/ASR/TTS) verified via **fake-upstream** adapters, not live Qwen-VL/GLM-4V/Doubao endpoints. | Journeys 6–8 | Run one live multimodal smoke per provider (image-in, audio-in, text-to-speech) against real upstreams before GA. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline, this report §2) | `go test ./...` output — analytics-svc, go-observability, models-catalogue, gateway analytics/handlers, adapters, notification-svc — all `ok` |
| log | (inline, this report §2) | `vitest run` output — console 9.1/9.1b/9.2/9.3 — 58 pass / 1 intentional skip |

No screenshots captured (no live browser session — see SMOKE-9-001).

## 7. Recommendation

**Result**: PASS

Epic 9 is production-ready at the unit + integration level: all 8 stories are Done and
the full regression suite (~285 tests, 0 failures, 1 intentional skip) is green across
the analytics backend (ingestion, ClickHouse, query APIs, export), OpenTelemetry
tracing, multimodal vision/ASR/TTS handlers and adapters, the export-ready
notification path, and the console analytics UI unit suites.

Confidence is **MEDIUM** because two real-world slices were not exercised in this
environment: live console Playwright e2e against a running app (SMOKE-9-001, MEDIUM)
and live multimodal round-trips against real provider upstreams (SMOKE-9-002, LOW).
Recommend running the console e2e specs on a staging console and one live multimodal
smoke per provider before the GA cutover.
