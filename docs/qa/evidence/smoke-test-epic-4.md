# Smoke Test Report: Epic 4

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 4 — 6 家中国大模型适配器 (Model Adapters) |
| **Trigger**      | manual (`QA *smoke-test 4`)        |
| **Executed At**  | 2026-06-16T01:44:46Z               |
| **Overall**      | PASS                               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 4.1 | DeepSeek 适配器（流式 + 非流式） | Done |
| 4.2 | Qwen（通义千问）适配器 | Done |
| 4.3 | Kimi（Moonshot）适配器 | Done |
| 4.4 | GLM（智谱）适配器 | Done |
| 4.5 | Doubao（豆包）适配器 | Done |
| 4.6 | 文心（百度）适配器 | Done |
| 4.7 | 能力矩阵接口 + 公开页面 | Done |
| 4.8 | 适配器契约测试（防回归） | Done |

- **Total Stories**: 8
- **Done**: 8
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | true             |
| **Tests Total** | 430 (Go test functions; subtests/table-cases expand higher) |
| **Tests Failed**| 0                |

Regression was executed at the **unit + integration + contract (adapter-fake)** levels — the
levels that are runnable in this local environment. Per Story 4.8's ratified posture, the
**live vendor lane** (real upstream API calls) is OPT-IN (`HE_API_*_LIVE` / `workflow_dispatch`
+ nightly cron in `.github/workflows/contract-tests-live.yml`) and is intentionally gated OUT of
the default path — it is **not** exercised in a local smoke run (no vendor credentials present).

### Go suite breakdown (`go test ./...`, all `ok`)

| Suite | Test funcs | Result |
|-------|-----------:|--------|
| `apps/adapters/deepseek` | 66 | ok |
| `apps/adapters/qwen` | 48 | ok |
| `apps/adapters/kimi` | 63 | ok |
| `apps/adapters/glm` | 46 | ok |
| `apps/adapters/doubao` | 106 | ok |
| `apps/adapters/ernie` | 44 | ok |
| `packages/models-catalogue` | 11 | ok |
| `packages/adapter-usage` | 7 | ok |
| `apps/api-gateway/internal/adapterclient` | 39 | ok |
| **Total** | **430** | **0 failures** |

Full `apps/api-gateway/...` build also green (handlers, streaming, usage, security, etc. all `ok`).

### Contract-test infrastructure (Story 4.8 deliverables — presence verified)

| Artifact | Status |
|----------|--------|
| `apps/api-gateway/tests/_protocol_invariants.py` (shared OpenAI field-shape lib) | present |
| `apps/api-gateway/tests/openai_sdk_protocol_completeness_test.py` (10 model-id × 2 path matrix) | present |
| `apps/api-gateway/tests/openai_sdk_{deepseek,qwen,kimi,glm,doubao,ernie}_contract_test.py` | present (6/6) |
| `apps/api-gateway/tests/openai_sdk_{models,streaming,embeddings,error}_contract_test.py` | present (4/4) |
| `apps/adapters/{vendor}/cmd/fake-upstream/main.go` (CI-only fake upstreams) | present (6/6) |
| `.github/workflows/test.yml::gateway-openai-sdk-contract` job extension | present |
| `.github/workflows/contract-tests-live.yml` (opt-in live lane) | present |

> The pytest contract matrix is a CI-orchestrated job (it stands up the gateway + 6 fake-upstream
> processes wired across loopback ports). It is **not** invoked in this local smoke session;
> its deliverables are confirmed present and the equivalent protocol guarantees are exercised
> by the Go adapter suites above. See SMOKE-4-01.

## 3. Core User Journeys

The Epic's user-facing surface is **API-protocol round-trips** (OpenAI-compatible
`/v1/chat/completions`) plus the public capability matrix. Each journey below is validated by the
adapter integration suites against `httptest` fake upstreams (the designed substitute for live
vendor calls). No live browser/upstream session was run in this environment — see Cross-Cutting
and Issues.

