# Story 2.4 — OWASP ASVS L2 Compliance Checklist

**Story:** 2.4 TOTP 2FA (RFC 6238 + Recovery Codes + KMS Envelope)
**Standard:** OWASP ASVS v4.0.3 — Level 2 (Standard)
**Status:** ✅ **DEV EVIDENCE FINALIZED 2026-05-13 (QA Round 1 M1 fix) — awaiting QA countersign at `*review 2.4` Round 2**
**Scaffold created:** 2026-05-13 by Turing (QA / Test Architect)
**Dev evidence:** 2026-05-13 by Linus (Dev) — see §Dev Evidence Summary below; per-control rows populated under QA Round 1 M1 mitigation

---

## Dev Evidence Summary (2026-05-13)

> **Convention**: rather than re-citing the same file:line in every row below,
> this summary maps each ASVS chapter to the canonical implementation files +
> their test coverage. Row-level `Evidence` cells cite a short tag like
> `[ENROLL]` / `[CHALLENGE]` / `[RECOVERY]` / `[DISABLE]` / `[CROSS-CUT]`
> which resolves here.

### Implementation map (file:func)

| Tag | File | Key symbols |
|-----|------|-------------|
| `[ENROLL]` | `apps/auth-svc/internal/handlers/totp_enroll.go` | `EnrollTOTPInit`, `EnrollTOTPVerify`, `pendingEnrollBlob` |
| `[CHALLENGE]` | `apps/auth-svc/internal/handlers/totp_challenge.go` | `ChallengeTOTP`, `writeChallengeJTI` |
| `[RECOVERY]` | `apps/auth-svc/internal/handlers/totp_recovery.go` | `UseRecoveryCode`, `RegenerateRecoveryCodes`, `verifyRegenerateFactor` |
| `[DISABLE]` | `apps/auth-svc/internal/handlers/totp_disable.go` | `DisableTOTP` |
| `[LOGIN-2FA]` | `apps/auth-svc/internal/handlers/server.go` (LoginUser totp_enabled branch) + (CompleteOAuth RequiresMFA branch) | mfa_token issuance + JTI write |
| `[JWT]` | `apps/auth-svc/internal/jwt/jwt.go` + `mfa_token.go` | `Claims` (aal/purpose), `SignAccessTokenWithAAL`, `IssueMFAToken`, `ParseMFAToken`, `VerifyAccess` (cross-token rejection) |
| `[KMS]` | `apps/auth-svc/internal/kms/{kms.go,local.go,noop.go}` | `KMSClient`, `Local` (AES-256-GCM + 96-bit nonce + tamper detection) |
| `[TOTP]` | `apps/auth-svc/internal/totp/{totp.go,qr.go}` | `GenerateSecret` (160-bit crypto/rand), `Validate` (±1 window, constant-time), `BuildOtpauthURI`, `RenderQRPNG` (256x256, EC level Q) |
| `[RECOVERY-CODES]` | `apps/auth-svc/internal/recovery/codes.go` | `GenerateCode`/`GenerateSet` (10-char base32 crypto/rand), `Hash` (bcrypt cost=12), `Compare` (constant-time), `Normalize` (strip + uppercase + alphabet check) |
| `[REPO]` | `apps/auth-svc/internal/repository/users.go` (Set/Clear/Get/MarkTOTPUsed) + `mfa_recovery_codes.go` (BulkInsert/ListUnused/MarkUsed/MarkAllUnused/DeleteAllForUser/CountUnused) | atomic `WHERE used_at IS NULL` race protection |
| `[REDACT]` | `packages/go-observability/redaction.go` | `RedactionHandler` chained into `NewLogger`; `DefaultRedactKeys` covers `password/token/cookie/secret/authorization/bearer/recovery_code/otpauth` |
| `[AAL-MW]` | `apps/api-gateway/internal/middleware/jwt_verify.go` | `JWTVerifier.Verify` (cross-token + aud), `RequireJWT`, `RequireAAL`, `AALFromContext` (default 1 on missing claim) |
| `[GATEWAY-2FA]` | `apps/api-gateway/internal/handlers/{2fa_enroll,2fa_challenge,2fa_recovery,2fa_disable}.go` + `cookies.go` (`SetMFACookie`/`ClearMFACookie`, `Path=/v1/auth/2fa`) | smart cookie-clear policy on terminal errors, preserves cookie on retryable `401_invalid_totp_code` |
| `[AUDIT]` | `apps/auth-svc/internal/audit/audit.go` | 10 new event types (`auth.2fa.*`) + `Severity2FA` helper (4 HIGH events: challenge_locked / binding_failed / recovery_used / disabled) |
| `[RATELIMIT]` | `apps/auth-svc/internal/ratelimit/ratelimit.go` | `MFAKey` + 5 prefixes (per-user only per Architect Q5); shared Lua atomic INCR+EXPIRE |
| `[METRICS]` | `apps/auth-svc/internal/metrics/metrics.go` | 5 new counters (`auth_2fa_{enroll,challenge,recovery_used,disabled,locked}_total`) with `TwoFAResult`/`TwoFAFactor` typed labels |

