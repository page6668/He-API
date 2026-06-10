-- scripts/dev/seed-hkd-fxrate.sql — Story 7.6 T0.3 (Q-HKD-FX) dev/CI data seed.
--
-- WeChat Pay HK / cross-border (Story 7.6) is the FIRST channel to settle HKD. An
-- HKD settlement credits balances.current_usd at the latest USD→HKD fx_rate at
-- settlement via the INHERITED billing-svc credit.go toUSD() branch (credit.go
-- fxRateSQL). credit.go fail-CLOSES (parks the order, no credit at an unknown
-- rate) when no HKD row exists — so an HKD `he_api.fx_rates` row MUST exist for the
-- HKD recharge path to convert.
--
-- This is a DATA/config prerequisite, NOT a schema migration (db_schema=false for
-- 7.6 — he_api.fx_rates already accepts ANY ISO-4217 quote currency; 0009 created
-- it and seeded only USD→CNY). It mirrors the 0009 bootstrap-seed convention (a
-- fixed fetched_at + source='bootstrap' → idempotent re-apply) but lives as a
-- data-only dev/CI seed so 7.6 introduces ZERO migration.
--
-- Apply locally / in CI:  psql "$DATABASE_URL" -f scripts/dev/seed-hkd-fxrate.sql
--
-- ⚠️ PRODUCTION daily refresh: the 7.2 fx-refresh CronJob is single-currency
-- (USD→CNY, apps/billing-svc/internal/fx). Extending the daily refresh to ALSO
-- fetch+append USD→HKD is a 7.2-component enhancement (multi-quote refresh) and is
-- a follow-up (flagged in docs/dev/secrets/fxrate-provider.md). Until then the HKD
-- rate is config-seeded (this row) and refreshed by redeploy — sufficient for the
-- 7.6 MVP (a stale-but-present HKD rate converts; an ABSENT rate fail-closes).

INSERT INTO he_api.fx_rates (base_currency, quote_currency, rate, source, fetched_at)
VALUES ('USD', 'HKD', 7.80000000, 'bootstrap', '2026-06-10T00:00:00Z')
ON CONFLICT (base_currency, quote_currency, fetched_at) DO NOTHING;
