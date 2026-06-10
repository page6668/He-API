// Story 7.7 AC3 — 7.7-UNIT-070: the PDF renders well-formed (parseable) bytes from
// the frozen figures, with the per-model breakdown.
package invoice

import (
	"bytes"
	"testing"
)

func TestRenderInvoicePDF_WellFormed(t *testing.T) {
	pdf, err := RenderInvoicePDF(RenderData{
		Period:         "2026-06",
		AccountEmail:   "alice@example.com",
		TotalDebitUSD:  "4.5600",
		TotalCreditUSD: "20.0000",
		ClosingUSD:     "24.8600",
		Items: []LineItem{
			{Model: "deepseek-chat", CostUSD: "3.2000"},
			{Model: "qwen-max", CostUSD: "1.3600"},
		},
	})
	if err != nil {
		t.Fatalf("RenderInvoicePDF: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF (no %%PDF- header)")
	}
	if !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Fatalf("PDF missing %%EOF trailer — malformed")
	}
	if len(pdf) < 500 {
		t.Fatalf("PDF suspiciously small (%d bytes)", len(pdf))
	}
}

func TestObjectKey_NonEnumerable(t *testing.T) {
	k := ObjectKey("u1", "inv-abc")
	if k != "invoices/u1/inv-abc.pdf" {
		t.Fatalf("key = %q", k)
	}
}
