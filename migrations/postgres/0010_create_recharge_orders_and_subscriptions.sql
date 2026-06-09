-- Story 7.3 — Stripe + PayPal 集成: create he_api.recharge_orders (the recharge
-- order state-machine + exactly-once credit fence) + he_api.subscriptions (the
-- subscription lifecycle SoT). Both tables were pre-defined in
-- docs/architecture/data-models.md §4.1 (recharge_orders lines 107-119,
-- subscriptions lines 61-71) and explicitly NOT created by Story 7.1
-- ("recharge_orders / subscriptions tables are NOT created here — Stories 7.8 /
-- 7.3+"). 0009 is migration HEAD; 0010 is the correct next version.
--
-- Atlas versioned mode (file://migrations/postgres) — plain forward-only DDL,
-- mirroring 0008/0009: NO inline `-- atlas:up/down` annotations and NO paired
-- `.down.sql` file. `atlas migrate down 1` computes the reverse DDL dynamically
-- (DROP subscriptions, then recharge_orders — the reverse of creation order;
-- neither table is referenced by the other, so FK order is safe).
--
-- Migration safety:
--   - ADDITIVE — 2× CREATE TABLE + indexes only; NO ALTER/DROP of any existing
--     column. balances.auto_recharge_* (§4.1) stay UNTOUCHED (Story 7.7).
--   - REVERSIBLE — Atlas dynamic down drops both new tables; no data loss on any
--     pre-existing table.
--   - NON-DESTRUCTIVE — empty tables (no backfill); rows are written lazily by
--     billing-svc CreateRechargeOrder (recharge_orders) / payment-svc subscribe
--     (subscriptions).
--
-- Exactly-once credit fence (Q-CREDIT-IDEMPOTENCY, Architect RATIFY): the §4.1
-- plain idx(payment_provider, external_order_id) is UPGRADED to a UNIQUE
-- constraint so two recharge orders can never bind one provider charge. NULL
-- external_order_id (the create-time state, before the provider id is known) is
-- exempt from the constraint (Postgres treats NULLs as distinct), so multiple
-- pending orders coexist; the fence engages once the provider id is bound at
-- credit time. The pending→paid state-machine is the primary idempotency guard
-- (billing-svc internal/credit), this UNIQUE is defence-in-depth.

-- 充值订单 — docs/architecture/data-models.md §4.1 lines 107-119. status:
-- pending → paid (credit) | failed | refunded (reserved-but-unused in 7.3,
-- Q-REFUND). amount is the INTENT (NUMERIC(12,4)); the CREDITED value is the
-- provider-confirmed settled amount from the verified webhook (Q-AMOUNT).
CREATE TABLE he_api.recharge_orders (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID NOT NULL REFERENCES he_api.users(id),
    amount            NUMERIC(12,4) NOT NULL,                 -- intent (display/UX); credit = provider truth (BR-R-1)
    currency          VARCHAR(3) NOT NULL,                    -- USD (7.3 enables USD only — Q-CURRENCY)
    payment_provider  VARCHAR(50) NOT NULL,                   -- stripe / paypal (usdc/alipay/wechat are 7.4-7.6)
    external_order_id VARCHAR(200),                           -- provider session/intent id; bound at credit time
    status            VARCHAR(20) NOT NULL,                   -- pending | paid | failed | refunded
    paid_at           TIMESTAMPTZ,                            -- set on the pending→paid transition
    created_at        TIMESTAMPTZ DEFAULT NOW(),
    -- Exactly-once credit fence (Q-CREDIT-IDEMPOTENCY): two orders cannot bind one
    -- provider charge. NULLs (pre-credit) are distinct, so pending rows coexist.
    CONSTRAINT uq_recharge_orders_provider_external UNIQUE (payment_provider, external_order_id)
);
CREATE INDEX idx_recharge_orders_user ON he_api.recharge_orders (user_id);

-- 订阅 — docs/architecture/data-models.md §4.1 lines 61-71. 7.3 ships the RAIL
-- ONLY (Q-SUBSCOPE): this table records the subscription lifecycle (active /
-- past_due / cancelled) driven by recurring webhooks. The plan→entitlement
-- mapping (tier catalog, Beta gate) is Story 7.8, which READS this row. plan is
-- an opaque string in 7.3 (free / pro / team / enterprise).
CREATE TABLE he_api.subscriptions (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                  UUID NOT NULL REFERENCES he_api.users(id),
    plan                     VARCHAR(50) NOT NULL,            -- free / pro / team / enterprise (opaque in 7.3)
    status                   VARCHAR(20) NOT NULL,            -- active / cancelled / past_due
    current_period_start     TIMESTAMPTZ,
    current_period_end       TIMESTAMPTZ,
    payment_provider         VARCHAR(50),                     -- stripe / paypal
    external_subscription_id VARCHAR(200),                    -- provider subscription id (webhook lookup)
    created_at               TIMESTAMPTZ DEFAULT NOW()
);
-- Webhook lookup index: a recurring invoice.paid resolves the subscription by
-- (payment_provider, external_subscription_id).
CREATE INDEX idx_subscriptions_external ON he_api.subscriptions (payment_provider, external_subscription_id);
