-- Story 8.5 — 内容安全治理日志（拦截记录）+ 6 个月保留 + 备案材料 (Epic-8 FINALE).
--
-- CREATEs he_api.content_safety_logs, the §9.3 治理日志 sink the persisting
-- Recorder (apps/api-gateway/internal/safetylog) writes one row to per acted-on
-- block (8.2 入参 reject / 8.3 出参 redact / 8.3 stream terminate). The column
-- shapes were pre-chosen in data-models.md to match contentsafety.SafetyEvent
-- (direction VARCHAR(10) / matched_rule VARCHAR(100) / action VARCHAR(20)) so
-- the Recorder binds with no contract change.
--
-- 0013 is migration HEAD; 0014 is the next version (verified: 0001..0013 head).
--
-- Atlas versioned, forward-only (0006/0012/0013 convention; NO inline
-- `-- atlas:up/down` annotation and NO paired `.down.sql` — `atlas migrate down 1`
-- computes the reverse DROP dynamically per database-bootstrap.md §2).
--
-- ── The load-bearing 备案 design point: NO `REFERENCES he_api.users(id)` FK ──
-- §9.3 mandates the governance log SURVIVES a user's GDPR account deletion
-- ("合规优先于个人删除请求"). data_export_requests deliberately uses
-- `ON DELETE CASCADE` (0005) precisely so account deletion tidies it up;
-- content_safety_logs needs the OPPOSITE. OMITTING the FK on user_id is the
-- mechanism: a `DELETE FROM he_api.users` cannot cascade into the compliance
-- log. This is asserted as a HARD test (delete user → rows remain) so a later
-- "tidy up the FK" refactor cannot silently re-introduce a cascade that would
-- breach 备案 retention (Story 8.5 BR-2.2 / AC2).
--
-- Migration safety:
--   - ADDITIVE — brand-new table + 2 indexes; touches NO existing object.
--   - REVERSIBLE — Atlas dynamic down drops the table + both indexes; no data
--     loss (a brand-new table has no backfill).
--   - NON-DESTRUCTIVE — no ALTER/DROP of any existing column or constraint.
--
-- The `strictness` column is the OQ-8.5-7 (Architect APPROVED) deliberate,
-- documented deviation from the data-models pre-declaration: it lets the 备案
-- PDF attribute "blocked under <level>" per-row from SafetyEvent.Strictness
-- (8.4) without a re-derivation/join. data-models.md is updated to match.

CREATE TABLE he_api.content_safety_logs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- NO `REFERENCES he_api.users(id)` — the §9.3 GDPR-erasure exemption
    -- mechanism (BR-2.2). Do NOT add a FK here: it would cascade-purge the
    -- compliance log on account deletion.
    user_id          UUID NOT NULL,
    api_key_id       UUID,
    he_request_id    VARCHAR(50) NOT NULL,
    direction        VARCHAR(10) NOT NULL
                         CHECK (direction IN ('input', 'output')),
    matched_rule     VARCHAR(100),
    action           VARCHAR(20) NOT NULL
                         CHECK (action IN ('blocked', 'warned')),
    -- 8.4 effective per-Key level the match was acted on under (OQ-8.5-7);
    -- '' on a pre-8.4 / no-strictness event. Defence-in-depth CHECK allows the
    -- empty string so a future code path that omits it still writes.
    strictness       VARCHAR(10) NOT NULL DEFAULT ''
                         CHECK (strictness IN ('', 'strict', 'default', 'loose')),
    -- 脱敏的命中内容片段: NEVER raw user text NOR the literal 敏感词 — the matched
    -- span is masked (■×rune-count) and surrounding PII redacted (BR-1.4). NULL
    -- is a valid state (the conservative fallback, BR-1.4).
    excerpt_redacted TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The pre-declared per-user read path (data-models.md).
CREATE INDEX idx_safety_logs_user_time
    ON he_api.content_safety_logs (user_id, created_at DESC);

-- NEW (additive) — backs the AC2 retention sweep
-- (`DELETE ... WHERE created_at < NOW() - INTERVAL '6 months'`) as a bounded
-- range delete rather than a full-table scan (BR-1.1 / BR-2.1).
CREATE INDEX idx_safety_logs_created_at
    ON he_api.content_safety_logs (created_at);
