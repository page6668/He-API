// usage_log_export.go — notification-svc RequestUsageLogExport +
// GetCurrentUsageLogExport handlers (Story 9.3 AC1). Implements
// usagelogv1connect.UsageLogExportServiceHandler.
//
// Mirrors data_export.go (the 2.6 GDPR pipeline) but:
//   - kind='usage_logs' rows (migration 0015);
//   - idempotency scoped by (user_id, kind, format) — json/csv independent
//     (BR-EX-4);
//   - a SEPARATE rate-limit key namespace (BR-EX-5) so usage-log exports do
//     not consume the GDPR-export quota;
//   - the published event carries `format` + the resolved [range_start,
//     range_end] window the analytics-svc worker needs (BR-EX-9).
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/audit"
	"github.com/he-api/he-api/apps/notification-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/notification-svc/internal/repository"
)

// EventUsageLogExportRequested is the audit event type for AC1 (BR-EX-16).
const EventUsageLogExportRequested = audit.EventType("usage_log.export.requested")

// maxUsageLogRangeDays bounds the dump window (BR-EX-3) — matches the
// request_logs 90d TTL. Defense-in-depth: the gateway already validates.
const maxUsageLogRangeDays = 90

// UsageLogExportLimiter is the Redis safety-net contract (BR-EX-5).
type UsageLogExportLimiter interface {
	CheckAndIncr(ctx context.Context, userID string) error
	Reset(ctx context.Context, userID string) error
}

// UsageLogExportPublisher is the Kafka producer contract for
// usage.log.export.requested (BR-EX-9).
type UsageLogExportPublisher interface {
	Publish(ctx context.Context, exportID, userID, format string, rangeStart, rangeEnd time.Time) error
}

