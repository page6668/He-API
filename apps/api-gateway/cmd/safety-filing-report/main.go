// safety-filing-report — Story 8.5 AC3 §9.3 "备案审计可调取" on-demand filing-report
// generator. For a half-open [start, end) date range it runs a READ-ONLY
// aggregate over he_api.content_safety_logs and renders a 备案材料 summary PDF
// (counts by direction / severity / category / strictness + top matched_rule
// canonical ids + the 6-month-retention statement). It NEVER mutates the logs and
// is decoupled from the live filter hot path (BR-3.5).
//
// On-demand only in v1 (OQ-8.5-6 APPROVED: "可调取" = retrievable, not scheduled);
// a periodic auto-archive is a clean follow-up. Mirrors billing-svc/cmd/
// monthly-invoice (PG pool, no HTTP-server stack).
//
// Usage:
//
//	safety-filing-report --start 2026-01-01 --end 2026-07-01 --out filing-2026H1.pdf
//
// Env:
//
//	HE_API_DB_POSTGRES_URI — required (PG DSN; the same pool the gateway uses)
//
// Exit codes: 0 success (incl. an empty range → a valid "0 interceptions" PDF);
// 1 infra failure (config / PG connect / aggregate / render / write); 2 invalid
// range arguments (validated BEFORE any DB work).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/api-gateway/internal/safetylog"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

const dateLayout = "2006-01-02"

type config struct {
	start, end time.Time
	out        string
}

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	os.Exit(run(ctx, logger, os.Args[1:], time.Now))
}

// run parses + validates args (exit 2 on bad range, NO DB query), wires the read
// pool, then delegates to generate. Factored from main for testability.
func run(ctx context.Context, logger *slog.Logger, args []string, now func() time.Time) int {
	cfg, code, ok := parseArgs(args, logger)
	if !ok {
		return code
	}
	uri := os.Getenv("HE_API_DB_POSTGRES_URI")
	if uri == "" {
		logger.Error("safety_log: filing report config error", slog.String("error", "HE_API_DB_POSTGRES_URI unset"))
		return 1
	}
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		logger.Error("safety_log: filing report pg connect failed", slog.String("error", err.Error()))
		return 1
	}
	defer pool.Close()
	return generate(ctx, logger, pool, cfg, now, os.WriteFile)
}

// parseArgs returns the validated config, or (exit code, false) on a bad range.
// start >= end → exit 2 (arg validation; no DB touched).
func parseArgs(args []string, logger *slog.Logger) (config, int, bool) {
	fs := flag.NewFlagSet("safety-filing-report", flag.ContinueOnError)
	var startStr, endStr, out string
	fs.StringVar(&startStr, "start", "", "range start, inclusive (YYYY-MM-DD, UTC)")
	fs.StringVar(&endStr, "end", "", "range end, exclusive (YYYY-MM-DD, UTC)")
	fs.StringVar(&out, "out", "", "output PDF path")
	if err := fs.Parse(args); err != nil {
		return config{}, 2, false
	}
	start, serr := time.Parse(dateLayout, startStr)
	end, eerr := time.Parse(dateLayout, endStr)
	if serr != nil || eerr != nil || out == "" {
		logger.Error("safety_log: invalid range", slog.String("error", "start/end must be YYYY-MM-DD and --out required"))
		return config{}, 2, false
	}
	if !start.Before(end) {
		logger.Error("safety_log: invalid range", slog.String("error", "start must be before end (half-open)"))
		return config{}, 2, false
	}
	return config{start: start.UTC(), end: end.UTC(), out: out}, 0, true
}

// generate runs the read-only aggregate, renders the PDF, and writes it. 0 on
// success (incl. an empty range); 1 on aggregate / render / write failure (no
// partial PDF — the write is the last step). writeFile is injected for tests.
func generate(
	ctx context.Context,
	logger *slog.Logger,
	db safetylog.ReportQuerier,
	cfg config,
	now func() time.Time,
	writeFile func(string, []byte, os.FileMode) error,
) int {
	rep, err := safetylog.AggregateFilingReport(ctx, db, cfg.start, cfg.end, now().UTC(), lexiconCategorizer)
	if err != nil {
		logger.Error("safety_log: filing report failed", slog.String("error", err.Error()))
		return 1
	}
	pdf, err := safetylog.RenderFilingReport(rep)
	if err != nil {
		logger.Error("safety_log: filing report render failed", slog.String("error", err.Error()))
		return 1
	}
	if err := writeFile(cfg.out, pdf, 0o644); err != nil {
		logger.Error("safety_log: filing report write failed", slog.String("error", err.Error()))
		return 1
	}
	logger.Info("safety_log: filing report written",
		slog.Int("total_interceptions", rep.Total),
		slog.String("out", cfg.out))
	return 0
}

// lexiconCategorizer resolves a canonical matched_rule id to its §9.3 (category,
// severity) via the authoritative in-process lexicon. A miss (retired / aged-out
// rule) → ("unknown","unknown") so the row still counts.
func lexiconCategorizer(canonical string) (string, string) {
	if m, ok := safetylexicon.DefaultLexicon.Lookup(canonical); ok {
		return string(m.Category), string(m.Severity)
	}
	return "unknown", "unknown"
}
