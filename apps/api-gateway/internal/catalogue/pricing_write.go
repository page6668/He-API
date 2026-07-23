// AD-003 —— 定价写入(specs/admin-pricing-arch.md)。
//
// 这是 model_pricing 表**唯一的运行时写入者**(在此之前只有 migration 写过)。
// 三条不变量落在这里:
//
//  1. 换算不经浮点:入参是「元/百万 tokens」,USD/1K = cny/1000/fx,ROUND 到 6 位,
//     全程在 SQL 里用 NUMERIC 完成 —— Go 只搬字符串。这是 M-1 铁律(money 不走 float)。
//  2. 追加不覆盖:插入新 effective_at 行(= NOW()),旧行保留。PK 是 (model_id,
//     effective_at),历史账单据此永远可还原。
//  3. 只给存在的 active 模型定价:model_id 必须已在 he_api.models 且 status='active',
//     否则拒绝 —— 不给不存在或已下架的模型写价。
package catalogue

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier 是本包写侧需要的最小 pgx 面 —— *pgxpool.Pool 与 pgxmock 都满足
// (与 safetylog.Querier 同范式)。
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PriceInput 是一次调价的原始输入,均为十进制字符串(不经 float64)。
type PriceInput struct {
	ModelID         string
	InputCNYPerMil  string // 元 / 百万 input tokens
	OutputCNYPerMil string // 元 / 百万 output tokens
	FXUSDToCNY      string // 换算用汇率 USD→CNY
}

// PricingWriter 向 he_api.model_pricing 追加定价行。
type PricingWriter struct {
	pool Querier
}

func NewPricingWriter(pool Querier) *PricingWriter { return &PricingWriter{pool: pool} }

// insertSQL —— 换算 + 归属校验都在一条语句里:
//   - 子查询强制 model_id 是 active 模型(否则 SELECT 无行 → INSERT 0 行)。
//   - 价格由 $2/$3(元/百万)/1000/$4(汇率)现算,NUMERIC 精度,ROUND 到 6 位
//     (与列 NUMERIC(10,6) 一致)。
//   - effective_at = NOW():每次调价一行新记录。同一秒重复提交由 PK 去重
//     (ON CONFLICT DO NOTHING),不报错。
const insertSQL = `
INSERT INTO he_api.model_pricing
    (model_id, effective_at, upstream_price_per_1k_input_tokens, upstream_price_per_1k_output_tokens)
SELECT m.id,
       NOW(),
       ROUND($2::numeric / 1000 / $4::numeric, 6),
       ROUND($3::numeric / 1000 / $4::numeric, 6)
  FROM he_api.models m
 WHERE m.id = $1 AND m.status = 'active'
ON CONFLICT (model_id, effective_at) DO NOTHING`

// SetPrice 追加一条定价。返回是否真的写入(false = 模型不存在/非 active,或同秒重复)。
// 入参的数值有效性(>0、可解析)由调用方在 handler 层先校验;这里再用 ::numeric
// 转换,非法字符串会让整条语句报错回滚,不会写半截。
func (w *PricingWriter) SetPrice(ctx context.Context, in PriceInput) (bool, error) {
	if w == nil || w.pool == nil {
		return false, fmt.Errorf("pricing writer: no database pool")
	}
	tag, err := w.pool.Exec(ctx, insertSQL,
		in.ModelID, in.InputCNYPerMil, in.OutputCNYPerMil, in.FXUSDToCNY)
	if err != nil {
		return false, fmt.Errorf("pricing writer: insert: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// LatestFXUSDToCNY 读 he_api.fx_rates 里最新的 USD→CNY 汇率,作为后台的默认值。
// 无行时返回 ("", nil) —— 调用方据此让管理员必须手动填汇率。
func (w *PricingWriter) LatestFXUSDToCNY(ctx context.Context) (string, error) {
	if w == nil || w.pool == nil {
		return "", fmt.Errorf("pricing writer: no database pool")
	}
	var rate string
	err := w.pool.QueryRow(ctx,
		`SELECT rate::text FROM he_api.fx_rates
          WHERE base_currency = 'USD' AND quote_currency = 'CNY'
          ORDER BY fetched_at DESC LIMIT 1`).Scan(&rate)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("pricing writer: fx lookup: %w", err)
	}
	return rate, nil
}
