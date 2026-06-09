package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
)

// 7.1-CONTRACT-001 — BillingService.CheckBalance round-trips over the real
// Connect handler+client (validating the hand-authored proto on the wire) and
// returns the PG-authoritative current_usd as a string-decimal + sufficiency.
func TestCheckBalance_Contract_RoundTrip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("12.3400"))

	_, handler := billingv1connect.NewBillingServiceHandler(NewServer(mock, nil))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := billingv1connect.NewBillingServiceClient(http.DefaultClient, srv.URL)
	resp, err := client.CheckBalance(context.Background(),
		connect.NewRequest(&billingv1.CheckBalanceRequest{UserId: "u-1"}))
	if err != nil {
		t.Fatalf("CheckBalance: %v", err)
	}
	if resp.Msg.GetCurrentUsd() != "12.3400" {
		t.Fatalf("current_usd = %q, want 12.3400 (string-decimal)", resp.Msg.GetCurrentUsd())
	}
	if !resp.Msg.GetSufficient() {
		t.Fatal("sufficient = false, want true (12.34 > 0)")
	}
}

// Absent balances row → "0.0000" / not sufficient (lazy-uncreated reads zero).
func TestCheckBalance_AbsentRow(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-2").
		WillReturnError(pgx.ErrNoRows)

	resp, err := NewServer(mock, nil).CheckBalance(context.Background(),
		connect.NewRequest(&billingv1.CheckBalanceRequest{UserId: "u-2"}))
	if err != nil {
		t.Fatalf("CheckBalance: %v", err)
	}
	if resp.Msg.GetCurrentUsd() != "0.0000" || resp.Msg.GetSufficient() {
		t.Fatalf("absent row = %+v, want current_usd=0.0000 sufficient=false", resp.Msg)
	}
}

// Empty user_id → InvalidArgument (no PG query).
func TestCheckBalance_EmptyUserID(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	_, err := NewServer(mock, nil).CheckBalance(context.Background(),
		connect.NewRequest(&billingv1.CheckBalanceRequest{UserId: "  "}))
	if err == nil {
		t.Fatal("expected InvalidArgument for empty user_id")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if e := mock.ExpectationsWereMet(); e != nil {
		t.Fatalf("no PG query expected: %v", e)
	}
}

// Negative balance → not sufficient (the gate's hard-zero threshold, BR-A-6).
func TestCheckBalance_NegativeNotSufficient(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-3").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("-0.0186"))

	resp, err := NewServer(mock, nil).CheckBalance(context.Background(),
		connect.NewRequest(&billingv1.CheckBalanceRequest{UserId: "u-3"}))
	if err != nil {
		t.Fatalf("CheckBalance: %v", err)
	}
	if resp.Msg.GetCurrentUsd() != "-0.0186" || resp.Msg.GetSufficient() {
		t.Fatalf("negative = %+v, want current_usd=-0.0186 sufficient=false", resp.Msg)
	}
}
