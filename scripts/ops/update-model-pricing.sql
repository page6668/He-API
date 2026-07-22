-- 模型调价模板 —— 填空后执行。
--
-- ============================================================================
-- 两条必须先读懂的规则
-- ============================================================================
--
-- 1) 【单位是 USD】he_api.model_pricing 的两个价格列是 **USD / 1K tokens**
--    (migration 0007 定义,整条计费链以 USD 为基准,展示时才按 he_api.fx_rates
--    换算成人民币)。而百炼/火山的价目页公布的是**人民币**。直接把人民币数字
--    填进去会让计费整体偏约 7 倍 —— 下面的模板强制你写明汇率并显式换算,
--    就是为了让这一步无法被跳过。
--
-- 2) 【调价是插入,不是覆盖】主键是 (model_id, effective_at)。每次调价插入一行
--    新的 effective_at,旧行保留。网关取「effective_at <= NOW() 中最新的一行」。
--    这样历史账单永远能还原成当时的价格 —— 覆盖会让过去的账单变得无法解释。
--
-- ============================================================================
-- 用法
-- ============================================================================
--
--   1. 去厂商价目页抄下人民币单价。**照抄「元 / 百万 tokens」,不要自己换算** ——
--      厂商现在统一按每百万 tokens 标价,下面的 SQL 会替你除以 1000。
--      注意输入价与输出价是两个不同的数。
--   2. 填下面的 VALUES 列表:模型 id、元/百万 输入价、元/百万 输出价
--   3. 核对 :fx_usd_cny 这个汇率是否仍然合理
--   4. 在集群内执行(跳板机连不到 RDS):
--
--        kubectl -n he-api-staging create configmap pricing-update \
--          --from-file=pricing.sql=scripts/ops/update-model-pricing.sql
--        # 然后用与 migrate-0021 相同形态的 Job 跑它(应用账号即可,纯 INSERT)
--
--   5. 生效:网关 5 分钟内自动刷新快照,无需发版。
--
-- 加价率:markup_percent 默认 10.00,由 he_api.model_pricing 自己承载,
-- 这里填的是**上游成本价**(厂商向我们收的),不是对客户的售价。
-- 网关对外展示与计费时会自动乘上 (1 + markup_percent/100)。

BEGIN;

-- 汇率:填写你据以换算的 USD→CNY 汇率,并在下面的注释里记下来源与日期。
-- 这个值只用于本次换算;它不写入数据库,但会决定所有价格的正确性。
\set fx_usd_cny 7.2   -- 例:2026-07 参考汇率。改成你实际使用的值。

INSERT INTO he_api.model_pricing (
    model_id,
    effective_at,
    upstream_price_per_1k_input_tokens,
    upstream_price_per_1k_output_tokens
)
SELECT
    v.model_id,
    NOW(),                                        -- 立即生效;要预约调价就改成未来时间
    -- 元/百万 → USD/千:先 /1000 换算到每 1K tokens,再 /汇率 换算到 USD。
    -- 两步都在 SQL 里用 NUMERIC 精确完成,不经过任何手算或浮点。
    ROUND(v.cny_per_m_in  / 1000 / :fx_usd_cny, 6),
    ROUND(v.cny_per_m_out / 1000 / :fx_usd_cny, 6)
FROM (VALUES
    -- ┌─ 模型 id ─────────┬─ 元/百万 输入 ─┬─ 元/百万 输出 ─┐
    --   照抄厂商价目页的「元/百万 tokens」,不要自己除 1000。
    --   下面全是占位。不需要调价的行请删掉,或留 0(会被自动跳过)。
    ('qwen3.7-max',        0.0000::numeric,     0.0000::numeric),
    ('qwen3.7-plus',       0.0000,              0.0000),
    ('qwen3.6-flash',      0.0000,              0.0000),
    ('deepseek-v4-pro',    0.0000,              0.0000),
    ('deepseek-v4-flash',  0.0000,              0.0000),
    ('glm-5.2',            0.0000,              0.0000),
    ('kimi-k2.6',          0.0000,              0.0000)
) AS v(model_id, cny_per_m_in, cny_per_m_out)
WHERE v.cny_per_m_in > 0 AND v.cny_per_m_out > 0             -- 未填写的行自动跳过,不会写入 0 价
ON CONFLICT (model_id, effective_at) DO NOTHING;

-- 执行前先看一眼换算结果对不对。确认无误再 COMMIT;不对就 ROLLBACK。
SELECT m.id,
       p.upstream_price_per_1k_input_tokens  AS usd_in,
       p.upstream_price_per_1k_output_tokens AS usd_out,
       p.markup_percent,
       ROUND(p.upstream_price_per_1k_input_tokens * (1 + p.markup_percent/100), 6) AS 对客户单价_in,
       p.effective_at
  FROM he_api.models m
  JOIN he_api.model_pricing p ON p.model_id = m.id
 WHERE m.status = 'active'
   AND p.effective_at > NOW() - INTERVAL '1 minute'
 ORDER BY m.id;

COMMIT;
