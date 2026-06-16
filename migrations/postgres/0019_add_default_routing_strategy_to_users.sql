-- Story 6.5 — account-level default routing strategy (the user-facing capstone of
-- Epic 6). Persists the user's chosen routing strategy so chat requests that name
-- a concrete model with no per-request directive route by the user's preference
-- instead of bare STRATEGY_DEFAULT passthrough.
--
-- Atlas versioned, forward-only (0013/0015/0016/0017/0018 convention; NO inline
-- `-- atlas:up/down` and NO paired `.down.sql` — `atlas migrate down 1` computes
-- the reverse DROP COLUMN dynamically per database-bootstrap.md §2).
--
-- ADDITIVE · reversible · non-destructive (BR2-1):
--   ADD COLUMN users.default_routing_strategy VARCHAR(20) NULL — lives alongside
--   locale/timezone (0002) as a per-user preference. NULL = "no default" =
--   today's STRATEGY_DEFAULT passthrough, so existing rows need NO backfill and
--   behaviour is unchanged for everyone until they opt in (Q-B / Q-D / DATA-001).
--
-- Nullable with NO default → backfill-free, non-locking metadata-only change on
-- PG16. NO CHECK constraint — the enum {quality,cost,latency} is application-
-- validated in auth-svc UpdateProfile (where locale/timezone are already
-- validated), keeping a single validation home and avoiding a migration-coupled
-- enum (Q-D / Dev Notes "Database Design").
--
-- Down (computed by `atlas migrate down 1`):
--   ALTER TABLE he_api.users DROP COLUMN default_routing_strategy;

ALTER TABLE he_api.users
  ADD COLUMN default_routing_strategy VARCHAR(20);  -- NULL = no default (Q-D)
