-- Story 8.4 — per-Key 内容安全严格度 (Epic-8 DoD "严格度可按 Key 配置").
--
-- Additive, non-destructive, reversible. NOT NULL + DEFAULT backfills every
-- existing row atomically (no data migration). Atlas versioned, forward-only
-- (0006/0012 convention; NO inline `-- atlas:up/down` annotation and NO paired
-- `.down.sql` — `atlas migrate down 1` computes the reverse DROP COLUMN
-- dynamically per database-bootstrap.md §2).
--
-- 0012 is migration HEAD; 0013 is the next version (verified: 0001..0012 head).
--
-- Default posture = 'strict' (OQ-8.4-1, Architect Round-1 APPROVED): every
-- existing key keeps the exact block-all behaviour Stories 8.2/8.3 ship today
-- (zero regression on a live 备案 compliance gate). Relaxation (default/loose)
-- is ALWAYS an explicit, audited, persisted owner opt-in via
-- PATCH /v1/me/keys/{id}. The CHECK constraint is defence-in-depth — the
-- gateway validates the enum BEFORE the RPC, so the CHECK is unreachable via
-- the validated path but still rejects a direct bad DB write.
--
-- Migration safety:
--   - ADDITIVE — 1× ALTER TABLE ADD COLUMN; NO ALTER/DROP of any existing
--     column or constraint.
--   - REVERSIBLE — Atlas dynamic down drops the new column; no data loss.
--   - NON-DESTRUCTIVE — NOT NULL + DEFAULT backfills existing rows in the same
--     statement; no separate data step, no rewrite of unrelated columns.
--   - NO index/FK needed — the column rides the api_keys row already fetched by
--     ValidateApiKey / the config SELECT FOR UPDATE.

ALTER TABLE he_api.api_keys
  ADD COLUMN content_safety_strictness VARCHAR(10) NOT NULL
    DEFAULT 'strict'
    CHECK (content_safety_strictness IN ('strict', 'default', 'loose'));
