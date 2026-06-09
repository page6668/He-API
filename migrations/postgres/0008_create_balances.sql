-- Story 7.1 — 计费引擎: create he_api.balances + he_api.usage_ledger, and add
-- the (OFF-in-7.1) he_api.model_pricing.per_call_price_usd column.
--
-- Per docs/architecture/data-models.md §4.1 (balances lines 74-83). Atlas
-- versioned mode (file://migrations/postgres) — plain forward-only DDL,
-- mirroring the Story-6.2 0007_create_models_and_pricing.sql convention: NO
-- inline `-- atlas:up/down` annotations and NO paired `.down.sql` file.
-- `atlas migrate down 1` computes the reverse DDL dynamically (DROP the column,
-- then usage_ledger, then balances) per database-bootstrap.md §2.
--
-- Migration safety (Architect Round-1 ruling — Medium-2 "no-widen"):
--   - ADDITIVE — 2× CREATE TABLE + 1 nullable-column ADD on model_pricing; no
--     ALTER/DROP of any existing column. In particular api_keys.current_month_-
--     cost_usd is LEFT AT NUMERIC(10,2) (its existing Story-5.4 cap-gate role);
--     the billing-accuracy SoT is usage_ledger (NUMERIC(12,4)), reconciled
--     against balances — NOT the coarse cap counter (Architect M-2).
--   - REVERSIBLE — down drops the column + the two new tables (FK order:
--     usage_ledger has no FK to balances, so either order is safe; the column
--     ADD reverses to a DROP COLUMN — lossy only for unused per-call data,
--     acceptable for a test-only down).
--   - NON-DESTRUCTIVE — no data loss on any pre-existing table.
--
-- Q-LAZY: NO backfill of a zero-balance row per existing user. The balances row
-- is created lazily on first deduction via INSERT ... ON CONFLICT (user_id) DO
-- UPDATE (billing-svc internal/ledger). So this migration creates EMPTY tables.

-- 余额 — docs/architecture/data-models.md §4.1 lines 74-83. The schema doc keys
-- balances by user_id with current_usd/current_rmb + the auto-recharge columns
-- (Story 7.7 consumes the auto_recharge_* set; 7.1 only reads/writes current_usd
-- — current_rmb stays DEFAULT 0 until Story 7.2 multi-currency, BR-D-6).
CREATE TABLE he_api.balances (
    user_id                          UUID PRIMARY KEY REFERENCES he_api.users(id),
    current_usd                      NUMERIC(12,4) NOT NULL DEFAULT 0,   -- authoritative USD money truth (BR-A-2)
    current_rmb                      NUMERIC(12,4) NOT NULL DEFAULT 0,   -- Story 7.2 — untouched by 7.1 (BR-D-6)
    auto_recharge_enabled            BOOLEAN DEFAULT FALSE,              -- Story 7.7
    auto_recharge_threshold_usd      NUMERIC(10,2),                      -- Story 7.7
    auto_recharge_amount_usd         NUMERIC(10,2),                      -- Story 7.7
    auto_recharge_payment_method_id  UUID,                              -- Story 7.7
    updated_at                       TIMESTAMPTZ DEFAULT NOW()
);

-- 计费流水（账单准确 SoT + 幂等围栏）— Architect Q-LEDGER (lands in §4.1 canonically)
-- + Q-ABKEY (composite ledger_key PRIMARY KEY).
--
-- ledger_key is the exactly-once idempotency fence (BR-D-1): for a single
-- request it is the he_request_id; for an A/B leg it is `{he_request_id}:{leg}`
-- so each billable leg dedups INDEPENDENTLY (6.4 dual-billing parity, INT-011).
-- A plain (non-unique) he_request_id column is kept alongside for per-request
-- aggregation (GET /v1/usage by_model + reconciliation). cost_usd is the
-- 4-decimal billing-accuracy authority (NUMERIC(12,4)); the reconciliation
-- invariant compares SUM(usage_ledger.cost_usd) against balances.current_usd
-- (both NUMERIC(12,4)) — NOT the NUMERIC(10,2) cap counter (Architect M-2 / E2E-001).
CREATE TABLE he_api.usage_ledger (
    ledger_key        TEXT PRIMARY KEY,                                   -- Q-ABKEY: he_request_id | {he_request_id}:{leg}
    he_request_id     TEXT NOT NULL,                                      -- non-unique; per-request aggregation
    user_id           UUID NOT NULL,                                      -- the billed subject
    api_key_id        UUID NOT NULL,                                      -- the 5.2/5.4 counter subject
    model             VARCHAR(100) NOT NULL,
    prompt_tokens     BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd          NUMERIC(12,4) NOT NULL,                             -- HALF-UP rounded once (BR-C-1 / Q-ROUND)
    billing_mode      VARCHAR(16) NOT NULL DEFAULT 'per_token',          -- per_token (only billed mode in 7.1) | per_call
    ts                TIMESTAMPTZ NOT NULL,                               -- event timestamp (current-month aggregation, BR-A-5)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Per-request aggregation index (GET /v1/usage groups by user_id within the
-- current UTC month; he_request_id index supports A/B leg roll-up).
CREATE INDEX idx_usage_ledger_user_ts ON he_api.usage_ledger (user_id, ts);
CREATE INDEX idx_usage_ledger_he_request_id ON he_api.usage_ledger (he_request_id);

-- 按调用 flat-fee column (Q-PERCALL option-a) — additive, nullable, UNWIRED in
-- 7.1. The cost engine reads it ONLY when a future story enables per-call mode;
-- when enabled, per_call_price_usd REPLACES the per-token cost (Q-PERCALL =
-- replace, not add). NULL → per-token is the billed mode (the only mode in 7.1).
ALTER TABLE he_api.model_pricing
    ADD COLUMN per_call_price_usd NUMERIC(10,6);
