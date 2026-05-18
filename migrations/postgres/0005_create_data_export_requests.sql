-- Story 2.6 — create he_api.data_export_requests table (AC3).
--
-- Per docs/architecture/data-models.md §4.1 + AC3 BR-3.1..BR-3.3.
-- Architectural rationale:
--   - Separate table (vs embedding state on users) because the row has its
--     own lifecycle (pending → processing → completed | failed → expired);
--     hot-path 24h idempotency lookup needs a per-user index that would be
--     awkward on the users table.
--   - ON DELETE CASCADE to users(id) so Story 2.7 (account deletion)
--     naturally cleans up export-request history (GDPR right-of-erasure
--     consistency, BR-3.4).
--   - status CHECK constraint allows the 5 values but state-machine
--     transitions are enforced at the application layer (BR-3.5).
--   - failure_reason TEXT is operator-readable (no PII / no signed-url —
--     BR-3.5 / TS-CONS-008).

CREATE TABLE he_api.data_export_requests (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id               UUID NOT NULL REFERENCES he_api.users(id) ON DELETE CASCADE,
    status                VARCHAR(20) NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending','processing','completed','failed','expired')),
    requested_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    oss_object_key        VARCHAR(500),
    signed_url_expires_at TIMESTAMPTZ,
    email_sent_at         TIMESTAMPTZ,
    failure_reason        TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- BR-3.2: covers the 24-hour idempotency lookup (AC2 BR-2.5) AND the
-- "current in-progress export" hydration read for AC1 BR-1.5.
CREATE INDEX idx_data_export_requests_user_recent
    ON he_api.data_export_requests (user_id, requested_at DESC);

-- BR-3.3: partial index supporting the future daily cron that flips
-- `completed` rows whose signed_url_expires_at < NOW() to `expired`
-- (BR-4.7 — deferred to a follow-up Story; the OSS lifecycle rule
-- provides defense-in-depth in the meantime).
CREATE INDEX idx_data_export_requests_status_expiry
    ON he_api.data_export_requests (status, signed_url_expires_at)
    WHERE status = 'completed' AND signed_url_expires_at IS NOT NULL;