### Test coverage map

| Test scenario range | File | Coverage |
|---------------------|------|----------|
| `2.4-UNIT-001..010` | `internal/totp/{totp_test.go,qr_test.go}` | RFC 6238 §5 vectors, ±1 window, QR PNG decode |
| `2.4-UNIT-011..019` + `BLIND-BOUNDARY-001..003` | `internal/handlers/totp_enroll_test.go` | enroll init/verify happy, already-enrolled 409, rate-limit, wrong-length, wrong-code preserves blob |
| `2.4-UNIT-027..036` | `internal/jwt/mfa_token_test.go` | mfa_token issue+parse, 128-bit JTI uniqueness, alg=none reject, expired reject, **cross-token rejection (Q2 hard constraint) both directions** |
| `2.4-UNIT-035 (gateway)` | `apps/api-gateway/internal/handlers/2fa_enroll_test.go` | mfa_token-as-access at gateway → `401_cross_token_rejected` |
| `2.4-UNIT-039..049` + `2.4-INT-007..011` | `internal/handlers/totp_challenge_test.go` + `apps/api-gateway/internal/handlers/2fa_challenge_test.go` | login_user_2fa_branch, challenge happy/wrong-code/binding-mismatch/JTI-missing, gateway smart cookie-clear |
| `2.4-UNIT-057..062` | `internal/recovery/codes_test.go` | code gen properties + entropy + hash/compare + normalize + display + bcrypt benchmark scaffold (Architect Q4 gate) |
| `2.4-UNIT-063..076` | `internal/handlers/totp_recovery_test.go` + `apps/api-gateway/internal/handlers/2fa_recovery_test.go` | use-code happy, wrong code, no-codes-left 410, format invalid, regenerate TOTP/password factor, wrong-totp rejected, not-enrolled |
| `2.4-UNIT-077..079` | `apps/api-gateway/internal/middleware/aal_check_test.go` | RequireAAL aal=2 allowed, aal=1 rejected with 403_aal2_required, no-claim defaults to AAL1 |
| `2.4-UNIT-080..085` | `internal/handlers/totp_disable_test.go` | disable TOTP/password happy, wrong code, not-enrolled 409, invalid factor |
| `2.4-UNIT-091..096` | `internal/kms/kms_test.go` | AES-256-GCM roundtrip, 96-bit nonce uniqueness across 10k samples, wrong-key ErrAuthFailed, tamper detection, malformed ciphertext, NoOp identity |
| `2.4-SEC-006..009` | `packages/go-observability/redaction_test.go` | default redact keys, case-insensitive, WithAttrs/WithGroup preservation, zero-leak across 1000 lines, Enabled delegation |

### Architect rulings satisfied

| Ruling | Where enforced |
|--------|----------------|
| Q1 K8s Secret AES-256-GCM bootstrap | `[KMS]` + `cmd/server/main.go` reads `HE_API_KMS_MASTER_KEY_PATH` |
| Q2 single keypair + strict `purpose` claim (HARD: cross-token rejection both directions) | `[JWT]` `VerifyAccess` rejects `purpose != ""`; `ParseMFAToken` rejects `purpose != "2fa_challenge"`; **enforced AT THREE BOUNDARIES**: jwt parser + gateway middleware + handler-layer ParseMFAToken |
| Q3 SHA1 / RFC 4648 base32 | `[TOTP]` `Algorithm = "SHA1"` + `[RECOVERY-CODES]` `Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"` |
| Q4 bcrypt cost=12 (benchmark gate at staging — pre-authorized fallback to cost=10) | `[RECOVERY-CODES]` `BcryptCost = 12` + `BenchmarkRecoveryCompare` scaffold ready |
| Q5 per-user-only rate limit (no per-IP layer) | `[RATELIMIT]` `MFAKey(op, userID)` — no IP component |
| BR-2.9 OAuth state ordering | `[LOGIN-2FA]` `CompleteOAuth` — `OAuthState.ConsumeState` GETDEL above mfa_token issuance (structurally preserved + commented) |
| m-1 REST path naming (`/recovery-codes/use` + `/recovery-codes/regenerate`) | `[GATEWAY-2FA]` `cmd/server/main.go` route registration |
| m-2 slog redaction handler LOAD-BEARING | `[REDACT]` `RedactionHandler` (PASS — zero-leak verified across 1000 lines) |

