// data_export.go — notification-svc RequestDataExport + GetCurrentExport
// handlers (Story 2.6 T2.2 / AC2).
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/audit"
	"github.com/he-api/he-api/apps/notification-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/notification-svc/internal/repository"
)

// idempotencyWindowMin / Max bound the proto field at the handler edge.
// Per BR-2.3 the caller defaults to 86400 (24h); we accept 60..604800
// (1 minute .. 7 days) to keep future tuning flexible without a wire break.
const (
	idempotencyWindowMin     = 60
	idempotencyWindowMax     = 7 * 24 * 60 * 60
	defaultIdempotencyWindow = 86400
)

// DataExportDB is the Querier subset the handler needs. *pgxpool.Pool
// satisfies this; tests pass a fake.
type DataExportDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// GDPRExportLimiter is the Redis safety-net contract.
type GDPRExportLimiter interface {
	CheckAndIncr(ctx context.Context, userID string) error
	Reset(ctx context.Context, userID string) error
}

// GDPRPublisher is the Kafka producer contract for gdpr.export.requested.
type GDPRPublisher interface {
	Publish(ctx context.Context, exportID, userID string, requestedAt time.Time) error
}

// DataExportServer wires the dependencies for RequestDataExport +
// GetCurrentExport. It satisfies the subset of the
// notificationv1connect.NotificationServiceHandler interface introduced
// by Story 2.6; the existing SendEmail surface is served by
// NotificationServer (which embeds *DataExportServer in production).
type DataExportServer struct {
	DB       DataExportDB
	Limiter  GDPRExportLimiter
	Producer GDPRPublisher
	Audit    audit.Publisher
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewDataExportServer constructs the handler with sensible defaults
// (slog.Default + time.Now). DB / Limiter / Producer / Audit are
// REQUIRED and panic on nil at construction time.
func NewDataExportServer(
	db DataExportDB,
	limiter GDPRExportLimiter,
	producer GDPRPublisher,
	auditPub audit.Publisher,
	logger *slog.Logger,
) *DataExportServer {
	if db == nil || limiter == nil || producer == nil || auditPub == nil {
		panic("handlers: data export server missing required dependency")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DataExportServer{
		DB:       db,
		Limiter:  limiter,
		Producer: producer,
		Audit:    auditPub,
		Logger:   logger,
		Now:      time.Now,
	}
}

// RequestDataExport — Story 2.6 AC2 BR-2.1..BR-2.9 implementation.
//
// Flow (the "PG transaction with Redis Lua INCR race-safety net" idiom,
// BR-2.5 — the spec also names `SELECT ... FOR UPDATE` on a sentinel row
// as an acceptable mechanism; we use the Redis-Lua variant since it costs
// one fewer round-trip and the atomicity is server-side. See Story 2.6
// QA Round 1 ISSUE-5 rationale.):
//
//  1. Validate user_id (BR-2.4 — gateway already derived from JWT; defense
//     in depth here).
//  2. BEGIN.
//  3. Idempotency lookup — return existing row if within window (BR-2.5;
//     TS-CONS-016-failed-excluded — failed rows do NOT count).
//  4. INSERT new row (RETURNING id, requested_at).
//  5. Redis CheckAndIncr (Lua INCR + EXPIRE, server-atomic); on the race-
//     condition loser path (count > 1), rollback PG + return 429.
//  6. COMMIT.
//  7. Kafka publish gdpr.export.requested with key=user_id; if publish
//     fails post-commit, log + return 200 (the PG row is the source of
//     truth; T8.6 operator runbook flags rows older than 15 min).
//  8. Best-effort audit emit (TS-CONS-005 non-blocking).
//
// Returns:
//   - 200 + RequestDataExportResponse on success (new or idempotent).
//   - InvalidArgument when user_id is missing / malformed.
//   - ResourceExhausted on BR-2.5 race-condition rate-limit hit (gateway
//     translates to 429 + error_code 429_rate_limit_gdpr_export).
//   - Internal on PG / Redis / Kafka transport failure (gateway → 500).
func (s *DataExportServer) RequestDataExport(
	ctx context.Context,
	req *connect.Request[notificationv1.RequestDataExportRequest],
) (*connect.Response[notificationv1.RequestDataExportResponse], error) {
	in := req.Msg
	userID := in.GetUserId()
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request_data_export: user_id required"))
	}
	window := time.Duration(in.GetIdempotencyWindowSeconds()) * time.Second
	if window <= 0 {
		window = defaultIdempotencyWindow * time.Second
	}
	if int(window.Seconds()) < idempotencyWindowMin || int(window.Seconds()) > idempotencyWindowMax {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_data_export: idempotency_window_seconds out of bounds [%d..%d]", idempotencyWindowMin, idempotencyWindowMax))
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_data_export: begin: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	repo := repository.NewDataExportRequestsRepo(tx)

	// Idempotency path — existing row inside the window (per BR-2.5 +
	// TS-CONS-016-failed-excluded). Caller gets 200 + same export_id.
	existing, err := repo.FindCurrentInWindow(ctx, userID, window)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_data_export: lookup: %w", err))
	}
	if existing != nil {
		// No Kafka / audit emission for idempotent hits — the original
		// request already produced them.
		if err := tx.Commit(ctx); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_data_export: commit-idempotent: %w", err))
		}
		committed = true
		return connect.NewResponse(&notificationv1.RequestDataExportResponse{
			ExportId:    existing.ID,
			Status:      existing.Status,
			RequestedAt: timestamppb.New(existing.RequestedAt),
		}), nil
	}

	// New export — INSERT first to get the export_id assigned by gen_random_uuid.
	exportID, requestedAt, err := repo.Insert(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_data_export: insert: %w", err))
	}

	// Redis race-safety net (BR-2.5). On rate-limit, PG transaction is
	// rolled back (no orphan row) and 429 is returned.
	if err := s.Limiter.CheckAndIncr(ctx, userID); err != nil {
		if errors.Is(err, ratelimit.ErrRateLimited) {
			// PG rollback is implicit via the deferred Rollback (committed=false).
			return nil, connect.NewError(connect.CodeResourceExhausted, ratelimit.ErrRateLimited)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_data_export: ratelimit: %w", err))
	}

	// Commit before Kafka publish — BR-2.5: if Kafka fails, the row stays
	// at `pending`. Operator runbook (T8.6) flags rows older than 15
	// minutes via PromQL. This is the lighter-weight outbox pattern.
	if err := tx.Commit(ctx); err != nil {
		// Best-effort undo of the Redis counter so we don't burn the user's
		// 24h window on a transient PG failure.
		_ = s.Limiter.Reset(context.Background(), userID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_data_export: commit: %w", err))
	}
	committed = true

	// Produce gdpr.export.requested AFTER commit.
	if err := s.Producer.Publish(ctx, exportID, userID, requestedAt); err != nil {
		// Row already committed; log + audit + return success (the row is
		// the user-visible truth; cron will detect orphan `pending` rows
		// per T8.6). Per BR-2.5 the response is still 200 in this edge.
		s.Logger.WarnContext(ctx, "gdpr export requested kafka publish failed",
			slog.String("export_id", exportID),
			slog.String("user_id", userID),
			slog.String("error", err.Error()),
		)
	}

	// Best-effort audit emit (TS-CONS-005 — non-blocking).
	audit.PublishBestEffort(ctx, s.Audit, s.Logger, audit.Event{
		EventType: audit.EventGdprExportRequested,
		UserID:    userID,
		Timestamp: s.Now(),
		Success:   true,
		Metadata: map[string]any{
			"export_id": exportID,
			"severity":  audit.SeverityLow,
		},
	})

	return connect.NewResponse(&notificationv1.RequestDataExportResponse{
		ExportId:    exportID,
		Status:      "pending",
		RequestedAt: timestamppb.New(requestedAt),
	}), nil
}

// GetCurrentExport — Story 2.6 AC1 BR-1.5 hydration read. Returns
// has_current=false when the user has never requested an export or
// when their most-recent row is in a terminal state (failed, expired)
// — the CTA-enabled path.
//
// "Current" includes status=completed AND signed_url_expires_at > now
// because the UI shows a "your last export is still available" banner
// in that window (AC1 UI Interaction row 3).
func (s *DataExportServer) GetCurrentExport(
	ctx context.Context,
	req *connect.Request[notificationv1.GetCurrentExportRequest],
) (*connect.Response[notificationv1.GetCurrentExportResponse], error) {
	userID := req.Msg.GetUserId()
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("get_current_export: user_id required"))
	}

	// Use a Pool-backed Tx (read-only) for consistency with the write
	// path's repository contract; in production main.go will satisfy
	// DataExportDB via *pgxpool.Pool, which has Begin.
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get_current_export: begin: %w", err))
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	repo := repository.NewDataExportRequestsRepo(tx)
	row, err := repo.FindLatestForUser(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return connect.NewResponse(&notificationv1.GetCurrentExportResponse{HasCurrent: false}), nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get_current_export: find: %w", err))
	}

	// has_current iff the row is in a state the UI should react to (BR-1.5
	// CTA disable + banners). Failed / expired rows do NOT block the CTA.
	hasCurrent := row.Status == "pending" || row.Status == "processing"
	if row.Status == "completed" && row.SignedURLExpiresAt != nil && row.SignedURLExpiresAt.After(s.Now()) {
		hasCurrent = true
	}

	resp := &notificationv1.GetCurrentExportResponse{
		HasCurrent:  hasCurrent,
		ExportId:    row.ID,
		Status:      row.Status,
		RequestedAt: timestamppb.New(row.RequestedAt),
	}
	if row.SignedURLExpiresAt != nil {
		resp.SignedUrlExpiresAt = timestamppb.New(*row.SignedURLExpiresAt)
	}
	return connect.NewResponse(resp), nil
}

// poolAdapter lets *pgxpool.Pool satisfy DataExportDB without an explicit
// type assertion at call sites. main.go uses PoolAdapter.
type poolAdapter struct{ pool *pgxpool.Pool }

// PoolAdapter wraps a *pgxpool.Pool to satisfy DataExportDB.
func PoolAdapter(pool *pgxpool.Pool) DataExportDB { return &poolAdapter{pool: pool} }

func (p *poolAdapter) Begin(ctx context.Context) (pgx.Tx, error) { return p.pool.Begin(ctx) }
