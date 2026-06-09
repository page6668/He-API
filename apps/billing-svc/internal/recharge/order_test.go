// Story 7.3 — 7.3-CONTRACT-001 / 7.3-INT-001. The recharge Writer inserts a
// PENDING order (external_order_id left NULL — bound at credit time) and returns
// the generated id; a non-positive / unparseable amount is rejected before any
// write (money discipline, Q-Spec-4).
package recharge

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v3"
)

func TestCreate_InsertsPendingOrder(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("INSERT INTO he_api.recharge_orders").
		WithArgs("user-1", "50.0000", "USD", "stripe").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("order-1"))

	w := New(mock)
	id, err := w.Create(context.Background(), "user-1", "50.00", "USD", "stripe")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "order-1" {
		t.Fatalf("id = %q, want order-1", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreate_RejectsBadAmount(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	// No DB call expected — validation rejects before the write.
	w := New(mock)
	for _, amt := range []string{"0", "-5.00", "abc", ""} {
		if _, err := w.Create(context.Background(), "user-1", amt, "USD", "stripe"); !errors.Is(err, ErrInvalid) {
			t.Errorf("amount %q: err = %v, want ErrInvalid", amt, err)
		}
	}
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	w := New(mock)
	if _, err := w.Create(context.Background(), "", "50.00", "USD", "stripe"); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty user: err = %v, want ErrInvalid", err)
	}
	if _, err := w.Create(context.Background(), "user-1", "50.00", "USD", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty provider: err = %v, want ErrInvalid", err)
	}
}
