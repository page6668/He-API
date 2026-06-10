// Story 7.7 AC3 — BillingService invoice retrieval handler tests. 7.7-INT-080 list,
// 7.7-INT-081 owner download (proxy-stream), 7.7-INT-082 IDOR (foreign id → 404).
package grpc

import (
	"bytes"
	"context"
	"io"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

type fakeUploader struct{ data map[string][]byte }

func (f fakeUploader) PutObject(_ context.Context, key string, body io.Reader) error {
	b, _ := io.ReadAll(body)
	f.data[key] = b
	return nil
}
func (f fakeUploader) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data[key])), nil
}

func TestListInvoices_JSON(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.invoices WHERE user_id").WithArgs("u1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "period", "debit", "credit", "currency", "status", "has_pdf", "created_at"}).
			AddRow("inv1", "2026-06", "4.5600", "20.0000", "USD", "emailed", true, "2026-07-01T00:00:00Z"))
	s := NewServer(mock, nil)
	resp, err := s.ListInvoices(context.Background(), connect.NewRequest(&billingv1.ListInvoicesRequest{UserId: "u1"}))
	if err != nil {
		t.Fatalf("ListInvoices: %v", err)
	}
	if !bytes.Contains([]byte(resp.Msg.GetInvoicesJson()), []byte("2026-06")) {
		t.Fatalf("invoices_json missing period: %s", resp.Msg.GetInvoicesJson())
	}
	// Money is a string-decimal, never a JSON number.
	if !bytes.Contains([]byte(resp.Msg.GetInvoicesJson()), []byte(`"4.5600"`)) {
		t.Fatalf("money not string-decimal in JSON: %s", resp.Msg.GetInvoicesJson())
	}
}

func TestGetInvoicePdf_OwnerStream(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.invoices WHERE id").WithArgs("inv1", "u1").
		WillReturnRows(pgxmock.NewRows([]string{"period", "debit", "credit", "status", "key"}).
			AddRow("2026-06", "4.5600", "20.0000", "emailed", "invoices/u1/inv1.pdf"))
	s := NewServer(mock, nil)
	s.SetInvoiceUploader(fakeUploader{data: map[string][]byte{"invoices/u1/inv1.pdf": []byte("%PDF-1.4 body %%EOF")}})

	resp, err := s.GetInvoicePdf(context.Background(), connect.NewRequest(&billingv1.GetInvoicePdfRequest{UserId: "u1", InvoiceId: "inv1"}))
	if err != nil {
		t.Fatalf("GetInvoicePdf: %v", err)
	}
	if !resp.Msg.GetFound() || !bytes.HasPrefix(resp.Msg.GetPdf(), []byte("%PDF-")) {
		t.Fatalf("did not stream the PDF: %+v", resp.Msg)
	}
	if resp.Msg.GetFilename() != "invoice-2026-06.pdf" {
		t.Fatalf("filename = %q", resp.Msg.GetFilename())
	}
}

// 7.7-INT-082 — a foreign invoice id → CodeNotFound (gateway 404, no existence
// disclosure). The object store is never even consulted.
func TestGetInvoicePdf_ForeignIDOR(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.invoices WHERE id").WithArgs("inv1", "attacker").
		WillReturnError(pgx.ErrNoRows)
	s := NewServer(mock, nil)
	s.SetInvoiceUploader(fakeUploader{data: map[string][]byte{}})

	_, err := s.GetInvoicePdf(context.Background(), connect.NewRequest(&billingv1.GetInvoicePdfRequest{UserId: "attacker", InvoiceId: "inv1"}))
	if err == nil {
		t.Fatal("expected NotFound for a foreign invoice id")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound (404, not 403)", connect.CodeOf(err))
	}
}
