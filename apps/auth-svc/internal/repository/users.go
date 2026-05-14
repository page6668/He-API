package repository

import (
	"context"
	"encoding/base64"
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

	// Story 2.3 — OAuth-specific queries.
	//
	// getUserByOAuthSQL drives Branch A (existing OAuth user re-login).
	// idx_users_oauth (partial WHERE oauth_provider IS NOT NULL) carries the
	// index scan; without the partial filter the planner would have to scan
	// the password-only majority of the table.
	getUserByOAuthSQL = `SELECT id, email, password_hash, email_verified_at, oauth_provider, oauth_subject,
       locale, timezone, totp_secret_encrypted, totp_enabled, status, locked_until,
       pending_deletion_at, created_at, updated_at
FROM he_api.users
WHERE oauth_provider = $1 AND oauth_subject = $2
LIMIT 1`

	// linkOAuthIdentitySQL drives Branch B.1 (auto-link existing verified
	// user). The WHERE oauth_provider IS NULL guard is the lost-update
	// defence (two concurrent OAuth callbacks for the same email each see
	// the user as unlinked; whichever UPDATE arrives second matches 0 rows
	// and the handler falls back to Branch A re-fetch).
	//
	// password_hash / email_verified_at / status are intentionally NOT
	// updated — BR-3.7 preserves password login path + email-verified state.
	linkOAuthIdentitySQL = `UPDATE he_api.users
SET oauth_provider = $1, oauth_subject = $2, updated_at = NOW()
WHERE id = $3 AND oauth_provider IS NULL`

	// upsertOAuthUserSQL drives Branch C (new user via OAuth). ON CONFLICT
	// (email) DO NOTHING guarantees race-safety: when two concurrent
	// callbacks for the same new email arrive, only one INSERT succeeds; the
	// loser receives 0 rows and the handler falls back to Branch B.
	//
	// password_hash is NULL (BR-3.6 — OAuth-only user). email_verified_at
	// is set to NOW() because the provider already vouched (BR-1.7 / BR-2.7
	// — provider email_verified=true was already validated).
	upsertOAuthUserSQL = `INSERT INTO he_api.users
       (id, email, password_hash, email_verified_at, oauth_provider, oauth_subject,
        locale, timezone, status, created_at, updated_at)
VALUES (gen_random_uuid(), $1, NULL, NOW(), $2, $3, $4, 'UTC', 'active', NOW(), NOW())
ON CONFLICT (email) DO NOTHING
RETURNING id`

	// touchUserUpdatedAtSQL drives Branch A re-login bookkeeping. No identity
	// fields are touched — only updated_at refreshes so Grafana / audit see
	// the last OAuth login timestamp.
	touchUserUpdatedAtSQL = `UPDATE he_api.users SET updated_at=NOW() WHERE id=$1`

	// -- Story 2.4 TOTP queries ---------------------------------------

	// setTOTPSecretSQL flips totp_enabled=TRUE atomically with the secret
	// write + totp_enrolled_at. Called from EnrollTOTPVerify inside a PG
	// transaction that ALSO inserts the 10 mfa_recovery_codes rows; the
	// caller is responsible for the transaction boundary.
	setTOTPSecretSQL = `UPDATE he_api.users
SET totp_secret_encrypted=$1, totp_enabled=TRUE, totp_enrolled_at=NOW(), updated_at=NOW()
WHERE id=$2`

	// clearTOTPSecretSQL is the AC4 Disable companion. NULLs every TOTP
	// column + flips totp_enabled=FALSE atomically with the caller's
	// `DELETE FROM mfa_recovery_codes WHERE user_id=...`.
	clearTOTPSecretSQL = `UPDATE he_api.users
SET totp_secret_encrypted=NULL, totp_enabled=FALSE,
    totp_enrolled_at=NULL, totp_last_used_at=NULL, updated_at=NOW()
WHERE id=$1`

	// markTOTPUsedSQL refreshes totp_last_used_at on a successful 2FA
	// challenge or recovery code use. Caller invokes after issuing the
	// access/refresh tokens (best-effort; failure here does NOT block the
	// session minting).
	markTOTPUsedSQL = `UPDATE he_api.users SET totp_last_used_at=NOW(), updated_at=NOW() WHERE id=$1`

	// getTOTPSecretSQL retrieves the KMS-encrypted secret + enrollment
	// state. Used by ChallengeTOTP + DisableTOTP factor='totp' paths.
	getTOTPSecretSQL = `SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at
FROM he_api.users WHERE id=$1 LIMIT 1`
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

// -- Story 2.3 OAuth helpers ----------------------------------------------

// UpsertOAuthUserParams is the input to UpsertOAuthUser. Spelling it as a
// struct rather than positional args keeps Branch C INSERT signature
// stable even if Story 2.5 (profile management) adds optional fields like
// display_name down the line.
type UpsertOAuthUserParams struct {
	Email    string
	Provider string
	Subject  string
	Locale   string
}

// GetUserByOAuth drives the Branch A lookup. Returns the canonical User
// row when oauth_provider+oauth_subject match; ErrUserNotFound otherwise.
//
// The partial index idx_users_oauth (WHERE oauth_provider IS NOT NULL)
// carries this lookup — Story 2.2 landed the index ahead of the OAuth
// feature so EXPLAIN ANALYZE shows Index Scan, not Seq Scan.
func GetUserByOAuth(ctx context.Context, q Querier, provider, subject string) (*User, error) {
	return scanUserRow(q.QueryRow(ctx, getUserByOAuthSQL, provider, subject))
}

// LinkOAuthIdentity drives Branch B.1 — the auto-link. WHERE oauth_provider
// IS NULL is the lost-update guard: when two concurrent callbacks try to
// link the same row, only the first UPDATE matches; the second receives
// 0 rows and the caller falls back to Branch A re-fetch.
//
// Returns (true, nil) when the row was successfully linked; (false, nil)
// when the WHERE clause matched nothing (already linked — caller falls
// back to Branch A); error on Redis / DB failure.
func LinkOAuthIdentity(ctx context.Context, q Querier, userID uuid.UUID, provider, subject string) (bool, error) {
	tag, err := q.Exec(ctx, linkOAuthIdentitySQL, provider, subject, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// UpsertOAuthUser drives Branch C — the new-user-via-OAuth INSERT. ON
// CONFLICT (email) DO NOTHING returns 0 rows on race; caller falls back
// to Branch B.
//
// Returns (userID, true, nil) on fresh INSERT; (uuid.Nil, false, nil) on
// race (caller MUST re-run the email lookup); (uuid.Nil, false, err) on
// driver failure. Note this CANNOT return ErrEmailExists like InsertUser
// does — the ON CONFLICT DO NOTHING swallows the unique violation by
// design.
func UpsertOAuthUser(ctx context.Context, q Querier, p UpsertOAuthUserParams) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, upsertOAuthUserSQL,
		p.Email, p.Provider, p.Subject, p.Locale,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil // race — caller falls back to Branch B
		}
		return uuid.Nil, false, err
	}
	return id, true, nil
}

