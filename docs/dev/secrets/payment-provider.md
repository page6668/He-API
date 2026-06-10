# Stripe + PayPal + Coinbase + Alipay+ Payment Provider Credentials — Vault & Rotation Path

> Story 7.3 — Stripe + PayPal 集成; Story 7.4 — USDC（Coinbase Commerce）集成;
> Story 7.5 — Alipay+（Antom international）集成.
> Vault path + rotation runbook for the payment provider secrets consumed by
> `apps/payment-svc` (Q-SECRETS / BR-W-6).
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
| Path | `kv/data/he-api/payment/stripe/` + `kv/data/he-api/payment/paypal/` + `kv/data/he-api/payment/coinbase/` |
| External Secrets Operator `ClusterSecretStore` | `he-api-vault` |
| K8s `Secret` (target) | `he-api-payment-provider` in namespace `he-api-services` |
| Consuming service | `apps/payment-svc` (provider impls under `internal/provider/{stripe,paypal,coinbase}`) |

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

### Coinbase Commerce keys (Story 7.4 — USDC)

| Vault key | Env var | Used by | Notes |
|---|---|---|---|
| `api_key` | `COINBASE_COMMERCE_API_KEY` | `X-CC-Api-Key` — REST auth (charge create) | gates the channel: unset → coinbase not wired → `400_unsupported_payment_provider` |
| `webhook_secret` | `COINBASE_COMMERCE_WEBHOOK_SECRET` | `X-CC-Webhook-Signature` HMAC verify | **DISTINCT** from `api_key` (separate blast radius). ⚠️ Coinbase signs lowercase-hex `HMAC-SHA256(rawBody)` with **NO timestamp** → no signature-freshness window; replay defence is the `recharge_orders` state-machine ALONE (BR-W-4). |
| — | `COINBASE_COMMERCE_API_BASE_URL` | optional base override | default `https://api.commerce.coinbase.com`; tests point at an `httptest` stub |

### Alipay+ / Antom keys (Story 7.5 — Alipay+ international)

⚠️ **FIRST ASYMMETRIC scheme on the platform.** Unlike the symmetric HMAC secrets
above, Antom signs notifications with ITS private key and we verify with Alipay+'s
PUBLIC key — so the **verify** key is NOT a secret (a leak cannot forge a
notification). The **sign** key, however, authenticates ALL our OUTBOUND Antom
calls, a BROADER blast radius than any HMAC webhook secret — treat it as the
highest-value payment secret.

| Vault key | Env var | Used by | Notes |
|---|---|---|---|
| `client_id` | `ALIPAY_PLUS_CLIENT_ID` | `Client-Id` header (sign + verify) | gates the channel: unset → alipay not wired → `400_unsupported_payment_provider`. NOT a secret (an identifier). |
| `merchant_private_key` | `ALIPAY_PLUS_MERCHANT_PRIVATE_KEY` | RSA-SHA256 **signs OUR outbound** `/ams/api/v1/payments/pay` calls | ⚠️ **HIGHEST blast radius** — a leak lets an attacker impersonate US to Antom (create/refund payments). PKCS#8/PKCS#1 PEM or bare base64. **NEVER logged.** |
| `alipay_public_key` | `ALIPAY_PLUS_ALIPAY_PUBLIC_KEY` | RSA-SHA256 **verifies INBOUND** notification `Signature` | Alipay+'s PUBLIC key — config-managed but **not a secret** (a leak cannot forge). PKIX/PKCS#1 PEM or bare base64. |
| — | `ALIPAY_PLUS_API_BASE_URL` | optional base override | default `https://open-na.alipay.com`; tests point at an `httptest` stub |
| — | `ALIPAY_PLUS_NOTIFY_PATH` | optional PUBLIC notify-path override | default `/v1/billing/webhooks/alipay`; the URL Antom signs over (Q-NOTIFY-PATH) — MUST be the **public** gateway path, NOT the internal proxied path. |

⚠️ The Alipay+ signature is over a CONSTRUCTED string (`POST <publicNotifyPath>\n<Client-Id>.<Request-Time>.<rawBody>`), and `Request-Time` gives a freshness window → replay defence REGAINS a signature-timestamp layer (on par with Stripe; BETTER than Coinbase 7.4 which had none) on top of the inherited `recharge_orders` state-machine (BR-W-4). Rotation is supported via the `keyVersion` field stamped on the `Signature` header.

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
webhook id; Coinbase Commerce sandbox `api_key` + a sandbox `webhook_secret`;
Alipay+ sandbox `ALIPAY_PLUS_CLIENT_ID` + a sandbox merchant private key + the
Antom sandbox public key) and point `STRIPE_API_BASE_URL` / `PAYPAL_API_BASE_URL` /
`COINBASE_COMMERCE_API_BASE_URL` / `ALIPAY_PLUS_API_BASE_URL`
at the provider sandbox (or, in unit tests, at an `httptest` stub). With NO provider
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
| Coinbase Commerce thin REST + stdlib `X-CC-Webhook-Signature` HMAC | SM Q-SDK (7.4) | 2026-06-10 | Mirrors the PayPal thin-REST decision: no third-party SDK; the signature verify is a stdlib `crypto/hmac` over the raw body (offline-testable). ⚠️ The scheme has NO timestamp → replay defence rests on the inherited `recharge_orders` state-machine alone (BR-W-4). |
| Alipay+/Antom thin REST + stdlib `crypto/rsa` RSA2 asymmetric | SM Q-SDK (7.5) | 2026-06-10 | Mirrors the thin-REST precedent: no third-party SDK; sign/verify are stdlib `crypto/rsa` (`rsa.SignPKCS1v15`/`VerifyPKCS1v15`, `crypto.SHA256`) over the constructed string (offline-testable with an ephemeral keypair). FIRST ASYMMETRIC scheme — we verify with Alipay+'s PUBLIC key. |
| Merchant PRIVATE signing key DISTINCT from the verify key + client id | SM Q-SECRETS (7.5) | 2026-06-10 | The outbound-signing private key has a BROADER blast radius than an HMAC webhook secret (it authenticates all our calls, not one channel); the inbound verify key is a public key (not a secret). Distinct items, distinct handling. |
| Secrets env-injected, NEVER logged | SM Q-SECRETS (7.2 cascade) → Architect RATIFIED | 2026-06-09 | A leaked credential on the money-IN path is a direct fraud vector. |
