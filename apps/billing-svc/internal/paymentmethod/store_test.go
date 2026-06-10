// Story 7.7 AC1 (saved methods) — payment_methods store tests. The security
// invariants: the display-safe List NEVER returns the token (7.7-INT-011), the
// owner-scoped read rejects a foreign id (cross-user binding, 7.7-INT-002/013),
// and Delete cascades to disable a referencing auto-recharge (7.7-INT-012).
package paymentmethod

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"
)

// 7.7-INT-010 — Save binds the token to the user, makes it default (unsetting
// priors) in ONE tx, and returns the new id.
func TestSave_BindsAndDefaults(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE he_api.payment_methods SET is_default = FALSE").WithArgs("u1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery("INSERT INTO he_api.payment_methods").
		WithArgs("u1", "stripe", "pm_tok_secret", "visa", "4242").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("m1"))
	mock.ExpectCommit()

	s := New(mock)
	id, isDefault, err := s.Save(context.Background(), "u1", "stripe", "pm_tok_secret", "visa", "4242")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id != "m1" || !isDefault {
		t.Fatalf("id=%q isDefault=%v, want m1/true", id, isDefault)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestSave_RejectsMissing(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	s := New(mock)
	if _, _, err := s.Save(context.Background(), "", "stripe", "tok", "", ""); err != ErrInvalid {
		t.Errorf("empty user: %v, want ErrInvalid", err)
	}
	if _, _, err := s.Save(context.Background(), "u1", "stripe", "", "", ""); err != ErrInvalid {
		t.Errorf("empty token: %v, want ErrInvalid", err)
	}
}

// 7.7-INT-011 — List returns display-safe rows; the Method struct has no token
// field, so the token can never leak through this path.
func TestList_DisplaySafeNoToken(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("SELECT id::text, payment_provider").WithArgs("u1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "provider", "brand", "last4", "is_default", "created_at"}).
			AddRow("m1", "stripe", "visa", "4242", true, "2026-06-10T00:00:00Z"))
	s := New(mock)
	methods, err := s.List(context.Background(), "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(methods) != 1 || methods[0].Brand != "visa" || methods[0].Last4 != "4242" || !methods[0].IsDefault {
		t.Fatalf("unexpected methods: %+v", methods)
	}
	// Static guarantee: a serialized Method never carries a token-shaped field.
	if strings.Contains(strings.ToLower(methods[0].Provider+methods[0].Brand+methods[0].Last4), "pm_tok") {
		t.Fatalf("token leaked into display-safe projection")
	}
}

func TestList_EmptyIsEmptySlice(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("SELECT id::text, payment_provider").WithArgs("u1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "provider", "brand", "last4", "is_default", "created_at"}))
	s := New(mock)
	methods, err := s.List(context.Background(), "u1")
	if err != nil || methods == nil || len(methods) != 0 {
		t.Fatalf("want empty non-nil slice, got %v err=%v", methods, err)
	}
}

// 7.7-INT-002 — the owner-scoped token read rejects a foreign id (a method that is
// not the caller's resolves to found=false, never another user's token).
func TestGetOwnedToken_ForeignRejected(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("SELECT provider_pm_token").WithArgs("pm1", "attacker").
		WillReturnError(pgx.ErrNoRows)
	s := New(mock)
	tok, _, found, err := s.GetOwnedToken(context.Background(), "attacker", "pm1")
	if err != nil {
		t.Fatalf("GetOwnedToken: %v", err)
	}
	if found || tok != "" {
		t.Fatalf("foreign id resolved a token: found=%v tok=%q", found, tok)
	}
}

// 7.7-INT-012 / 7.7-BLIND-DATA-002 — Delete a referenced method → revoke cascade:
// the auto-recharge that pointed at it is disabled, and the row is removed.
func TestDelete_RevokeCascade(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE he_api.balances SET auto_recharge_enabled = FALSE").WithArgs("u1", "m1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1)) // it WAS referenced
	mock.ExpectExec("DELETE FROM he_api.payment_methods").WithArgs("m1", "u1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	s := New(mock)
	deleted, disabled, err := s.Delete(context.Background(), "u1", "m1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted || !disabled {
		t.Fatalf("deleted=%v disabled=%v, want both true", deleted, disabled)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 7.7-INT-013 — Delete of a foreign id removes nothing (IDOR guard → gateway 404).
func TestDelete_ForeignRemovesNothing(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE he_api.balances SET auto_recharge_enabled = FALSE").WithArgs("attacker", "m1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("DELETE FROM he_api.payment_methods").WithArgs("m1", "attacker").
		WillReturnResult(pgxmock.NewResult("DELETE", 0)) // not the owner → nothing removed
	mock.ExpectRollback()

	s := New(mock)
	deleted, disabled, err := s.Delete(context.Background(), "attacker", "m1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted || disabled {
		t.Fatalf("foreign delete affected state: deleted=%v disabled=%v", deleted, disabled)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}
