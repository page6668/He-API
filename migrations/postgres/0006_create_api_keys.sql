-- Story 3.2 — create he_api.api_keys table (AC3).
--
-- Per docs/architecture/data-models.md §4.1 + AC3 BR-3.1..BR-3.8.
-- Atlas versioned mode (file://migrations/postgres). Plain forward-only DDL —
-- no inline `-- atlas:up` / `-- atlas:down` annotations and NO paired
-- 0006_create_api_keys.down.sql file. `atlas migrate down 1` computes the
-- reverse DDL dynamically per database-bootstrap.md §2.
--
-- Architectural rationale:
--   - Separate table (NOT users-extension) — a user holds N keys; per-key
--     scope / revocation / cost cap require row-per-key granularity.
--   - key_prefix is indexed but NOT unique — base62^12 collisions are
--     statistically near-zero, but enforcement at the app layer (bcrypt
--     iteration over candidate rows, AC2 BR-2.1) keeps schema simple.
--   - key_hash is TEXT (not VARCHAR(60)) — future migration to argon2id
--     produces a longer format; TEXT future-proofs (BR-3.4).
--   - scope defaults to '{}'::jsonb (not NULL) — Story 5.x consumers can
--     always JSON-query without COALESCE (BR-3.3).
--   - team_id intentionally has NO REFERENCES clause in this Story — the
--     he_api.teams table does not exist yet (BR-3.1). Epic 5 lands the
--     `ALTER TABLE he_api.api_keys ADD CONSTRAINT fk_api_keys_team
--     FOREIGN KEY (team_id) REFERENCES he_api.teams(id) ON DELETE CASCADE`
--     follow-up alongside the teams table creation.

CREATE TABLE he_api.api_keys (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID NOT NULL REFERENCES he_api.users(id) ON DELETE CASCADE,
    -- team_id FK deferred to Epic 5; he_api.teams table not yet created.
    team_id                 UUID,
    name                    VARCHAR(100) NOT NULL,
    key_prefix              VARCHAR(16) NOT NULL,
    key_hash                TEXT NOT NULL,
    scope                   JSONB NOT NULL DEFAULT '{}'::jsonb,
    monthly_cost_cap_usd    NUMERIC(10,2),
    current_month_cost_usd  NUMERIC(10,2) DEFAULT 0,
    last_used_at            TIMESTAMPTZ,
    revoked_at              TIMESTAMPTZ,
    created_at              TIMESTAMPTZ DEFAULT NOW()
);

-- BR-3.x: covers the "list all keys for a user" gateway / console path.
CREATE INDEX idx_api_keys_user_id ON he_api.api_keys(user_id);

-- BR-3.x: companion index for forensic audit lookups by exact key_hash
-- (operator runbook — "which user owns hash X"). The hot Validate path
-- uses key_prefix → bcrypt-compare iteration (AC2 BR-2.1).
CREATE INDEX idx_api_keys_hash ON he_api.api_keys(key_hash);