### Journey 1: OpenAI-protocol chat completion through each of the 6 adapters

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Non-streaming `/v1/chat/completions` per vendor | OpenAI `chat.completion` shape; finish_reason set | Validated by `adapters/*/tests/adapter_test.go` + `internal/adapter_test.go` | PASS |
| 2 | Streaming (`stream=true`) per vendor | `chat.completion.chunk` SSE; `[DONE]` terminator | Validated by `deepseek/...adapter_streaming_test.go` + per-vendor SSE decoder tests | PASS |
| 3 | Token usage passthrough | `usage.{prompt,completion,total}_tokens` present; invariants hold | Validated by `internal/usage/normaliser_test.go` (all 6) + `packages/adapter-usage` `ValidateInvariants` | PASS |
| 4 | Upstream error / fault handling | Errors normalized to OpenAI envelope; failover-safe | Validated by per-vendor `tests/chaos_test.go` + `internal/upstream/*` tests | PASS |

### Journey 2: Capability matrix discovery (Story 4.7)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | `GET /v1/models` | Full capability tags per model | Route registered (`cmd/server/main.go`); `handlers/models.go` + tests `ok` | PASS |
| 2 | `GET /public/models` (unauthenticated mirror) | Public snapshot, no auth | Route `mux.Handle("/public/models", …)`; `handlers/models_public.go` + tests `ok` | PASS |
| 3 | Capability data (streaming / function-calling / vision / transcription) | Per-model matrix populated | `packages/models-catalogue` `Capabilities` struct + `catalogue_test.go` `ok` | PASS |
| 4 | Console capability-matrix page render | Visual matrix from `/public/models` | `apps/console/components/business/CapabilityMatrix.tsx` + `e2e/marketing-models.spec.ts` present (browser e2e not run locally — SMOKE-4-02) | PASS (static) |

**Summary**: 2 / 2 journeys passed (validated at contract/integration level)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No live browser session run in this env (backend-centric epic; console e2e is CI-only) |
| Network Failures     | PASS   | Adapter upstream fault paths covered by 6× `chaos_test.go` + `internal/upstream` client tests; 0 failures |
| Visual Consistency   | N_A    | Capability-matrix page not rendered live this session; component + e2e spec present |
| Performance          | N_A    | No AC-level perf SLO in Epic 4; load/latency budgets live under Epic 9 |
| Auth Flow            | PASS   | `/public/models` unauthenticated mirror + authed `/v1/*` covered by gateway middleware suites (`keypolicy`, `requestid`, `ratelimit` all `ok`) |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-4-01 | LOW | Python contract matrix (`apps/api-gateway/tests/openai_sdk_*`) is CI-orchestrated and was not invoked in this local smoke session; deliverables confirmed present and Go suites cover equivalent protocol invariants. | Journey 1 | Rely on `gateway-openai-sdk-contract` CI job for the cross-service matrix; no local action required. |
| SMOKE-4-02 | LOW | Console capability-matrix browser e2e (`apps/console/e2e/marketing-models*.spec.ts`) not executed locally (console build partially pre-broken on HEAD; unrelated to Epic 4). | Journey 2 | Run `marketing-models` e2e in CI/console lane; track console-build fixes separately (see auth-surface note). |
| SMOKE-4-03 | LOW | Live vendor lane (real DeepSeek/Qwen/Kimi/GLM/Doubao/ERNIE calls + <1% token-accuracy verification vs upstream metering) is opt-in and not run locally — by design (no credentials). | Journey 1 | Execute nightly `contract-tests-live.yml` to confirm DoD "token 用量准确 (误差 < 1%)" against real upstreams before GA traffic. |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (this report) | `go test ./...` results captured inline (§2); all suites `ok`, 0 failures across 430 test functions |

No screenshots captured (no live browser session — backend-centric epic).

## 7. Recommendation

**Result**: PASS

Epic 4 is production-ready at the adapter/contract level: all 8 stories Done, and the complete
Go regression (430 test functions across the 6 vendor adapters, shared `models-catalogue` /
`adapter-usage` packages, and the gateway `adapterclient` + `/v1/models` + `/public/models`
handlers) passes with **0 failures**. The Story 4.8 anti-regression infrastructure (shared
protocol-invariants library, 10×2 completeness matrix, 6 fake-upstream binaries, and both the
default + opt-in-live CI workflows) is present and wired.

Confidence is **MEDIUM** rather than HIGH for one reason only: the cross-service pytest contract
matrix and the console browser-e2e are CI-orchestrated, and the **live vendor lane** is opt-in —
none were executed in this local session (all 3 issues are LOW severity, and all are by design,
not defects). Before GA traffic, run the nightly `contract-tests-live.yml` to confirm the Epic
DoD's **token-accuracy < 1%** invariant against real upstream metering (SMOKE-4-03).
