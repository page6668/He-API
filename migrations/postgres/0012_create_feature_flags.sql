-- Story 7.8 — 订阅档 + Beta 模式开关: realise he_api.feature_flags (the global
-- Beta-mode / Feature-Flag cold-start SoT) and seed the `beta_mode` row.
--
-- 0011 is migration HEAD; 0012 is the correct next version (Q-MIGNUM, Architect
-- CONFIRM — HEAD=0011 verified). The table is pre-DEFINED in data-models §4.1
-- ("Beta 模式 / Feature Flag (Unleash 备份；冷启动数据)") but was NEVER migrated
-- (no 0001-0011 creates it); 0012 realises it verbatim — parity with how Story
-- 7.3 realised the pre-defined subscriptions/recharge_orders tables.
--
-- Per Q-PLAN-CATALOG (Architect RATIFY = code catalogue packages/plan-catalogue,
-- NO plan_entitlements PG table), this migration is feature_flags-ONLY — the
-- filename drops the SM-draft `_and_plan_entitlements` suffix.
--
-- Atlas versioned mode (file://migrations/postgres) — plain forward-only DDL,
-- mirroring 0008/0009/0010/0011: NO inline `-- atlas:up/down` annotations and NO
-- paired `.down.sql` file. `atlas migrate down 1` computes the reverse DDL
-- dynamically (DROP TABLE he_api.feature_flags).
--
-- Migration safety:
--   - ADDITIVE — 1× CREATE TABLE + 1 idempotent seed; NO ALTER/DROP of any
--     existing object. The pre-existing he_api.subscriptions (0010) is UNTOUCHED
--     (its opaque `plan` string is already the tier binding; 7.8 adds tier
--     SEMANTICS in code, not schema).
--   - REVERSIBLE — Atlas dynamic down drops the new table; no data loss on any
--     pre-existing table.
--   - NON-DESTRUCTIVE — the table is empty save one seed row; the seed is
--     idempotent (ON CONFLICT (key) DO NOTHING) so a re-run / backoff inserts
--     zero additional rows (7.2 fx bootstrap-seed precedent).

-- Beta 模式 / Feature Flag — docs/architecture/data-models.md §4.1 (pre-defined,
-- Q-BETA-MIGRATION RATIFY: realise verbatim). The PG row is the COLD-START SoT a
-- booting gateway pod reads; Redis `flag:beta_mode` (§4.3) is the runtime read;
-- Unleash (tech-stack §2.1) is the live push. PG remains authoritative; Redis +
-- Unleash are derived runtime mirrors. `variants` (灰度 cohort config) is
-- wired-ready but cohort targeting is DEFERRED to Epic 6.4 (Q-BETA-SCOPE: 7.8 is
-- GLOBAL-only).
CREATE TABLE he_api.feature_flags (
    key         VARCHAR(100) PRIMARY KEY,
    enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    variants    JSONB,                              -- 灰度配置 (wired-ready; cohort targeting deferred — Q-BETA-SCOPE)
    description TEXT,
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Idempotent cold-start seed (Q-BETA-MIGRATION): give every booting pod a
-- deterministic `beta_mode=false` default BEFORE Unleash connects. ON CONFLICT
-- (key) DO NOTHING makes a re-applied migration / retry a zero-row no-op.
INSERT INTO he_api.feature_flags (key, enabled, variants, description, updated_at)
VALUES ('beta_mode', FALSE, NULL, 'Global Beta sandbox switch — Story 7.8 (Unleash mirror; PG cold-start SoT)', NOW())
ON CONFLICT (key) DO NOTHING;
