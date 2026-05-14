-- Story 2.4 — TOTP 2FA schema additions (Atlas versioned mode, Story 1.6 Q1).
--
-- 1) ALTER users — add `totp_enrolled_at` + `totp_last_used_at` timestamp
--    columns. `totp_secret_encrypted` + `totp_enabled` already exist (Story
--    2.2 migration 0002 anticipated this Story; see data-models.md §4.1).
--
-- 2) CREATE mfa_recovery_codes — 10 rows per enrolled user; bcrypt-hashed at
--    rest (BR-3.2). Partial index on WHERE used_at IS NULL keeps the lookup
--    path tiny (≤ 10 rows × N enrolled users). FK ON DELETE CASCADE means
--    deleting a user cascades the recovery codes (Story 2.7).

ALTER TABLE he_api.users
    ADD COLUMN totp_enrolled_at  TIMESTAMPTZ,
    ADD COLUMN totp_last_used_at TIMESTAMPTZ;

CREATE TABLE he_api.mfa_recovery_codes (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID         NOT NULL REFERENCES he_api.users(id) ON DELETE CASCADE,
    code_hash           TEXT         NOT NULL,                    -- bcrypt(cost=12) per Architect Q4
    used_at             TIMESTAMPTZ,                              -- NULL = unused (BR-3.3 atomic single-use)
    used_from_ip_hash   TEXT,                                     -- /24 IPv4 / /64 IPv6 prefix hash (Story 2.3 m-4 helpers)
    used_from_ua_hash   TEXT,                                     -- sha256(full UA)
    regenerated_at      TIMESTAMPTZ,                              -- populated when bulk-marked by RegenerateRecoveryCodes
    regenerated_reason  VARCHAR(50),                              -- 'user_initiated' | 'security_event' | 'support_reset'
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- idx_mfa_recovery_user_unused — primary lookup path for AC3 UseRecoveryCode
-- (SELECT FOR UPDATE … WHERE user_id=$1 AND used_at IS NULL). Partial filter
-- keeps the index size proportional to active-2FA users only.
CREATE INDEX idx_mfa_recovery_user_unused
    ON he_api.mfa_recovery_codes (user_id)
    WHERE used_at IS NULL;

-- idx_mfa_recovery_user_all — forensic / audit lookups ordered by created_at
-- (full set of codes for a user, including used). Used by audit-svc joins.
CREATE INDEX idx_mfa_recovery_user_all
    ON he_api.mfa_recovery_codes (user_id, created_at DESC);
