// Story 9.3 — usage-log export repository tests.
//
//   - 9.3-UNIT (kind-scope, Architect HIGH): the 2.6 GDPR methods query
//     `kind = 'gdpr_full'`; the new usage-log methods query
//     `kind = 'usage_logs'` — the two never read each other's rows.
//   - 9.3-INT-002 (idempotency): FindCurrentUsageLogInWindow is scoped by
//     (user_id, format) so json/csv are independent.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/notification-svc/internal/repository"
)

// 9.3-UNIT — FindCurrentUsageLogInWindow scans the 13-col projection, is scoped
// to kind='usage_logs' + the format arg, and binds format as $2 + window as $3.
func TestFindCurrentUsageLogInWindow_Scoped(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC().Add(-2 * time.Hour)
	format := "csv"
	rows := mock.NewRows([]string{
		"id", "user_id", "status", "format", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	}).AddRow(
		"ul-1", "u-1", "processing", &format, requestedAt, (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), requestedAt, requestedAt,
	)
	// The regex REQUIRES kind = 'usage_logs' AND format = $2 — a regression
	// guard: dropping the kind/format scope fails this expectation.
	mock.ExpectQuery(`kind = 'usage_logs'[\s\S]*format = \$2`).
		WithArgs("u-1", "csv", "86400 seconds").
		WillReturnRows(rows)

	repo := repository.NewDataExportRequestsRepo(mock)
	got, err := repo.FindCurrentUsageLogInWindow(context.Background(), "u-1", "csv", 24*time.Hour)
	if err != nil {
		t.Fatalf("FindCurrentUsageLogInWindow: %v", err)
	}
	if got.Kind != "usage_logs" || got.Format == nil || *got.Format != "csv" {
		t.Errorf("kind/format mismatch: %+v (format=%v)", got, got.Format)
	}
	if got.ID != "ul-1" || got.Status != "processing" {
		t.Errorf("scan mismatch: %+v", got)
	}
}

// 9.3-UNIT — FindLatestUsageLogForUser is kind-scoped to usage_logs (any format).
func TestFindLatestUsageLogForUser_KindScoped(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC().Add(-30 * time.Minute)
	format := "json"
	rows := mock.NewRows([]string{
		"id", "user_id", "status", "format", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	}).AddRow(
		"ul-2", "u-1", "completed", &format, requestedAt, (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), requestedAt, requestedAt,
	)
	mock.ExpectQuery(`kind = 'usage_logs'`).
		WithArgs("u-1").
		WillReturnRows(rows)

	repo := repository.NewDataExportRequestsRepo(mock)
	got, err := repo.FindLatestUsageLogForUser(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("FindLatestUsageLogForUser: %v", err)
	}
	if got.Kind != "usage_logs" || got.Status != "completed" {
		t.Errorf("mismatch: %+v", got)
	}
}

// 9.3-UNIT — InsertUsageLog writes kind='usage_logs' + the chosen format.
func TestInsertUsageLog_WritesKindFormat(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC()
	rows := mock.NewRows([]string{"id", "requested_at"}).AddRow("ul-3", requestedAt)
	mock.ExpectQuery(`INSERT INTO he_api.data_export_requests[\s\S]*kind, format`).
		WithArgs("u-1", "csv").
		WillReturnRows(rows)

	repo := repository.NewDataExportRequestsRepo(mock)
	id, ts, err := repo.InsertUsageLog(context.Background(), "u-1", "csv")
	if err != nil {
		t.Fatalf("InsertUsageLog: %v", err)
	}
	if id != "ul-3" || !ts.Equal(requestedAt) {
		t.Errorf("insert result mismatch: id=%s ts=%v", id, ts)
	}
}

// 9.3-UNIT (kind-scope regression, Architect HIGH): the 2.6 GDPR
// FindLatestForUser query is scoped to kind='gdpr_full' so it can never
// surface a usage-log row after migration 0015.
func TestFindLatestForUser_GdprKindScoped(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC().Add(-10 * time.Minute)
	rows := mock.NewRows([]string{
		"id", "user_id", "status", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	}).AddRow(
		"gx-1", "u-1", "pending", requestedAt, (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), requestedAt, requestedAt,
	)
	mock.ExpectQuery(`kind = 'gdpr_full'`).
		WithArgs("u-1").
		WillReturnRows(rows)

	repo := repository.NewDataExportRequestsRepo(mock)
	got, err := repo.FindLatestForUser(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("FindLatestForUser: %v", err)
	}
	if got.Kind != "gdpr_full" {
		t.Errorf("expected gdpr_full kind, got %q", got.Kind)
	}
}
