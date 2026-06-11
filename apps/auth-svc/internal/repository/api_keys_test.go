package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

func apiKeysRowCols() []string {
	// Story 5.2 — lookupAPIKeysByPrefixSQL selects monthly_cost_cap_usd (between
	// scope and revoked_at) so the Validate hot path can carry the cap to the
	// gateway keypolicy middleware (AC4). Story 8.4 appends
	// content_safety_strictness (last) so the gateway gates the content-safety
	// filter from the bearer cache.
	return []string{"id", "user_id", "team_id", "key_prefix", "key_hash", "scope", "monthly_cost_cap_usd", "revoked_at", "created_at", "content_safety_strictness"}
}

// Scenario: 3.2-UNIT-001
// LookupAPIKeysByPrefix returns empty slice + nil-error for a prefix with
// zero matching rows. Caller treats as REASON_NOT_FOUND.
func TestLookupAPIKeysByPrefix_Empty(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	mock.ExpectQuery(`SELECT id, user_id, team_id, key_prefix, key_hash, scope, monthly_cost_cap_usd, revoked_at, created_at, content_safety_strictness\s+FROM he_api\.api_keys\s+WHERE key_prefix = \$1`).
		WithArgs("he-NOPREFIX").
		WillReturnRows(pgxmock.NewRows(apiKeysRowCols()))

	rows, err := repository.LookupAPIKeysByPrefix(context.Background(), mock, "he-NOPREFIX")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0", len(rows))
	}
}

// Scenario: 3.2-UNIT-002
// LookupAPIKeysByPrefix returns a single populated row when the prefix
// matches exactly one row.
func TestLookupAPIKeysByPrefix_SingleRow(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	wantID := uuid.New()
	wantUser := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`SELECT .* FROM he_api\.api_keys WHERE key_prefix = \$1`).
		WithArgs("he-ABC123XY").
		WillReturnRows(pgxmock.NewRows(apiKeysRowCols()).
			AddRow(wantID, wantUser, pgtype.UUID{}, "he-ABC123XY", "$2a$04$h", []byte(`{}`), pgtype.Numeric{}, pgtype.Timestamptz{}, now, "strict"),
		)

	rows, err := repository.LookupAPIKeysByPrefix(context.Background(), mock, "he-ABC123XY")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ID != wantID {
		t.Errorf("id = %s, want %s", rows[0].ID, wantID)
	}
	if rows[0].UserID != wantUser {
		t.Errorf("user_id = %s, want %s", rows[0].UserID, wantUser)
	}
	if rows[0].KeyHash != "$2a$04$h" {
		t.Errorf("hash mismatch")
	}
}

// Scenario: 3.2-UNIT-003
// LookupAPIKeysByPrefix returns multiple rows for a colliding prefix,
// preserving ORDER BY id ASC determinism (BR-2.1 / BR-2.3).
func TestLookupAPIKeysByPrefix_MultiRowDeterministic(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	id1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	id2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	now := time.Now()
	user := uuid.New()

	mock.ExpectQuery(`SELECT .* FROM he_api\.api_keys WHERE key_prefix = \$1`).
		WithArgs("he-COLLIDED").
		WillReturnRows(pgxmock.NewRows(apiKeysRowCols()).
			AddRow(id1, user, pgtype.UUID{}, "he-COLLIDED", "$2a$04$1", []byte(`{}`), pgtype.Numeric{}, pgtype.Timestamptz{}, now, "strict").
			AddRow(id2, user, pgtype.UUID{}, "he-COLLIDED", "$2a$04$2", []byte(`{}`), pgtype.Numeric{}, pgtype.Timestamptz{}, now, "strict"),
		)

	rows, err := repository.LookupAPIKeysByPrefix(context.Background(), mock, "he-COLLIDED")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].ID != id1 || rows[1].ID != id2 {
		t.Errorf("row order: got [%s, %s], want [%s, %s]", rows[0].ID, rows[1].ID, id1, id2)
	}
}

// LookupAPIKeysByPrefix propagates DB errors verbatim so the service layer
// can map them to connect.CodeUnavailable.
func TestLookupAPIKeysByPrefix_DBError(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	dbErr := errors.New("conn refused")
	mock.ExpectQuery(`SELECT .* FROM he_api\.api_keys`).
		WithArgs("he-FOOBAR1234").
		WillReturnError(dbErr)

	_, err := repository.LookupAPIKeysByPrefix(context.Background(), mock, "he-FOOBAR1234")
	if !errors.Is(err, dbErr) {
		t.Fatalf("err = %v, want %v", err, dbErr)
	}
}

// TouchAPIKeyLastUsed parameterises by the api_keys.id UUID.
func TestTouchAPIKeyLastUsed(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	id := uuid.New()
	mock.ExpectExec(`UPDATE he_api\.api_keys SET last_used_at = NOW\(\) WHERE id = \$1`).
		WithArgs(id).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repository.TouchAPIKeyLastUsed(context.Background(), mock, id); err != nil {
		t.Fatalf("err: %v", err)
	}
}
