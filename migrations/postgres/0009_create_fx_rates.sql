-- Story 7.2 — 多币种 + 汇率刷新: create he_api.fx_rates (effective-dated USD-base
-- FX rate rows) + an idempotent bootstrap seed (one USD→CNY row) so day-0 /
-- cold-start display conversions work BEFORE the first daily refresh cron fires.
--
-- Per docs/architecture/data-models.md §4.1 (fx_rates NEW). Atlas versioned mode
-- (file://migrations/postgres) — plain forward-only DDL, mirroring the
-- 0007/0008 convention: NO inline `-- atlas:up/down` annotations and NO paired
-- `.down.sql` file. `atlas migrate down 1` computes the reverse DDL dynamically
-- (DROP TABLE he_api.fx_rates) per database-bootstrap.md §2.
--
-- Migration safety (Architect Round-1 BR-C-6):
--   - ADDITIVE — 1× CREATE TABLE + 1 idempotent seed row; NO ALTER/DROP of any
--     existing column. In particular balances.current_rmb is NOT widened or
--     altered — it already exists reserved/DEFAULT 0 from 0008 (Q-RMBCOL DEFAULT:
--     stays unused; RMB is compute-on-read display-only, not a stored mirror).
--   - REVERSIBLE — down drops fx_rates (the column ADD-equivalent here is a whole
--     new table, so the reverse is a clean DROP TABLE; no data loss on any
--     pre-existing table).
--   - NON-DESTRUCTIVE — no data loss on any pre-existing table.
--
-- The active rate for a (base,quote) pair is the latest row read as
-- `ORDER BY fetched_at DESC LIMIT 1` (Architect L-1; mirrors model_pricing
-- latest-effective_at, NOT a MAX() subquery). Append-only — the daily cron
-- INSERTs a new row on each successful refresh; the historical rows are retained
-- (small audit trail, BR-C-1). A provider failure inserts NOTHING and the last
-- row stays active (stale-serve, BR-C-3).

-- 汇率快照 — docs/architecture/data-models.md §4.1. base/quote are ISO-4217
-- 3-letter codes (CHAR(3)); rate is NUMERIC(18,8) (read as ::text, parsed with
-- shopspring/decimal — NEVER float64, M-1 cascade). source distinguishes a
-- provider row from the 'bootstrap' seed. fetched_at is the effective timestamp
-- AND a PK component so the same (base,quote) pair can carry many historical rows.
CREATE TABLE he_api.fx_rates (
    base_currency   CHAR(3)        NOT NULL,                    -- ISO-4217, e.g. 'USD'
    quote_currency  CHAR(3)        NOT NULL,                    -- ISO-4217, e.g. 'CNY'
    rate            NUMERIC(18,8)  NOT NULL,                    -- units of quote per 1 base; > 0 (cron-validated, BR-C-3)
    source          VARCHAR(50)    NOT NULL,                    -- provider id, or 'bootstrap' for the seed
    fetched_at      TIMESTAMPTZ    NOT NULL,                    -- effective timestamp; exposed to clients as fx_as_of (BR-B-8)
    PRIMARY KEY (base_currency, quote_currency, fetched_at)
);

-- Bootstrap seed (BR-C-6) — one USD→CNY row with a FIXED fetched_at so a re-apply
-- of this migration is idempotent (ON CONFLICT DO NOTHING → exactly ONE bootstrap
-- row, UNIT-014). source='bootstrap' distinguishes it from provider rows. The
-- value is a reasonable day-0 placeholder; the first successful cron refresh
-- appends a fresher provider row that supersedes it (latest fetched_at wins).
INSERT INTO he_api.fx_rates (base_currency, quote_currency, rate, source, fetched_at)
VALUES ('USD', 'CNY', 7.20000000, 'bootstrap', '2026-06-09T00:00:00Z')
ON CONFLICT (base_currency, quote_currency, fetched_at) DO NOTHING;
