-- Story 7.7 — 自动充值 + 余额预警 + 月度账单 PDF: create he_api.payment_methods
-- (the FIRST stored, off-session-chargeable payment token surface — a standing
-- charge authorization, PCI §8.4 token-only) + he_api.invoices (the monthly
-- statement SoT + idempotent per-user-month fence) + the additive FK
-- balances.auto_recharge_payment_method_id → payment_methods(id).
--
-- 0010 is migration HEAD; 0011 is the correct next version. The SM-assumed 0014
-- was CORRECTED to 0011 by the Architect (Round-1 Medium #1): Stories 7.4/7.5/7.6
-- were all ADDITIVE-no-migration (they reuse recharge_orders), so 0011-0013 were
-- never consumed.
--
-- Atlas versioned mode (file://migrations/postgres) — plain forward-only DDL,
-- mirroring 0008/0009/0010: NO inline `-- atlas:up/down` annotations and NO
-- paired `.down.sql` file. `atlas migrate down 1` computes the reverse DDL
-- dynamically. The reverse order is: DROP the balances FK first, then invoices,
-- then payment_methods (payment_methods is referenced by the balances FK, so it
-- must drop last).
--
-- Migration safety:
--   - ADDITIVE — 2× CREATE TABLE + indexes + 1 additive FK constraint; NO
--     ALTER/DROP of any existing column. balances.auto_recharge_* (§4.1, created
--     nullable by 0008) stay UNTOUCHED — only a FK is ADDED on the pre-existing
--     auto_recharge_payment_method_id column (which referenced nothing until now).
--   - REVERSIBLE — Atlas dynamic down drops the FK + both new tables; no data
--     loss on any pre-existing table.
--   - NON-DESTRUCTIVE — empty tables (no backfill). The balances FK is added on a
--     column that is NULL for every existing row, so the constraint validates
--     with zero violations.

-- 支付方式 — docs/architecture/data-models.md §4.1 (NEW, Q-PAYMENT-METHODS, Architect
-- RATIFY). Stores ONLY the opaque provider PaymentMethod token (PCI §8.4 SAQ-A:
-- token ONLY, NEVER a card PAN/CVV/expiry). brand + last4 are display-safe values
-- for the saved-method list view. Card off-session is Stripe-only in 7.7
-- (Q-OFFSESSION); payment_provider is reserved for future non-card providers.
CREATE TABLE he_api.payment_methods (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID NOT NULL REFERENCES he_api.users(id),
    payment_provider  VARCHAR(50) NOT NULL,                 -- stripe (card off-session is Stripe-only in 7.7)
    provider_pm_token VARCHAR(200) NOT NULL,                -- opaque Stripe PaymentMethod id — SECRET-grade, NEVER a PAN (PCI §8.4)
    brand             VARCHAR(20),                          -- display-safe (visa / mastercard / amex …)
    last4             CHAR(4),                              -- display-safe last four digits
    is_default        BOOLEAN NOT NULL DEFAULT FALSE,       -- at most one default per user (app-enforced)
    created_at        TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_payment_methods_user ON he_api.payment_methods (user_id);

-- 月度账单 — docs/architecture/data-models.md §4.1 (NEW, Q-INVOICE-TABLE, Architect
-- RATIFY). A monthly statement is a point-in-time financial record: the figures
-- are FROZEN at generation time (BR-I-2 — a later refund does NOT retro-mutate a
-- sent statement). status: generated → emailed (advanced once, after the invoice
-- email dispatches). The UNIQUE(user_id, period) is the exactly-one-per-user-month
-- fence: the monthly CronJob inserts ON CONFLICT (user_id, period) DO NOTHING, so
-- a retry / backoff re-run inserts zero rows for an already-generated user-month.
CREATE TABLE he_api.invoices (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES he_api.users(id),
    period           CHAR(7) NOT NULL,                      -- 'YYYY-MM' (UTC just-ended calendar month)
    total_debit_usd  NUMERIC(12,4) NOT NULL DEFAULT 0,      -- SUM(usage_ledger.cost_usd in period) — string-decimal on the API
    total_credit_usd NUMERIC(12,4) NOT NULL DEFAULT 0,      -- SUM(recharge_orders.amount WHERE paid in period)
    currency         CHAR(3) NOT NULL DEFAULT 'USD',        -- USD only (7.7 — Q-SOT cascade)
    status           VARCHAR(20) NOT NULL DEFAULT 'generated', -- generated | emailed
    pdf_object_key   VARCHAR(300),                          -- private, non-enumerable OSS key (written after render)
    emailed_at       TIMESTAMPTZ,                           -- set on the generated→emailed transition
    created_at       TIMESTAMPTZ DEFAULT NOW(),
    -- Exactly-one-per-user-month fence (BR-I-1): the cron INSERT ... ON CONFLICT
    -- (user_id, period) DO NOTHING relies on this UNIQUE.
    CONSTRAINT uq_invoices_user_period UNIQUE (user_id, period)
);
-- List view: a user's invoices ordered most-recent-first.
CREATE INDEX idx_invoices_user_period ON he_api.invoices (user_id, period DESC);

-- Durable auto-recharge storm fence (Q-TRIGGER, Architect RATIFY-with-hardening):
-- the at-most-one-in-flight invariant must survive Redis loss, so the DB carries
-- the DURABLE guard while the Redis SETNX lock is only the TTL-bounded fast gate.
-- Mark auto-recharge orders (additive, defaulted column — NON-DESTRUCTIVE on the
-- existing 7.3 rows, which are all manual=false) and enforce AT MOST ONE pending
-- auto-recharge per user via a PARTIAL UNIQUE index: a second concurrent
-- auto-recharge insert for the same user (while one is still pending) fails with
-- a unique violation → the trigger treats it as fence-held → no double top-up.
ALTER TABLE he_api.recharge_orders
    ADD COLUMN is_auto_recharge BOOLEAN NOT NULL DEFAULT FALSE;
CREATE UNIQUE INDEX uq_recharge_orders_one_pending_auto
    ON he_api.recharge_orders (user_id)
    WHERE status = 'pending' AND is_auto_recharge;

-- Additive FK on the pre-existing balances.auto_recharge_payment_method_id column
-- (created nullable by 0008 with NO references clause — Q-PAYMENT-METHODS RATIFY).
-- A configured auto-recharge method must be a real, owned payment_methods row; the
-- application ALSO enforces user-ownership (defence in depth, BR-R-7). ON DELETE
-- SET NULL so revoking a method (DELETE /v1/billing/payment-methods/{id}) clears
-- the reference; the application disables auto_recharge_enabled in the same op.
ALTER TABLE he_api.balances
    ADD CONSTRAINT fk_balances_auto_recharge_pm
    FOREIGN KEY (auto_recharge_payment_method_id)
    REFERENCES he_api.payment_methods(id)
    ON DELETE SET NULL;
