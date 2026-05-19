# Kimi (Moonshot) Upstream API Key — Credentials & Vault Migration Path

> Story 4.3 — Kimi adapter. Vault path + rotation runbook for the
> upstream `https://api.moonshot.cn/v1/chat/completions` Bearer token
> consumed by the `apps/adapters/kimi/` Connect-RPC service.
>
> Template structure mirrors Story 1.6 m-4 `docs/architecture/database-
> bootstrap.md` "Credentials & Vault Migration Path" section (Topology /
> Migration Workflow / Credentials & Vault Migration Path / Capacity /
> Operator Runbook / Decision Lineage) — REUSE structure from Story-4.2
> `docs/dev/secrets/qwen-upstream.md` verbatim with Kimi swaps.

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/kimi/` (per Story-4.1 OQ3 ratification, cascaded to Story 4.3; same `upstream/` prefix groups all six Epic 4 vendor keys) |
| Secret key | `api_key` (string; Moonshot-issued Bearer token; format `sk-...`) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `adapter-kimi-upstream-api-key` in namespace `he-api-adapters` |
| K8s `Secret` data key | `api_key` |
| Consuming env var | `KIMI_UPSTREAM_API_KEY` (Architect Round 1 OQ-4.3-2 ratification: BRAND-name naming `KIMI_*` — NOT `MOONSHOT_*`; rule: brand wins when brand and model-id-prefix diverge) |

The `kv/data/he-api/upstream/` prefix groups the six Epic 4 vendor keys
(DeepSeek / Qwen / Kimi / GLM / Doubao / Ernie) under a single Vault
policy + a single ClusterSecretStore. Operator rotates them as a cohort
where their support-contract renewal cadences align; Moonshot keys are
independently rotatable.

## Migration Workflow

Story 4.3 lands the **contract** (Helm chart references the ExternalSecret;
adapter reads `KIMI_UPSTREAM_API_KEY` env var at startup). The actual
Vault sync infrastructure (Vault server, ClusterSecretStore, External
Secrets Operator deployment) is **out of scope** for Story 4.3 — those
land in Epic 9 ops hardening (same posture as Stories 4.1 + 4.2).

Story 4.3 dev-mode fallback: operators set `KIMI_UPSTREAM_API_KEY`
directly via `.env.local` for local development; the bootstrap script
`scripts/dev/seed-kimi-key.sh` sources it. CI runs against the mocked
`httptest.NewTLSServer` upstream — no real key required for the default
test lane. Live-exercise e2e tests are gated by `HE_API_KIMI_LIVE=1`
(separate from `HE_API_DEEPSEEK_LIVE` and `HE_API_QWEN_LIVE` so operators
can run vendor suites independently — Story-4.3 BR-2.7 caveat: Moonshot
latency + 128k variant processing time from non-mainland-China runners
may flap).

## Credentials & Vault Migration Path

| Phase | State | Source |
|---|---|---|
| Story 4.3 dev | Local `.env.local` `KIMI_UPSTREAM_API_KEY=sk-...` | Operator-supplied; never committed |
| Story 4.3 CI (default lane) | Mocked upstream via `httptest.NewTLSServer` | No real key consumed |
| Story 4.3 CI (live lane) | Operator-triggered workflow_dispatch with `HE_API_KIMI_LIVE=1`; key injected from GitHub Actions secret `KIMI_UPSTREAM_API_KEY` | Bypasses Vault; one-off for E2E exercise |
| Story 4.3 staging (manual deploy) | Manually-created K8s `Secret` carrying `KIMI_UPSTREAM_API_KEY` | One-off `kubectl create secret`; documented as TEMPORARY |
| Epic 9 ops | External Secrets Operator pulls from `kv/data/he-api/upstream/kimi/` | Vault → ESO → K8s `Secret` → pod env var |

## Capacity

Moonshot API quota is account-level (NOT per-key); the 128k variant has
substantially higher upstream-side processing latency than 8k/32k.
Story 4.3 uses a single account key shared across all adapter-kimi
replicas and across all three model sizes (rate-limiting is upstream-
imposed at the account level); throttling surfaces as HTTP 429 →
adapter Connect-RPC `Code.Unavailable` → gateway 502 (BR-1.4 mapping +
BR-4.4 `upstream_error_kind=rate_limit_throttle` slog disambiguation,
REUSE Story-4.2 OQ-4.2-4 cascade).

Vault-side: one secret entry, ~50 bytes plaintext. Negligible.

## Operator Runbook

### Rotation (manual; Moonshot does not support API-key auto-rotation)

1. Generate a new key in the Moonshot platform console (account-level →
   "API Keys" management page).
2. Update `kv/data/he-api/upstream/kimi/api_key` in Vault.
   `vault kv put kv/he-api/upstream/kimi api_key=$NEW_KEY`
3. ExternalSecret's `refreshInterval: 1h` picks up the new value; force
   immediate refresh with:
   `kubectl annotate externalsecret adapter-kimi-upstream-api-key
   force-sync=$(date +%s) --namespace he-api-adapters --overwrite`
4. Adapter pods restart-by-rollout (the pod env var is read at startup;
   running pods continue with the old key until restarted):
   `kubectl rollout restart deployment/adapter-kimi --namespace he-api-adapters`
5. Verify in adapter logs:
   `kubectl logs -l app.kubernetes.io/name=adapter-kimi -n he-api-adapters --tail 50 | grep adapter_chat_request_end | head -5`
6. Revoke the old key in the Moonshot console (after step 4 fully rolls).

### Break-glass (key compromise / urgent revocation)

1. Revoke the compromised key in the Moonshot console FIRST. The
   adapter will start returning 502 `upstream_unavailable` with logged
   `upstream_error_kind=auth_revoked` — Grafana alert
   `KimiAdapterAuthRevoked` fires within 30s.
2. Generate replacement key + update Vault + force ExternalSecret sync +
   rollout-restart (steps 2-5 above).
3. The window of `502_upstream_unavailable` to users is the time from
   step 1 revocation to step 4 rollout (typically < 90s).

### Rate-limit / throttle (operator runbook for 429s, NOT key rotation)

`upstream_error_kind=rate_limit_throttle` (BR-4.4 — Kimi-specific
cascade from Story-4.2) logs indicate the Moonshot account hit its
rate-limit / monthly cap. Operator action: contact Moonshot for quota
top-up; do NOT rotate the key (rotation does not increase quota).
Distinct from `auth_revoked` (`401` — key problem) and `upstream_5xx`
(`>=500` — upstream incident).

### Context-length-exceeded (operator triage, NOT user-actionable)

`upstream_error_kind=context_length_exceeded` (BR-4.5 — NEW for Story 4.3
per Architect Round 1 m-1 body-aware classifier) logs indicate a user
request whose prompt exceeded the chosen model's context-window budget
(8k / 32k / 128k tokens). Common triage signal: bursty volume from a
single tenant likely indicates a bad client implementation (e.g.,
unbounded conversation history). Story 4.7 will lift this from
slog-only to a distinct `400_context_length_exceeded` envelope code
with actionable messaging ("Prompt of N tokens exceeds moonshot-v1-8k's
budget; consider moonshot-v1-32k or moonshot-v1-128k"). Until then,
this slog kind is ops-only.

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Vault path `kv/data/he-api/upstream/kimi/` | Story 4.1 OQ3 ratification cascade (inherited by 4.3) | 2026-05-19 | `upstream/` prefix groups the six Epic 4 vendor keys under one Vault policy |
| Manual rotation (NOT Vault dynamic-secret) | Story 4.1 OQ3 ruling (cascade) | 2026-05-19 | Moonshot does not support API-key auto-rotation; dynamic-secret is N/A |
| Env-var name `KIMI_UPSTREAM_API_KEY` (BRAND-name, NOT `MOONSHOT_*`) | Story 4.3 Architect Round 1 OQ-4.3-2 ratification | 2026-05-19 | Consistent with service name `adapter-kimi`, dir `apps/adapters/kimi/`, Vault path `kv/data/he-api/upstream/kimi/`. Kimi is the unique Epic-4 vendor where brand (Kimi) and model-id-prefix (moonshot-v1) diverge; rule: brand wins for env vars |
| OpenAI-compatible endpoint URL | Story 4.3 Architect Round 1 OQ-4.3-1 ratification | 2026-05-19 | Moonshot publishes only the OpenAI drop-in endpoint; identity-mapped translate.go |
| Single service hosts ALL THREE sizes (N=3) | Story 4.2 OQ-4.2-2 cascade (Story 4.3 extends N=2 → N=3) | 2026-05-19 | Matches Moonshot's own API surface; upstream rate-limit is per-account-not-per-model |
| Context-length slog-only (BR-4.5; distinct envelope code deferred to Story 4.7) | Story 4.3 Architect Round 1 OQ-4.3-4 ratification | 2026-05-19 | Pre-validation requires tokeniser dependency (violates BR-3.1 oracle); distinct envelope code is Story-4.7 capability matrix work |
| Body-aware classifier `ClassifyMoonshotErrorBody` (m-1) | Story 4.3 Architect Round 1 m-1 ratification | 2026-05-19 | Moonshot returns context-length errors as HTTP 400 with `invalid_request_error` body shape; status alone cannot disambiguate |
| K8s namespace `he-api-adapters` | Story 4.1 M4 ruling (cascade) | 2026-05-19 | Shared namespace across all six adapter Deployments per ADR-9 |
| Live-exercise gate `HE_API_KIMI_LIVE` (separate from `HE_API_DEEPSEEK_LIVE` and `HE_API_QWEN_LIVE`) | Story 4.3 T4.5 SM lean | 2026-05-19 | Operators run vendor suites independently |
