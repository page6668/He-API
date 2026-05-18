package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/notification-svc/internal/repository"
)

func newMock(t *testing.T) pgxmock.PgxConnIface {
	t.Helper()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock.NewConn: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
		mock.Close(context.Background())
	})
	return mock
}

// 2.6-UNIT-050 — FindCurrentInWindow happy path: row scanned with the
// full 12-column projection (pin against accidental SELECT-shape drift).
func TestFindCurrentInWindow_RowReturned(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC().Add(-1 * time.Hour)
	startedAt := requestedAt.Add(5 * time.Second)
	objKey := "gdpr-exports/u-1/exp-1.zip"
	rows := mock.NewRows([]string{
		"id", "user_id", "status", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	}).AddRow(
		"exp-1", "u-1", "processing", requestedAt, &startedAt,
		(*time.Time)(nil), &objKey, (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), requestedAt, requestedAt,
	)
	mock.ExpectQuery(`FROM he_api.data_export_requests`).
		WithArgs("u-1", "86400 seconds").
		WillReturnRows(rows)

	repo := repository.NewDataExportRequestsRepo(mock)
	got, err := repo.FindCurrentInWindow(context.Background(), "u-1", 24*time.Hour)
	if err != nil {
		t.Fatalf("FindCurrentInWindow: %v", err)
	}
	if got.ID != "exp-1" || got.UserID != "u-1" || got.Status != "processing" {
		t.Errorf("scan mismatch: %+v", got)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(startedAt) {
		t.Errorf("started_at: got %v, want %v", got.StartedAt, startedAt)
	}
	if got.OSSObjectKey == nil || *got.OSSObjectKey != objKey {
		t.Errorf("oss_object_key: got %v", got.OSSObjectKey)
	}
	if got.CompletedAt != nil || got.SignedURLExpiresAt != nil || got.EmailSentAt != nil || got.FailureReason != nil {
		t.Errorf("expected null nullable fields to be nil; got %+v", got)
	}
}

// 2.6-UNIT-051 — FindCurrentInWindow no-row → ErrNotFound (not a DB error).
func TestFindCurrentInWindow_NotFound(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	emptyRows := mock.NewRows([]string{
		"id", "user_id", "status", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	})
	mock.ExpectQuery(`FROM he_api.data_export_requests`).
		WithArgs("u-1", "86400 seconds").
		WillReturnRows(emptyRows)

	repo := repository.NewDataExportRequestsRepo(mock)
	_, err := repo.FindCurrentInWindow(context.Background(), "u-1", 24*time.Hour)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// 2.6-UNIT-052 — FindCurrentInWindow uses the seconds-string interval
// shape that PG accepts as INTERVAL casting. Pin so a future refactor
// doesn't accidentally pass an int/Duration directly.
func TestFindCurrentInWindow_IntervalShape(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	emptyRows := mock.NewRows([]string{
		"id", "user_id", "status", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	})
	mock.ExpectQuery(`FROM he_api.data_export_requests`).
		WithArgs("u-1", "300 seconds").
		WillReturnRows(emptyRows)

	repo := repository.NewDataExportRequestsRepo(mock)
	_, _ = repo.FindCurrentInWindow(context.Background(), "u-1", 5*time.Minute)
}

// 2.6-UNIT-053 — FindLatestForUser scans the latest row regardless of
// status (BR-1.5 hydration path; failed rows DO surface here).
func TestFindLatestForUser_AllStatuses(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC().Add(-2 * time.Hour)
	reason := "oss upload failed"
	rows := mock.NewRows([]string{
		"id", "user_id", "status", "requested_at", "started_at",
		"completed_at", "oss_object_key", "signed_url_expires_at",
		"email_sent_at", "failure_reason", "created_at", "updated_at",
	}).AddRow(
		"exp-2", "u-1", "failed", requestedAt, (*time.Time)(nil),
		(*time.Time)(nil), (*string)(nil), (*time.Time)(nil),
		(*time.Time)(nil), &reason, requestedAt, requestedAt,
	)
	mock.ExpectQuery(`FROM he_api.data_export_requests`).
		WithArgs("u-1").
		WillReturnRows(rows)

	repo := repository.NewDataExportRequestsRepo(mock)
	got, err := repo.FindLatestForUser(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("FindLatestForUser: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status: got %q, want failed", got.Status)
	}
	if got.FailureReason == nil || *got.FailureReason != reason {
		t.Errorf("failure_reason: got %v, want %q", got.FailureReason, reason)
	}
}

// 2.6-UNIT-054 — Insert RETURNING id + requested_at.
func TestInsert_RoundTrip(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	requestedAt := time.Now().UTC()
	mock.ExpectQuery(`INSERT INTO he_api.data_export_requests`).
		WithArgs("u-1").
		WillReturnRows(mock.NewRows([]string{"id", "requested_at"}).AddRow("exp-new", requestedAt))

	repo := repository.NewDataExportRequestsRepo(mock)
	id, ts, err := repo.Insert(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id != "exp-new" {
		t.Errorf("id: got %q", id)
	}
	if !ts.Equal(requestedAt) {
		t.Errorf("requested_at: got %v, want %v", ts, requestedAt)
	}
}

// 2.6-UNIT-055 — Insert DB error is wrapped (not swallowed).
func TestInsert_DBError(t *testing.T) {
	t.Parallel()
	mock := newMock(t)

	mock.ExpectQuery(`INSERT INTO he_api.data_export_requests`).
		WithArgs("u-1").
		WillReturnError(errors.New("connection refused"))

	repo := repository.NewDataExportRequestsRepo(mock)
	_, _, err := repo.Insert(context.Background(), "u-1")
	if err == nil {
		t.Fatal("expected error")
	}
}
