// Package repository contains the notification-svc PG access layer.
// Story 2.6 introduces the data_export_requests table (migration 0005).
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx/v5 surface this package needs; satisfied by
// *pgx.Conn, pgx.Tx, and *pgxpool.Pool.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DataExportRequest mirrors he_api.data_export_requests (migration 0005).
// All time fields are TIMESTAMPTZ; nullable values use pointers so the
// caller can distinguish unset vs zero-time.
type DataExportRequest struct {
	ID                  string     // UUID
	UserID              string     // UUID
	Status              string     // pending|processing|completed|failed|expired
	RequestedAt         time.Time
	StartedAt           *time.Time
	CompletedAt         *time.Time
	OSSObjectKey        *string
	SignedURLExpiresAt  *time.Time
	EmailSentAt         *time.Time
	FailureReason       *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ErrNotFound is returned by repository reads when no row matches.
var ErrNotFound = errors.New("data_export_requests: not found")

// DataExportRequestsRepo is the data_export_requests data-access surface.
type DataExportRequestsRepo struct {
	db Querier
}

// NewDataExportRequestsRepo returns a repository bound to the supplied
// Querier (pool or transaction).
func NewDataExportRequestsRepo(db Querier) *DataExportRequestsRepo {
	return &DataExportRequestsRepo{db: db}
}

// FindCurrentInWindow returns the most-recent non-failed row for the user
// inside the idempotency window, or ErrNotFound when none exists. Per
// Story 2.6 AC2 BR-2.5 + TS-CONS-016-failed-excluded (R-4): the status
// filter excludes 'failed' rows so users can retry immediately after a
// failure.
//
// Race-safety: the as-built design delegates atomicity to the Redis Lua
// INCR safety-net in ratelimit.GDPRExportLimiter (Lua script is atomic
// server-side). When two concurrent requests both miss this idempotency
// lookup and both INSERT, exactly one wins the post-INSERT INCR (count=1);
// the loser sees ErrRateLimited and the surrounding PG transaction rolls
// back, removing the orphan row. BR-2.5 specifies `SELECT ... FOR UPDATE`
// on a sentinel row as one acceptable mechanism — the Redis-Lua mechanism
// provides equivalent race coverage with one fewer round-trip. See
// handlers/data_export.go for the canonical wiring + Story 2.6 QA Round 1
// ISSUE-5 for the design-deviation rationale.
func (r *DataExportRequestsRepo) FindCurrentInWindow(ctx context.Context, userID string, window time.Duration) (*DataExportRequest, error) {
	const q = `
		SELECT id, user_id, status, requested_at, started_at, completed_at,
		       oss_object_key, signed_url_expires_at, email_sent_at,
		       failure_reason, created_at, updated_at
		FROM he_api.data_export_requests
		WHERE user_id = $1
		  AND status IN ('pending', 'processing', 'completed')
		  AND requested_at > NOW() - $2::interval
		ORDER BY requested_at DESC
		LIMIT 1
	`
	row := r.db.QueryRow(ctx, q, userID, fmt.Sprintf("%d seconds", int(window.Seconds())))
	return scanOne(row)
}

// FindLatestForUser returns the user's most-recent row (any status) for
// the AC1 BR-1.5 hydration read. Returns ErrNotFound when the user has
// never requested an export.
func (r *DataExportRequestsRepo) FindLatestForUser(ctx context.Context, userID string) (*DataExportRequest, error) {
	const q = `
		SELECT id, user_id, status, requested_at, started_at, completed_at,
		       oss_object_key, signed_url_expires_at, email_sent_at,
		       failure_reason, created_at, updated_at
		FROM he_api.data_export_requests
		WHERE user_id = $1
		ORDER BY requested_at DESC
		LIMIT 1
	`
	row := r.db.QueryRow(ctx, q, userID)
	return scanOne(row)
}

// Insert creates a new pending row and returns the assigned ID + requested_at.
func (r *DataExportRequestsRepo) Insert(ctx context.Context, userID string) (string, time.Time, error) {
	const q = `
		INSERT INTO he_api.data_export_requests (user_id)
		VALUES ($1)
		RETURNING id, requested_at
	`
	var id string
	var requestedAt time.Time
	if err := r.db.QueryRow(ctx, q, userID).Scan(&id, &requestedAt); err != nil {
		return "", time.Time{}, fmt.Errorf("data_export_requests: insert: %w", err)
	}
	return id, requestedAt, nil
}

func scanOne(row pgx.Row) (*DataExportRequest, error) {
	out := &DataExportRequest{}
	err := row.Scan(
		&out.ID, &out.UserID, &out.Status, &out.RequestedAt,
		&out.StartedAt, &out.CompletedAt, &out.OSSObjectKey,
		&out.SignedURLExpiresAt, &out.EmailSentAt, &out.FailureReason,
		&out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("data_export_requests: scan: %w", err)
	}
	return out, nil
}