// UsageLogExportServer wires the dependencies for the two usage-log RPCs.
// DataExportDB is the same pgx Begin surface used by the 2.6 DataExportServer.
type UsageLogExportServer struct {
	DB       DataExportDB
	Limiter  UsageLogExportLimiter
	Producer UsageLogExportPublisher
	Audit    audit.Publisher
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewUsageLogExportServer constructs the handler. DB / Limiter / Producer /
// Audit are REQUIRED and panic on nil.
func NewUsageLogExportServer(
	db DataExportDB,
	limiter UsageLogExportLimiter,
	producer UsageLogExportPublisher,
	auditPub audit.Publisher,
	logger *slog.Logger,
) *UsageLogExportServer {
	if db == nil || limiter == nil || producer == nil || auditPub == nil {
		panic("handlers: usage-log export server missing required dependency")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &UsageLogExportServer{
		DB:       db,
		Limiter:  limiter,
		Producer: producer,
		Audit:    auditPub,
		Logger:   logger,
		Now:      time.Now,
	}
}

func validUsageLogFormat(f string) bool { return f == "json" || f == "csv" }

// resolveRange returns the dump window. The gateway resolves range_days/start/
// end → [start,end] and passes Timestamps; when both are zero we default to the
// last 90 days. Defense-in-depth clamps the span to ≤90d (BR-EX-3).
func (s *UsageLogExportServer) resolveRange(start, end *timestamppb.Timestamp) (time.Time, time.Time, error) {
	now := s.Now().UTC()
	var rs, re time.Time
	if end != nil && end.IsValid() && !end.AsTime().IsZero() {
		re = end.AsTime().UTC()
	} else {
		re = now
	}
	if start != nil && start.IsValid() && !start.AsTime().IsZero() {
		rs = start.AsTime().UTC()
	} else {
		rs = re.AddDate(0, 0, -maxUsageLogRangeDays)
	}
	if !re.After(rs) {
		return time.Time{}, time.Time{}, errors.New("range end must be after start")
	}
	if re.Sub(rs) > time.Duration(maxUsageLogRangeDays)*24*time.Hour+time.Minute {
		return time.Time{}, time.Time{}, fmt.Errorf("range span exceeds %d days", maxUsageLogRangeDays)
	}
	return rs, re, nil
}

// RequestUsageLogExport — Story 9.3 AC1 (BR-EX-1..BR-EX-9). Same transaction +
// Redis-Lua race-safety idiom as the 2.6 GDPR path.
func (s *UsageLogExportServer) RequestUsageLogExport(
	ctx context.Context,
	req *connect.Request[usagelogv1.RequestUsageLogExportRequest],
) (*connect.Response[usagelogv1.RequestUsageLogExportResponse], error) {
	in := req.Msg
	userID := in.GetUserId()
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request_usage_log_export: user_id required"))
	}
	format := in.GetFormat()
	if !validUsageLogFormat(format) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request_usage_log_export: format must be json or csv"))
	}
	rangeStart, rangeEnd, err := s.resolveRange(in.GetRangeStart(), in.GetRangeEnd())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_usage_log_export: %w", err))
	}

	window := time.Duration(in.GetIdempotencyWindowSeconds()) * time.Second
	if window <= 0 {
		window = defaultIdempotencyWindow * time.Second
	}
	if int(window.Seconds()) < idempotencyWindowMin || int(window.Seconds()) > idempotencyWindowMax {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_usage_log_export: idempotency_window_seconds out of bounds [%d..%d]", idempotencyWindowMin, idempotencyWindowMax))
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_usage_log_export: begin: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	repo := repository.NewDataExportRequestsRepo(tx)

	// Idempotency — existing non-failed (kind=usage_logs, format) row in window.
	existing, err := repo.FindCurrentUsageLogInWindow(ctx, userID, format, window)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_usage_log_export: lookup: %w", err))
	}
	if existing != nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_usage_log_export: commit-idempotent: %w", err))
		}
		committed = true
		return connect.NewResponse(&usagelogv1.RequestUsageLogExportResponse{
			ExportId:    existing.ID,
			Status:      existing.Status,
			Format:      derefFormat(existing.Format, format),
			RequestedAt: timestamppb.New(existing.RequestedAt),
		}), nil
	}

	exportID, requestedAt, err := repo.InsertUsageLog(ctx, userID, format)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_usage_log_export: insert: %w", err))
	}

	// Redis race-safety net (BR-EX-5, separate namespace).
	if err := s.Limiter.CheckAndIncr(ctx, userID); err != nil {
		if errors.Is(err, ratelimit.ErrRateLimited) {
			return nil, connect.NewError(connect.CodeResourceExhausted, ratelimit.ErrRateLimited)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_usage_log_export: ratelimit: %w", err))
	}

	if err := tx.Commit(ctx); err != nil {
		_ = s.Limiter.Reset(context.Background(), userID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("request_usage_log_export: commit: %w", err))
	}
	committed = true

	// Publish AFTER commit (BR-EX-9). Row is the source of truth; a publish
	// failure leaves it at `pending` (redrive-safe), still returns 200.
	if err := s.Producer.Publish(ctx, exportID, userID, format, rangeStart, rangeEnd); err != nil {
		s.Logger.WarnContext(ctx, "usage-log export kafka publish failed",
			slog.String("export_id", exportID),
			slog.String("user_id", userID),
			slog.String("error", err.Error()),
		)
	}

	audit.PublishBestEffort(ctx, s.Audit, s.Logger, audit.Event{
		EventType: EventUsageLogExportRequested,
		UserID:    userID,
		Timestamp: s.Now(),
		Success:   true,
		Metadata: map[string]any{
			"export_id": exportID,
			"format":    format,
			"severity":  audit.SeverityLow,
		},
	})

	return connect.NewResponse(&usagelogv1.RequestUsageLogExportResponse{
		ExportId:    exportID,
		Status:      "pending",
		Format:      format,
		RequestedAt: timestamppb.New(requestedAt),
	}), nil
}

// GetCurrentUsageLogExport — Story 9.3 AC1 BR-EX-7 kind-scoped hydration read.
func (s *UsageLogExportServer) GetCurrentUsageLogExport(
	ctx context.Context,
	req *connect.Request[usagelogv1.GetCurrentUsageLogExportRequest],
) (*connect.Response[usagelogv1.GetCurrentUsageLogExportResponse], error) {
	userID := req.Msg.GetUserId()
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("get_current_usage_log_export: user_id required"))
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get_current_usage_log_export: begin: %w", err))
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	repo := repository.NewDataExportRequestsRepo(tx)
	row, err := repo.FindLatestUsageLogForUser(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return connect.NewResponse(&usagelogv1.GetCurrentUsageLogExportResponse{HasCurrent: false}), nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get_current_usage_log_export: find: %w", err))
	}

	hasCurrent := row.Status == "pending" || row.Status == "processing"
	if row.Status == "completed" && row.SignedURLExpiresAt != nil && row.SignedURLExpiresAt.After(s.Now()) {
		hasCurrent = true
	}

	resp := &usagelogv1.GetCurrentUsageLogExportResponse{
		HasCurrent:  hasCurrent,
		ExportId:    row.ID,
		Status:      row.Status,
		Format:      derefFormat(row.Format, ""),
		RequestedAt: timestamppb.New(row.RequestedAt),
	}
	if row.SignedURLExpiresAt != nil {
		resp.SignedUrlExpiresAt = timestamppb.New(*row.SignedURLExpiresAt)
	}
	return connect.NewResponse(resp), nil
}

func derefFormat(f *string, fallback string) string {
	if f != nil {
		return *f
	}
	return fallback
}
