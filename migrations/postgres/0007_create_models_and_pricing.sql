-- Story 6.2 — create he_api.models + he_api.model_pricing (AC2 BR2-1; Story-6.1
-- Q-C carry-over).
--
-- Per docs/architecture/data-models.md §4.1 (lines 101-119). Atlas versioned
-- mode (file://migrations/postgres) — plain forward-only DDL, mirroring the
-- Story-3.2 0006_create_api_keys.sql convention: NO inline `-- atlas:up/down`
-- annotations and NO paired `.down.sql` file. `atlas migrate down 1` computes
-- the reverse DDL dynamically (DROP TABLE he_api.model_pricing, then
-- he_api.models) per database-bootstrap.md §2.
--
-- Migration safety (BR2-1):
--   - ADDITIVE — CREATE TABLE only; no ALTER/DROP of any existing table.
--   - REVERSIBLE — down drops only the two new tables (FK order: pricing first).
--   - NON-DESTRUCTIVE — no data loss on any pre-existing table.
--
-- The 8-concrete-model + pricing seed lands in the SAME migration (idempotent
-- INSERTs) so cost routing (AC2) has a real backing source in every
-- environment from first boot. The 3 he-router-* virtual catalogue entries are
-- routing directives, NOT routable upstreams (Q-D) — they are intentionally
-- NOT seeded here. `atlas migrate down 1` removes the seed with the tables, so
-- the seed is part of the reversible unit (BLIND-DATA-003 down→up idempotency).

-- 模型与定价 — docs/architecture/data-models.md §4.1
CREATE TABLE he_api.models (
    id                  VARCHAR(100) PRIMARY KEY,             -- 'qwen-max' / 'deepseek-v3'
    display_name        VARCHAR(100) NOT NULL,
    vendor              VARCHAR(50)  NOT NULL,                -- alibaba / deepseek / moonshot / zhipu / bytedance / baidu
    capabilities        JSONB        NOT NULL,                -- {chat:true, vision:true, function_calling:true,...}
    upstream_endpoint   VARCHAR(500),
    upstream_model_id   VARCHAR(100),
    status              VARCHAR(20)  DEFAULT 'active',        -- active / deprecated
    created_at          TIMESTAMPTZ  DEFAULT NOW()
);

CREATE TABLE he_api.model_pricing (
    model_id                            VARCHAR(100) NOT NULL REFERENCES he_api.models(id),
    effective_at                        TIMESTAMPTZ  NOT NULL,
    upstream_price_per_1k_input_tokens  NUMERIC(10,6) NOT NULL,
    upstream_price_per_1k_output_tokens NUMERIC(10,6) NOT NULL,
    markup_percent                      NUMERIC(5,2) NOT NULL DEFAULT 10.00,  -- 5-15%
    PRIMARY KEY (model_id, effective_at)
);

-- Seed: the 8 concrete catalogue models (packages/models-catalogue
-- DefaultRegistry MINUS the 3 he-router-* virtual entries). capabilities
-- mirror the in-process catalogue. Idempotent (ON CONFLICT DO NOTHING) so a
-- down→up cycle is a no-op flap (BLIND-DATA-003).
INSERT INTO he_api.models (id, display_name, vendor, capabilities) VALUES
    ('qwen-max',         'Qwen Max',        'alibaba',   '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true}'::jsonb),
    ('qwen-plus',        'Qwen Plus',       'alibaba',   '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true}'::jsonb),
    ('deepseek-v3',      'DeepSeek V3',     'deepseek',  '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true}'::jsonb),
    ('moonshot-v1-128k', 'Moonshot v1 128k','moonshot',  '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true}'::jsonb),
    ('glm-4',            'GLM-4',           'zhipu',     '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true}'::jsonb),
    ('doubao-pro',       'Doubao Pro',      'bytedance', '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":false}'::jsonb),
    ('doubao-lite',      'Doubao Lite',     'bytedance', '{"chat":true,"streaming":true,"function_calling":false,"vision":false,"json_mode":false}'::jsonb),
    ('ernie-4.0',        'ERNIE 4.0',       'baidu',     '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true}'::jsonb)
ON CONFLICT (id) DO NOTHING;

-- Seed: per-model upstream pricing (USD per 1k tokens), single effective_at row
-- per model. Values are plausible launch defaults; admin-mutable later. The
-- cost strategy ranks by (input + output) ascending (Q-J), so the seed gives a
-- deterministic ordering — doubao-lite is the cheapest concrete model.
INSERT INTO he_api.model_pricing
    (model_id, effective_at, upstream_price_per_1k_input_tokens, upstream_price_per_1k_output_tokens) VALUES
    ('doubao-lite',      TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.000300, 0.000600),
    ('deepseek-v3',      TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.000200, 0.000800),
    ('qwen-plus',        TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.000800, 0.002000),
    ('doubao-pro',       TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.001100, 0.002800),
    ('moonshot-v1-128k', TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.006000, 0.006000),
    ('glm-4',            TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.010000, 0.010000),
    ('qwen-max',         TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.004000, 0.012000),
    ('ernie-4.0',        TIMESTAMPTZ '2026-01-01 00:00:00+00', 0.012000, 0.012000)
ON CONFLICT (model_id, effective_at) DO NOTHING;
