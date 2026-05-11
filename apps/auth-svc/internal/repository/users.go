package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgErrCodeUniqueViolation is PostgreSQL SQLSTATE 23505 — raised by
// idx_users_email on duplicate-email INSERT. The handler maps this to
// BR-1.4 anti-enumeration response.
const pgErrCodeUniqueViolation = "23505"

// Querier is the minimal pgx/v5 surface this package needs. It is satisfied
// by *pgx.Conn, pgx.Tx, *pgxpool.Pool, and pgxmock.PgxConnIface — so the
// caller can pass a transaction (for signup's mixed INSERT + outbound side
// effects) without the repository caring.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// User mirrors he_api.users (migration 0002_create_users.sql). Nullable
// columns surface as pointer / sql.Null* types; OAuth columns stay nil until
// Story 2.3 populates them.
type User struct {
	ID                  uuid.UUID
	Email               string
	PasswordHash        []byte
	EmailVerifiedAt     *time.Time
	OAuthProvider       *string
	OAuthSubject        *string
	Locale              string
	Timezone            string
	TOTPSecretEncrypted *string
	TOTPEnabled         bool
	Status              string
	LockedUntil         *time.Time
	PendingDeletionAt   *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

var (
	// ErrEmailExists is returned by InsertUser when PG raises SQLSTATE 23505
	// on idx_users_email. The handler MUST translate this into the same 200
	// response shape as a first-time signup (BR-1.4 anti-enumeration).
	ErrEmailExists = errors.New("repository: email already exists")

	// ErrUserNotFound is returned by GetUserByEmail on pgx.ErrNoRows. The
	// LoginUser handler uses this to trigger the dummy-bcrypt branch
	// (BR-3.2 timing parity).
	ErrUserNotFound = errors.New("repository: user not found")
)

// Pre-baked SQL strings. Constants make them grep-able from QA static scans
// (2.2-UNIT-020 / -023 / -024 / -025) and keep the placeholder discipline
// (2.2-UNIT-026) visible at a glance.
const (
	insertUserSQL = `INSERT INTO he_api.users (id, email, password_hash, email_verified_at, locale, status, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NULL, $3, 'active', NOW(), NOW())
RETURNING id`

	getUserByEmailSQL = `SELECT id, email, password_hash, email_verified_at, oauth_provider, oauth_subject,
       locale, timezone, totp_secret_encrypted, totp_enabled, status, locked_until,
       pending_deletion_at, created_at, updated_at
FROM he_api.users WHERE email = $1 LIMIT 1`

	getUserByIDSQL = `SELECT id, email, password_hash, email_verified_at, oauth_provider, oauth_subject,
       locale, timezone, totp_secret_encrypted, totp_enabled, status, locked_until,
       pending_deletion_at, created_at, updated_at
FROM he_api.users WHERE id = $1 LIMIT 1`

	softLockUserSQL = `UPDATE he_api.users SET status='locked', locked_until=$1, updated_at=NOW() WHERE id=$2`

	selfHealLockSQL = `UPDATE he_api.users SET status='active', locked_until=NULL, updated_at=NOW()
WHERE id=$1 AND status='locked' AND locked_until <= NOW()`

	markEmailVerifiedSQL = `UPDATE he_api.users SET email_verified_at=NOW(), updated_at=NOW()
WHERE id=$1 AND email_verified_at IS NULL`
)

// InsertUser inserts a fresh user row (status='active', email_verified_at=NULL)
// and returns the generated id. On UNIQUE violation (idx_users_email) returns
// ErrEmailExists; the caller is responsible for the anti-enumeration response.
func InsertUser(ctx context.Context, q Querier, email string, passwordHash []byte, locale string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, insertUserSQL, email, passwordHash, locale).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgErrCodeUniqueViolation {
			return uuid.Nil, ErrEmailExists
		}
		return uuid.Nil, err
	}
	return id, nil
}

// GetUserByEmail returns the hydrated User row or ErrUserNotFound. Email is
// matched exactly (callers normalize via strings.ToLower + TrimSpace before
// invoking — see BR-4.2 email-hash invariant).
func GetUserByEmail(ctx context.Context, q Querier, email string) (*User, error) {
	return scanUserRow(q.QueryRow(ctx, getUserByEmailSQL, email))
}

// GetUserByID returns the hydrated User row by primary key, or
// ErrUserNotFound when absent. VerifyEmail's already-verified path uses
// this to retrieve the canonical email_verified_at timestamp.
func GetUserByID(ctx context.Context, q Querier, userID uuid.UUID) (*User, error) {
	return scanUserRow(q.QueryRow(ctx, getUserByIDSQL, userID))
}

// scanUserRow centralizes the column ordering so GetUserByEmail and
// GetUserByID stay in lockstep with the SELECT clause.
func scanUserRow(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerifiedAt,
		&u.OAuthProvider, &u.OAuthSubject, &u.Locale, &u.Timezone,
		&u.TOTPSecretEncrypted, &u.TOTPEnabled, &u.Status, &u.LockedUntil,
		&u.PendingDeletionAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &u, nil
}

// SoftLockUser flips status to 'locked' with the supplied locked_until.
// Caller (signin handler) invokes this once the 5th-failed-attempt threshold
// trips (BR-3.3).
func SoftLockUser(ctx context.Context, q Querier, userID uuid.UUID, until time.Time) error {
	_, err := q.Exec(ctx, softLockUserSQL, until, userID)
	return err
}

// SelfHealLock atomically flips status back to 'active' iff the lock has
// expired (locked_until <= NOW()). Returns (true, nil) when the row was
// cleared; (false, nil) when the WHERE clause matched nothing (either the
// user was not locked or the lock has not yet expired).
//
// The conditional UPDATE in a single statement guarantees BR-4.4 "self-heal
// in same transaction, no cron job" semantics.
func SelfHealLock(ctx context.Context, q Querier, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, selfHealLockSQL, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkEmailVerified sets email_verified_at=NOW() iff currently NULL. The
// WHERE-clause idempotency guard means a second call against an already-
// verified user returns (false, nil) without raising — BR-2.3 idempotency.
func MarkEmailVerified(ctx context.Context, q Querier, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, markEmailVerifiedSQL, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
