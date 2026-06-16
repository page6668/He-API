package deletion

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

var fixedNow = time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC)

// --- seam fakes ----------------------------------------------------------

type fakeStripe struct {
	detached []string
	err      error
}

func (f *fakeStripe) DetachPaymentMethod(_ context.Context, tok string) error {
	f.detached = append(f.detached, tok)
	return f.err
}

type fakeCH struct {
	called bool
	err    error
}

func (f *fakeCH) AnonymizeUser(_ context.Context, _ uuid.UUID) error { f.called = true; return f.err }

type fakeOSS struct {
	called bool
	err    error
}

func (f *fakeOSS) PurgeUserObjects(_ context.Context, _ uuid.UUID) error {
	f.called = true
	return f.err
}

type fakeMailer struct {
	delCalls int
	lastTo   string
	lastTmpl notification.AccountDeletionTemplate
	err      error
}

func (m *fakeMailer) SendVerificationEmail(_ context.Context, _, _, _, _ string) error { return nil }
func (m *fakeMailer) SendSecurityAlert(_ context.Context, _ notification.SecurityAlertTemplate, _, _ string, _ map[string]string) error {
	return nil
}
func (m *fakeMailer) SendAccountDeletionEmail(_ context.Context, t notification.AccountDeletionTemplate, to, _ string, _ map[string]string) error {
	m.delCalls++
	m.lastTo = to
	m.lastTmpl = t
	return m.err
}

type recAudit struct{ events []audit.Event }

