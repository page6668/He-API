-- Story 9.6 — ASR API（兼容 Whisper）+ Doubao. Adds the per-audio-MINUTE
-- billing dimension (the token-only model_pricing cannot express per-duration
-- cost) + seeds the doubao-asr model + pricing rows.
--
-- Atlas versioned, forward-only (0013/0015 convention; NO inline
-- `-- atlas:up/down` and NO paired `.down.sql` — `atlas migrate down 1` computes
-- the reverse DROP COLUMN + DELETE dynamically per database-bootstrap.md §2).
--
-- ADDITIVE · reversible · non-destructive (BR2-1 / Q-ASR-BILLING RATIFIED):
--   1. ADD COLUMN model_pricing.price_per_minute_audio_usd NUMERIC(10,6) NULL —
--      NULL for every token model (mirrors the dormant per_call_price_usd
--      additive-nullable precedent in 0008). No existing row is rewritten.
--   2. Seed the doubao-asr he_api.models row — the FIRST Chat:false /
--      Transcription:true model (capabilities JSONB mirrors the in-process
--      packages/models-catalogue entry).
--   3. Seed its model_pricing row. Q-ASR-BILLING Amend 1: the token NOT NULL
--      columns STAY NOT NULL — the ASR row carries 0/0 for both token columns
--      (never relax NOT NULL on a money table); the per-minute column is the
--      billed dimension. 0.006/min is a plausible launch rate + the default 10%
--      markup. The PER_MINUTE cost engine (apps/billing-svc ComputeCost) reads
--      price_per_minute_audio_usd ONLY (ignores the 0/0 token columns).
--
-- Money one-SoT: this column feeds usage_ledger via the SAME exactly-once
-- (ledger_key) + atomic-balance-debit transaction as tokens — only the
-- ComputeCost FORMULA branches (project_cost_source_oq5_usage_ledger).
--
-- 0015 is migration HEAD; 0016 is the next version.

ALTER TABLE he_api.model_pricing
    ADD COLUMN price_per_minute_audio_usd NUMERIC(10,6);  -- nullable; NULL for token models

INSERT INTO he_api.models (id, display_name, vendor, capabilities) VALUES
    ('doubao-asr', 'Doubao ASR', 'bytedance',
        '{"chat":false,"streaming":false,"function_calling":false,"vision":false,"json_mode":false,"transcription":true}'::jsonb)
ON CONFLICT (id) DO NOTHING;

INSERT INTO he_api.model_pricing
    (model_id, effective_at,
     upstream_price_per_1k_input_tokens, upstream_price_per_1k_output_tokens,
     markup_percent, price_per_minute_audio_usd) VALUES
    ('doubao-asr', TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.000000, 0.000000, 10.00, 0.006000)
ON CONFLICT (model_id, effective_at) DO NOTHING;
