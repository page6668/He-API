# Doubao (Volcengine Ark) Upstream API Key — Credentials & Vault Migration Path

> Story 4.5 — Doubao adapter. Vault path + rotation runbook for the
> upstream `https://ark.cn-beijing.volces.com/api/v3/chat/completions`
> Bearer token consumed by the `apps/adapters/doubao/` Connect-RPC service.
>
> Template structure mirrors Story 1.6 m-4 `docs/architecture/database-
> bootstrap.md` "Credentials & Vault Migration Path" section (Topology /
> Migration Workflow / Credentials & Vault Migration Path / Capacity /
> Operator Runbook / Decision Lineage) — REUSE structure from Story-4.4
> `docs/dev/secrets/glm-upstream.md` verbatim with Doubao swaps + a NEW
> "Endpoint ID Configuration" section for the Story-4.5-specific
> bidirectional `model`-field rewrite concern (OQ-4.5-3 + OQ-4.5-4).

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/doubao/` (per Story-4.1 OQ3 ratification, cascaded to Story 4.5; same `upstream/` prefix groups all six Epic 4 vendor keys) |
| Secret key | `api_key` (string; Volcengine-issued Bearer token at the Ark v3 OpenAI-compat endpoint; OQ-4.5-1 plain Bearer scheme — NOT HMAC-SHA256-signed Maas legacy scheme) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `adapter-doubao-upstream-api-key` in namespace `he-api-adapters` |
| K8s `Secret` data key | `api_key` |
| Consuming env var | `DOUBAO_UPSTREAM_API_KEY` (Architect Round 1 OQ-4.5-2 ratification: BRAND-name naming `DOUBAO_*` — NOT `VOLCENGINE_*` / `BYTEDANCE_*`; rule: brand wins, cascades to Story 4.6 `ERNIE_*`) |
| **Endpoint-id ConfigMap** | `doubao-endpoint-ids` in namespace `he-api-adapters` (Story 4.5 NEW; OQ-4.5-4 ratification — endpoint ids identify resources, not authorise access; see "Endpoint ID Configuration" below) |

The `kv/data/he-api/upstream/` prefix groups the six Epic 4 vendor keys
(DeepSeek / Qwen / Kimi / GLM / Doubao / Ernie) under a single Vault
policy + a single ClusterSecretStore. Operator rotates them as a cohort
where their support-contract renewal cadences align; Volcengine keys are
independently rotatable.

## Migration Workflow

Story 4.5 lands the **contract** (Helm chart references the ExternalSecret;
adapter reads `DOUBAO_UPSTREAM_API_KEY` env var at startup; reads
`DOUBAO_PRO_ENDPOINT_ID` + `DOUBAO_LITE_ENDPOINT_ID` from the
`doubao-endpoint-ids` ConfigMap). The actual Vault sync infrastructure
(Vault server, ClusterSecretStore, External Secrets Operator deployment)
is **out of scope** for Story 4.5 — those land in Epic 9 ops hardening
(same posture as Stories 4.1-4.4).

Story 4.5 dev-mode fallback: operators set `DOUBAO_UPSTREAM_API_KEY`
plus the two endpoint-id env vars directly via `.env.local` for local
development; the bootstrap script `scripts/dev/seed-doubao-key.sh`
sources them. CI runs against the mocked `httptest.NewTLSServer`
upstream — no real key required for the default test lane.
Live-exercise e2e tests are gated by `HE_API_DOUBAO_LIVE=1` (separate
from `HE_API_DEEPSEEK_LIVE` / `HE_API_QWEN_LIVE` / `HE_API_KIMI_LIVE` /
`HE_API_GLM_LIVE` so operators can run vendor suites independently —
BR-2.7 caveat: Volcengine Ark latency from non-mainland-China runners
may flap).

## Credentials & Vault Migration Path

| Phase | State | Source |
|---|---|---|
| Story 4.5 dev | Local `.env.local` `DOUBAO_UPSTREAM_API_KEY=...` + `DOUBAO_PRO_ENDPOINT_ID=...` + `DOUBAO_LITE_ENDPOINT_ID=...` | Operator-supplied; never committed |
| Story 4.5 CI (default lane) | Mocked upstream via `httptest.NewTLSServer` + canary endpoint ids `ep-test-pro-001` / `ep-test-lite-001` | No real key consumed |
| Story 4.5 CI (live lane) | Operator-triggered workflow_dispatch with `HE_API_DOUBAO_LIVE=1`; key injected from GitHub Actions secret `DOUBAO_UPSTREAM_API_KEY`; endpoint ids injected from operator-supplied env | Bypasses Vault; one-off for E2E exercise |
| Story 4.5 staging (manual deploy) | Manually-created K8s `Secret` carrying `DOUBAO_UPSTREAM_API_KEY` + ConfigMap `doubao-endpoint-ids` populated with staging endpoint ids | One-off `kubectl create secret` + `kubectl create configmap`; documented as TEMPORARY |
| Epic 9 ops | External Secrets Operator pulls `api_key` from `kv/data/he-api/upstream/doubao/`; ConfigMap `doubao-endpoint-ids` populated via Helm overlay per environment | Vault → ESO → K8s `Secret` → pod env var; values.yaml → ConfigMap → pod env var |

## Endpoint ID Configuration (Story 4.5 NEW — OQ-4.5-3 + OQ-4.5-4)

Volcengine Ark uses **opaque endpoint IDs** (`ep-20240xxx-xxxx`-shaped
strings) as the upstream `model` body field, NOT the consumer-facing
friendly names (`doubao-pro` / `doubao-lite`). The adapter rewrites the
`model` field bidirectionally:

- **Outbound**: `req.Model` (= `doubao-pro` / `doubao-lite`) →
  `endpoint_map.Lookup(req.Model)` → Volcengine endpoint id, applied
  BEFORE the upstream HTTPS call. Per BR-1.7.f + BR-3.8a.
- **Inbound**: Volcengine's response `model` field (= the echoed endpoint
  id) → `friendlyModelID` (sourced from `req.Model` per-request closure),
  applied per-chunk for streaming and on the terminal response for
  non-streaming. Per BR-1.11 + BR-3.8b.

### Why ConfigMap (not Vault) per OQ-4.5-4

Endpoint ids identify resources (parameter-like metadata that Volcengine
provisions per-model-per-account on the Volcengine console), they do
NOT authorise access. The authorisation gate is the API key, which
remains in Vault. Per-environment endpoint ids (dev / staging / prod
ConfigMap overlays) are a natural fit for ConfigMap; Vault-secret-
version drift would be operationally awkward.

### Where they live

| Aspect | Value |
|---|---|
| ConfigMap name | `doubao-endpoint-ids` |
| Namespace | `he-api-adapters` |
| Keys | `DOUBAO_PRO_ENDPOINT_ID`, `DOUBAO_LITE_ENDPOINT_ID` |
| Mounted via | `envFrom: configMapRef` on the adapter Deployment |
| Helm source | `.Values.endpointIDs.pro` + `.Values.endpointIDs.lite` |
| Adapter loader | `apps/adapters/doubao/internal/upstream/endpoint_map.go::NewFromOS()` (calls `New(os.Getenv)`); test-injectable via `New(envProvider)` per Architect Round 1 m-1 refactor |

### Rotation runbook (Volcengine endpoint-id rotation — NOT API-key rotation)

1. Provision a new endpoint id in the Volcengine console (Ark v3 →
   "Endpoint Management" page).
2. Update the corresponding values overlay (`infra/helm/adapter-doubao/
   values-dev.yaml` / `-staging.yaml` / `-prod.yaml`) with the new
   `endpointIDs.pro` or `endpointIDs.lite` value.
3. `helm upgrade adapter-doubao infra/helm/adapter-doubao -f values-<env>.yaml`
   (ArgoCD also picks this up on next sync).
4. **CRITICAL** — the env-var snapshot is read at process init per BR-1.12
   + Architect Round 1 OQ-4.5-4 rollover note: the new ConfigMap value is
   ignored until pod restart. Operator MUST rollout-restart:
   `kubectl rollout restart deployment/adapter-doubao -n he-api-adapters`
5. Verify in adapter startup logs:
   `kubectl logs -l app.kubernetes.io/name=adapter-doubao -n he-api-adapters
   --tail 50 | grep adapter-doubao | head -5` — the startup-validation
   `event=adapter_startup_validation` slog records help catch drift.
6. Decommission the old endpoint id in the Volcengine console (after step
   4 fully rolls).

## Capacity

Volcengine Ark v3 quota is account-level (NOT per-key) per current
platform docs. Story 4.5 uses a single account key shared across all
adapter-doubao replicas; throttling surfaces as HTTP 429 → adapter
Connect-RPC `Code.Unavailable` → gateway 502 (BR-1.4 mapping + BR-4.4
`upstream_error_kind=rate_limit_throttle` slog disambiguation, REUSE
Story-4.2 OQ-4.2-4 cascade).

Vault-side: one secret entry (`api_key`), ~50 bytes plaintext.
ConfigMap-side: two values (each ~25 bytes). Negligible.

## Operator Runbook

### API-key rotation (manual; Volcengine does not support API-key auto-rotation)

1. Generate a new key in the Volcengine platform console (account-level →
   "API Keys" management page).
2. Update `kv/data/he-api/upstream/doubao/api_key` in Vault.
   `vault kv put kv/he-api/upstream/doubao api_key=$NEW_KEY`
3. ExternalSecret's `refreshInterval: 1h` picks up the new value; force
   immediate refresh with:
   `kubectl annotate externalsecret adapter-doubao-upstream-api-key
   force-sync=$(date +%s) --namespace he-api-adapters --overwrite`
4. Adapter pods restart-by-rollout (the pod env var is read at startup;
   running pods continue with the old key until restarted):
   `kubectl rollout restart deployment/adapter-doubao --namespace he-api-adapters`
5. Verify in adapter logs:
   `kubectl logs -l app.kubernetes.io/name=adapter-doubao -n he-api-adapters
   --tail 50 | grep adapter_chat_request_end | head -5`
6. Revoke the old key in the Volcengine console (after step 4 fully rolls).

### Break-glass (key compromise / urgent revocation)

1. Revoke the compromised key in the Volcengine console FIRST. The
   adapter will start returning 502 `upstream_unavailable` with logged
   `upstream_error_kind=auth_revoked` — Grafana alert
   `DoubaoAdapterAuthRevoked` fires within 30s.
2. Generate replacement key + update Vault + force ExternalSecret sync +
   rollout-restart (steps 2-5 above).
3. The window of `502_upstream_unavailable` to users is the time from
   step 1 revocation to step 4 rollout (typically < 90s).

### Rate-limit / throttle (operator runbook for 429s, NOT key rotation)

`upstream_error_kind=rate_limit_throttle` (BR-4.4 — cascade REUSE from
Story-4.2) logs indicate the Volcengine account hit its rate-limit /
monthly cap. Operator action: contact Volcengine for quota top-up; do NOT
rotate the key (rotation does not increase quota). Distinct from
`auth_revoked` (`401` — key problem), `upstream_5xx` (`>=500` — upstream
incident), and `endpoint_id_not_configured` (BR-4.6 NEW — operator
misconfiguration of the `doubao-endpoint-ids` ConfigMap; see below).

### Endpoint-id misconfiguration (BR-4.6 NEW — Story 4.5)

`upstream_error_kind=endpoint_id_not_configured` slog records indicate
the `DOUBAO_PRO_ENDPOINT_ID` and/or `DOUBAO_LITE_ENDPOINT_ID` env var
is unset/empty at process startup. The adapter fails fast with
`connect.CodeFailedPrecondition` per BR-1.12 — the upstream HTTPS call
is NEVER initiated. Operator action:

1. Inspect the ConfigMap: `kubectl get configmap doubao-endpoint-ids -n
   he-api-adapters -o yaml`
2. If the values are missing, patch them in via Helm overlay update +
   `kubectl rollout restart deployment/adapter-doubao -n he-api-adapters`.
3. If the values are present but the pod env-var snapshot is stale (the
   adapter started BEFORE the ConfigMap was patched), `kubectl rollout
   restart` to pick up the latest values (see "Rotation runbook" above
   for the rollover behaviour explanation).

### Ark v3 vs legacy Maas endpoint (do not regress)

The Vault-stored key MUST be an Ark v3 OpenAI-compatible key —
Volcengine's legacy `maas-api.ml-platform-cn-beijing.volces.com`
endpoint uses an HMAC-SHA256-signed auth scheme distinct from the plain
Bearer scheme accepted at the Ark v3 path. Operator verification step
on first deploy: run the curl exploration documented in
`docs/dev/logs/4.5-dev-log.md` Phase 0; if the Ark v3 endpoint returns
an HMAC-signing requirement, escalate to Architect Round 2 (adapter
MUST implement HMAC signing — substantial complexity addition).

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Vault path `kv/data/he-api/upstream/doubao/` | Story 4.1 OQ3 ratification cascade (inherited by 4.5) | 2026-05-19 | `upstream/` prefix groups the six Epic 4 vendor keys under one Vault policy |
| Manual API-key rotation (NOT Vault dynamic-secret) | Story 4.1 OQ3 ruling (cascade) | 2026-05-19 | Volcengine does not support API-key auto-rotation; dynamic-secret is N/A |
| Env-var name `DOUBAO_UPSTREAM_API_KEY` (BRAND-name, NOT `VOLCENGINE_*` / `BYTEDANCE_*`) | Story 4.5 Architect Round 1 OQ-4.5-2 ratification | 2026-05-19 | Consistent with service name `adapter-doubao`, dir `apps/adapters/doubao/`, Vault path `kv/data/he-api/upstream/doubao/`. Cascades to Story 4.6 (`ERNIE_*`) per Story-4.2 OQ-4.2-6 cascade text |
| Ark v3 OpenAI-compatible endpoint URL + plain Bearer auth | Story 4.5 Architect Round 1 OQ-4.5-1 ratification | 2026-05-19 | Volcengine's Ark v3 platform is documented as OpenAI drop-in; legacy HMAC-signed Maas scheme does NOT apply at the Ark v3 path |
| Bidirectional `model`-field rewrite (friendly id ↔ endpoint id) | Story 4.5 Architect Round 1 OQ-4.5-3 ratification | 2026-05-19 | Volcengine Ark requires opaque endpoint IDs as the upstream `model` body field; SDK consumers expect `response.model == request.model`; surfacing endpoint ids would leak deployment-time configuration |
| Endpoint ids in ConfigMap (NOT Vault) | Story 4.5 Architect Round 1 OQ-4.5-4 ratification | 2026-05-19 | Endpoint ids identify resources (parameter-like metadata), not authorise access; API key remains in Vault; per-environment values are a natural ConfigMap fit |
| Test-injectable `endpoint_map.New(env)` constructor | Story 4.5 Architect Round 1 m-1 refactor | 2026-05-19 | Package-level `var x = map{...os.Getenv}` evaluated at import time would break `t.Setenv` based unit tests for the env-var-empty branch |
| NO `ReverseLookup` helper (per-request closure pattern) | Story 4.5 Architect Round 1 l-1 ratification | 2026-05-19 | Closure pattern is rotation-safe (mid-flight requests with old endpoint id back-translate correctly even after operator rotation); ReverseLookup would lose recovery on a map miss |
| Two-model-id N=2 service topology (RESTORATION from Story-4.4 N=1) | Story 4.5 BR-1.10 | 2026-05-19 | Both `doubao-pro` and `doubao-lite` share one Helm chart + Deployment + ServiceAccount; M2 `NewRegistry` endpoint-dedup branch returns to actually-executing form via 4.5-UNIT-013 `assert.Same(h_pro, h_lite)` chain |
| Verbatim REUSE of Story-4.2 BR-4.4 + Story-4.1 BR-1.4 error mapping | Story 4.5 Architect Round 1 OQ-4.5-6 ratification | 2026-05-19 | No Volcengine-specific failure-shape carve-outs documented at draft time; additive `ErrorKind` entries permitted (NEW `endpoint_id_not_configured` for BR-4.6) |
| K8s namespace `he-api-adapters` | Story 4.1 M4 ruling (cascade) | 2026-05-19 | Shared namespace across all six adapter Deployments per ADR-9 |
| Live-exercise gate `HE_API_DOUBAO_LIVE` (separate from sibling vendor gates) | Story 4.5 T4.2 SM lean | 2026-05-19 | Operators run vendor suites independently |
