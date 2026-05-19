# Qwen Upstream API Key — Credentials & Vault Migration Path

> Story 4.2 — Qwen (通义千问) adapter. Vault path + rotation runbook for
> the upstream `https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions`
> Bearer token consumed by the `apps/adapters/qwen/` Connect-RPC service.
>
> Template structure mirrors Story 1.6 m-4 `docs/architecture/database-
> bootstrap.md` "Credentials & Vault Migration Path" section (Topology /
> Migration Workflow / Credentials & Vault Migration Path / Capacity /
> Operator Runbook / Decision Lineage) — REUSE structure from Story-4.1
> `docs/dev/secrets/deepseek-upstream.md` verbatim with Qwen swaps.

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/qwen/` (per Story-4.1 OQ3 ratification, inherited verbatim by Story 4.2; same `upstream/` prefix groups all six Epic 4 vendor keys) |
| Secret key | `api_key` (string; Alibaba Cloud DashScope-issued Bearer token, no prefix; format `sk-...`) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `adapter-qwen-upstream-api-key` in namespace `he-api-adapters` |
| K8s `Secret` data key | `api_key` |
| Consuming env var | `QWEN_UPSTREAM_API_KEY` (Architect Round 1 OQ-4.2-6 ratification: model-family naming — NOT `DASHSCOPE_UPSTREAM_API_KEY`) |

The `kv/data/he-api/upstream/` prefix groups the six Epic 4 vendor keys
(DeepSeek / Qwen / Kimi / GLM / Doubao / Ernie) under a single Vault
policy + a single ClusterSecretStore. Operator rotates them as a cohort
where their support-contract renewal cadences align; Qwen / DashScope
keys are independently rotatable.

## Migration Workflow

Story 4.2 lands the **contract** (Helm chart references the ExternalSecret;
adapter reads `QWEN_UPSTREAM_API_KEY` env var at startup). The actual
Vault sync infrastructure (Vault server, ClusterSecretStore, External
Secrets Operator deployment) is **out of scope** for Story 4.2 — those
land in Epic 9 ops hardening (same posture as Story 4.1).

Story 4.2 dev-mode fallback: operators set `QWEN_UPSTREAM_API_KEY`
directly via `.env.local` for local development; the bootstrap script
`scripts/dev/seed-qwen-key.sh` sources it. CI runs against the mocked
`httptest.NewTLSServer` upstream — no real key required for the default
test lane. Live-exercise e2e tests are gated by `HE_API_QWEN_LIVE=1`
(separate from `HE_API_DEEPSEEK_LIVE` so operators can run vendor suites
independently per Story-4.2 BR-2.7 caveat — DashScope latency from
non-mainland-China runners may flap).

## Credentials & Vault Migration Path

| Phase | State | Source |
|---|---|---|
| Story 4.2 dev | Local `.env.local` `QWEN_UPSTREAM_API_KEY=sk-...` | Operator-supplied; never committed |
| Story 4.2 CI (default lane) | Mocked upstream via `httptest.NewTLSServer` | No real key consumed |
| Story 4.2 CI (live lane) | Operator-triggered workflow_dispatch with `HE_API_QWEN_LIVE=1`; key injected from GitHub Actions secret `QWEN_UPSTREAM_API_KEY` | Bypasses Vault; one-off for E2E exercise |
| Story 4.2 staging (manual deploy) | Manually-created K8s `Secret` carrying `QWEN_UPSTREAM_API_KEY` | One-off `kubectl create secret`; documented as TEMPORARY |
| Epic 9 ops | External Secrets Operator pulls from `kv/data/he-api/upstream/qwen/` | Vault → ESO → K8s `Secret` → pod env var |

## Capacity

DashScope API quota is account-level (NOT per-key); Qwen-Max free-tier
quota is tight. Story 4.2 uses a single account key shared across all
adapter-qwen replicas; rate-limiting is upstream-imposed and surfaces as
HTTP 429 → adapter Connect-RPC `Code.Unavailable` → gateway 502
(BR-1.4 mapping + BR-4.4 `upstream_error_kind=rate_limit_throttle` slog
disambiguation per Architect Round 1 OQ-4.2-4 ratification).

Vault-side: one secret entry, ~50 bytes plaintext. Negligible.

## Operator Runbook

### Rotation (manual; DashScope does not support API-key auto-rotation)

1. Generate a new key in the DashScope console (account-level →
   "API Keys" management page).
2. Update `kv/data/he-api/upstream/qwen/api_key` in Vault.
   `vault kv put kv/he-api/upstream/qwen api_key=$NEW_KEY`
3. ExternalSecret's `refreshInterval: 1h` picks up the new value; force
   immediate refresh with:
   `kubectl annotate externalsecret adapter-qwen-upstream-api-key
   force-sync=$(date +%s) --namespace he-api-adapters --overwrite`
4. Adapter pods restart-by-rollout (the pod env var is read at startup;
   running pods continue with the old key until restarted):
   `kubectl rollout restart deployment/adapter-qwen --namespace he-api-adapters`
5. Verify in adapter logs:
   `kubectl logs -l app.kubernetes.io/name=adapter-qwen -n he-api-adapters --tail 50 | grep adapter_chat_request_end | head -5`
6. Revoke the old key in the DashScope console (after step 4 fully rolls).

### Break-glass (key compromise / urgent revocation)

1. Revoke the compromised key in the DashScope console FIRST. The
   adapter will start returning 502 `upstream_unavailable` with logged
   `upstream_error_kind=auth_revoked` — Grafana alert
   `QwenAdapterAuthRevoked` fires within 30s.
2. Generate replacement key + update Vault + force ExternalSecret sync +
   rollout-restart (steps 2-5 above).
3. The window of `502_upstream_unavailable` to users is the time from
   step 1 revocation to step 4 rollout (typically < 90s).

### Rate-limit / throttle (operator runbook for 429s, NOT key rotation)

`upstream_error_kind=rate_limit_throttle` (BR-4.4 — Qwen-specific
disambiguation per Architect Round 1 OQ-4.2-4) logs indicate the
DashScope account hit its rate-limit / monthly cap. Operator action:
contact Alibaba Cloud for quota top-up; do NOT rotate the key (rotation
does not increase quota). Distinct from `auth_revoked` (`401` — key
problem) and `upstream_5xx` (`>=500` — upstream incident).

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Vault path `kv/data/he-api/upstream/qwen/` | Story 4.1 OQ3 ratification (inherited by 4.2) | 2026-05-19 | `upstream/` prefix groups the six Epic 4 vendor keys under one Vault policy |
| Manual rotation (NOT Vault dynamic-secret) | Story 4.1 OQ3 ruling (inherited) | 2026-05-19 | DashScope does not support API-key auto-rotation; dynamic-secret is N/A |
| Env-var name `QWEN_UPSTREAM_API_KEY` (model-family, NOT `DASHSCOPE_*`) | Story 4.2 Architect Round 1 OQ-4.2-6 ratification | 2026-05-19 | Consistent with model-id naming (`qwen-max`/`qwen-plus`), source-tree (`apps/adapters/qwen/`), and Helm chart (`adapter-qwen`); single mental model for operators |
| Compat-mode endpoint vs DashScope native | Story 4.2 Architect Round 1 OQ-4.2-1 ratification | 2026-05-19 | OpenAI-shape body + usage; minimises divergence from Story-4.1 template |
| Single service hosts qwen-max + qwen-plus | Story 4.2 Architect Round 1 OQ-4.2-2 ratification | 2026-05-19 | Matches DashScope's own API surface; upstream rate-limit is per-account-not-per-model |
| K8s namespace `he-api-adapters` | Story 4.1 M4 ruling (inherited) | 2026-05-19 | Shared namespace across all six adapter Deployments per ADR-9 |
| Live-exercise gate `HE_API_QWEN_LIVE` (separate from `HE_API_DEEPSEEK_LIVE`) | Story 4.2 T4.5 SM lean | 2026-05-19 | Operators run vendor suites independently |
