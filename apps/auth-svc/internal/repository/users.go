package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
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

// User mirrors he_api.users (migrations 0002_create_users.sql +
// 0004_add_display_name_to_users.sql). Nullable columns surface as pointer /
// sql.Null* types; OAuth columns stay nil until Story 2.3 populates them.
// DisplayName (Story 2.5) is nullable — Story 2.5 BR-1.6 normalises NULL and
// empty string to the same "unset" UX state.
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
	DisplayName         *string // Story 2.5 — nullable display name (BR-1.6)
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

	// ErrEtagMismatch is returned by UpdateProfile when the supplied
	// If-Match etag does not match the current users.updated_at row value.
	// Story 2.5 BR-2.7 — optimistic concurrency. Architect Q2 ruling: etag
	// format is `UnixMicro()` (16-digit int64) Go-side compared inside the
	// same FOR UPDATE transaction.
	ErrEtagMismatch = errors.New("repository: etag mismatch")

	// ErrAccountPendingDeletion is surfaced when a profile read/update
	// targets a user whose status='pending_deletion' (Story 2.7 grace
	// window). Story 2.5 BR-1.9 / BR-2.x — already-deleted accounts MUST
	// NOT surface the profile editor; api-gateway translates this to 403.
	ErrAccountPendingDeletion = errors.New("repository: account pending deletion")
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

	// Story 2.5 — narrower profile-only projection for GET /v1/me + UpdateProfile.
	// Excludes the auth-leg-only columns (totp_secret_encrypted, locked_until,
	// pending_deletion_at) — auth-svc handlers read those via separate
	// SQL paths (GetTOTPSecret + SoftLockUser bookkeeping). Keeps the profile
	// hot path tight and avoids leaking encrypted secrets through the Settings
	// page response surface.
	getProfileByIDSQL = `SELECT id, email, password_hash, email_verified_at, oauth_provider, oauth_subject,
       locale, timezone, totp_enabled, status, created_at, updated_at, display_name
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

// scanProfileRow is the Story 2.5 GetProfileByID + UpdateProfile RETURNING
// scanner. Narrower than scanUserRow — excludes auth-leg-only columns
// (totp_secret_encrypted, locked_until, pending_deletion_at) but adds
// display_name. The matching SELECT/RETURNING clauses pin the column order.
func scanProfileRow(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerifiedAt,
		&u.OAuthProvider, &u.OAuthSubject, &u.Locale, &u.Timezone,
		&u.TOTPEnabled, &u.Status, &u.CreatedAt, &u.UpdatedAt,
		&u.DisplayName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &u, nil
}

// GetProfileByID is the Story 2.5 profile-focused read path used by the
// GetMe RPC. Returns ErrUserNotFound when the row is missing, or
// ErrAccountPendingDeletion when status='pending_deletion' (Story 2.5
// BR-1.9 — already-deleting accounts must NOT surface the profile editor).
// Other status values (active / locked / suspended) flow through; the
// handler decides per-status policy.
func GetProfileByID(ctx context.Context, q Querier, userID uuid.UUID) (*User, error) {
	u, err := scanProfileRow(q.QueryRow(ctx, getProfileByIDSQL, userID))
	if err != nil {
		return nil, err
	}
	if u.Status == "pending_deletion" {
		return nil, ErrAccountPendingDeletion
	}
	return u, nil
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

// -- Story 2.5 profile helpers ---------------------------------------------

// UpdateProfileParams carries the partial-update payload for UpdateProfile.
// Each *_Set bool toggles whether the corresponding field participates in
// the UPDATE statement (partial-update semantics per Story 2.5 BR-2.1).
// DisplayName uses *string so callers can express both "set to <value>" and
// "set to NULL" by passing a nil-valued pointer with DisplayNameSet=true.
type UpdateProfileParams struct {
	DisplayName    *string
	DisplayNameSet bool
	Locale         string
	LocaleSet      bool
	Timezone       string
	TimezoneSet    bool
}

// UpdateProfile applies the partial profile update, validating the supplied
// etag against the current updated_at (Story 2.5 BR-2.7 — Go-side UnixMicro
// compare per Architect Q2 ruling 2026-05-16), then issuing the UPDATE.
//
// **The caller MUST pass a transactional Querier** (a pgx.Tx obtained via
// pgxpool.Pool.Begin) so the inner SELECT FOR UPDATE + UPDATE land in the
// same transaction with row-level locking. Passing a non-transactional
// connection breaks the BR-2.7 concurrency guarantee (two-tab race becomes
// a lost-update). The Querier surface (vs pgx.Tx directly) is for test
// ergonomics — pgxmock.PgxConnIface satisfies Querier.
//
// Returns:
//   - the refreshed *User on success (DisplayName / Locale / Timezone /
//     UpdatedAt reflect the new state).
//   - ErrUserNotFound if the user_id row is missing.
//   - ErrAccountPendingDeletion if status='pending_deletion'.
//   - ErrEtagMismatch if ifMatchMicros does not equal current updated_at.UnixMicro().
//
// The caller is responsible for application-layer field validation (NFC
// normalisation, locale allowlist, IANA timezone check) BEFORE invoking —
// the repository is a thin SQL boundary.
func UpdateProfile(
	ctx context.Context,
	q Querier,
	userID uuid.UUID,
	ifMatchMicros int64,
	params UpdateProfileParams,
) (*User, error) {
	// 1. SELECT updated_at + status FOR UPDATE — holds the row lock until
	//    commit/rollback. Two-tab race resolves here: the second tab's
	//    SELECT blocks until the first tab commits, then sees the advanced
	//    updated_at and falls through to the ErrEtagMismatch branch.
	var currentUpdatedAt time.Time
	var currentStatus string
	err := q.QueryRow(ctx,
		`SELECT updated_at, status FROM he_api.users WHERE id=$1 FOR UPDATE`,
		userID,
	).Scan(&currentUpdatedAt, &currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if currentStatus == "pending_deletion" {
		return nil, ErrAccountPendingDeletion
	}
	if currentUpdatedAt.UnixMicro() != ifMatchMicros {
		return nil, ErrEtagMismatch
	}

	// 2. Build the UPDATE statement dynamically based on which fields were
	//    flagged as "set" by the caller. Always advance updated_at=NOW()
	//    (BR-2.11 — load-bearing for etag).
	//
	//    The SET clause is built from STATIC fragments only (column name +
	//    placeholder index — both compile-time literals). User-supplied
	//    values flow exclusively through args[] → pgx parameter binding.
	//    No format directives appear in the SQL fragments, satisfying the
	//    package-level "no concatenated SQL" static scan.
	var setBuilder strings.Builder
	setBuilder.WriteString("updated_at=NOW()")
	args := []any{}
	if params.DisplayNameSet {
		args = append(args, params.DisplayName) // *string — nil → SQL NULL
		setBuilder.WriteString(", display_name=")
		setBuilder.WriteString(placeholder(len(args)))
	}
	if params.LocaleSet {
		args = append(args, params.Locale)
		setBuilder.WriteString(", locale=")
		setBuilder.WriteString(placeholder(len(args)))
	}
	if params.TimezoneSet {
		args = append(args, params.Timezone)
		setBuilder.WriteString(", timezone=")
		setBuilder.WriteString(placeholder(len(args)))
	}
	args = append(args, userID)

	var sqlBuilder strings.Builder
	sqlBuilder.WriteString(updateProfilePrefix)
	sqlBuilder.WriteString(setBuilder.String())
	sqlBuilder.WriteString(" WHERE id=")
	sqlBuilder.WriteString(placeholder(len(args)))
	sqlBuilder.WriteString(updateProfileReturning)

	return scanProfileRow(q.QueryRow(ctx, sqlBuilder.String(), args...))
}

// placeholder returns the pgx positional placeholder string ($1, $2, …) for
// the supplied 1-based index. Returns "$0" for zero (caller error — only
// indices ≥ 1 are valid). Kept local so the SQL builder above stays free of
// fmt.Sprintf calls (package-level static scan ban).
func placeholder(idx int) string {
	// Fast path for the common indices (1..16) — avoids strconv allocation
	// on the hot UPDATE path. Beyond 16, fall through to strconv (no profile
	// UPDATE will ever hit this branch — only 3 fields + 1 WHERE id).
	switch idx {
	case 1:
		return "$1"
	case 2:
		return "$2"
	case 3:
		return "$3"
	case 4:
		return "$4"
	case 5:
		return "$5"
	case 6:
		return "$6"
	case 7:
		return "$7"
	case 8:
		return "$8"
	}
	return "$" + strconvItoa(idx)
}

// strconvItoa is a tiny wrapper so we don't import strconv just for this.
// Profile UPDATE never exercises the >8-args path; this is purely defensive.
func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

const (
	// updateProfilePrefix + updateProfileReturning bracket the dynamically-
	// built SET clause. Splitting them keeps every fragment a static literal
	// (no fmt.Sprintf with SQL verbs in format string per the package-level
	// static SQL scan in users_test.go TestSourceUsesParameterizedSQL).
	updateProfilePrefix    = "UPDATE he_api.users SET "
	updateProfileReturning = " RETURNING id, email, password_hash, email_verified_at, oauth_provider, oauth_subject, locale, timezone, totp_enabled, status, created_at, updated_at, display_name"
)

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
