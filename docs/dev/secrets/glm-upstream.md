# GLM (Zhipu) Upstream API Key — Credentials & Vault Migration Path

> Story 4.4 — GLM adapter. Vault path + rotation runbook for the
> upstream `https://open.bigmodel.cn/api/paas/v4/chat/completions`
> Bearer token consumed by the `apps/adapters/glm/` Connect-RPC service.
>
> Template structure mirrors Story 1.6 m-4 `docs/architecture/database-
> bootstrap.md` "Credentials & Vault Migration Path" section (Topology /
> Migration Workflow / Credentials & Vault Migration Path / Capacity /
> Operator Runbook / Decision Lineage) — REUSE structure from Story-4.3
> `docs/dev/secrets/kimi-upstream.md` verbatim with GLM swaps.

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/glm/` (per Story-4.1 OQ3 ratification, cascaded to Story 4.4; same `upstream/` prefix groups all six Epic 4 vendor keys) |
| Secret key | `api_key` (string; Zhipu-issued Bearer token at the v4 OpenAI-compat endpoint; OQ-4.4-1 plain Bearer scheme — NOT JWT-signed) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `adapter-glm-upstream-api-key` in namespace `he-api-adapters` |
| K8s `Secret` data key | `api_key` |
| Consuming env var | `GLM_UPSTREAM_API_KEY` (Architect Round 1 OQ-4.4-2 ratification: BRAND-name naming `GLM_*` — NOT `ZHIPU_*`; rule: brand wins, cascades to Stories 4.5-4.6 `DOUBAO_*` + `ERNIE_*`) |

The `kv/data/he-api/upstream/` prefix groups the six Epic 4 vendor keys
(DeepSeek / Qwen / Kimi / GLM / Doubao / Ernie) under a single Vault
policy + a single ClusterSecretStore. Operator rotates them as a cohort
where their support-contract renewal cadences align; Zhipu keys are
independently rotatable.

## Migration Workflow

Story 4.4 lands the **contract** (Helm chart references the ExternalSecret;
adapter reads `GLM_UPSTREAM_API_KEY` env var at startup). The actual
Vault sync infrastructure (Vault server, ClusterSecretStore, External
Secrets Operator deployment) is **out of scope** for Story 4.4 — those
land in Epic 9 ops hardening (same posture as Stories 4.1-4.3).

Story 4.4 dev-mode fallback: operators set `GLM_UPSTREAM_API_KEY`
directly via `.env.local` for local development; the bootstrap script
`scripts/dev/seed-glm-key.sh` sources it. CI runs against the mocked
`httptest.NewTLSServer` upstream — no real key required for the default
test lane. Live-exercise e2e tests are gated by `HE_API_GLM_LIVE=1`
(separate from `HE_API_DEEPSEEK_LIVE`, `HE_API_QWEN_LIVE`, and
`HE_API_KIMI_LIVE` so operators can run vendor suites independently —
BR-2.7 caveat: Zhipu latency from non-mainland-China runners may flap).

## Credentials & Vault Migration Path

| Phase | State | Source |
|---|---|---|
| Story 4.4 dev | Local `.env.local` `GLM_UPSTREAM_API_KEY=...` | Operator-supplied; never committed |
| Story 4.4 CI (default lane) | Mocked upstream via `httptest.NewTLSServer` | No real key consumed |
| Story 4.4 CI (live lane) | Operator-triggered workflow_dispatch with `HE_API_GLM_LIVE=1`; key injected from GitHub Actions secret `GLM_UPSTREAM_API_KEY` | Bypasses Vault; one-off for E2E exercise |
| Story 4.4 staging (manual deploy) | Manually-created K8s `Secret` carrying `GLM_UPSTREAM_API_KEY` | One-off `kubectl create secret`; documented as TEMPORARY |
| Epic 9 ops | External Secrets Operator pulls from `kv/data/he-api/upstream/glm/` | Vault → ESO → K8s `Secret` → pod env var |

## Capacity

Zhipu v4 API quota is account-level (NOT per-key) per current platform
docs. Story 4.4 uses a single account key shared across all
adapter-glm replicas; throttling surfaces as HTTP 429 → adapter
Connect-RPC `Code.Unavailable` → gateway 502 (BR-1.4 mapping + BR-4.4
`upstream_error_kind=rate_limit_throttle` slog disambiguation, REUSE
Story-4.2 OQ-4.2-4 cascade).

Vault-side: one secret entry, ~50 bytes plaintext. Negligible.

## Operator Runbook

### Rotation (manual; Zhipu does not support API-key auto-rotation)

1. Generate a new key in the Zhipu platform console (account-level →
   "API Keys" management page).
2. Update `kv/data/he-api/upstream/glm/api_key` in Vault.
   `vault kv put kv/he-api/upstream/glm api_key=$NEW_KEY`
3. ExternalSecret's `refreshInterval: 1h` picks up the new value; force
   immediate refresh with:
   `kubectl annotate externalsecret adapter-glm-upstream-api-key
   force-sync=$(date +%s) --namespace he-api-adapters --overwrite`
4. Adapter pods restart-by-rollout (the pod env var is read at startup;
   running pods continue with the old key until restarted):
   `kubectl rollout restart deployment/adapter-glm --namespace he-api-adapters`
5. Verify in adapter logs:
   `kubectl logs -l app.kubernetes.io/name=adapter-glm -n he-api-adapters --tail 50 | grep adapter_chat_request_end | head -5`
6. Revoke the old key in the Zhipu console (after step 4 fully rolls).

### Break-glass (key compromise / urgent revocation)

1. Revoke the compromised key in the Zhipu console FIRST. The
   adapter will start returning 502 `upstream_unavailable` with logged
   `upstream_error_kind=auth_revoked` — Grafana alert
   `GLMAdapterAuthRevoked` fires within 30s.
2. Generate replacement key + update Vault + force ExternalSecret sync +
   rollout-restart (steps 2-5 above).
3. The window of `502_upstream_unavailable` to users is the time from
   step 1 revocation to step 4 rollout (typically < 90s).

### Rate-limit / throttle (operator runbook for 429s, NOT key rotation)

`upstream_error_kind=rate_limit_throttle` (BR-4.4 — cascade REUSE from
Story-4.2) logs indicate the Zhipu account hit its rate-limit / monthly
cap. Operator action: contact Zhipu for quota top-up; do NOT rotate the
key (rotation does not increase quota). Distinct from `auth_revoked`
(`401` — key problem) and `upstream_5xx` (`>=500` — upstream incident).

### v4 vs legacy endpoint (do not regress)

The Vault-stored key MUST be a v4 OpenAI-compatible key — Zhipu's
legacy pre-v4 endpoints (`/api/llm/v3/` and earlier) use a JWT-signed
auth scheme distinct from the plain Bearer scheme accepted at the v4
path. Operator verification step on first deploy: run the curl
exploration documented in `docs/dev/logs/4.4-dev-log.md` Phase 0; if
the v4 endpoint returns a JWT-signing requirement, escalate to
Architect Round 2 (adapter MUST implement JWT signing — substantial
complexity addition).

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Vault path `kv/data/he-api/upstream/glm/` | Story 4.1 OQ3 ratification cascade (inherited by 4.4) | 2026-05-19 | `upstream/` prefix groups the six Epic 4 vendor keys under one Vault policy |
| Manual rotation (NOT Vault dynamic-secret) | Story 4.1 OQ3 ruling (cascade) | 2026-05-19 | Zhipu does not support API-key auto-rotation; dynamic-secret is N/A |
| Env-var name `GLM_UPSTREAM_API_KEY` (BRAND-name, NOT `ZHIPU_*`) | Story 4.4 Architect Round 1 OQ-4.4-2 ratification | 2026-05-19 | Consistent with service name `adapter-glm`, dir `apps/adapters/glm/`, Vault path `kv/data/he-api/upstream/glm/`. Cascades to Stories 4.5-4.6 (`DOUBAO_*` + `ERNIE_*`) per Story-4.2 OQ-4.2-6 cascade text |
| v4 OpenAI-compatible endpoint URL + plain Bearer auth | Story 4.4 Architect Round 1 OQ-4.4-1 ratification | 2026-05-19 | Zhipu's v4 platform is documented as OpenAI drop-in; legacy JWT-signed pre-v4 scheme does NOT apply at the v4 path |
| Identity-mapping translate.go | Story 4.4 Architect Round 1 OQ-4.4-3 ratification | 2026-05-19 | Zhipu v4 body shape matches OpenAI field-for-field |
| Single service hosts `glm-4` only (N=1 degenerate) | Story 4.4 BR-1.10 | 2026-05-19 | Zhipu's `glm-4` is the only model id wired in Story 4.4; future GLM sizes would extend `boundModelIDs` without adapter code changes |
| Verbatim REUSE of Story-4.2 BR-4.4 + Story-4.1 BR-1.4 error mapping | Story 4.4 Architect Round 1 OQ-4.4-6 ratification | 2026-05-19 | No Zhipu-specific failure-shape carve-outs documented at draft time; additive `ErrorKind` entries permitted without Architect Round 2 per the Story-4.3 m-1 precedent |
| K8s namespace `he-api-adapters` | Story 4.1 M4 ruling (cascade) | 2026-05-19 | Shared namespace across all six adapter Deployments per ADR-9 |
| Live-exercise gate `HE_API_GLM_LIVE` (separate from sibling vendor gates) | Story 4.4 T4.2 SM lean | 2026-05-19 | Operators run vendor suites independently |
