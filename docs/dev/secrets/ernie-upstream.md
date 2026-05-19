# Ernie (Baidu Qianfan v2) Upstream API Key — Credentials & Vault Migration Path

> Story 4.6 — Ernie adapter. Vault path + rotation runbook for the
> upstream `https://qianfan.baidubce.com/v2/chat/completions` Bearer
> token consumed by the `apps/adapters/ernie/` Connect-RPC service.
>
> Template structure mirrors Story 1.6 m-4 `docs/architecture/database-
> bootstrap.md` "Credentials & Vault Migration Path" section (Topology /
> Migration Workflow / Credentials & Vault Migration Path / Capacity /
> Operator Runbook / Decision Lineage) — REUSE structure from Story-4.4
> `docs/dev/secrets/glm-upstream.md` verbatim with Ernie swaps.

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/ernie/` (per Story-4.1 OQ3 ratification, cascaded to Story 4.6; same `upstream/` prefix groups all six Epic 4 vendor keys) |
| Secret key | `api_key` (string; Baidu Qianfan IAM-issued Bearer token at the v2 OpenAI-compat endpoint; OQ-4.6-1 plain Bearer scheme — NOT api_key+secret_key→access_token legacy OAuth scheme) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `adapter-ernie-upstream-api-key` in namespace `he-api-adapters` |
| K8s `Secret` data key | `api_key` |
| Consuming env var | `ERNIE_UPSTREAM_API_KEY` (Architect Round 1 OQ-4.6-2 ratification: BRAND-name naming `ERNIE_*` — NOT `WENXIN_*` or `BAIDU_*`; rule: brand wins, final Epic-4 cascade closure after `DOUBAO_*`) |

The `kv/data/he-api/upstream/` prefix groups the six Epic 4 vendor keys
(DeepSeek / Qwen / Kimi / GLM / Doubao / Ernie) under a single Vault
policy + a single ClusterSecretStore. Operator rotates them as a cohort
where their support-contract renewal cadences align; Baidu keys are
independently rotatable.

## Migration Workflow

Story 4.6 lands the **contract** (Helm chart references the ExternalSecret;
adapter reads `ERNIE_UPSTREAM_API_KEY` env var at startup). The actual
Vault sync infrastructure (Vault server, ClusterSecretStore, External
Secrets Operator deployment) is **out of scope** for Story 4.6 — those
land in Epic 9 ops hardening (same posture as Stories 4.1-4.5).

Story 4.6 dev-mode fallback: operators set `ERNIE_UPSTREAM_API_KEY`
directly via `.env.local` for local development; the bootstrap script
`scripts/dev/seed-ernie-key.sh` sources it. CI runs against the mocked
`httptest.NewTLSServer` upstream — no real key required for the default
test lane. Live-exercise e2e tests are gated by `HE_API_ERNIE_LIVE=1`
(separate from `HE_API_DEEPSEEK_LIVE`, `HE_API_QWEN_LIVE`,
`HE_API_KIMI_LIVE`, `HE_API_GLM_LIVE`, and `HE_API_DOUBAO_LIVE` so
operators can run vendor suites independently — BR-2.7 caveat: Baidu
latency from non-mainland-China runners may flap).

## Credentials & Vault Migration Path

| Phase | State | Source |
|---|---|---|
| Story 4.6 dev | Local `.env.local` `ERNIE_UPSTREAM_API_KEY=...` | Operator-supplied; never committed |
| Story 4.6 CI (default lane) | Mocked upstream via `httptest.NewTLSServer` | No real key consumed |
| Story 4.6 CI (live lane) | Operator-triggered workflow_dispatch with `HE_API_ERNIE_LIVE=1`; key injected from GitHub Actions secret `ERNIE_UPSTREAM_API_KEY` | Bypasses Vault; one-off for E2E exercise |
| Story 4.6 staging (manual deploy) | Manually-created K8s `Secret` carrying `ERNIE_UPSTREAM_API_KEY` | One-off `kubectl create secret`; documented as TEMPORARY |
| Epic 9 ops | External Secrets Operator pulls from `kv/data/he-api/upstream/ernie/` | Vault → ESO → K8s `Secret` → pod env var |

## Capacity

Baidu Qianfan v2 API quota is account-level (NOT per-key) per current
platform docs. Story 4.6 uses a single account key shared across all
adapter-ernie replicas; throttling surfaces as HTTP 429 → adapter
Connect-RPC `Code.Unavailable` → gateway 502 (BR-1.4 mapping + BR-4.4
`upstream_error_kind=rate_limit_throttle` slog disambiguation, REUSE
Story-4.2 OQ-4.2-4 cascade).

Vault-side: one secret entry, ~50 bytes plaintext. Negligible.

## Operator Runbook

### Rotation (manual; Baidu does not support API-key auto-rotation)

1. Generate a new key in the Baidu Qianfan console (account-level → IAM
   "API Keys" management page).
2. Update `kv/data/he-api/upstream/ernie/api_key` in Vault.
   `vault kv put kv/he-api/upstream/ernie api_key=$NEW_KEY`
3. ExternalSecret's `refreshInterval: 1h` picks up the new value; force
   immediate refresh with:
   `kubectl annotate externalsecret adapter-ernie-upstream-api-key
   force-sync=$(date +%s) --namespace he-api-adapters --overwrite`
4. Adapter pods restart-by-rollout (the pod env var is read at startup;
   running pods continue with the old key until restarted):
   `kubectl rollout restart deployment/adapter-ernie --namespace he-api-adapters`
5. Verify in adapter logs:
   `kubectl logs -l app.kubernetes.io/name=adapter-ernie -n he-api-adapters --tail 50 | grep adapter_chat_request_end | head -5`
6. Revoke the old key in the Baidu Qianfan console (after step 4 fully rolls).

### Break-glass (key compromise / urgent revocation)

1. Revoke the compromised key in the Baidu Qianfan console FIRST. The
   adapter will start returning 502 `upstream_unavailable` with logged
   `upstream_error_kind=auth_revoked` — Grafana alert
   `ErnieAdapterAuthRevoked` fires within 30s.
2. Generate replacement key + update Vault + force ExternalSecret sync +
   rollout-restart (steps 2-5 above).
3. The window of `502_upstream_unavailable` to users is the time from
   step 1 revocation to step 4 rollout (typically < 90s).

### Rate-limit / throttle (operator runbook for 429s, NOT key rotation)

`upstream_error_kind=rate_limit_throttle` (BR-4.4 — cascade REUSE from
Story-4.2) logs indicate the Baidu account hit its rate-limit / monthly
cap. Operator action: contact Baidu for quota top-up; do NOT rotate the
key (rotation does not increase quota). Distinct from `auth_revoked`
(`401` — key problem) and `upstream_5xx` (`>=500` — upstream incident).

### Missing-usage triage (BR-3.4)

`validation_failure=missing_usage` or `validation_failure=missing_tail_usage`
slog records indicate Baidu Qianfan v2 did not return a `usage` field on
the response (non-streaming) or the tail SSE chunk (streaming, even
with `stream_options.include_usage=true` set). Per OQ-4.6-4 ratification
the adapter does NOT fabricate `usage` counts; the user-visible result
is 502 `upstream_unavailable`. Operator action: file ticket with Baidu
support referencing the missing `usage` field. Until Baidu confirms /
fixes, consider falling back to mock for affected traffic via
`unset ERNIE_ADAPTER_ENDPOINT` + gateway rollout-restart.

### Qianfan v2 vs legacy endpoint (do not regress)

The Vault-stored key MUST be a Qianfan v2 IAM key — Baidu's legacy
pre-Qianfan-v2 endpoint (`https://aip.baidubce.com/rpc/2.0/ai_custom/v1/
wenxinworkshop/chat/completions_pro`) uses an api_key+secret_key→
access_token OAuth-style exchange scheme distinct from the plain Bearer
scheme accepted at the Qianfan v2 path. Operator verification step on
first deploy: run the curl exploration documented in
`docs/dev/logs/4.6-dev-log.md` Phase 0; if the Qianfan v2 endpoint
returns an authentication-failure that requires the legacy AK/SK→
access_token exchange, escalate to Architect Round 2 (the option-(b)
legacy path was REJECTED at Story 4.6 Round 1 ratification; re-opening
it requires explicit architecture review).

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Vault path `kv/data/he-api/upstream/ernie/` | Story 4.1 OQ3 ratification cascade (inherited by 4.6) | 2026-05-19 | `upstream/` prefix groups the six Epic 4 vendor keys under one Vault policy |
| Manual rotation (NOT Vault dynamic-secret) | Story 4.1 OQ3 ruling (cascade) | 2026-05-19 | Baidu does not support API-key auto-rotation; dynamic-secret is N/A |
| Env-var name `ERNIE_UPSTREAM_API_KEY` (BRAND-name, NOT `WENXIN_*`/`BAIDU_*`) | Story 4.6 Architect Round 1 OQ-4.6-2 ratification | 2026-05-19 | Consistent with service name `adapter-ernie`, dir `apps/adapters/ernie/`, Vault path `kv/data/he-api/upstream/ernie/`. Final Epic-4 cascade closure per Story-4.2 OQ-4.2-6 cascade text |
| Qianfan v2 OpenAI-compatible endpoint URL + plain Bearer auth | Story 4.6 Architect Round 1 OQ-4.6-1 ratification (option a) | 2026-05-19 | Baidu's Qianfan v2 platform is documented as OpenAI drop-in; legacy aip.baidubce.com AK/SK→access_token exchange scheme + URL-path-segment translate concern REJECTED on KISS + operational-safety grounds |
| Identity-mapping translate.go | Story 4.6 Architect Round 1 OQ-4.6-3 ratification | 2026-05-19 | Qianfan v2 body shape matches OpenAI field-for-field (cascade from OQ-4.2-3) |
| Single service hosts `ernie-4.0` only (N=1 degenerate) | Story 4.6 BR-1.10 | 2026-05-19 | Baidu's `ernie-4.0` is the only model id wired in Story 4.6; future Ernie sizes (e.g., ernie-3.5, ernie-bot-8k) would extend `boundModelIDs` without adapter code changes |
| Verbatim REUSE of Story-4.2 BR-4.4 + Story-4.1 BR-1.4 error mapping | Story 4.6 Architect Round 1 OQ-4.6-6 ratification | 2026-05-19 | No Baidu-specific failure-shape carve-outs documented at draft time; additive `ErrorKind` entries permitted without Architect Round 2 per the Story-4.3 m-1 precedent |
| K8s namespace `he-api-adapters` | Story 4.1 M4 ruling (cascade) | 2026-05-19 | Shared namespace across all six adapter Deployments per ADR-9 |
| Live-exercise gate `HE_API_ERNIE_LIVE` (separate from sibling vendor gates) | Story 4.6 T4.2 SM lean | 2026-05-19 | Operators run vendor suites independently; closes the Epic-4 vendor-live-gate cohort |
| `x-bce-request-id` upstream-request-id header (SM lean per m-2; pending Phase 0 empirical-verify) | Story 4.6 Architect Round 1 OQ-4.6-6 SM-lean | 2026-05-19 | Baidu BCE platform convention emits `x-bce-request-id` on responses; empirical verify deferred to first staging deploy. If different header (e.g., `qianfan-request-id`, `x-trace-id`), update `apps/adapters/ernie/internal/adapter.go:upstreamRequestIDHeader` constant (one-line change) |
