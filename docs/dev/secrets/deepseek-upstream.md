# DeepSeek Upstream API Key — Credentials & Vault Migration Path

> Story 4.1 — DeepSeek adapter. Vault path + rotation runbook for the
> upstream `https://api.deepseek.com` Bearer token consumed by the
> `apps/adapters/deepseek/` Connect-RPC service.
>
> Template structure mirrors Story 1.6 m-4 `docs/architecture/database-
> bootstrap.md` "Credentials & Vault Migration Path" section (Topology /
> Migration Workflow / Credentials & Vault Migration Path / Capacity /
> Operator Runbook / Decision Lineage).

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/deepseek/` (per OQ3 Architect Round 2 ruling, Story 4.1) |
| Secret key | `api_key` (string; DeepSeek-issued Bearer token, no prefix) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `adapter-deepseek-upstream-api-key` in namespace `he-api-adapters` |
| K8s `Secret` data key | `api_key` |
| Consuming env var | `DEEPSEEK_UPSTREAM_API_KEY` |

The `kv/data/he-api/upstream/` prefix groups the six Epic 4 vendor keys
(DeepSeek / Qwen / Kimi / GLM / Doubao / Ernie) under a single Vault
policy + a single ClusterSecretStore. Operator rotates them as a cohort
(vendor support-contracts renew on similar cadences).

## Migration Workflow

Story 4.1 lands the **contract** (Helm chart references the ExternalSecret;
adapter reads `DEEPSEEK_UPSTREAM_API_KEY` env var at startup). The actual
Vault sync infrastructure (Vault server, ClusterSecretStore, External
Secrets Operator deployment) is **out of scope** for Story 4.1 — those
land in Epic 9 ops hardening.

Story 4.1 dev-mode fallback: operators set `DEEPSEEK_UPSTREAM_API_KEY`
directly via `.env.local` for local development; the bootstrap script
`scripts/dev/seed-deepseek-key.sh` sources it. CI runs against the
mocked `httptest.NewTLSServer` upstream — no real key required for the
default test lane.

## Credentials & Vault Migration Path

| Phase | State | Source |
|---|---|---|
| Story 4.1 dev | Local `.env.local` `DEEPSEEK_UPSTREAM_API_KEY=sk-...` | Operator-supplied; never committed |
| Story 4.1 CI | Mocked upstream via `httptest.NewTLSServer` | No real key consumed |
| Story 4.1 staging (manual deploy) | Manually-created K8s `Secret` carrying `DEEPSEEK_UPSTREAM_API_KEY` | One-off `kubectl create secret`; documented as TEMPORARY |
| Epic 9 ops | External Secrets Operator pulls from `kv/data/he-api/upstream/deepseek/` | Vault → ESO → K8s `Secret` → pod env var |

## Capacity

DeepSeek API quota is account-level (NOT per-key). Story 4.1 uses a single
account key shared across all gateway replicas; rate-limiting is upstream-
imposed and surfaces as HTTP 429 → adapter Connect-RPC `Code.Unavailable`
→ gateway 502 (BR-1.4 + M2 disambiguation kind `quota_exhausted`).

Vault-side: one secret entry, ~50 bytes plaintext. Negligible.

## Operator Runbook

### Rotation (manual; DeepSeek does not support API-key auto-rotation)

1. Generate a new key in the DeepSeek console (account-level).
2. Update `kv/data/he-api/upstream/deepseek/api_key` in Vault.
   `vault kv put kv/he-api/upstream/deepseek api_key=$NEW_KEY`
3. ExternalSecret's `refreshInterval: 1h` picks up the new value; force
   immediate refresh with:
   `kubectl annotate externalsecret adapter-deepseek-upstream-api-key
   force-sync=$(date +%s) --namespace he-api-adapters --overwrite`
4. Adapter pods restart-by-rollout (the pod env var is read at startup;
   running pods continue with the old key until restarted):
   `kubectl rollout restart deployment/adapter-deepseek --namespace he-api-adapters`
5. Verify in adapter logs: `kubectl logs -l app.kubernetes.io/name=adapter-deepseek
   -n he-api-adapters --tail 50 | grep adapter_chat_request_end | head -5`
6. Revoke the old key in the DeepSeek console (after step 4 fully rolls).

### Break-glass (key compromise / urgent revocation)

1. Revoke the compromised key in the DeepSeek console FIRST. The adapter
   will start returning 502 `upstream_unavailable` with logged
   `upstream_error_kind=auth_revoked` (M2 disambiguation) — Grafana alert
   `DeepSeekAdapterAuthRevoked` fires within 30s.
2. Generate replacement key + update Vault + force ExternalSecret sync +
   rollout-restart (steps 2-5 above).
3. The window of `502_upstream_unavailable` to users is the time from
   step 1 revocation to step 4 rollout (typically < 90s).

### Quota-exhausted (operator runbook for 429s, NOT key rotation)

`upstream_error_kind=quota_exhausted` logs indicate the DeepSeek account
hit its rate-limit / monthly cap. Operator action: contact DeepSeek for
quota top-up; do NOT rotate the key (rotation does not increase quota).

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Vault path `kv/data/he-api/upstream/deepseek/` | Story 4.1 Architect Round 2 OQ3 ruling | 2026-05-19 | `upstream/` prefix groups the six Epic 4 vendor keys under one Vault policy |
| Manual rotation (NOT Vault dynamic-secret) | Story 4.1 OQ3 ruling | 2026-05-19 | DeepSeek does not support API-key auto-rotation; dynamic-secret is N/A |
| Env-var name `DEEPSEEK_UPSTREAM_API_KEY` | Story 4.1 Dev Notes BR-1.7 (b) | 2026-05-19 | Consistent with the `<VENDOR>_UPSTREAM_API_KEY` pattern Stories 4.2-4.6 inherit |
| K8s namespace `he-api-adapters` | Story 4.1 Architect Round 2 M4 ruling | 2026-05-19 | Shared namespace across all six adapter Deployments (blast-radius isolation per ADR-9; NOT one-namespace-per-adapter) |
