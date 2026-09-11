// AD-006 — 模型目录管理写入（新增 / 下架）。
//
// 只做两件事：
//
//	CreateModel   — 插入一条新模型（id 必须唯一，默认 status=pending，不对外可见）
//	DeprecateModel — 将 status 改为 deprecated（对外隐藏，保留行）
//
// 读取用已有的 catalogue.Store.Load（只取 active + priced）。管理后台另需要看到
// pending / deprecated 的行，故这里再提供 ListAllModels（不含定价）。
package catalogue

import (
	"context"
	"fmt"
	"time"
)

// Writer 向 he_api.models 写入（新增 / 下架 / 列表）。
//
// pool 复用本包写侧的 Querier 接口（见 pricing_write.go：Exec + QueryRow + Query，
// *pgxpool.Pool 与 pgxmock 都满足）。
type Writer struct {
	pool Querier
}

func NewWriter(pool Querier) *Writer { return &Writer{pool: pool} }

// createModelSQL — 插入一条新模型，默认 status=pending（不可见）。
// capabilities 传 JSON 字符串（由 handler 解析验证后传入）。
const createModelSQL = `
INSERT INTO he_api.models
    (id, display_name, vendor, capabilities, upstream_endpoint, upstream_model_id, status)
VALUES ($1, $2, $3, $4::jsonb, NULLIF($5, ''), NULLIF($6, ''), 'pending')`

// CreateModel 插入一条新模型。返回是否成功（id 冲突 → false）。
func (w *Writer) CreateModel(ctx context.Context, id, displayName, vendor, capabilities, upstreamModelID string) (bool, error) {
	if w == nil || w.pool == nil {
		return false, fmt.Errorf("catalogue writer: no database pool")
	}
	tag, err := w.pool.Exec(ctx, createModelSQL, id, displayName, vendor, capabilities, "", upstreamModelID)
	if err != nil {
		return false, fmt.Errorf("catalogue writer: insert: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// deprecateModelSQL — 将指定模型的 status 改为 deprecated。
const deprecateModelSQL = `
UPDATE he_api.models
   SET status = 'deprecated', updated_at = NOW()
 WHERE id = $1 AND status != 'deprecated'`

// DeprecateModel 将模型下架。返回是否真的做了更新（id 不存在 → false）。
func (w *Writer) DeprecateModel(ctx context.Context, modelID string) (bool, error) {
	if w == nil || w.pool == nil {
		return false, fmt.Errorf("catalogue writer: no database pool")
	}
	tag, err := w.pool.Exec(ctx, deprecateModelSQL, modelID)
	if err != nil {
		return false, fmt.Errorf("catalogue writer: deprecate: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// listAllModelsSQL — 查全部模型（含 deprecated/pending），不含定价。
const listAllModelsSQL = `
SELECT id, display_name, vendor, capabilities, upstream_endpoint,
       COALESCE(upstream_model_id, ''), status, created_at, updated_at
  FROM he_api.models
 ORDER BY vendor, id`

// ModelRow 是 ListAllModels 的一行。CreatedAt / UpdatedAt 为 RFC3339 字符串
// （与 providers / me 等接口一致），可直接透传给前端；UpdatedAt 为空时省略。
type ModelRow struct {
	ID               string
	DisplayName      string
	Vendor           string
	CapabilitiesJSON string
	UpstreamEndpoint *string
	UpstreamModelID  string
	Status           string
	CreatedAt        string
	UpdatedAt        *string
}

// ListAllModels 返回所有模型（含 deprecated/pending）。用于管理后台列表。
//
// 注意：created_at / updated_at 是 TIMESTAMPTZ。pgx 默认走二进制协议，二进制格式下
// 时间戳无法直接 Scan 进 string（scanPlanFail），故这里先 Scan 进 time.Time/*time.Time，
// 再格式化为 RFC3339 —— 与 internal/providers/store.go 的做法一致。
func (w *Writer) ListAllModels(ctx context.Context) ([]ModelRow, error) {
	if w == nil || w.pool == nil {
		return nil, fmt.Errorf("catalogue writer: no database pool")
	}
	rows, err := w.pool.Query(ctx, listAllModelsSQL)
	if err != nil {
		return nil, fmt.Errorf("catalogue writer: list all: %w", err)
	}
	defer rows.Close()
	var out []ModelRow
	for rows.Next() {
		var m ModelRow
		var createdAt *time.Time
		var updatedAt *time.Time
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.Vendor, &m.CapabilitiesJSON,
			&m.UpstreamEndpoint, &m.UpstreamModelID, &m.Status, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("catalogue writer: scan: %w", err)
		}
		if createdAt != nil {
			m.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		}
		m.UpdatedAt = isoOrNil(updatedAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

// isoOrNil 把可空时间转成 RFC3339 字符串指针（NULL → nil）。
func isoOrNil(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
