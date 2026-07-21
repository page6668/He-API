-- AD-002 — 模型目录以数据库为权威,上游同步只做"发现"。
-- 详见 specs/model-catalogue-arch.md 与 knowledge/registry/db.yaml。
--
-- 背景:0007 建好 he_api.models / model_pricing 后从未被读取,网关一直用编译进
-- Go 的 DefaultRegistry。2026-07 实测那批 id 已被上游全部下线,线上调用一律
-- 403 Model.AccessDenied 且静默无告警。本迁移把 DB 变成权威源。
--
-- ADDITIVE ONLY:仅 ALTER ... ADD / UPDATE 既有行 / CREATE INDEX。
-- 不 DROP 任何列或表(usage_ledger 对 models 有外键,历史账单需可追溯)。

-- 1) status 取值扩展为 active | pending | deprecated。
--    列本就是 VARCHAR(20),此处补 CHECK 让"未定义状态"无法写入。
--    pending = 同步任务发现的新模型,待人工补定价与能力,**不对外可见、不可计费**。
--    deprecated = 上游已下线,对外隐藏但保留行(永不物理删除)。
ALTER TABLE he_api.models
    DROP CONSTRAINT IF EXISTS models_status_check;
ALTER TABLE he_api.models
    ADD CONSTRAINT models_status_check
    CHECK (status IN ('active', 'pending', 'deprecated'));

-- 2) 同步任务的审计字段:最后一次在上游清单中出现的时间。
--    NULL = 从未被同步任务见过(0007 的人工 seed 行)。
ALTER TABLE he_api.models
    ADD COLUMN IF NOT EXISTS last_seen_upstream_at TIMESTAMPTZ;
ALTER TABLE he_api.models
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- 3) 网关按 status 过滤读取(只取 active),建索引避免全表扫。
CREATE INDEX IF NOT EXISTS idx_models_status ON he_api.models (status);

-- 4) 把 0007 seed 的过时模型标记为 deprecated —— 它们在上游已不存在,
--    继续对外展示等于欺骗调用方(实测调用返回 403)。保留行以维持外键与账单可追溯。
UPDATE he_api.models
   SET status = 'deprecated', updated_at = NOW()
 WHERE id IN ('qwen-max', 'qwen-plus', 'deepseek-v3', 'moonshot-v1-128k',
              'glm-4', 'doubao-pro', 'doubao-lite', 'ernie-4.0')
   AND status <> 'deprecated';

-- 5) 上架当前真实可用的模型(id 取自百炼 /compatible-mode/v1/models 实测清单,
--    2026-07-21 核验)。upstream_model_id 与 id 相同则留空,由适配器原样透传。
--    注:百炼单一入口即可直连多厂商(DeepSeek V4 / GLM / Kimi),故 vendor 记真实
--    厂商归属,而路由统一走 qwen 适配器(它指向 dashscope 兼容端点)。
INSERT INTO he_api.models (id, display_name, vendor, capabilities, status, last_seen_upstream_at) VALUES
    ('qwen3.7-max',      'Qwen3.7 Max',      'alibaba',  '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW()),
    ('qwen3.7-plus',     'Qwen3.7 Plus',     'alibaba',  '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW()),
    ('qwen3.6-flash',    'Qwen3.6 Flash',    'alibaba',  '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW()),
    ('deepseek-v4-pro',  'DeepSeek V4 Pro',  'deepseek', '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW()),
    ('deepseek-v4-flash','DeepSeek V4 Flash','deepseek', '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW()),
    ('glm-5.2',          'GLM-5.2',          'zhipu',    '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW()),
    ('kimi-k2.6',        'Kimi K2.6',        'moonshot', '{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":131072,"max_output_tokens":8192}'::jsonb, 'active', NOW())
ON CONFLICT (id) DO UPDATE
    SET display_name = EXCLUDED.display_name,
        vendor       = EXCLUDED.vendor,
        capabilities = EXCLUDED.capabilities,
        status       = EXCLUDED.status,
        updated_at   = NOW();

-- 6) 定价 —— AD-002 不变量:**只有 active 且有生效定价的模型才对外可见可计费**。
--    上游 /v1/models 不返回价格,故价格只能人工维护。下列为占位单价(元/1K token),
--    上线真实计费前必须由运营按厂商实际价目表核准。
INSERT INTO he_api.model_pricing
    (model_id, effective_at, upstream_price_per_1k_input_tokens, upstream_price_per_1k_output_tokens) VALUES
    ('qwen3.7-max',       TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.004000, 0.012000),
    ('qwen3.7-plus',      TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.000800, 0.002000),
    ('qwen3.6-flash',     TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.000300, 0.000600),
    ('deepseek-v4-pro',   TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.000400, 0.001600),
    ('deepseek-v4-flash', TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.000200, 0.000800),
    ('glm-5.2',           TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.002000, 0.006000),
    ('kimi-k2.6',         TIMESTAMPTZ '2026-07-01 00:00:00+00', 0.004000, 0.004000)
ON CONFLICT (model_id, effective_at) DO NOTHING;
