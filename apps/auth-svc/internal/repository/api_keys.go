package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Story 3.2 — API-key repository surface.
//
// LookupByPrefix returns 0..MaxAPIKeyCandidates rows that share `key_prefix`.
// `key_prefix` is indexed but NOT UNIQUE (AC2 BR-2.1) — base62^12 collisions
// are statistically near-zero, but the handler bcrypt-compares ALL candidates
// in id-ascending order (AC2 BR-2.3 determinism) and treats first-match-wins.

// MaxAPIKeyCandidates caps the candidate fanout at 100 rows (AC2 BR-2.3).
// Exceeding the cap is unreachable on realistic data but defuses a
// hypothetical DoS where an attacker correlates colliding prefixes.
const MaxAPIKeyCandidates = 100

// ApiKeyRow mirrors he_api.api_keys for the api-key validation hot path
// (Story 3.2 AC2) AND the Story-5.1 management surface. Nullable columns
// surface as pgtype values so the handler can detect "no team" vs "empty
// UUID" without ambiguity.
//
// Story-5.1 management-surface fields (Name, MonthlyCostCapUSD,
// CurrentMonthCostUSD, LastUsedAt) carry their pgtype representations from
// `ListByUser` (the only repository method that selects them). The
// Validate hot path (`LookupAPIKeysByPrefix`) never populates them — those
// zero values are NOT a defect, they are an intentional "don't ship cents
// across the bcrypt-compare path" carve-out (BR-2.5 defence-in-depth).
type ApiKeyRow struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	TeamID              pgtype.UUID // nullable; .Valid==false when he_api.api_keys.team_id IS NULL
	Name                string      // user-supplied label (Story 5.1 BR-1.7); zero string on Validate path
	KeyPrefix           string
	KeyHash             string             // bcrypt output (cost=12 in prod, cost=4 in tests via IssueKeyForTest)
	Scope               []byte             // raw JSONB bytes; auth-svc forwards as-is into the response
	MonthlyCostCapUSD   pgtype.Numeric     // nullable; Story 5.2 mutates
	CurrentMonthCostUSD pgtype.Numeric     // DEFAULT 0; Stories 5.2-5.4 mutate
	LastUsedAt          pgtype.Timestamptz // nullable; Story-3.2 fire-and-forget UPDATE
	RevokedAt           pgtype.Timestamptz // nullable; .Valid==false when key is active
	CreatedAt           time.Time
	// ContentSafetyStrictness is the Story-8.4 per-Key 内容安全 level
	// (content_safety_strictness VARCHAR(10) NOT NULL DEFAULT 'strict' CHECK ∈
	// {strict,default,loose}). NOT NULL → never "" on a selected row. Carried on
	// the Validate hot path (gates the bidirectional filter) AND the List/Update
	// config read-back paths.
	ContentSafetyStrictness string
}