func (r *recAudit) Publish(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

// mkUser builds a repository.DueUser with a known ORIGINAL email so the
// completion-email assertion (BR-6.4) can verify the pre-anonymization address.
func mkUser(id string) repository.DueUser {
	return repository.DueUser{ID: uuid.MustParse(id), Email: "orig@example.com", Locale: "en"}
}

// --- happy path: UNIT-030 + INT-020 + INT-022 + INT-029 ------------------

func TestSweeper_FullErasure_HappyPath(t *testing.T) {
	mock, _ := pgxmock.NewConn()
	defer mock.Close(context.Background())
	uid := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")

	// First-pass PG txn: anonymize + 4 explicit child deletes (CASCADE-on-UPDATE
	// trap — BR-6.3) in order.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE he_api\.users\s+SET email = 'deleted`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`DELETE FROM he_api\.api_keys WHERE user_id=\$1`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("DELETE", 2))
	mock.ExpectExec(`DELETE FROM he_api\.mfa_recovery_codes WHERE user_id=\$1`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("DELETE", 10))
	mock.ExpectExec(`DELETE FROM he_api\.data_export_requests WHERE user_id=\$1`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectExec(`DELETE FROM he_api\.balances WHERE user_id=\$1`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	// finalize: read tokens → Stripe detach → CH → OSS → delete PM + mark.
	mock.ExpectQuery(`SELECT provider_pm_token FROM he_api\.payment_methods WHERE user_id=\$1`).WithArgs(uid).
		WillReturnRows(pgxmock.NewRows([]string{"provider_pm_token"}).AddRow("pm_live_123"))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM he_api\.payment_methods WHERE user_id=\$1`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectExec(`UPDATE he_api\.users\s+SET anonymized_at = NOW\(\)`).WithArgs(uid).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	stripe := &fakeStripe{}
	ch := &fakeCH{}
	oss := &fakeOSS{}
	mail := &fakeMailer{}
	aud := &recAudit{}
	d := Deps{DB: mock, Tx: mock, Stripe: stripe, CH: ch, OSS: oss, Mailer: mail, Audit: aud, Clock: func() time.Time { return fixedNow }}

	if err := d.processUser(context.Background(), mkUser("aaaaaaaa-0000-0000-0000-000000000001"), false); err != nil {
		t.Fatalf("processUser: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet pgx: %v", err)
	}
	// INT-022 — Stripe detach called for the live token BEFORE row delete.
	if len(stripe.detached) != 1 || stripe.detached[0] != "pm_live_123" {
		t.Errorf("stripe detached = %v, want [pm_live_123]", stripe.detached)
	}
	if !ch.called || !oss.called {
		t.Errorf("cross-store not all called: ch=%v oss=%v", ch.called, oss.called)
	}
	// INT-029 — HIGH executed audit.
	if len(aud.events) != 1 || aud.events[0].EventType != audit.EventAccountDeletionExecuted {
		t.Fatalf("audit events = %+v", aud.events)
	}
	if sev, _ := aud.events[0].Metadata["severity"].(string); sev != audit.SeverityHigh {
		t.Errorf("severity = %q, want HIGH", sev)
	}
	// UNIT-031 — completion email with the ORIGINAL email.
	if mail.delCalls != 1 || mail.lastTmpl != notification.AccountDeletionCompleted {
		t.Errorf("mail calls=%d tmpl=%v", mail.delCalls, mail.lastTmpl)
	}
	if mail.lastTo != "orig@example.com" {
		t.Errorf("email to = %q, want original orig@example.com", mail.lastTo)
	}
}

// BLIND-ERROR-003 — Stripe detach fails → erasure stands, anonymized_at left
// NULL (no MarkComplete), no payment_methods delete, processUser returns error.
func TestSweeper_StripeDetachFails_LeavesReconcile(t *testing.T) {
	mock, _ := pgxmock.NewConn()
	defer mock.Close(context.Background())
	uid := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002")

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE he_api\.users\s+SET email = 'deleted`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`DELETE FROM he_api\.api_keys`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`DELETE FROM he_api\.mfa_recovery_codes`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`DELETE FROM he_api\.data_export_requests`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`DELETE FROM he_api\.balances`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT provider_pm_token FROM he_api\.payment_methods`).WithArgs(uid).
		WillReturnRows(pgxmock.NewRows([]string{"provider_pm_token"}).AddRow("pm_live_x"))
	// NO finalize txn — Stripe failure returns before payment delete / mark.

	mail := &fakeMailer{}
	aud := &recAudit{}
	d := Deps{DB: mock, Tx: mock, Stripe: &fakeStripe{err: errors.New("stripe 503")}, Mailer: mail, Audit: aud, Clock: func() time.Time { return fixedNow }}

	err := d.processUser(context.Background(), mkUser("aaaaaaaa-0000-0000-0000-000000000002"), false)
	if err == nil {
		t.Fatal("want error from Stripe detach failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet pgx: %v", err)
	}
	if mail.delCalls != 0 || len(aud.events) != 0 {
		t.Errorf("failed cross-store must not email/audit-complete")
	}
}

// INT-028 — reconcile pass skips the PG anonymize + child-delete; runs only the
// cross-store scrub + finalize.
func TestSweeper_Reconcile_SkipsPGAnonymize(t *testing.T) {
	mock, _ := pgxmock.NewConn()
	defer mock.Close(context.Background())
	uid := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000003")

	// NO first-pass Begin/anonymize/child-deletes.
	mock.ExpectQuery(`SELECT provider_pm_token FROM he_api\.payment_methods`).WithArgs(uid).
		WillReturnRows(pgxmock.NewRows([]string{"provider_pm_token"})) // empty
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM he_api\.payment_methods`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`UPDATE he_api\.users\s+SET anonymized_at = NOW\(\)`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	ch := &fakeCH{}
	oss := &fakeOSS{}
	d := Deps{DB: mock, Tx: mock, CH: ch, OSS: oss, Audit: &recAudit{}, Clock: func() time.Time { return fixedNow }}
	if err := d.processUser(context.Background(), mkUser("aaaaaaaa-0000-0000-0000-000000000003"), true); err != nil {
		t.Fatalf("reconcile processUser: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet pgx: %v", err)
	}
	if !ch.called || !oss.called {
		t.Errorf("reconcile must still run cross-store")
	}
}

// UNIT-031 (email-non-fatal) — a completion-email failure does NOT fail erasure.
func TestSweeper_EmailFailureNonFatal(t *testing.T) {
	mock, _ := pgxmock.NewConn()
	defer mock.Close(context.Background())
	uid := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000004")
	mock.ExpectQuery(`SELECT provider_pm_token`).WithArgs(uid).WillReturnRows(pgxmock.NewRows([]string{"provider_pm_token"}))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM he_api\.payment_methods`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`UPDATE he_api\.users\s+SET anonymized_at`).WithArgs(uid).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	d := Deps{DB: mock, Tx: mock, Mailer: &fakeMailer{err: errors.New("smtp down")}, Audit: &recAudit{}, Clock: func() time.Time { return fixedNow }}
	if err := d.processUser(context.Background(), mkUser("aaaaaaaa-0000-0000-0000-000000000004"), true); err != nil {
		t.Fatalf("email failure must be non-fatal, got %v", err)
	}
}

// INT-026 — per-user fail-isolation at the batch level: one bad row is rolled
// back, the batch continues, and Run tallies Failed without aborting.
func TestSweeper_Run_PerUserFailIsolation(t *testing.T) {
	mock, _ := pgxmock.NewConn()
	defer mock.Close(context.Background())
	u1 := uuid.MustParse("aaaaaaaa-0000-0000-0000-0000000000f1")
	u2 := uuid.MustParse("aaaaaaaa-0000-0000-0000-0000000000f2")

	// Due cohort: two users.
	mock.ExpectQuery(`SELECT id, email, locale, display_name\s+FROM he_api\.users\s+WHERE status='pending_deletion' AND pending_deletion_at <= NOW\(\)`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "locale", "display_name"}).
			AddRow(u1, "a@x.io", "en", nil).
			AddRow(u2, "b@x.io", "en", nil))

	// u1 fails at anonymize → rollback, batch continues.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE he_api\.users\s+SET email = 'deleted`).WithArgs(u1).WillReturnError(errors.New("pg boom"))
	mock.ExpectRollback()

	// u2 full happy path (no payment methods, no cross-store seams).
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE he_api\.users\s+SET email = 'deleted`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`DELETE FROM he_api\.api_keys`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`DELETE FROM he_api\.mfa_recovery_codes`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`DELETE FROM he_api\.data_export_requests`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`DELETE FROM he_api\.balances`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT provider_pm_token`).WithArgs(u2).WillReturnRows(pgxmock.NewRows([]string{"provider_pm_token"}))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM he_api\.payment_methods`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec(`UPDATE he_api\.users\s+SET anonymized_at`).WithArgs(u2).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	// Reconcile cohort: empty.
	mock.ExpectQuery(`WHERE status='deleted' AND anonymized_at IS NULL`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "locale", "display_name"}))

	d := Deps{DB: mock, Tx: mock, Audit: &recAudit{}, Clock: func() time.Time { return fixedNow }}
	res, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet pgx: %v", err)
	}
	if res.Processed != 1 || res.Failed != 1 {
		t.Errorf("result = %+v, want Processed=1 Failed=1", res)
	}
}

// INT-023 — content_safety_logs is UNTOUCHED: the sweeper source must never
// reference it (compliance §9.3 owns its retention cron — M-1).
func TestSweeper_ContentSafetyLogsUntouched(t *testing.T) {
	for _, f := range []string{"sweeper.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(b), "content_safety_logs") {
			t.Errorf("%s references content_safety_logs — must be UNTOUCHED (M-1)", f)
		}
	}
	// Also assert the repository SQL never deletes it.
	b, _ := os.ReadFile("../repository/account_deletion.go")
	if strings.Contains(string(b), "content_safety_logs") {
		t.Error("repository/account_deletion.go references content_safety_logs (M-1)")
	}
}
