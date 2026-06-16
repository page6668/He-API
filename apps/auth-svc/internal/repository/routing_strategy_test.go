// Story 6.5 — repository persistence of users.default_routing_strategy.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// Scenario: 6.5 (repo) — GetProfileByID hydrates a populated
// default_routing_strategy through the User struct after the 0019 ALTER.
func TestGetProfileByID_DefaultRoutingStrategyPopulated(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	id := uuid.New()
	now := time.Now().UTC()
	want := "cost"

	mock.ExpectQuery(`SELECT .*default_routing_strategy.* FROM he_api\.users WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows(profileColumns).AddRow(
			id, "user@example.com", []byte("$2a$12$hash"), &now,
			nil, nil, "en", "UTC",
			false, "active", now, now,
			(*string)(nil), &want,
		))

	user, err := repository.GetProfileByID(context.Background(), mock, id)
	if err != nil {
		t.Fatalf("GetProfileByID: %v", err)
	}
	if user.DefaultRoutingStrategy == nil || *user.DefaultRoutingStrategy != want {
		t.Fatalf("DefaultRoutingStrategy = %v, want %q", user.DefaultRoutingStrategy, want)
	}
}

// Scenario: 6.5-INT-001 (repo) — UpdateProfile persists default_routing_strategy
// via the SET clause + RETURNING projection; the returned User reflects it.
func TestUpdateProfile_DefaultRoutingStrategy_Set(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	id := uuid.New()
	now := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	newer := now.Add(time.Second)
	val := "cost"

	mock.ExpectQuery(`SELECT updated_at, status FROM he_api\.users WHERE id=\$1 FOR UPDATE`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at", "status"}).AddRow(now, "active"))

	mock.ExpectQuery(`(?s)UPDATE he_api\.users SET updated_at=NOW\(\), default_routing_strategy=\$1 WHERE id=\$2 RETURNING `).
		WithArgs(&val, id).
		WillReturnRows(pgxmock.NewRows(profileColumns).AddRow(
			id, "user@example.com", []byte("$2a$12$hash"), &now,
			nil, nil, "en", "UTC",
			false, "active", now, newer,
			(*string)(nil), &val,
		))

	got, err := repository.UpdateProfile(context.Background(), mock, id, now.UnixMicro(),
		repository.UpdateProfileParams{DefaultRoutingStrategy: &val, DefaultRoutingStrategySet: true})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if got.DefaultRoutingStrategy == nil || *got.DefaultRoutingStrategy != "cost" {
		t.Fatalf("DefaultRoutingStrategy = %v, want cost", got.DefaultRoutingStrategy)
	}
}

// Scenario: 6.5-UNIT-011 (repo) — clear: a nil *string with Set=true binds SQL
// NULL through the pgx parameter (column cleared).
func TestUpdateProfile_DefaultRoutingStrategy_ClearToNull(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	id := uuid.New()
	now := time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT updated_at, status FROM he_api\.users WHERE id=\$1 FOR UPDATE`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at", "status"}).AddRow(now, "active"))

	mock.ExpectQuery(`(?s)UPDATE he_api\.users SET updated_at=NOW\(\), default_routing_strategy=\$1 WHERE id=\$2 RETURNING `).
		WithArgs((*string)(nil), id).
		WillReturnRows(pgxmock.NewRows(profileColumns).AddRow(
			id, "user@example.com", []byte("$2a$12$hash"), &now,
			nil, nil, "en", "UTC",
			false, "active", now, now.Add(time.Millisecond),
			(*string)(nil), (*string)(nil),
		))

	got, err := repository.UpdateProfile(context.Background(), mock, id, now.UnixMicro(),
		repository.UpdateProfileParams{DefaultRoutingStrategy: nil, DefaultRoutingStrategySet: true})
	if err != nil {
		t.Fatalf("UpdateProfile clear: %v", err)
	}
	if got.DefaultRoutingStrategy != nil {
		t.Fatalf("DefaultRoutingStrategy = %v, want nil after clear", got.DefaultRoutingStrategy)
	}
}