const (
	// lookupAPIKeysByPrefixSQL drives the AC2 Validate hot path. ORDER BY id
	// ASC for deterministic bcrypt iteration (BR-2.3). LIMIT bounds the
	// candidate fanout (BR-2.3 max_candidates=100).
	lookupAPIKeysByPrefixSQL = `SELECT id, user_id, team_id, key_prefix, key_hash, scope, monthly_cost_cap_usd, revoked_at, created_at, content_safety_strictness
FROM he_api.api_keys
WHERE key_prefix = $1
ORDER BY id ASC
LIMIT 101`

	// touchAPIKeyLastUsedSQL refreshes last_used_at on a successful Validate
	// (BR-2.5 fire-and-forget — caller invokes from a goroutine with its own
	// timeout context; failure is dropped).
	touchAPIKeyLastUsedSQL = `UPDATE he_api.api_keys SET last_used_at = NOW() WHERE id = $1`

	// insertAPIKeyForTestSQL drives IssueKeyForTest (apikey/seed_test.go).
	// Production key-issuance UX lands in Epic 5; this is test-only seeding.
	insertAPIKeyForTestSQL = `INSERT INTO he_api.api_keys (id, user_id, team_id, name, key_prefix, key_hash, scope, created_at)
VALUES (gen_random_uuid(), $1, NULL, $2, $3, $4, '{}'::jsonb, NOW())
RETURNING id`

	// insertAPIKeySQL drives the Story-5.1 CreateApiKey RPC (AC1). Server-
	// generated `id` (gen_random_uuid()) and `created_at` (NOW()) populate
	// the RETURNING clause so the handler can build the response without
	// a re-SELECT. team_id stays NULL per Architect Q3 defer (user-scoped
	// keys only in Story 5.1); scope starts at `{}` (Story 5.2 mutates);
	// monthly_cost_cap_usd is NULL by default; current_month_cost_usd is
	// the table DEFAULT 0; last_used_at NULL until first Validate hit;
	// revoked_at NULL.
	insertAPIKeySQL = `INSERT INTO he_api.api_keys (id, user_id, team_id, name, key_prefix, key_hash, scope, created_at)
VALUES (gen_random_uuid(), $1, NULL, $2, $3, $4, '{}'::jsonb, NOW())
RETURNING id, created_at`

	// listAPIKeysByUserSQL drives the Story-5.1 ListApiKeys RPC (AC2).
	// LIMIT 100 per BR-2.2 MVP cap; ORDER BY created_at DESC, id ASC for
	// deterministic ordering even when two rows share microsecond-equal
	// created_at (BR-2.3). **key_hash is INTENTIONALLY OMITTED from the
	// SELECT list** per BR-2.5 defence-in-depth — the column never enters
	// auth-svc memory on this path; 5.1-UNIT-019 grep-asserts.
	listAPIKeysByUserSQL = `SELECT id, user_id, team_id, name, key_prefix, scope,
       monthly_cost_cap_usd, current_month_cost_usd, last_used_at, revoked_at, created_at, content_safety_strictness
FROM he_api.api_keys
WHERE user_id = $1
ORDER BY created_at DESC, id ASC
LIMIT 100`

	// selectAPIKeyForUpdateSQL drives the Story-5.1 RevokeApiKey RPC (AC3).
	// FOR UPDATE serializes concurrent revoke attempts on the same id (the
	// idempotent path short-circuits before the UPDATE). Returns the full
	// row including `revoked_at` so the handler can detect "already revoked"
	// and `user_id` so the handler can apply the BR-3.2 anti-enumeration
	// owner check (cross-user → same NotFound envelope as miss).
	selectAPIKeyForUpdateSQL = `SELECT id, user_id, name, key_prefix, revoked_at
FROM he_api.api_keys
WHERE id = $1
LIMIT 1
FOR UPDATE`

	// updateAPIKeyRevokedAtSQL drives the Story-5.1 RevokeApiKey RPC UPDATE
	// path. Returns NOW() so the handler can populate the audit payload +
	// response without a re-SELECT.
	updateAPIKeyRevokedAtSQL = `UPDATE he_api.api_keys
SET revoked_at = NOW()
WHERE id = $1
RETURNING revoked_at`

	// selectAPIKeyConfigForUpdateSQL drives the Story-5.2 UpdateApiKey RPC
	// (AC1). Selects the FULL row (minus key_hash per BR-2.5 defence-in-depth)
	// so the handler can (a) apply the BR-1.8 anti-enumeration owner/revoke
	// guard, (b) read the current `scope` JSONB for the BR-1.7 partial-merge,
	// and (c) carry forward monthly_cost_cap_usd when the patch omits it.
	// FOR UPDATE serializes concurrent config writes on the same id
	// (last-writer-wins per Architect Q-G).
	selectAPIKeyConfigForUpdateSQL = `SELECT id, user_id, team_id, name, key_prefix, scope,
       monthly_cost_cap_usd, current_month_cost_usd, last_used_at, revoked_at, created_at, content_safety_strictness
FROM he_api.api_keys
WHERE id = $1
LIMIT 1
FOR UPDATE`

	// updateAPIKeyConfigSQL drives the Story-5.2 UpdateApiKey RPC UPDATE path.
	// The WHERE clause re-asserts `user_id = $2 AND revoked_at IS NULL` against
	// TOCTOU (a concurrent revoke between the SELECT and the UPDATE) — 0 rows
	// returned ⇒ the handler maps to NotFound (BR-1.8). NO updated_at write —
	// the schema carries `created_at` only (Architect Q-K ratified 2026-05-25;
	// the Kafka api_key.config_updated event `ts` is the last-modified SoT).
	// Returns the full updated row so the handler builds the response without
	// a re-SELECT.
	updateAPIKeyConfigSQL = `UPDATE he_api.api_keys
SET scope = $3, monthly_cost_cap_usd = $4, content_safety_strictness = $5
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
RETURNING id, user_id, team_id, name, key_prefix, scope,
          monthly_cost_cap_usd, current_month_cost_usd, last_used_at, revoked_at, created_at, content_safety_strictness`
)

