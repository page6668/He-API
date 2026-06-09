# Stripe + PayPal Payment Provider Credentials — Vault & Rotation Path

> Story 7.3 — Stripe + PayPal 集成. Vault path + rotation runbook for the payment
> provider secrets consumed by `apps/payment-svc` (Q-SECRETS / BR-W-6).
>
> Template mirrors `docs/dev/secrets/fxrate-provider.md` (Topology / Migration
> Workflow / Capacity / Operator Runbook / Decision Lineage). These secrets gate
> the MONEY-IN path — they are held to strict secret discipline (env-injected,
> NEVER logged; the providers' impls construct GENERIC errors that never embed the
> key/URL, mirroring the 7.2 FX-secret discipline). ⚠️ The webhook **signing
> secret** is DISTINCT from the API **secret key** (separate blast radius): a
> leaked signing secret lets an attacker forge a balance-crediting webhook; a
> leaked API key lets an attacker call the provider API. They rotate independently.

## Topology

| Aspect | Value |
|---|---|
| Vault server | `vault.internal` (managed by Platform Ops) |
| Vault namespace | `he-api` |
| Secret engine | `kv-v2` |
| Path | `kv/data/he-api/payment/stripe/` + `kv/data/he-api/payment/paypal/` |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `he-api-payment-provider` in namespace `he-api-services` |
| Consuming service | `apps/payment-svc` (provider impls under `internal/provider/{stripe,paypal}`) |

### Stripe keys

| Vault key | Env var | Used by | Notes |
|---|---|---|---|
| `secret_key` | `STRIPE_SECRET_KEY` | REST auth (CreateCheckout) | `sk_live_…` (prod) / `sk_test_…` (sandbox/CI) |
| `webhook_signing_secret` | `STRIPE_WEBHOOK_SIGNING_SECRET` | webhook HMAC verify | `whsec_…` — **DISTINCT** from `secret_key` |
| — | `STRIPE_API_BASE_URL` | optional base override | sandbox/test points at a stub; default `https://api.stripe.com` |

### PayPal keys

| Vault key | Env var | Used by | Notes |
|---|---|---|---|
| `client_id` | `PAYPAL_CLIENT_ID` | OAuth2 + REST | |
| `client_secret` | `PAYPAL_CLIENT_SECRET` | OAuth2 + REST | |
| `webhook_id` | `PAYPAL_WEBHOOK_ID` | `verify-webhook-signature` binds to it | the configured webhook's id |
| — | `PAYPAL_API_BASE_URL` | optional base override | default `https://api-m.paypal.com`; sandbox `https://api-m.sandbox.paypal.com` |

The Secret is injected via `envFrom.secretRef` so the keys land in the pod
environment and NEVER render into a manifest. payment-svc's slog uses the shared
`go-observability` redaction handler (keys containing `secret`/`token` are
`[REDACTED]`); provider impls additionally build GENERIC errors (no URL/key in the
message) so a key cannot leak into a log line on an error path.

## Migration Workflow

Story 7.3 lands the **contract** (payment-svc reads the env vars above; the Helm
chart references the Secret via a `paymentProvider.secretName` value). The actual
Vault sync infrastructure (Vault server, ClusterSecretStore, External Secrets
Operator) is **out of scope** for 7.3 — same posture as the Epic-4 vendor keys +
the 7.2 FX secret (lands in Epic 9 ops hardening).

**Dev / CI / sandbox fallback**: set the `*_TEST` sandbox credentials (Stripe
`sk_test_…` + a sandbox `whsec_…`; PayPal sandbox client id/secret + a sandbox
webhook id) and point `STRIPE_API_BASE_URL` / `PAYPAL_API_BASE_URL` at the
provider sandbox (or, in unit tests, at an `httptest` stub). With NO provider
secrets configured, payment-svc still boots — the corresponding provider is simply
not wired (the `PaymentService` falls back to Unimplemented; health still serves),
so a partial config (e.g. Stripe-only in dev) never CrashLoops.

## Capacity

The payment path is NOT a hot path — a user recharges occasionally, not per chat
request. Webhook throughput is provider-bounded + low. Each `CreateCheckout` /
`CreateSubscription` carries a provider `Idempotency-Key` (our order/subscription
id, BR-R-6) so a client double-submit never creates two provider charges.

## Operator Runbook

- **Rotation (API key)**: update `secret_key` / `client_secret` in Vault; the
  ExternalSecret re-syncs; restart the payment-svc deployment to pick up the new
  env. No data migration.
- **Rotation (webhook signing secret)**: roll the signing secret in the provider
  dashboard, update `webhook_signing_secret` / re-create the PayPal webhook +
  update `webhook_id`, sync, restart. Until the restart, in-flight webhooks signed
  with the OLD secret fail verification (400) and the provider redelivers — the
  pending→paid fence means no credit is lost (the redelivery after restart
  verifies + credits exactly once).
- **Suspected signing-secret leak (HIGH severity)**: rotate the signing secret
  IMMEDIATELY (a leaked signing secret = forgeable balance credits). The
  `he_payment_webhook_total{result="rejected_signature"}` counter spiking is the
  detection signal.
- **Break-glass**: disable a provider by unsetting its secrets + restarting — the
  recharge endpoint then rejects that provider with `400_unsupported_payment_provider`.

## Decision Lineage

| Decision | Source | Date | Rationale |
|---|---|---|---|
| NEW `payment-svc` owns the provider SDKs + secrets | SM Q-SVC → Architect RATIFIED | 2026-06-09 | Blast-radius isolation of third-party-API churn + secrets from the billing-svc money-ledger core (topology §3.2). |
| Webhook signing secret DISTINCT from API key | SM Q-SECRETS → Architect RATIFIED | 2026-06-09 | Separate blast radius — a forgeable-webhook leak ≠ an API-call leak; they rotate independently. |
| Stripe `stripe-go` scheme / PayPal thin REST + verify-webhook-signature | Architect Q-SDK | 2026-06-09 | PayPal's official Go SDK is unmaintained; the Stripe-Signature HMAC scheme is implemented over the stdlib behind the seam (offline-testable, no SDK/pin risk — see Dev Agent Record). |
| Secrets env-injected, NEVER logged | SM Q-SECRETS (7.2 cascade) → Architect RATIFIED | 2026-06-09 | A leaked credential on the money-IN path is a direct fraud vector. |
