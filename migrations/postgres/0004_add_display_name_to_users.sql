-- Story 2.5 — add display_name column to users table.
--
-- Per docs/architecture/data-models.md §4.1 (canonical schema) — display_name
-- was NOT in the Story 2.2 baseline (0002_create_users.sql) because Story 2.2
-- scope was authentication-only. Story 2.5 introduces it as the user-visible
-- display label on Settings → Profile.
--
-- Notes:
--   - VARCHAR(100) per BR-2.3 (rune limit enforced at the application layer;
--     column bound is defense-in-depth — PG VARCHAR(N) caps at N characters,
--     not bytes, under utf8 encoding).
--   - NULL allowed: distinguishes "never set" from "explicitly cleared" — but
--     application normalises both to NULL (BR-1.6 / BR-2.4).
--   - No index: BR-2.5 disclaims global uniqueness; only ever queried via
--     primary-key user_id lookup (no filter by display_name).

ALTER TABLE he_api.users
    ADD COLUMN display_name VARCHAR(100);