// LookupAPIKeysByPrefix returns every api_keys row whose key_prefix matches
// the supplied 12-char prefix, ordered by id ASC (AC2 BR-2.3). Empty slice
// + nil-error indicates zero matches — caller treats as REASON_NOT_FOUND.
//
// Implementation notes:
//   - SELECT LIMIT 101 lets the caller detect the BR-2.3 "candidate count
//     exceeded" branch without an extra COUNT(*) round-trip: when len(rows)
//     == 101 the handler fails closed (REASON_NOT_FOUND + ERROR log).
//   - The schema-qualified `he_api.api_keys` reference is intentional — the
//     auth-svc binary runs with search_path defaults that may not include
//     `he_api` first; explicit qualification matches the migration precedent
//     from Stories 1.6 / 2.2 / 2.4 / 2.5 / 2.6.
func LookupAPIKeysByPrefix(ctx context.Context, q Querier, prefix string) ([]ApiKeyRow, error) {
	rows, err := q.Query(ctx, lookupAPIKeysByPrefixSQL, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ApiKeyRow, 0, 1)
	for rows.Next() {
		var r ApiKeyRow
		if err := rows.Scan(
			&r.ID, &r.UserID, &r.TeamID,
			&r.KeyPrefix, &r.KeyHash, &r.Scope,
			&r.MonthlyCostCapUSD, &r.RevokedAt, &r.CreatedAt, &r.ContentSafetyStrictness,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// TouchAPIKeyLastUsed updates last_used_at to NOW() for the matched row.
// Per AC2 BR-2.5 callers invoke this from a goroutine with a short timeout
// context; failure here is logged at DEBUG and dropped — it MUST NOT block
// the validation response.
func TouchAPIKeyLastUsed(ctx context.Context, q Querier, apiKeyID uuid.UUID) error {
	_, err := q.Exec(ctx, touchAPIKeyLastUsedSQL, apiKeyID)
	return err
}

// InsertAPIKeyForTest inserts a single row using the supplied hash + prefix.
// Test-only — the only caller is apikey.IssueKeyForTest in the
// auth-svc/internal/apikey package (file is `seed_test.go` so the helper
// cannot link into the production binary, AC2 BR-2.8).
func InsertAPIKeyForTest(ctx context.Context, q Querier, userID uuid.UUID, name, prefix, hash string) (uuid.UUID, error) {
	pgxRow := q.QueryRow(ctx, insertAPIKeyForTestSQL, userID, name, prefix, hash)
	var id uuid.UUID
	if err := pgxRow.Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Compile-time check — pgx.Rows is satisfied by the production *pgxpool.Rows
// AND by pgxmock's mocked rows. No runtime guard needed.
var _ pgx.Rows = pgx.Rows(nil)

// ErrAPIKeyNotFound is returned by SelectAPIKeyForUpdate when WHERE id=$1
// matches zero rows. The handler maps this to gRPC NotFound (BR-3.2 anti-
// enumeration — same envelope as cross-user-owned id).
var ErrAPIKeyNotFound = errors.New("repository: api_key not found")

// InsertAPIKey persists a new api_keys row for the Story-5.1 CreateApiKey
// path. Returns the server-generated id + created_at so the caller can
// build the gRPC response without a re-SELECT.
//
// Callers MUST pre-hash the plaintext (apikey.Generate produces the bcrypt
// output) — this function NEVER sees plaintext, and the `keyHash` parameter
// is treated as opaque bytes by PG.
func InsertAPIKey(ctx context.Context, q Querier, userID uuid.UUID, name, keyPrefix, keyHash string) (uuid.UUID, time.Time, error) {
	var (
		id        uuid.UUID
		createdAt time.Time
	)
	row := q.QueryRow(ctx, insertAPIKeySQL, userID, name, keyPrefix, keyHash)
	if err := row.Scan(&id, &createdAt); err != nil {
		return uuid.Nil, time.Time{}, err
	}
	return id, createdAt, nil
}

// ListAPIKeysByUser returns 0..100 api_keys rows owned by userID, ordered
// by created_at DESC then id ASC for stable tie-breaks (BR-2.3). The
// `key_hash` column is EXPLICITLY OMITTED from the SELECT list (BR-2.5
// defence-in-depth — never enters auth-svc memory on this path).
//
// Empty slice + nil error indicates "no keys" — handler returns the
// canonical empty-list response (BR-2.8).
func ListAPIKeysByUser(ctx context.Context, q Querier, userID uuid.UUID) ([]ApiKeyRow, error) {
	rows, err := q.Query(ctx, listAPIKeysByUserSQL, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ApiKeyRow, 0, 8)
	for rows.Next() {
		var r ApiKeyRow
		if err := rows.Scan(
			&r.ID, &r.UserID, &r.TeamID,
			&r.Name, &r.KeyPrefix, &r.Scope,
			&r.MonthlyCostCapUSD, &r.CurrentMonthCostUSD,
			&r.LastUsedAt, &r.RevokedAt, &r.CreatedAt, &r.ContentSafetyStrictness,
		); err != nil {
			return nil, err
		}
		// Defence-in-depth: KeyHash is the only sensitive field that could
		// leak via repository struct access. We didn't SELECT it; this is
		// the assertion at the struct boundary.
		r.KeyHash = ""
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SelectAPIKeyForUpdate fetches the minimal projection (id, user_id, name,
// key_prefix, revoked_at) needed by the Story-5.1 RevokeApiKey RPC, holding
// a FOR UPDATE row-lock so concurrent revokes serialize. Returns
// ErrAPIKeyNotFound when WHERE id=$1 matches zero rows (handler MUST map
// to gRPC NotFound per BR-3.2).
//
// The caller MUST invoke this inside a pgx.Tx so the FOR UPDATE lock is
// released by the surrounding COMMIT / ROLLBACK; calling against a pool
// directly works but degrades the lock to a no-op (the row is unlocked
// immediately after this function returns).
func SelectAPIKeyForUpdate(ctx context.Context, q Querier, apiKeyID uuid.UUID) (ApiKeyRow, error) {
	var r ApiKeyRow
	row := q.QueryRow(ctx, selectAPIKeyForUpdateSQL, apiKeyID)
	if err := row.Scan(&r.ID, &r.UserID, &r.Name, &r.KeyPrefix, &r.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ApiKeyRow{}, ErrAPIKeyNotFound
		}
		return ApiKeyRow{}, err
	}
	return r, nil
}

// UpdateAPIKeyRevokedAt sets revoked_at = NOW() and returns the new
// timestamp. Caller MUST have already validated row existence + ownership
// via SelectAPIKeyForUpdate; UPDATE here is unconditional (matched-by-id).
func UpdateAPIKeyRevokedAt(ctx context.Context, q Querier, apiKeyID uuid.UUID) (time.Time, error) {
	var revokedAt time.Time
	row := q.QueryRow(ctx, updateAPIKeyRevokedAtSQL, apiKeyID)
	if err := row.Scan(&revokedAt); err != nil {
		return time.Time{}, err
	}
	return revokedAt, nil
}

// scanAPIKeyConfigRow scans the full-row projection shared by
// selectAPIKeyConfigForUpdateSQL and updateAPIKeyConfigSQL. key_hash is
// NEVER selected (BR-2.5) so the field is left zero-valued.
func scanAPIKeyConfigRow(row pgx.Row) (ApiKeyRow, error) {
	var r ApiKeyRow
	if err := row.Scan(
		&r.ID, &r.UserID, &r.TeamID, &r.Name, &r.KeyPrefix, &r.Scope,
		&r.MonthlyCostCapUSD, &r.CurrentMonthCostUSD, &r.LastUsedAt, &r.RevokedAt, &r.CreatedAt,
		&r.ContentSafetyStrictness,
	); err != nil {
		return ApiKeyRow{}, err
	}
	return r, nil
}

// SelectAPIKeyConfigForUpdate fetches the full api_keys row (minus key_hash)
// under a FOR UPDATE row-lock for the Story-5.2 UpdateApiKey RPC (AC1).
// Returns ErrAPIKeyNotFound when WHERE id=$1 matches zero rows. The caller
// MUST hold a pgx.Tx for the lock to outlive this call (see
// SelectAPIKeyForUpdate's note on pool-vs-tx lock degradation).
func SelectAPIKeyConfigForUpdate(ctx context.Context, q Querier, apiKeyID uuid.UUID) (ApiKeyRow, error) {
	r, err := scanAPIKeyConfigRow(q.QueryRow(ctx, selectAPIKeyConfigForUpdateSQL, apiKeyID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ApiKeyRow{}, ErrAPIKeyNotFound
		}
		return ApiKeyRow{}, err
	}
	return r, nil
}

// CapNotificationContext is the single-statement JOIN projection feeding the
// Story-5.4 notification-svc monthly-cap email path (Architect Round 1 Q-L
// Fix-A). UserDisplayName is COALESCE'd to "" when the column is NULL so the
// caller's resolveDisplayName fallback (display_name → email local-part) sees a
// clean empty string rather than a SQL NULL.
type CapNotificationContext struct {
	UserEmail            string
	UserLocale           string
	UserDisplayName      string
	KeyName              string
	KeyMonthlyCostCapUSD string // NUMERIC(10,2)::text e.g. "50.00"; "" if NULL
}

const (
	// lookupCapNotificationContextSQL drives the Story-5.4 GetCapNotificationContext
	// RPC. Single round-trip JOIN; auth-svc remains the sole canonical reader
	// of the PII-sensitive api_keys table (notification-svc reaches it only via
	// this gRPC hop, never a cross-module repository import).
	lookupCapNotificationContextSQL = `SELECT u.email, u.locale, COALESCE(u.display_name, ''), ak.name, COALESCE(ak.monthly_cost_cap_usd::text, '')
FROM he_api.users u
JOIN he_api.api_keys ak ON u.id = ak.user_id
WHERE ak.id = $1
LIMIT 1`

	// resetMonthlyCostsSQL drives the Story-5.4 monthly-cost-reset CronJob
	// (AC3 BR-3.5). Column-level constant assignment — idempotent on re-run.
	// `WHERE revoked_at IS NULL` freezes revoked rows' aggregates for audit.
	resetMonthlyCostsSQL = `UPDATE he_api.api_keys SET current_month_cost_usd = 0 WHERE revoked_at IS NULL`
)

// LookupCapNotificationContext returns the user email/locale/display_name + key
// name for a single api_key id (Story-5.4 Q-L Fix-A). Returns ErrAPIKeyNotFound
// on zero rows (the handler maps to gRPC NotFound — anti-enumeration parity
// with Story-5.1; the payload is internal-only).
func LookupCapNotificationContext(ctx context.Context, q Querier, apiKeyID uuid.UUID) (CapNotificationContext, error) {
	var c CapNotificationContext
	row := q.QueryRow(ctx, lookupCapNotificationContextSQL, apiKeyID)
	if err := row.Scan(&c.UserEmail, &c.UserLocale, &c.UserDisplayName, &c.KeyName, &c.KeyMonthlyCostCapUSD); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CapNotificationContext{}, ErrAPIKeyNotFound
		}
		return CapNotificationContext{}, err
	}
	return c, nil
}

// ResetMonthlyCosts zeroes current_month_cost_usd for every non-revoked
// api_keys row (Story-5.4 AC3 BR-3.5). Returns rows_affected. Idempotent —
// re-running sets the same column to the same value.
func ResetMonthlyCosts(ctx context.Context, q Querier) (int64, error) {
	tag, err := q.Exec(ctx, resetMonthlyCostsSQL)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// UpdateAPIKeyConfig writes the merged scope JSONB + monthly_cost_cap_usd +
// content_safety_strictness for the Story-5.2/8.4 UpdateApiKey RPC. The WHERE
// clause re-asserts ownership + revoke-state against TOCTOU; a concurrent revoke
// between SELECT and UPDATE collapses to ErrAPIKeyNotFound (BR-1.8). Returns the
// full updated row. `cap` with Valid=false stores SQL NULL ("no cap"). `strictness`
// is the already-resolved level (caller preserves the existing value when the
// patch omits it; the column is NOT NULL so this is never "").
func UpdateAPIKeyConfig(ctx context.Context, q Querier, apiKeyID, userID uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (ApiKeyRow, error) {
	r, err := scanAPIKeyConfigRow(q.QueryRow(ctx, updateAPIKeyConfigSQL, apiKeyID, userID, scope, cap, strictness))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ApiKeyRow{}, ErrAPIKeyNotFound
		}
		return ApiKeyRow{}, err
	}
	return r, nil
}
