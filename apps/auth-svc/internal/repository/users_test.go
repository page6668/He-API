package repository_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// newMock returns a pgxmock connection in regex query-match mode (the
// default). All ExpectQuery / ExpectExec patterns are Go regular expressions.
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

// Scenario: 2.2-UNIT-020
// InsertUser issues a parameterized INSERT against he_api.users with the
// canonical column list and value clause, returning the generated user id.
func TestInsertUser_IssuesParameterizedInsert(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	wantID := uuid.New()

	// Regex anchors on column list + literal NULL / 'active' / NOW() / gen_random_uuid().
	mock.ExpectQuery(`(?s)INSERT INTO he_api\.users\s*\(id, email, password_hash, email_verified_at, locale, status, created_at, updated_at\)\s*VALUES\s*\(gen_random_uuid\(\),\s*\$1,\s*\$2,\s*NULL,\s*\$3,\s*'active',\s*NOW\(\),\s*NOW\(\)\)\s*RETURNING id`).
		WithArgs("user@example.com", []byte("$2a$12$abcdefghij"), "en").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(wantID))

	gotID, err := repository.InsertUser(context.Background(), mock, "user@example.com", []byte("$2a$12$abcdefghij"), "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("InsertUser id = %s, want %s", gotID, wantID)
	}
}

// Scenario: 2.2-UNIT-021
// InsertUser maps PG 23505 (unique violation on idx_users_email) to
// repository.ErrEmailExists so the handler can apply BR-1.4 anti-enumeration
// without leaking the duplicate.
func TestInsertUser_ReturnsErrEmailExistsOn23505(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	pgErr := &pgconn.PgError{
		Code:           "23505",
		Message:        "duplicate key value violates unique constraint \"idx_users_email\"",
		ConstraintName: "idx_users_email",
	}
	mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("existing@example.com", []byte("$2a$12$x"), "en").
		WillReturnError(pgErr)

	_, err := repository.InsertUser(context.Background(), mock, "existing@example.com", []byte("$2a$12$x"), "en")
	if !errors.Is(err, repository.ErrEmailExists) {
		t.Fatalf("InsertUser on 23505 = %v, want ErrEmailExists", err)
	}
}

func TestInsertUser_OtherErrorPropagates(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	pgErr := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("user@example.com", []byte("$2a$12$x"), "en").
		WillReturnError(pgErr)

	_, err := repository.InsertUser(context.Background(), mock, "user@example.com", []byte("$2a$12$x"), "en")
	if errors.Is(err, repository.ErrEmailExists) {
		t.Fatalf("non-23505 error must NOT map to ErrEmailExists, got %v", err)
	}
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
}

// Scenario: 2.2-UNIT-022
// GetUserByEmail returns ErrUserNotFound on pgx.ErrNoRows (caller decides
// dummy-bcrypt path per BR-3.2).
func TestGetUserByEmail_ReturnsErrUserNotFound(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("absent@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "password_hash", "email_verified_at",
			"oauth_provider", "oauth_subject", "locale", "timezone",
			"totp_secret_encrypted", "totp_enabled", "status", "locked_until",
			"pending_deletion_at", "created_at", "updated_at",
		})) // empty rowset → pgx.ErrNoRows from QueryRow.Scan

	user, err := repository.GetUserByEmail(context.Background(), mock, "absent@example.com")
	if !errors.Is(err, repository.ErrUserNotFound) {
		t.Fatalf("GetUserByEmail(missing) = %v, want ErrUserNotFound", err)
	}
	if user != nil {
		t.Fatalf("GetUserByEmail(missing) user = %+v, want nil", user)
	}
}

func TestGetUserByEmail_ReturnsHydratedUser(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	id := uuid.New()
	now := time.Now().UTC()
	verifiedAt := now.Add(-time.Hour)
	mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "password_hash", "email_verified_at",
			"oauth_provider", "oauth_subject", "locale", "timezone",
			"totp_secret_encrypted", "totp_enabled", "status", "locked_until",
			"pending_deletion_at", "created_at", "updated_at",
		}).AddRow(
			id, "user@example.com", []byte("$2a$12$hash"), &verifiedAt,
			nil, nil, "en", "UTC",
			nil, false, "active", nil,
			nil, now, now,
		))

	user, err := repository.GetUserByEmail(context.Background(), mock, "user@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.ID != id {
		t.Fatalf("user.ID = %s, want %s", user.ID, id)
	}
	if user.Email != "user@example.com" {
		t.Fatalf("user.Email = %q", user.Email)
	}
	if user.Status != "active" {
		t.Fatalf("user.Status = %q, want active", user.Status)
	}
	if user.EmailVerifiedAt == nil || !user.EmailVerifiedAt.Equal(verifiedAt) {
		t.Fatalf("user.EmailVerifiedAt = %v, want %v", user.EmailVerifiedAt, verifiedAt)
	}
}

