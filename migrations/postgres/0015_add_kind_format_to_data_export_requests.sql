-- Story 9.3 — 历史日志下载 (Epic-9 DoD "90 天历史日志可下载").
--
-- EXTEND he_api.data_export_requests with a `kind` discriminator + an export
-- `format`, so the Story-2.6 GDPR-export state machine / idempotency index /
-- current-export hydration / expiry-cron are REUSED for usage-log exports
-- (Architect Q-TABLE ruling = EXTEND, NOT a sibling table — reuse the lifecycle,
-- the row already carries oss_object_key / signed_url_expires_at / email_sent_at,
-- exactly what a usage-log export needs).
--
-- Atlas versioned, forward-only (0013 convention; NO inline `-- atlas:up/down`
-- and NO paired `.down.sql` — `atlas migrate down 1` computes the reverse
-- DROP COLUMN dynamically per database-bootstrap.md §2).
--
-- 0014 is migration HEAD; 0015 is the next version (verified: 0001..0014 head).
--
-- Back-compat (Architect HIGH condition): every existing row is a 2.6 GDPR
-- export, so `kind` backfills to 'gdpr_full' atomically (NOT NULL + DEFAULT).
-- The 2.6 repo methods (FindCurrentInWindow / FindLatestForUser / Insert) MUST
-- be made kind-aware in the same Story (add `kind='gdpr_full'` to the existing
-- call-sites, `kind='usage_logs'` to the new usage-log paths) or the GDPR
-- /current endpoint would start matching usage-log rows (regression, BR-EX-7).
--
-- `format` is NULLable: GDPR exports have no caller-chosen format (they are a
-- fixed ZIP bundle), so existing rows stay NULL; usage-log exports persist the
-- json|csv choice (BR-EX-2) used by the worker serializer (BR-EX-11).
--
-- Migration safety:
--   - ADDITIVE — 2× ALTER TABLE ADD COLUMN; NO ALTER/DROP of any existing
--     column, constraint, or index.
--   - REVERSIBLE — Atlas dynamic down drops the two new columns; no data loss.
--   - NON-DESTRUCTIVE — `kind` NOT NULL + DEFAULT backfills existing rows in the
--     same statement; `format` is NULLable (no backfill needed).
--   - NO new index — the existing idx_data_export_requests_user_recent
--     (user_id, requested_at DESC) still serves the kind+format-scoped lookup;
--     `kind` / `format` are added to the WHERE clause in the repo (BR-EX-4/7).
--   - The CHECK constraints are defence-in-depth — the gateway + notification-svc
--     validate the enums BEFORE the INSERT, so they are unreachable via the
--     validated path but still reject a direct bad DB write.

ALTER TABLE he_api.data_export_requests
  ADD COLUMN kind VARCHAR(20) NOT NULL
    DEFAULT 'gdpr_full'
    CHECK (kind IN ('gdpr_full', 'usage_logs'));

ALTER TABLE he_api.data_export_requests
  ADD COLUMN format VARCHAR(8)
    CHECK (format IS NULL OR format IN ('json', 'csv'));