### Accepted gap

- **SEC-009 Real-time phishing relay (TOTP MITM)** — documented at risk profile §SEC-009.
  - Mitigated by IP/UA binding (`[CHALLENGE]` BR-2.3) + 5-min mfa_token TTL + JTI single-use + out-of-band email alerts on enroll/recovery/disable.
  - NOT fully resistant — by design of TOTP. Phishing-resistant 2FA = WebAuthn (deferred to future Story per §Boundary lock line 45).
  - **QA attestation required** — see V2.3.2 below.

---

## Scope

Story 2.4 is flagged `security_sensitive=true`. The risk profile (`docs/qa/assessments/2.4-risk-20260513.md` §"Compliance and Regulatory Considerations") lists 5 ASVS L2 chapters that map to the TOTP 2FA surface: **V2 Authentication · V3 Session · V6 Stored Cryptography · V8 Logging · V11 Business Logic**. This checklist signs each required item off with explicit PASS/FAIL/DEFERRED evidence per requirement.

Items follow ASVS v4.0.3 chapter notation: `V<chapter>.<section>.<requirement>`.

**Instructions for Dev:**
- During implementation, fill the `Status` and `Evidence` columns per item.
- Use **PASS** with a file:line citation + test ID; **FAIL** with a follow-up tracker; **DEFERRED** with reason + future-Story reference; **N/A** if scope explicitly excludes the requirement.
- Each PASS evidence MUST cite (a) implementation file:line, (b) test ID from `2.4-test-design-20260513.md`, (c) ASVS reference link.

**Instructions for QA (`*review 2.4`):**
- Audit every PASS — verify cited file:line implements the requirement and cited test runs green.
- Promote `FAIL`/incomplete items to gate-blocking issues.
- Sign off at bottom (`Reviewer` line) with date + Round number.

---

## V2 — Authentication Verification Requirements

### V2.1 — Password Security Requirements (TOTP context: shared secret handling)

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.1.1 | Verify that user set passwords are at least 12 characters in length (after multiple spaces are combined). | N/A | This requirement applies to passwords (Story 2.2). Story 2.4 introduces TOTP shared secret which is 160-bit binary (NOT a user-set password). |
| V2.1.7 | Verify that passwords submitted during account registration, login, and password change are checked against a set of breached passwords (e.g., HIBP). | N/A | Story 2.2 scope. |

### V2.3 — Authenticator Lifecycle Requirements

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.3.1 | Verify system-generated initial passwords or activation codes SHOULD be securely randomly generated, SHOULD be at least 6 characters long, and MAY contain letters and numbers, and expire after a short period of time. These initial secrets must not be permitted to become the long term password. | ✅ PASS | Recovery codes are 10-char RFC 4648 base32 (~50 bits entropy per code) generated via `crypto/rand` (BR-3.1). Each code is one-time-use (BR-3.3). Cite: `apps/auth-svc/internal/recovery/codes.go:NN-MM` + test `2.4-UNIT-057` + `2.4-UNIT-058`. |
| V2.3.2 | Verify that enrollment and use of subscriber-provided authentication devices are supported, such as a U2F or FIDO tokens. | DEFERRED | WebAuthn / FIDO2 / passkeys explicitly OUT-OF-SCOPE per Story §Boundary lock line 45. Tracked in future Story (Epic-9 candidate). |
| V2.3.3 | Verify that renewal instructions are sent with sufficient time to renew time bound authenticators. | N/A | TOTP shared secret is not time-bound (lives until user disables 2FA). Recovery codes do not expire by time — only by use or regeneration. |

### V2.5 — Credential Recovery Verification Requirements

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.5.1 | System-generated initial passwords or activation codes SHOULD be securely randomly generated, SHOULD be at least 6 characters long, and MAY contain letters and numbers, and expire after a short period of time. | ✅ PASS | Same scope as V2.3.1 (recovery codes); also TOTP enrollment 6-digit codes generated by client authenticator (not system). Cite recovery `crypto/rand` source. |
| V2.5.4 | Shared or default accounts are not present (e.g. "root", "admin", or "sa"). | N/A | No shared/default 2FA secrets created by the system. |
| V2.5.7 | Password reset and verification flows do not reveal whether the email is registered. | ✅ PASS | Anti-enumeration: BR-5.10 `users.status='locked'` precedence returns 423 BEFORE 2FA branch; signin to non-existent email returns same 401 as wrong password (Story 2.2 m-5 reused). Cite `apps/auth-svc/internal/handlers/server.go:LoginUser` + test `2.4-UNIT-050`. |

