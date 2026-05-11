-- Story 2.2 — users table (Atlas versioned mode, Story 1.6 Q1 ruling).
--
-- Schema per docs/architecture/data-models.md §4.1 plus Wright Round 1 Q5 ruling:
-- `locked_until TIMESTAMPTZ` column and `'locked'` admitted into the status set
-- (status is VARCHAR, validated at the application layer — BR-3.3 + BR-4.4).
--
-- All future Epic 2-10 user-bound tables (api_keys, teams, subscriptions,
-- balances, recharge_orders, …) reference users(id) via FK. Changes to this
-- table's invariants (PK type, email UNIQUE, status set) propagate broadly;
-- treat the schema below as load-bearing.

CREATE TABLE he_api.users (
    id                        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    email                     VARCHAR(255) UNIQUE NOT NULL,
    password_hash             TEXT,                              -- bcrypt cost=12; NULL allowed for future OAuth-only users (Story 2.3)
    email_verified_at         TIMESTAMPTZ,
    oauth_provider            VARCHAR(50),                       -- google / github / NULL — populated from Story 2.3 onward
    oauth_subject             VARCHAR(255),
    locale                    VARCHAR(10)  NOT NULL DEFAULT 'en',
    timezone                  VARCHAR(50)  NOT NULL DEFAULT 'UTC',
    totp_secret_encrypted     TEXT,                              -- KMS-encrypted; populated from Story 2.4 onward
    totp_enabled              BOOLEAN      NOT NULL DEFAULT FALSE,
    status                    VARCHAR(20)  NOT NULL DEFAULT 'active', -- active / locked / suspended / pending_deletion
    locked_until              TIMESTAMPTZ,                       -- Wright Round 1 Q5 ruling: soft-lock expiry (BR-3.3 self-heal)
    pending_deletion_at       TIMESTAMPTZ,                       -- Story 2.7 grace-window marker
    created_at                TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- idx_users_email — primary lookup path (signin, resend-verification, duplicate-email check).
CREATE INDEX idx_users_email ON he_api.users (email);

-- idx_users_oauth — populated from Story 2.3 onward (Google/GitHub linkage).
-- Partial index gates the empty NULL,NULL pair so the index stays small until OAuth rolls out.
CREATE INDEX idx_users_oauth ON he_api.users (oauth_provider, oauth_subject)
    WHERE oauth_provider IS NOT NULL;
