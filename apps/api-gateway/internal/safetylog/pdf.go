// Story 8.5 AC3 — 备案材料 PDF render (pure-Go go-pdf/fpdf, the Story-7.7
// invoice precedent / Architect Q-PDF ruling). The report is an aggregate
// SUMMARY for 生成式 AI 服务备案 audit retrieval ("备案审计可调取", §9.3): counts by
// direction / severity / category / strictness + the top matched_rule canonical
// ids + the 6-month-retention statement. It renders NO raw user content and NO
// literal 敏感词 — only counts + canonical rule ids (BR-3.3 no-leak). Document
// labels render in the core latin font; CJK canonical ids embed as a documented
// follow-up (the 7.7 invoice posture) — the figures + structure are locale-
// independent and the no-leak property is byte-structural, not font-dependent.
package safetylog

import (
	"bytes"
	"fmt"

	"github.com/go-pdf/fpdf"
)

// RenderFilingReport produces a well-formed single-page A4 PDF from the aggregate
// report. The returned bytes start with `%PDF-` and end with `%%EOF`. The body is
// deterministic for a fixed report (BR-3.4); only fpdf's embedded creation
// timestamp varies, matching the 7.7 invoice posture.
func RenderFilingReport(r FilingReport) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	// Uncompressed content stream: makes the 备案 PDF text-searchable for audit AND
	// makes the HARD no-leak property a genuine byte-level guarantee (a leaked raw
	// term would be visible in the bytes, not hidden behind zlib) — 8.5-UNIT-021.
	pdf.SetCompression(false)
	pdf.SetTitle("He-API Content-Safety Filing Report", false)
	pdf.AddPage()

	pdf.SetFont("Helvetica", "B", 18)
	pdf.Cell(0, 12, "He-API Content-Safety Filing Report")
	pdf.Ln(13)
	pdf.SetFont("Helvetica", "", 9)
	pdf.Cell(0, 6, "Generative-AI service compliance governance log (备案材料) — aggregate summary")
	pdf.Ln(10)

	pdf.SetFont("Helvetica", "", 11)
	line := func(label, val string) {
		pdf.SetFont("Helvetica", "B", 11)
		pdf.Cell(55, 8, label)
		pdf.SetFont("Helvetica", "", 11)
		pdf.Cell(0, 8, val)
		pdf.Ln(8)
	}
	const dateFmt = "2006-01-02"
	line("Period:", fmt.Sprintf("%s to %s (half-open)", r.Start.UTC().Format(dateFmt), r.End.UTC().Format(dateFmt)))
	line("Generated:", r.GeneratedAt.UTC().Format("2006-01-02 15:04:05")+" UTC")
	line("Retention window:", fmt.Sprintf("%d months (§9.3 governance-log retention)", r.RetentionMonths))
	line("Total interceptions:", fmt.Sprintf("%d", r.Total))

	table := func(title string, rows []Count) {
		pdf.Ln(3)
		pdf.SetFont("Helvetica", "B", 12)
		pdf.Cell(0, 8, title)
		pdf.Ln(9)
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(120, 7, "Key", "B", 0, "L", false, 0, "")
		pdf.CellFormat(0, 7, "Count", "B", 1, "R", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		if len(rows) == 0 {
			pdf.CellFormat(0, 7, "(none)", "", 1, "L", false, 0, "")
			return
		}
		for _, c := range rows {
			pdf.CellFormat(120, 7, c.Label, "", 0, "L", false, 0, "")
			pdf.CellFormat(0, 7, fmt.Sprintf("%d", c.N), "", 1, "R", false, 0, "")
		}
	}
	table("By direction", r.ByDirection)
	table("By severity", r.BySeverity)
	table("By category", r.ByCategory)
	table("By strictness (under-level attribution)", r.ByStrictness)
	table("Top matched rules (canonical id)", r.TopRules)

	pdf.Ln(8)
	pdf.SetFont("Helvetica", "I", 8)
	pdf.MultiCell(0, 5,
		"This report aggregates governance-log counts and canonical rule identifiers only. "+
			"It contains no raw request/response content and no literal sensitive terms "+
			"(data 不出境 / no-lexicon-leak, §9.1/§9.3).", "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("safetylog: render filing pdf: %w", err)
	}
	return buf.Bytes(), nil
}