// Scenario: 2.2-UNIT-023
// SoftLockUser issues the canonical lock UPDATE with parameterized
// locked_until + user_id.
func TestSoftLockUser_IssuesUpdate(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	userID := uuid.New()
	until := time.Now().Add(time.Hour)

	mock.ExpectExec(`UPDATE he_api\.users\s+SET status='locked',\s*locked_until=\$1,\s*updated_at=NOW\(\)\s+WHERE id=\$2`).
		WithArgs(until, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repository.SoftLockUser(context.Background(), mock, userID, until); err != nil {
		t.Fatalf("SoftLockUser: %v", err)
	}
}

// Scenario: 2.2-UNIT-024
// SelfHealLock issues the conditional atomic clear (BR-4.4 same-tx
// atomicity). Returns (true, nil) when the lock was cleared; (false, nil)
// when nothing changed (user not locked, or lock still pending).
func TestSelfHealLock_AtomicConditionalClear(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	userID := uuid.New()

	mock.ExpectExec(`UPDATE he_api\.users\s+SET status='active',\s*locked_until=NULL,\s*updated_at=NOW\(\)\s+WHERE id=\$1\s+AND status='locked'\s+AND locked_until <= NOW\(\)`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	healed, err := repository.SelfHealLock(context.Background(), mock, userID)
	if err != nil {
		t.Fatalf("SelfHealLock: %v", err)
	}
	if !healed {
		t.Fatalf("SelfHealLock: got healed=false, want true (mocked 1 row affected)")
	}
}

func TestSelfHealLock_NoChangeWhenStillLocked(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	userID := uuid.New()

	mock.ExpectExec(`UPDATE he_api\.users\s+SET status='active'`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	healed, err := repository.SelfHealLock(context.Background(), mock, userID)
	if err != nil {
		t.Fatalf("SelfHealLock: %v", err)
	}
	if healed {
		t.Fatalf("SelfHealLock: got healed=true, want false (0 rows affected)")
	}
}

// Scenario: 2.2-UNIT-025
// MarkEmailVerified issues the idempotent UPDATE; returns (true, nil) when
// the row newly verified, (false, nil) when already verified (no error).
func TestMarkEmailVerified_NewlyVerified(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	userID := uuid.New()

	mock.ExpectExec(`UPDATE he_api\.users\s+SET email_verified_at=NOW\(\),\s*updated_at=NOW\(\)\s+WHERE id=\$1\s+AND email_verified_at IS NULL`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	wasNewlyVerified, err := repository.MarkEmailVerified(context.Background(), mock, userID)
	if err != nil {
		t.Fatalf("MarkEmailVerified: %v", err)
	}
	if !wasNewlyVerified {
		t.Fatalf("MarkEmailVerified: got wasNewlyVerified=false, want true (1 row affected)")
	}
}

func TestMarkEmailVerified_IdempotentAlreadyVerified(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	userID := uuid.New()

	mock.ExpectExec(`UPDATE he_api\.users\s+SET email_verified_at=NOW`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	wasNewlyVerified, err := repository.MarkEmailVerified(context.Background(), mock, userID)
	if err != nil {
		t.Fatalf("MarkEmailVerified (already verified): %v — want nil error per BR-2.3 idempotency", err)
	}
	if wasNewlyVerified {
		t.Fatalf("MarkEmailVerified: got wasNewlyVerified=true, want false (0 rows affected — already verified)")
	}
}

// Scenario: 2.2-UNIT-026
// All SQL in this package uses parameterized placeholders ($1, $2, ...).
// No fmt.Sprintf-style concatenation of user input. Static scan of the
// package source.
func TestSourceUsesParameterizedSQL(t *testing.T) {
	t.Parallel()
	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	// Patterns that indicate concatenation of user input INTO SQL strings:
	//   fmt.Sprintf("SELECT ... WHERE x = %s ...", userInput)
	//   "SELECT ..." + userInput + "..."
	// We allow Sprintf used for non-SQL strings; the heuristic only fires when
	// "SELECT" / "INSERT" / "UPDATE" / "DELETE" appears inside the format string.
	badPatterns := []*regexp.Regexp{
		regexp.MustCompile(`fmt\.Sprintf\([^)]*"[^"]*\b(?:SELECT|INSERT|UPDATE|DELETE)\b[^"]*%[svqdfxX][^"]*"`),
		regexp.MustCompile(`"[^"]*\b(?:SELECT|INSERT|UPDATE|DELETE)\b[^"]*"\s*\+`),
	}
	// We also REQUIRE at least one $N placeholder in any source file that
	// references the SQL verbs.
	verbRe := regexp.MustCompile(`\b(SELECT|INSERT INTO|UPDATE|DELETE FROM)\b`)
	placeholderRe := regexp.MustCompile(`\$[1-9][0-9]*`)

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for _, re := range badPatterns {
			if loc := re.FindIndex(data); loc != nil {
				t.Errorf("%s:%d: SQL appears concatenated with non-literal input: %q",
					name, byteOffsetToLine(data, loc[0]), string(data[loc[0]:loc[1]]))
			}
		}
		if verbRe.Match(data) && !placeholderRe.Match(data) {
			t.Errorf("%s: contains SQL verb without any $N placeholder — suspicious", name)
		}
	}
}

func byteOffsetToLine(b []byte, off int) int {
	if off > len(b) {
		off = len(b)
	}
	count := 1
	for _, c := range b[:off] {
		if c == '\n' {
			count++
		}
	}
	return count
}