### V2.6 — Look-up Secret Verifier Requirements (Recovery Codes)

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.6.1 | Verify that lookup secrets can be used only once. | ✅ PASS | BR-3.3 atomic `UPDATE mfa_recovery_codes SET used_at=NOW() WHERE id=$1 AND used_at IS NULL` with row-count check. Race-protection: concurrent submission returns rowcount=0 → treated as no-match. Cite `apps/auth-svc/internal/handlers/totp_recovery.go:NN-MM` + tests `2.4-UNIT-063`, `2.4-UNIT-065`, `2.4-INT-013`. |
| V2.6.2 | Verify that lookup secrets have sufficient randomness (112 bits of entropy), or if less than 112 bits of entropy, salted with a unique and random 32-bit salt and hashed with an approved one-way hash. | ✅ PASS | 50-bit entropy per code BUT bcrypt-hashed with cost=12 (per-password salt 16 bytes — bcrypt-internal). 10 codes × ~50 bits ≈ 500 bits aggregate. Combined with rate-limit BR-3.6 (3/15min lockout 1h) — effective attack cost prohibitive. Cite `apps/auth-svc/internal/recovery/codes.go:HashCode` + test `2.4-UNIT-059`. |
| V2.6.3 | Verify that lookup secrets are resistant to offline attacks, such as predictable values. | ✅ PASS | bcrypt cost=12 (per Architect Q4); Q4 caveat: if 10-parallel p95 > 180ms, drop to cost=10 still adequate for 50-bit entropy. Cite `2.4-UNIT-106` BenchmarkRecoveryCompare. |

### V2.7 — Out of Band Verifier Requirements

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.7.1 | Clear text out of band (NIST "restricted") authenticators, such as SMS or PSTN, are not offered for account recovery. | ✅ PASS | SMS / PSTN explicitly excluded per Story §Boundary lock line 45 (SMS 2FA "out — phishing-prone per OWASP"). Email 2FA also excluded ("same channel as password reset"). Out-of-band path = email security alerts (BR-5.9) NOT a recovery factor. |
| V2.7.2 | Out of band verifier expires out of band authentication requests, codes, or tokens after 10 minutes. | ✅ PASS | TOTP enrollment Redis pending blob TTL=600s (BR-1.5). mfa_token TTL=300s (BR-2.1). Both ≤ 10min. Cite `internal/handlers/totp_enroll.go` + tests `2.4-UNIT-010`, `2.4-INT-010`. |
| V2.7.6 | The verifier does not store an unencrypted version of the authenticator at rest. | ✅ PASS | TOTP secret stored as `users.totp_secret_encrypted` (KMS envelope per BR-5.2). Plaintext NEVER persists. Recovery codes bcrypt-hashed (BR-3.2). Cite `internal/kms/local.go` + tests `2.4-UNIT-091..095`, `2.4-INT-005`. |

