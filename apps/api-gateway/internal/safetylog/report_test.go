package safetylog

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"
)

var (
	repStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repEnd   = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	repNow   = time.Date(2026, 7, 2, 9, 30, 0, 0, time.UTC)
)

// testCategorizer maps the synthetic canonical ids to a fixed taxonomy.
func testCategorizer(canonical string) (string, string) {
	switch canonical {
	case "term_alpha":
		return "political", "high"
	case "term_beta":
		return "abuse", "medium"
	case "term_gamma":
		return "contraband", "low"
	}
	return "unknown", "unknown"
}

func mixedRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"direction", "matched_rule", "strictness", "count"}).
		AddRow("input", "term_alpha", "strict", int64(5)).
		AddRow("output", "term_alpha", "default", int64(3)).
		AddRow("input", "term_beta", "strict", int64(2)).
		AddRow("output", "term_gamma", "loose", int64(1))
}

func aggregate(t *testing.T, rows *pgxmock.Rows) FilingReport {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.content_safety_logs").
		WithArgs(repStart, repEnd).
		WillReturnRows(rows)
	rep, err := AggregateFilingReport(context.Background(), mock, repStart, repEnd, repNow, testCategorizer)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	return rep
}

// 8.5-UNIT-020 — render a summary PDF over mixed rows: counts by direction /
// severity / category / strictness + top matched_rule canonical ids + retention
// statement + generation timestamp.
func Test8_5_UNIT020_RendersSummaryPDF(t *testing.T) {
	rep := aggregate(t, mixedRows())
	if rep.Total != 11 {
		t.Fatalf("total=%d want 11", rep.Total)
	}
	pdf, err := RenderFilingReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Fatal("not a well-formed PDF")
	}
	// derived taxonomy buckets present
	for _, want := range []string{"high", "medium", "low", "political", "abuse", "contraband"} {
		assertCount(t, rep.BySeverity, rep.ByCategory, want)
	}
	// by-direction: input 5+2=7, output 3+1=4
	if got := labelN(rep.ByDirection, "input"); got != 7 {
		t.Fatalf("input=%d want 7", got)
	}
	if got := labelN(rep.ByDirection, "output"); got != 4 {
		t.Fatalf("output=%d want 4", got)
	}
}

// 8.5-UNIT-021 (HARD, property) — NO-LEAK: the report is built ONLY from canonical
// matched_rule ids; a surface 敏感词 / raw user text that is NEVER fed cannot appear
// in the PDF. We assert the canonical id IS present (evidence) and a deliberately
// withheld surface term is NOT.
func Test8_5_UNIT021_NoLeakProperty(t *testing.T) {
	const surfaceTerm = "RAW_SURFACE_敏感词"
	rep := aggregate(t, mixedRows())
	pdf, err := RenderFilingReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pdf, []byte("term_alpha")) {
		t.Fatal("canonical id missing — no 备案 evidence")
	}
	if bytes.Contains(pdf, []byte(surfaceTerm)) {
		t.Fatalf("PDF leaked a surface term that was never fed: %q", surfaceTerm)
	}
}

// 8.5-UNIT-022 — empty range → valid "0 interceptions" PDF (auditable clean op).
func Test8_5_UNIT022_EmptyRangePDF(t *testing.T) {
	rep := aggregate(t, pgxmock.NewRows([]string{"direction", "matched_rule", "strictness", "count"}))
	if rep.Total != 0 {
		t.Fatalf("total=%d want 0", rep.Total)
	}
	pdf, err := RenderFilingReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Fatal("empty-range PDF malformed")
	}
}

// 8.5-UNIT-023 — deterministic report body: same rows → identical aggregate
// (stable count-desc, label-asc ordering; no map-iteration nondeterminism).
func Test8_5_UNIT023_DeterministicBody(t *testing.T) {
	a := aggregate(t, mixedRows())
	b := aggregate(t, mixedRows())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("aggregate is non-deterministic for fixed input")
	}
	// stable ordering: counts strictly non-increasing, ties broken by label asc
	for _, s := range [][]Count{a.ByDirection, a.BySeverity, a.ByCategory, a.ByStrictness, a.TopRules} {
		for i := 1; i < len(s); i++ {
			if s[i-1].N < s[i].N || (s[i-1].N == s[i].N && s[i-1].Label > s[i].Label) {
				t.Fatalf("unstable ordering: %+v", s)
			}
		}
	}
}

// 8.5-UNIT-025 — read-only: generation issues SELECT only (the ReportQuerier
// surface has no Exec); pgxmock confirms exactly the one query ran.
func Test8_5_UNIT025_ReadOnly(t *testing.T) {
	// aggregate() already asserts ExpectationsWereMet with a single ExpectQuery
	// and no ExpectExec — a write would fail the mock. This re-states intent.
	_ = aggregate(t, mixedRows())
}

// 8.5-UNIT-026 — renders strictness attribution + retention statement + timestamp.
func Test8_5_UNIT026_RendersAttributionAndRetention(t *testing.T) {
	pdf, err := RenderFilingReport(aggregate(t, mixedRows()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"strictness", "Retention window", "Generated", "2026-07-02"} {
		if !bytes.Contains(pdf, []byte(want)) {
			t.Errorf("PDF missing %q", want)
		}
	}
}

// 8.5-BLIND-BOUNDARY-006 — top-N truncation: > TopRulesLimit distinct rules →
// exactly TopRulesLimit, stable cut (highest counts kept).
func Test8_5_BLIND_BOUNDARY006_TopNTruncation(t *testing.T) {
	rows := pgxmock.NewRows([]string{"direction", "matched_rule", "strictness", "count"})
	for i := 0; i < TopRulesLimit+5; i++ {
		// distinct canonical id, distinct descending count
		rows.AddRow("input", ruleID(i), "strict", int64(TopRulesLimit+10-i))
	}
	rep := aggregate(t, rows)
	if len(rep.TopRules) != TopRulesLimit {
		t.Fatalf("top rules len=%d want %d", len(rep.TopRules), TopRulesLimit)
	}
	if rep.TopRules[0].N <= rep.TopRules[len(rep.TopRules)-1].N {
		t.Fatal("top-N not sorted by count desc")
	}
}

// 8.5-BLIND-ERROR-004 — aggregate DB error → AggregateFilingReport errors (the
// cmd maps it to exit 1, no partial PDF). Asserted via the query-error path.
func Test8_5_BLIND_ERROR004_AggregateDBError(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.content_safety_logs").
		WithArgs(repStart, repEnd).
		WillReturnError(errors.New("connection refused"))
	if _, err := AggregateFilingReport(context.Background(), mock, repStart, repEnd, repNow, testCategorizer); err == nil {
		t.Fatal("expected aggregate error on DB failure")
	}
}

// --- helpers ---

func labelN(c []Count, label string) int {
	for _, x := range c {
		if x.Label == label {
			return x.N
		}
	}
	return -1
}

func assertCount(t *testing.T, sev, cat []Count, label string) {
	t.Helper()
	if labelN(sev, label) > 0 || labelN(cat, label) > 0 {
		return
	}
	t.Errorf("expected a bucket for %q in severity or category", label)
}

func ruleID(i int) string {
	return "rule_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
