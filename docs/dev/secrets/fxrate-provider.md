# FX Rate Provider URL/Key — Credentials & Vault Migration Path

> Story 7.2 — 多币种 + 汇率刷新. Vault path + rotation runbook for the upstream
> FX-rate provider base-URL (and any embedded API key) consumed by the
> `apps/billing-svc/cmd/fx-refresh` CronJob (Architect Q-FXSRC / BR-C-7).
>
> Template structure mirrors `docs/dev/secrets/qwen-upstream.md` (Topology /
> Migration Workflow / Capacity / Operator Runbook / Decision Lineage) with FX
> swaps. This secret gates a PUBLIC market rate, not user PII — but it is held to
> secret-handling discipline (env-injected, NEVER logged) because a key may be
> embedded in the base URL.

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/upstream/fxrate/` (groups under the same `upstream/` prefix as the Epic-4 vendor keys; single Vault policy + ClusterSecretStore) |
| Secret key | `provider_url` (string; the full USD-base rates endpoint, e.g. `https://open.er-api.com/v6/latest/USD` — a key, if any, is embedded as a path segment / query param by the provider) |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `he-api-fxrate-provider` in namespace `he-api-services` |
| K8s `Secret` data key | `HE_API_FX_PROVIDER_URL` |
| Consuming env var | `HE_API_FX_PROVIDER_URL` (read by `fx.ProviderFromEnv` → `fx.NewHTTPProvider`) |

The CronJob references this Secret via `envFrom.secretRef` (Helm value
`fxRefresh.providerSecretName`), so the URL/key is injected into the pod
environment and NEVER rendered into the manifest. The `fx` package's HTTP
provider deliberately constructs GENERIC errors (no URL in the message) so a key
embedded in the URL cannot leak into slog (UNIT-019). The fetched **rate value**
MAY be logged — it is public market data (BR-C-7).

## Migration Workflow

Story 7.2 lands the **contract** (Helm chart references the Secret via
`fxRefresh.providerSecretName`; the cron reads `HE_API_FX_PROVIDER_URL`). The
actual Vault sync infrastructure (Vault server, ClusterSecretStore, External
Secrets Operator) is **out of scope** for Story 7.2 — same posture as the Epic-4
vendor keys (lands in Epic 9 ops hardening).

Dev / CI / air-gapped fallback: set **`FX_MANUAL_USD_CNY`** (a positive
string-decimal, e.g. `7.20`) instead of the provider URL. `fx.ProviderFromEnv`
selects the manual override when present and bypasses the HTTP provider entirely
(deterministic, no network). An unparseable / non-positive override FAILS FAST at
boot (UNIT-017) — it is never silently served as a zero rate.

## Capacity

A single daily fetch (`0 0 * * *` UTC) of one USD-base rates document. Negligible
quota against any free FX API tier (e.g. open.er-api.com / exchangerate.host
free tiers allow ≥1k req/month; we use ~30). The fetch carries a hard
`context` deadline (`fx.DefaultTimeout = 10s`, Q-FXSRC ADD) so a hung upstream
cannot stall the cron.

## Operator Runbook

- **Rotation**: update `kv/data/he-api/upstream/fxrate/provider_url` in Vault;
  the ExternalSecret re-syncs the K8s Secret; the next daily CronJob pod picks up
  the new value (no in-flight pod to restart — the cron is single-shot).
- **Provider outage**: STALE-SERVE is automatic — a failed fetch inserts NOTHING,
  the last-known `fx_rates` row stays active, `fx_refresh.failed` fires + alert,
  and the cron exits 0 (retries next day). No operator action required unless the
  `he_billing_fx_refresh_last_success_timestamp_seconds` gauge ages beyond ~2
  days (investigate the provider / rotate the URL).
- **Break-glass**: set `FX_MANUAL_USD_CNY` on the CronJob (dev values overlay) to
  pin a hand-entered rate while the provider is unreachable.

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| Pluggable `FxProvider` + manual override + new secret | SM Q-FXSRC → Architect RATIFIED | 2026-06-09 | Avoid hard-coding one vendor; dev/CI/air-gapped determinism; secret env-injected, never logged. |
| Hard `context` deadline on the fetch | Architect Q-FXSRC ADD | 2026-06-09 | Stale-serve correctness depends on a bounded fetch (UNIT-018). |
| STALE-SERVE on any failure (exit 0) | SM Q-FXFAIL → Architect RATIFIED | 2026-06-09 | A stale display rate is cosmetic; a zero/null rate is a money defect — never persist/serve it (BR-C-3). |
