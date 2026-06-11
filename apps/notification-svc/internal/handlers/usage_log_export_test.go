// Story 9.3 AC1 — notification-svc UsageLogExportServer handler tests.
//
//   - 9.3-UNIT format validation (json|csv required) → InvalidArgument
//   - 9.3-INT-001 publish-after-commit: new export INSERTs, commits, THEN
//     publishes (format + range carried), audits.
//   - 9.3-INT-002 idempotency: an in-window (kind,format) row returns the same
//     export_id with NO new Kafka publish.
//   - 9.3-INT-005 rate-limit race: limiter ErrRateLimited → ResourceExhausted,
//     PG rolled back, no publish.
package handlers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"

	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/audit"
	"github.com/he-api/he-api/apps/notification-svc/internal/handlers"
	"github.com/he-api/he-api/apps/notification-svc/internal/ratelimit"
)

// mockDB adapts a pgxmock conn to the handlers.DataExportDB Begin surface.
type mockDB struct{ conn pgxmock.PgxConnIface }

func (m mockDB) Begin(ctx context.Context) (pgx.Tx, error) { return m.conn.Begin(ctx) }

type fakeULLimiter struct {
	err    error
	resets int
}

func (f *fakeULLimiter) CheckAndIncr(context.Context, string) error { return f.err }
func (f *fakeULLimiter) Reset(context.Context, string) error        { f.resets++; return nil }

type fakeULPublisher struct {
	calls      int
	gotFormat  string
	gotStart   time.Time
	gotEnd     time.Time
	gotExport  string
	publishErr error
}

func (f *fakeULPublisher) Publish(_ context.Context, exportID, _ , format string, start, end time.Time) error {
	f.calls++
	f.gotExport = exportID
	f.gotFormat = format
	f.gotStart, f.gotEnd = start, end
	return f.publishErr
}

type noopAudit struct{}

func (noopAudit) Publish(context.Context, audit.Event) error { return nil }

func newMockConn(t *testing.T) pgxmock.PgxConnIface {
	t.Helper()
	conn, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock.NewConn: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
		conn.Close(context.Background())
	})
	return conn
}

