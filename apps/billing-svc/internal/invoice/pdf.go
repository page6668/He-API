// Story 7.7 AC3 — invoice PDF render (pure-Go go-pdf/fpdf, Architect Q-PDF ruling).
// The PDF is reproducible byte-for-byte from the FROZEN invoice row (BR-I-2). Note
// (Dev scope): the document labels render in English (the core latin font); the
// invoice EMAIL is localized per the notification-svc templates. Full PDF-body
// localization (CJK font embed) is a documented follow-up — the figures + structure
// are locale-independent.
package invoice

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/go-pdf/fpdf"
)

// LineItem is a per-model usage row in the breakdown.
type LineItem struct {
	Model      string
	CostUSD    string // string-decimal
}

// RenderData is the frozen figures + breakdown the PDF renders.
type RenderData struct {
	Period         string
	AccountEmail   string
	TotalDebitUSD  string
	TotalCreditUSD string
	ClosingUSD     string
	Items          []LineItem
}

// RenderInvoicePDF produces a well-formed single-page A4 PDF from the frozen
// figures. The returned bytes start with `%PDF-` and end with `%%EOF`.
func RenderInvoicePDF(d RenderData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetTitle(fmt.Sprintf("He-API Invoice %s", d.Period), false)
	pdf.AddPage()

	pdf.SetFont("Helvetica", "B", 18)
	pdf.Cell(0, 12, "He-API Invoice")
	pdf.Ln(14)

	pdf.SetFont("Helvetica", "", 11)
	line := func(label, val string) {
		pdf.SetFont("Helvetica", "B", 11)
		pdf.Cell(50, 8, label)
		pdf.SetFont("Helvetica", "", 11)
		pdf.Cell(0, 8, val)
		pdf.Ln(8)
	}
	line("Period:", d.Period)
	if d.AccountEmail != "" {
		line("Account:", d.AccountEmail)
	}
	line("Total usage (debit):", "$"+d.TotalDebitUSD)
	line("Total recharge (credit):", "$"+d.TotalCreditUSD)
	if d.ClosingUSD != "" {
		line("Closing balance:", "$"+d.ClosingUSD)
	}

	if len(d.Items) > 0 {
		pdf.Ln(4)
		pdf.SetFont("Helvetica", "B", 12)
		pdf.Cell(0, 8, "Usage by model")
		pdf.Ln(10)
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(120, 7, "Model", "B", 0, "L", false, 0, "")
		pdf.CellFormat(0, 7, "Cost (USD)", "B", 1, "R", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		for _, it := range d.Items {
			pdf.CellFormat(120, 7, it.Model, "", 0, "L", false, 0, "")
			pdf.CellFormat(0, 7, "$"+it.CostUSD, "", 1, "R", false, 0, "")
		}
	}

	pdf.Ln(10)
	pdf.SetFont("Helvetica", "I", 8)
	pdf.Cell(0, 6, fmt.Sprintf("Generated %s UTC. Amounts in USD.", time.Now().UTC().Format("2006-01-02")))

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("invoice: render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// Uploader is the private object-store seam (mirrors the analytics-svc gdpr_export
// Uploader, Architect Q-PDF). The cron stores the PDF under a non-enumerable key;
// the retrieval handler proxy-streams it owner-only. GetObject reads it back.
type Uploader interface {
	PutObject(ctx context.Context, key string, body io.Reader) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
}

// ObjectKey is the non-enumerable storage key for an invoice PDF.
func ObjectKey(userID, invoiceID string) string {
	return fmt.Sprintf("invoices/%s/%s.pdf", userID, invoiceID)
}
