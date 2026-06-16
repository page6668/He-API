-- Story 2.7 — account deletion (30-day grace) physical-erasure bookkeeping.
-- Adds the two timestamp columns the account-deletion-sweeper (AC6) stamps when
-- it soft-deletes (anonymizes) a due users row, plus a partial index that bounds
-- the daily sweeper scan to only pending rows.
--
-- Atlas versioned, forward-only (0013/0015/0016/0017 convention; NO inline
-- `-- atlas:up/down` and NO paired `.down.sql` — `atlas migrate down 1` computes
-- the reverse DROP COLUMN + DROP INDEX dynamically per database-bootstrap.md §2).
--
-- ADDITIVE · reversible · non-destructive (AC5 BR-5.1/5.2/5.6):
--   1. ADD COLUMN users.deleted_at    TIMESTAMPTZ NULL — set by the sweeper at
--      physical erasure (AC6 step 1). NULL for every non-deleted row.
--   2. ADD COLUMN users.anonymized_at TIMESTAMPTZ NULL — set when the cross-store
--      PII scrub (PG + ClickHouse + OSS + Stripe-detach) completes; a row with
--      status='deleted' AND anonymized_at IS NULL is re-swept for the
--      CH/OSS/Stripe steps only (AC6 BR-6.6 crash-safety reconcile).
--   3. CREATE INDEX (partial) idx_users_pending_deletion_due — supports
--      `WHERE status='pending_deletion' AND pending_deletion_at <= NOW()`.
--
-- Both columns are nullable with NO default → backfill-free, non-locking
-- metadata-only change on PG16. The grace-window marker users.pending_deletion_at
-- already exists (0002:25) and is NOT re-added (BR-5.3). 'deleted' is a NEW
-- app-layer terminal status value — the status set stays application-validated
-- (no CHECK constraint, consistent with 0002 — BR-5.4); no DDL needed for it.
--
-- Down (computed by `atlas migrate down 1`):
--   DROP INDEX he_api.idx_users_pending_deletion_due;
--   ALTER TABLE he_api.users DROP COLUMN anonymized_at, DROP COLUMN deleted_at;

ALTER TABLE he_api.users
  ADD COLUMN deleted_at    TIMESTAMPTZ,  -- set by sweeper at physical erasure (AC6)
  ADD COLUMN anonymized_at TIMESTAMPTZ;  -- set when PII anonymization completes (AC6)

-- Bounds the daily sweeper scan (AC6 BR-6.2) to the (typically tiny) set of
-- rows still inside or past their grace window — the index only carries
-- status='pending_deletion' rows, so it stays small and the scan never touches
-- the (vast majority) active/deleted rows.
CREATE INDEX idx_users_pending_deletion_due
  ON he_api.users (pending_deletion_at)
  WHERE status = 'pending_deletion';