// TouchUserUpdatedAt refreshes updated_at on the supplied user_id. Branch A
// re-login uses this so dashboards / audit see the OAuth-login timestamp
// without disturbing identity columns.
func TouchUserUpdatedAt(ctx context.Context, q Querier, userID uuid.UUID) error {
	_, err := q.Exec(ctx, touchUserUpdatedAtSQL, userID)
	return err
}

// -- Story 2.4 TOTP helpers -----------------------------------------------

// SetTOTPSecret persists the KMS-encrypted secret + flips totp_enabled=TRUE.
// Must be invoked inside a PG transaction that also inserts the 10 fresh
// mfa_recovery_codes rows (atomic enrollment per AC1 BR-1.6). Returns
// (true, nil) on a single-row UPDATE; (false, nil) when the WHERE clause
// matched nothing.
//
// totp_secret_encrypted is TEXT (migration 0002); we base64-encode the
// KMS ciphertext blob to fit. Encoding choice is local to the repository
// so handlers handle raw kms.Ciphertext bytes only.
func SetTOTPSecret(ctx context.Context, q Querier, userID uuid.UUID, encryptedSecret []byte) (bool, error) {
	encoded := base64.StdEncoding.EncodeToString(encryptedSecret)
	tag, err := q.Exec(ctx, setTOTPSecretSQL, encoded, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClearTOTPSecret zeroes every TOTP column + sets totp_enabled=FALSE. Must
// be invoked inside the AC4 Disable transaction that also DELETEs all
// mfa_recovery_codes for this user (atomic clean-slate per BR-3.9 + BR-4.3).
func ClearTOTPSecret(ctx context.Context, q Querier, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, clearTOTPSecretSQL, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkTOTPUsed refreshes totp_last_used_at on a successful challenge or
// recovery code use. Best-effort: failure should not block session issuance
// (caller swallows + logs at warn).
func MarkTOTPUsed(ctx context.Context, q Querier, userID uuid.UUID) error {
	_, err := q.Exec(ctx, markTOTPUsedSQL, userID)
	return err
}

// TOTPSecretRow is the minimal projection used by ChallengeTOTP + DisableTOTP
// factor='totp' paths. Separate from the full User row to avoid pulling the
// rest of the columns on every challenge.
type TOTPSecretRow struct {
	EncryptedSecret []byte     // NULL when totp_enabled=FALSE
	Enabled         bool
	EnrolledAt      *time.Time
}

// GetTOTPSecret returns the encrypted secret + enrollment state. Returns
// ErrUserNotFound if the user_id does not exist; otherwise the row even
// when totp_enabled=FALSE (callers check the Enabled bool).
//
// Base64-decoded inside the repository — handlers see raw kms.Ciphertext
// bytes. Returns nil + non-nil error on decode failure (corrupted column).
func GetTOTPSecret(ctx context.Context, q Querier, userID uuid.UUID) (*TOTPSecretRow, error) {
	var r TOTPSecretRow
	var enc *string
	err := q.QueryRow(ctx, getTOTPSecretSQL, userID).Scan(&enc, &r.Enabled, &r.EnrolledAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if enc != nil {
		decoded, decErr := base64.StdEncoding.DecodeString(*enc)
		if decErr != nil {
			return nil, decErr
		}
		r.EncryptedSecret = decoded
	}
	return &r, nil
}
