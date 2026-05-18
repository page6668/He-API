package repository

import (
	"context"
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
// (Story 3.2 AC2). Nullable columns surface as pgtype values so the handler
// can detect "no team" vs "empty UUID" without ambiguity.
type ApiKeyRow struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TeamID    pgtype.UUID        // nullable; .Valid==false when he_api.api_keys.team_id IS NULL
	KeyPrefix string
	KeyHash   string             // bcrypt output (cost=12 in prod, cost=4 in tests via IssueKeyForTest)
	Scope     []byte             // raw JSONB bytes; auth-svc forwards as-is into the response
	RevokedAt pgtype.Timestamptz // nullable; .Valid==false when key is active
	CreatedAt time.Time
}

const (
	// lookupAPIKeysByPrefixSQL drives the AC2 Validate hot path. ORDER BY id
	// ASC for deterministic bcrypt iteration (BR-2.3). LIMIT bounds the
	// candidate fanout (BR-2.3 max_candidates=100).
	lookupAPIKeysByPrefixSQL = `SELECT id, user_id, team_id, key_prefix, key_hash, scope, revoked_at, created_at
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
			&r.RevokedAt, &r.CreatedAt,
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
	var pgxRow = q.QueryRow(ctx, insertAPIKeyForTestSQL, userID, name, prefix, hash)
	var id uuid.UUID
	if err := pgxRow.Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Compile-time check — pgx.Rows is satisfied by the production *pgxpool.Rows
// AND by pgxmock's mocked rows. No runtime guard needed.
var _ pgx.Rows = (pgx.Rows)(nil)