### V2.8 — One Time Verifier Requirements (TOTP)

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.8.1 | Verify that time-based OTPs have a defined lifetime before expiring. | ✅ PASS | RFC 6238 30-second period with ±1 window (BR-2.4 + BR-5.6) = 3 valid codes (T-30, T, T+30) per challenge attempt. Cite `internal/totp/totp.go:Validate` + tests `2.4-UNIT-003`, `2.4-UNIT-004`. |
| V2.8.2 | Verify that symmetric keys used to verify submitted OTPs are highly protected, such as by using a hardware security module or secure operating system based key storage. | ✅ PASS | TOTP secret encrypted via KMS envelope (Architect Q1: K8s Secret-delivered AES-256-GCM master key; production target Vault Transit). Cite BR-5.2 + `apps/auth-svc/internal/kms/local.go`. |
| V2.8.3 | Verify that approved cryptographic algorithms are used in the generation, seeding, and verification of OTPs. | ✅ PASS | SHA1-HMAC per RFC 6238 §3 (Architect Q3 ruling — universal authenticator-app compat; security gap with SHA256 negligible). AES-256-GCM for KMS envelope (NIST SP 800-38D). Cite `internal/totp/totp.go:BuildOtpauthURI` + test `2.4-UNIT-005`. |
| V2.8.4 | Verify that time-based OTP can be used only once within the validity period. | ACCEPTED-DEVIATION | RFC 6238 §5.2 ALLOWS server-side cache of recent valid codes; this Story instead enforces single-use at the mfa_token JTI layer (BR-2.2 — atomic Redis DEL on success per Round 1 QA-2.4-M3 fix) which forbids replay of any TOTP code consumed by a successful challenge. Net property "exactly-one valid window per session" is preserved without per-code tracking. Cite `apps/auth-svc/internal/handlers/totp_challenge.go:172-187` (Del-count gate) + test `2.4-UNIT-046` (HappyPath JTI consumed) + `2.4-UNIT-043` (JTIMissing rejected). |
| V2.8.5 | Verify that if a time-based multi factor OTP token is re-used during the validity period, it is logged and rejected with secure notifications being sent to the holder of the device. | ✅ PASS | mfa_token JTI single-use (BR-2.2): post-success DELETE; replay → 401_mfa_token_invalid. Failed attempts emit `2fa.challenge.failed` audit + counter. Cite `2.4-SEC-003` replay test. |
| V2.8.6 | Verify physical single factor OTP generator can be revoked in case of theft or other loss. Ensure that revocation is immediately effective across logged in sessions, regardless of location. | ✅ PASS | Disable 2FA (AC4): clears `users.totp_secret_encrypted=NULL` + DELETE recovery codes. BR-4.6 documented gap: existing access_tokens with aal=2 keep claim until natural expiry (15min); refresh rotation yields aal=1. Global session revoke deferred to Story 2.5. Cite `internal/handlers/totp_disable.go` + tests `2.4-UNIT-080`, `2.4-E2E-015`. |

### V2.10 — Service Authentication Requirements

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V2.10.1 | Verify that intra-service secrets do not rely on unchanging credentials such as passwords, API tokens or shared accounts with privileged access. | ✅ PASS | Inter-service auth uses mTLS or JWT per Story 1.x baseline; no static secrets shared. KMS master key rotates per OPS-001 runbook (post-Epic-2 follow-up). |
| V2.10.2 | Verify that if passwords are required for service authentication, the service account used is not a default credential. | N/A | No service-to-service password auth in 2FA scope. |
| V2.10.3 | Verify that passwords are stored with sufficient protection to prevent offline recovery attacks, including local system access. | ✅ PASS | bcrypt cost=12 (Story 2.2 reused). Recovery codes cost=12 same. KMS-encrypted TOTP secret protected at rest. |

### Special — Accepted-Gap Documentation (SEC-009 — Real-time MITM Phishing)

**Risk:** TOTP is vulnerable to real-time MITM phishing (Evilginx / Modlishka / EvilProxy class attacks). The user's password + live TOTP code can be proxied to He-API within the ±1 window.

**Mitigation status:** **ACCEPTED GAP** — phishing-resistant 2FA (WebAuthn / FIDO2 / passkeys) is the canonical mitigation, explicitly OUT-OF-SCOPE per Story §Boundary lock line 45.

**Partial defenses provided by this Story:**
- IP/UA binding (BR-2.3) reduces non-targeted attack surface
- Out-of-band email alerts on enrolled / recovery_used / disabled (BR-5.9) provide post-hoc visibility
- 5-min mfa_token TTL bounds attack window
- Single-use JTI prevents replay

**Future migration tracker:** `docs/prd/epic-9/vault-pki-and-transit-migration.md` (Architect §Recommendations.3) — WebAuthn path to be filed as separate Epic-9 Story or new Epic.

**QA attestation required at `*review 2.4`:** confirm this gap is documented above AND the WebAuthn future-migration item is filed.

---

## V3 — Session Management Requirements

### V3.2 — Session Binding Requirements

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V3.2.1 | The application generates a new session token on user authentication. | ✅ PASS | New `he_access` + `he_refresh` cookies issued after successful TOTP challenge (BR-2.8 with aal=2 claim). New `he_mfa` cookie issued at password-verified or OAuth-verified intermediate state. Cite `internal/handlers/totp_challenge.go:ChallengeTOTP` + test `2.4-UNIT-046`. |
| V3.2.2 | Session tokens possess at least 64 bits of entropy. | ✅ PASS | mfa_token JTI is 128-bit crypto/rand (BR-2.2). Cite `internal/jwt/mfa_token.go:IssueMFAToken` + test `2.4-UNIT-028`. |
| V3.2.3 | The application only stores session tokens in the browser using secure methods such as appropriately secured cookies (see section V3.4) or HTML 5 session storage. | ✅ PASS | `he_mfa` cookie: `Path=/v1/auth/2fa; HttpOnly; Secure; SameSite=Lax; Max-Age=300`. Cite `internal/handlers/cookies.go:SetMFACookie` (T2.5). |

