-- Story 9.7 — TTS API（兼容 OpenAI Audio Speech）+ Doubao. Adds the
-- per-INPUT-CHARACTER billing dimension (neither the token nor the per-minute
-- (9.6) model_pricing columns can express per-character cost) + seeds the
-- doubao-tts model + pricing rows.
--
-- Atlas versioned, forward-only (0013/0015/0016 convention; NO inline
-- `-- atlas:up/down` and NO paired `.down.sql` — `atlas migrate down 1` computes
-- the reverse DROP COLUMN + DELETE dynamically per database-bootstrap.md §2).
--
-- ADDITIVE · reversible · non-destructive (BR2-1 / Q-TTS-BILLING RATIFIED):
--   1. ADD COLUMN model_pricing.price_per_1k_chars_audio_usd NUMERIC(10,6) NULL —
--      NULL for every token + ASR model (mirrors 0016's nullable
--      price_per_minute_audio_usd). No existing row is rewritten.
--   2. Seed the doubao-tts he_api.models row — the FIRST Speech:true model
--      (capabilities JSONB mirrors the in-process packages/models-catalogue
--      entry: chat:false / transcription:false / speech:true).
--   3. Seed its model_pricing row. The token NOT NULL columns STAY NOT NULL — the
--      TTS row carries 0/0 for both token columns (never relax NOT NULL on a
--      money table); the per-1k-chars column is the billed dimension. 0.015/1k
--      chars is a plausible launch rate + the default 10% markup. The
--      PER_CHARACTER cost engine (apps/billing-svc ComputeCost) reads
--      price_per_1k_chars_audio_usd ONLY (ignores the 0/0 token + NULL per-minute
--      columns).
--
-- Money one-SoT: this column feeds usage_ledger via the SAME exactly-once
-- (ledger_key) + atomic-balance-debit transaction as tokens/ASR — only the
-- ComputeCost FORMULA branches (project_cost_source_oq5_usage_ledger).
--
-- 0016 is migration HEAD; 0017 is the next version.

ALTER TABLE he_api.model_pricing
    ADD COLUMN price_per_1k_chars_audio_usd NUMERIC(10,6);  -- nullable; NULL for token + ASR models

INSERT INTO he_api.models (id, display_name, vendor, capabilities) VALUES
    ('doubao-tts', 'Doubao TTS', 'bytedance',
        '{"chat":false,"streaming":false,"function_calling":false,"vision":false,"json_mode":false,"transcription":false,"speech":true}'::jsonb)
ON CONFLICT (id) DO NOTHING;

INSERT INTO he_api.model_pricing
    (model_id, effective_at,
     upstream_price_per_1k_input_tokens, upstream_price_per_1k_output_tokens,
     markup_percent, price_per_1k_chars_audio_usd) VALUES
    ('doubao-tts', TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.000000, 0.000000, 10.00, 0.015000)
ON CONFLICT (model_id, effective_at) DO NOTHING;