func newULServer(t *testing.T, conn pgxmock.PgxConnIface, lim *fakeULLimiter, pub *fakeULPublisher) *handlers.UsageLogExportServer {
	t.Helper()
	s := handlers.NewUsageLogExportServer(mockDB{conn}, lim, pub, noopAudit{}, nil)
	s.Now = func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestRequestUsageLogExport_BadFormat_InvalidArgument(t *testing.T) {
	t.Parallel()
	conn := newMockConn(t) // no DB interaction expected
	s := newULServer(t, conn, &fakeULLimiter{}, &fakeULPublisher{})

	_, err := s.RequestUsageLogExport(context.Background(), connect.NewRequest(&usagelogv1.RequestUsageLogExportRequest{
		UserId: "u-1", Format: "xml",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument, got %v (err=%v)", connect.CodeOf(err), err)
	}
}

func TestRequestUsageLogExport_NewExport_PublishAfterCommit(t *testing.T) {
	t.Parallel()
	conn := newMockConn(t)
	pub := &fakeULPublisher{}
	s := newULServer(t, conn, &fakeULLimiter{}, pub)

	requestedAt := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	conn.ExpectBegin()
	// idempotency lookup → no row
	conn.ExpectQuery(`kind = 'usage_logs'`).
		WithArgs("u-1", "csv", "86400 seconds").
		WillReturnError(pgx.ErrNoRows)
	// insert → returns id + requested_at
	conn.ExpectQuery(`INSERT INTO he_api.data_export_requests`).
		WithArgs("u-1", "csv").
		WillReturnRows(conn.NewRows([]string{"id", "requested_at"}).AddRow("ul-9", requestedAt))
	conn.ExpectCommit()

	resp, err := s.RequestUsageLogExport(context.Background(), connect.NewRequest(&usagelogv1.RequestUsageLogExportRequest{
		UserId: "u-1", Format: "csv",
	}))
	if err != nil {
		t.Fatalf("RequestUsageLogExport: %v", err)
	}
	if resp.Msg.GetExportId() != "ul-9" || resp.Msg.GetStatus() != "pending" || resp.Msg.GetFormat() != "csv" {
		t.Errorf("unexpected response: %+v", resp.Msg)
	}
	if pub.calls != 1 || pub.gotFormat != "csv" || pub.gotExport != "ul-9" {
		t.Errorf("publisher not called correctly: %+v", pub)
	}
	// default 90d window: end=Now, start=Now-90d
	if !pub.gotEnd.Equal(s.Now()) || !pub.gotStart.Equal(s.Now().AddDate(0, 0, -90)) {
		t.Errorf("range default mismatch: start=%v end=%v", pub.gotStart, pub.gotEnd)
	}
}

func TestRequestUsageLogExport_Idempotent_SameIDNoPublish(t *testing.T) {
	t.Parallel()
	conn := newMockConn(t)
	pub := &fakeULPublisher{}
	s := newULServer(t, conn, &fakeULLimiter{}, pub)

	requestedAt := time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC)
	format := "csv"
	conn.ExpectBegin()
	conn.ExpectQuery(`kind = 'usage_logs'`).
		WithArgs("u-1", "csv", "86400 seconds").
		WillReturnRows(conn.NewRows([]string{
			"id", "user_id", "status", "format", "requested_at", "started_at",
			"completed_at", "oss_object_key", "signed_url_expires_at",
			"email_sent_at", "failure_reason", "created_at", "updated_at",
		}).AddRow("ul-existing", "u-1", "processing", &format, requestedAt,
			(*time.Time)(nil), (*time.Time)(nil), (*string)(nil), (*time.Time)(nil),
			(*time.Time)(nil), (*string)(nil), requestedAt, requestedAt))
	conn.ExpectCommit()

	resp, err := s.RequestUsageLogExport(context.Background(), connect.NewRequest(&usagelogv1.RequestUsageLogExportRequest{
		UserId: "u-1", Format: "csv",
	}))
	if err != nil {
		t.Fatalf("RequestUsageLogExport: %v", err)
	}
	if resp.Msg.GetExportId() != "ul-existing" {
		t.Errorf("idempotent export_id mismatch: %s", resp.Msg.GetExportId())
	}
	if pub.calls != 0 {
		t.Errorf("publisher MUST NOT be called on idempotent hit; calls=%d", pub.calls)
	}
}

func TestRequestUsageLogExport_RateLimit_ResourceExhausted(t *testing.T) {
	t.Parallel()
	conn := newMockConn(t)
	pub := &fakeULPublisher{}
	lim := &fakeULLimiter{err: ratelimit.ErrRateLimited}
	s := newULServer(t, conn, lim, pub)

	requestedAt := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	conn.ExpectBegin()
	conn.ExpectQuery(`kind = 'usage_logs'`).
		WithArgs("u-1", "csv", "86400 seconds").
		WillReturnError(pgx.ErrNoRows)
	conn.ExpectQuery(`INSERT INTO he_api.data_export_requests`).
		WithArgs("u-1", "csv").
		WillReturnRows(conn.NewRows([]string{"id", "requested_at"}).AddRow("ul-x", requestedAt))
	conn.ExpectRollback() // limiter loser → PG rolled back

	_, err := s.RequestUsageLogExport(context.Background(), connect.NewRequest(&usagelogv1.RequestUsageLogExportRequest{
		UserId: "u-1", Format: "csv",
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("want ResourceExhausted, got %v (err=%v)", connect.CodeOf(err), err)
	}
	if pub.calls != 0 {
		t.Errorf("publisher MUST NOT be called on rate-limit; calls=%d", pub.calls)
	}
}

// sanity: ErrRateLimited is the shared sentinel from the ratelimit package.
var _ = errors.Is(ratelimit.ErrRateLimited, ratelimit.ErrRateLimited)