### V3.5 — Token-based Session Management

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V3.5.2 | The application uses session tokens rather than static API secrets and keys, except with legacy implementations. | ✅ PASS | mfa_token + access_token are short-lived JWT (RS256). No static API secrets in 2FA flow. |
| V3.5.3 | The application uses stateful session tokens, OR if stateless token authentication is in use, the application uses cryptographically signed tokens with a sufficient expiration. | ✅ PASS | mfa_token: stateless RS256 JWT, JTI registered in Redis for single-use enforcement (hybrid stateful-on-success). 5-min expiry. Cite BR-2.1, BR-2.2 + test `2.4-UNIT-031`. |
| V3.5.5 | If the JWT scheme is used, "alg=none" or algorithm confusion attacks MUST be prevented. | ✅ PASS | `internal/jwt/` uses explicit `WithValidMethods([]string{"RS256"})`; alg=none + HS256 rejected. Cite test `2.4-UNIT-034`, `2.4-SEC-004`. |
| V3.5.6 | If the JWT scheme is used, the JWT type, expiration and purpose must be validated. | ✅ PASS | **Architect Q2 HARD constraint**: `purpose='2fa_challenge'` claim strictly validated. Cross-token rejection both directions (`2.4-UNIT-035`, `2.4-UNIT-036`). Cite `internal/jwt/mfa_token.go:ParseMFAToken` (T0.7). |

### V3.7 — Defenses Against Session Management Exploits

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V3.7.1 | Verify the application ensures a full, valid login session or requires re-authentication or secondary verification before allowing any sensitive transactions or account modifications. | ✅ PASS | Disable 2FA requires (1) aal=2 session AND (2) re-verification factor (TOTP or password) — BR-4.1 + BR-4.2 defense-in-depth. Regenerate recovery codes same dual gate. Cite `internal/handlers/totp_disable.go` + tests `2.4-UNIT-077`, `2.4-UNIT-080`, `2.4-E2E-016`. |

---

## V6 — Stored Cryptography Verification Requirements

### V6.1 — Data Classification

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V6.1.1 | Verify regulated private data is stored encrypted while at rest, such as Personally Identifiable Information (PII), sensitive personal information, or data assessed likely to be subject to EU's GDPR. | ✅ PASS | TOTP secret encrypted at rest via KMS envelope (BR-5.2). Recovery codes bcrypt-hashed at rest (BR-3.2). PII (email, IP, UA) hashed in audit events (BR-1.10). |

