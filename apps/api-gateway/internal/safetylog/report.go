package safetylog

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// retentionMonths is the §9.3 "保留 6 个月" window, surfaced in the 备案 report.
const retentionMonths = 6

// TopRulesLimit caps the matched_rule breakdown (BR-3.4 top-N, stable cut).
const TopRulesLimit = 20

// aggregateSQL is the read-only filing aggregate over a half-open [start, end)
// range. It GROUP BYs the persisted dimensions ONLY (direction / matched_rule /
// strictness) — category + severity are derived in Go from matched_rule via the
// authoritative in-process lexicon (Categorizer), so the table stays lean and the
// taxonomy can never drift from the lexicon. SELECT-only (BR-3.5).
const aggregateSQL = `SELECT direction, COALESCE(matched_rule, ''), COALESCE(strictness, ''), COUNT(*)::bigint
	FROM he_api.content_safety_logs
	WHERE created_at >= $1 AND created_at < $2
	GROUP BY direction, matched_rule, strictness`

// ReportQuerier is the minimal read surface the aggregate needs (satisfied by
// *pgxpool.Pool and pgxmock).
type ReportQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Categorizer resolves a canonical matched_rule id to its §9.3 (category,
// severity). The production impl wraps safetylexicon.DefaultLexicon; a miss
// (retired / aged-out rule) yields ("unknown","unknown") so the row still counts.
type Categorizer func(canonical string) (category, severity string)

// Count is one labelled aggregate bucket.
type Count struct {
	Label string
	N     int
}

// FilingReport is the read-model the 备案 PDF renders. It is aggregate-only:
// counts + canonical rule ids + a retention statement — NEVER raw user content
// nor literal 敏感词 (BR-3.3).
type FilingReport struct {
	Start, End      time.Time
	GeneratedAt     time.Time
	RetentionMonths int
	Total           int
	ByDirection     []Count
	BySeverity      []Count
	ByCategory      []Count
	ByStrictness    []Count
	TopRules        []Count
}

// AggregateFilingReport runs the read-only aggregate and folds it into the report
// read-model. Deterministic (BR-3.4): every breakdown is sorted by count desc
// then label asc — no map-iteration nondeterminism reaches the PDF. `now` is
// injected so the generation timestamp is testable.
func AggregateFilingReport(ctx context.Context, db ReportQuerier, start, end, now time.Time, cat Categorizer) (FilingReport, error) {
	rows, err := db.Query(ctx, aggregateSQL, start, end)
	if err != nil {
		return FilingReport{}, err
	}
	defer rows.Close()

	dir := map[string]int{}
	sev := map[string]int{}
	categ := map[string]int{}
	strict := map[string]int{}
	rule := map[string]int{}
	total := 0
	for rows.Next() {
		var direction, matchedRule, strictness string
		var n int64
		if err := rows.Scan(&direction, &matchedRule, &strictness, &n); err != nil {
			return FilingReport{}, err
		}
		c := int(n)
		total += c
		dir[direction] += c
		strict[normStrictness(strictness)] += c
		if matchedRule != "" {
			rule[matchedRule] += c
			category, severity := cat(matchedRule)
			categ[category] += c
			sev[severity] += c
		} else {
			categ["unknown"] += c
			sev["unknown"] += c
		}
	}
	if err := rows.Err(); err != nil {
		return FilingReport{}, err
	}

	return FilingReport{
		Start:           start,
		End:             end,
		GeneratedAt:     now,
		RetentionMonths: retentionMonths,
		Total:           total,
		ByDirection:     sortedCounts(dir),
		BySeverity:      sortedCounts(sev),
		ByCategory:      sortedCounts(categ),
		ByStrictness:    sortedCounts(strict),
		TopRules:        topN(sortedCounts(rule), TopRulesLimit),
	}, nil
}

// normStrictness labels the "" (pre-8.4 / unspecified) bucket explicitly so the
// 备案 attribution table has no blank rows.
func normStrictness(s string) string {
	if s == "" {
		return "unspecified"
	}
	return s
}

// sortedCounts returns counts sorted by N desc, then Label asc — the BR-3.4
// stable ordering the deterministic-body test asserts.
func sortedCounts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{Label: k, N: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func topN(c []Count, n int) []Count {
	if len(c) > n {
		return c[:n]
	}
	return c
}