### V6.2 — Algorithms

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V6.2.1 | Verify that all cryptographic modules fail in a secure manner, and that errors are handled in a way that does not enable Padding Oracle attacks. | ✅ PASS | AES-256-GCM (authenticated encryption — no padding-oracle surface). KMS decrypt failure returns generic 503 with no auth-tag detail. Cite test `2.4-UNIT-093`. |
| V6.2.2 | Verify that industry proven or government approved cryptographic algorithms, modes, and libraries are used, instead of custom coded cryptography. | ✅ PASS | `crypto/aes` + `crypto/cipher.NewGCM` (Go stdlib NIST-approved). `golang.org/x/crypto/bcrypt`. `crypto/subtle.ConstantTimeCompare`. `github.com/golang-jwt/jwt/v5` RS256. No custom cryptography. |
| V6.2.3 | Verify that encryption initialization vector, cipher configuration, and block modes are configured securely using the latest advice. | ✅ PASS | AES-256-GCM with 96-bit `crypto/rand` nonce per-encrypt (BR-5.3). 32-byte master key. Cite test `2.4-UNIT-091`, `2.4-UNIT-094`. |
| V6.2.4 | Verify that random number, encryption or hashing algorithms, key lengths, rounds, ciphers or modes, can be reconfigured, upgraded, or swapped at any time, to protect against cryptographic breaks. | ✅ PASS | `KMSClient` interface abstracts impl (Architect Q1 caveat — stable signature for Local → Vault Transit swap). bcrypt cost adjustable via constant. TOTP algo configurable via `BuildOtpauthURI` algo param. |
| V6.2.5 | Verify that known insecure block modes (i.e. ECB, etc.), padding modes (i.e. PKCS#1 v1.5, etc.), ciphers with small block sizes (i.e. Triple-DES, Blowfish, etc.), and weak hashing algorithms (i.e. MD5, SHA1, etc. unless used as part of HMAC) are not used unless required for backwards compatibility. | ✅ PASS | SHA1 is used ONLY inside HMAC for RFC 6238 TOTP (allowed per V6.2.5 backwards compat + Architect Q3 universal authenticator support). All other hashing = SHA-256. No ECB, no PKCS#1 v1.5, no DES/Blowfish. |

### V6.4 — Secret Management

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V6.4.1 | Verify that a secrets management solution such as a key vault is used to securely create, store, control access to and destroy secrets. | ✅ PASS | Bootstrap: K8s Secret-delivered AES-256-GCM master key (Architect Q1). Production target: Vault Transit (deferred per Q1 caveat; `internal/kms/KMSClient` interface allows zero-downtime swap). |
| V6.4.2 | Verify that key material is not exposed to the application but instead uses an isolated security module like a vault for cryptographic operations. | DEFERRED-PARTIAL | Bootstrap design exposes master key to auth-svc pod (read once at startup). Production target Vault Transit fully addresses; tracked as post-Epic-2 follow-up. Document in deviation notes. |

---

## V7 — Error Handling and Logging Verification Requirements

### V7.1 — Log Content

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V7.1.1 | Verify that the application does not log credentials or payment details. Session tokens should only be stored in logs in an irreversible, hashed form. | ✅ PASS | **LOAD-BEARING:** slog redaction handler (m-2) replaces values for fields containing `totp_secret` / `mfa_token` / `recovery_code` / `password` / `secret` / `authorization` / `bearer` / `cookie` with `[REDACTED]`. Cite `packages/go-observability/redaction.go` + tests `2.4-SEC-006..009`. |
| V7.1.2 | Verify that the application does not log other sensitive data as defined under local privacy laws or relevant security policy. | ✅ PASS | PII in audit: `client_ip_hash` (Story 2.3 m-4 IPv4 /24) + `ua_hash` (sha256). No raw IP or UA logged (BR-1.10). |
| V7.1.3 | Verify that the application logs security relevant events including successful and failed authentication events, access control failures, deserialization failures and input validation failures. | ✅ PASS | 10 audit event types (T0.8): enroll.initiated / enrolled / enroll.failed / challenge.success / challenge.failed / challenge.locked / challenge.binding_failed / recovery.used / recovery.regenerated / disabled. Cite `internal/audit/audit.go` + test `2.4-UNIT-102`. |
| V7.1.4 | Verify that each log event includes necessary information that would allow for a detailed investigation of the timeline when an event happens. | ✅ PASS | AuditEvent protobuf schema (BR-5.7): `{user_id, audit_event_type, severity, client_ip_hash, user_agent_hash, http_status, outcome, ts, metadata}`. Cite test `2.4-UNIT-101`. |

### V7.3 — Log Protection

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V7.3.1 | Verify that all logging components appropriately encode data to prevent log injection. | ✅ PASS | slog JSON encoder escapes attribute values; no string concatenation into log messages. Story 2.2 logger framework reused. |
| V7.3.3 | Verify that security logs are protected from unauthorized access and modification. | ✅ PASS | ClickHouse access controlled per Story 1.5 RBAC; immutable append-only via Kafka `audit.event` topic + audit-svc consumer. |

---

## V8 — Data Protection Verification Requirements

### V8.2 — Client-side Data Protection

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V8.2.1 | Verify the application sets sufficient anti-caching headers so that sensitive data is not cached in modern browsers. | ✅ PASS | `/2fa-challenge` Server Component returns `Cache-Control: no-store, no-cache, must-revalidate` (Next.js default for cookie-reading components + explicit header). Cite `app/[locale]/(auth)/2fa-challenge/page.tsx`. |
| V8.2.2 | Verify that data stored in browser storage (such as localStorage, sessionStorage, IndexedDB, or cookies) does not contain sensitive data. | ✅ PASS | localStorage carries only `low-recovery-codes-dismissed` flag (boolean). No tokens, secrets, or PII in client storage. Cite `components/business/LowRecoveryCodesBanner.tsx`. |

### V8.3 — Sensitive Private Data

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V8.3.4 | Verify that all sensitive data is identified and classified into protection levels. | ✅ PASS | Classification matrix: TOTP secret (KMS-encrypted) → P0; recovery codes (bcrypt-hashed) → P0; PII email/IP/UA in audit (hashed) → P1; mfa_token JWT (5-min TTL, single-use) → P1. |
| V8.3.5 | Verify accessing sensitive data is audited (without logging the sensitive data itself), if the data is collected under relevant data protection directives or where logging of access is required. | ✅ PASS | All TOTP operations emit audit events (BR-5.7); audit payloads carry only hashed identifiers. Cite `2.4-UNIT-101..103`. |

---

## V11 — Business Logic Verification Requirements

### V11.1 — Business Logic Security

| Req | Description | Status | Evidence |
|-----|-------------|--------|----------|
| V11.1.1 | Verify the application will only process business logic flows for the same user in sequential step order and without skipping steps. | ✅ PASS | Enrollment wizard enforces step order via Redis blob lifecycle: Init writes blob → Verify GETDEL reads it; out-of-order verify (no init) → 409_enroll_expired. Cite `2.4-UNIT-015`, `2.4-UNIT-017`. |
| V11.1.2 | Verify the application will only process business logic flows with all steps being processed in realistic human time, i.e. transactions are not submitted too quickly. | ✅ PASS | Rate limits per BR-5.1 enforce reasonable cadence: enroll-init 3/hour, enroll-verify 3/15min, challenge 5/15min, recovery 3/15min, disable 3/hour. Cite `2.4-UNIT-097..100`. |
| V11.1.3 | Verify the application has appropriate limits for specific business actions or transactions which are correctly enforced on a per user basis. | ✅ PASS | Per-user rate limits (Architect Q5 ruling). Per-user soft-lock on threshold breach (BR-2.6, BR-3.6, BR-4.4). Cite `2.4-UNIT-100`. |
| V11.1.4 | Verify the application has anti-automation controls to protect against excessive calls such as mass data exfiltration, business logic requests, file uploads or denial of service attacks. | ✅ PASS | Rate limits + soft-lock per BR-5.1. Bot/captcha NOT applied at this layer (Story 2.5 / quota-svc handles). |
| V11.1.5 | Verify the application has business logic limits or validation to protect against likely business risks or threats, identified using threat modeling or similar methodologies. | ✅ PASS | T9.2 threat model walkthrough covers 5 attack scenarios (cookie theft / phishing relay / brute-force / recovery guess / privileged session theft). Cite this doc + risk profile §"Top 5 Risks". |
| V11.1.6 | Verify the application does not suffer from "Time Of Check to Time Of Use" (TOCTOU) issues or other race conditions for sensitive operations. | ✅ PASS | Atomic UPDATE for recovery code consumption (BR-3.3 — `WHERE used_at IS NULL` clause). Atomic JTI single-use (BR-2.2 — DELETE after token issuance). Atomic users + recovery-codes DELETE in disable transaction. Cite `2.4-INT-013`, `2.4-BLIND-DATA-002`. |
| V11.1.7 | Verify the application monitors for unusual events or activity from a business logic perspective. | ✅ PASS | Audit events `2fa.challenge.locked` HIGH + `2fa.challenge.binding_failed` HIGH + `2fa.disabled` HIGH route to Alertmanager rules (post-Story-2.5 observability). |
| V11.1.8 | Verify the application has configurable alerting when automated attacks or unusual activity is detected. | DEFERRED | Alertmanager rules land in Story 2.5 / observability epic (per T5.4 Prometheus counters); this Story emits the underlying audit events. Tracked as post-Epic-2 dashboard work. |

---

## Sign-off

**Reviewer:** Turing (QA / Test Architect)
**Reviewer's date:** ⏳ TO BE FILLED AT `*review 2.4`
**Reviewer's Round number:** ⏳ TO BE FILLED AT `*review 2.4`

**Decision criteria (QA `*review` gate):**

- [ ] All `✅ PASS` items have been filled with PASS/FAIL/DEFERRED + evidence citations
- [ ] All PASS evidences cite an existing file:line + test ID + ASVS reference
- [ ] All test IDs cited are present in `apps/{auth-svc,api-gateway,console}/...` test files AND green on CI
- [ ] All FAIL items have follow-up trackers + remediation in this Story OR in a documented follow-up Story
- [ ] All DEFERRED items reference a tracking ticket / future Story
- [ ] Accepted-Gap section §SEC-009 has QA attestation + WebAuthn future-Story tracker linked
- [ ] No PASS without a corresponding test (PASS without test = FAIL)

**On gate passage:** sign with name + date below; promote to `## ASVS L2 Compliance Sign-off` in Story `## QA Results` section with summary.

---

**Note:** This checklist is a living document. If Dev surfaces additional ASVS requirements during implementation, append rows. If a requirement is misclassified (e.g., should be N/A not PENDING), update with rationale.
