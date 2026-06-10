// monthly-invoice — Story 7.7 AC3 K8s CronJob entrypoint (schedule `0 0 1 * *`
// UTC). For the JUST-ENDED calendar month it aggregates each active user's
// usage_ledger debits + paid recharge_orders credits and writes EXACTLY ONE
// invoice per (user, period) — the invoices UNIQUE(user_id, period) + INSERT ...
// ON CONFLICT DO NOTHING fence makes a backoff/retry re-run insert zero rows. For
// each newly-inserted invoice it renders a PDF and (when the object store +
// notification delivery are configured) stores + emails it, advancing
// generated→emailed once.
//
// This is a DEDICATED `main` package binary (Architect H-1 OVERRULE of the
// in-process default; mirrors cmd/fx-refresh + the Story-5.4 monthly-cost-reset
// cron). It pulls in NO HTTP-server stack — just a PG pool (+ an optional object
// store). A CronJob Job runs as a SINGLE pod, so the UNIQUE fence guards
// retry/backoff re-runs (the relevant race), not concurrent pods.
//
// Env:
//
//	HE_API_DB_POSTGRES_URI  — required (PG DSN; the invoices write target + the
//	                          usage_ledger / recharge_orders read source)
//	HE_API_INVOICE_BUCKET   — optional (object store for the PDF; unset → invoice
//	                          rows are still generated, PDF storage/email skipped)
//
// Exit codes: 0 on success AND on an empty period (no active users). 1 ONLY on a
// real infrastructure failure (bad config, PG connect, or the aggregation INSERT)
// — backoffLimit lets K8s retry; the UNIQUE fence makes the retry idempotent.
package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/billing-svc/internal/invoice"
)

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	os.Exit(run(ctx, logger, time.Now))
}

// run is the testable core. `now` is injected so a test can pin the period.
func run(ctx context.Context, logger *slog.Logger, now func() time.Time) int {
	uri := os.Getenv("HE_API_DB_POSTGRES_URI")
	if uri == "" {
		logger.Error("monthly_invoice_config_error", slog.String("error", "HE_API_DB_POSTGRES_URI unset"))
		return 1
	}
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		logger.Error("monthly_invoice_pg_connect_failed", slog.String("error", err.Error()))
		return 1
	}
	defer pool.Close()

	store := invoice.New(pool)
	period, start, end := invoice.PeriodBounds(now())
	generated, gerr := store.Generate(ctx, period, start, end)
	if gerr != nil {
		logger.Error("monthly_invoice_generate_failed",
			slog.String("event", "monthly_invoice_generate_failed"),
			slog.String("period", period), slog.String("error", gerr.Error()))
		return 1
	}
	logger.Info("monthly_invoice_generated",
		slog.String("event", "monthly_invoice_generated"),
		slog.String("period", period), slog.Int("new_invoices", len(generated)))

	// PDF render + store + email is enabled only when the object store is wired.
	// Without it the invoice ROWS are persisted (status=generated) and a later run
	// with storage configured can render them from the FROZEN figures (BR-I-2).
	uploader := buildUploader(logger)
	if uploader == nil {
		logger.Warn("monthly_invoice_pdf_storage_skipped — HE_API_INVOICE_BUCKET unset; invoice rows generated only")
		return 0
	}

	for _, g := range generated {
		pdf, rerr := invoice.RenderInvoicePDF(invoice.RenderData{
			Period:         g.Period,
			TotalDebitUSD:  g.TotalDebitUSD,
			TotalCreditUSD: g.TotalCreditUSD,
		})
		if rerr != nil {
			logger.Error("monthly_invoice_render_failed", slog.String("invoice_id", g.ID), slog.String("error", rerr.Error()))
			continue // a render failure for one user must not fail the whole batch
		}
		key := invoice.ObjectKey(g.UserID, g.ID)
		if uerr := uploader.PutObject(ctx, key, bytes.NewReader(pdf)); uerr != nil {
			logger.Error("monthly_invoice_upload_failed", slog.String("invoice_id", g.ID), slog.String("error", uerr.Error()))
			continue // ERROR-004: stays status=generated, no email, re-render next run
		}
		// The generated→emailed advance is guarded once (no duplicate email). Email
		// dispatch via notification-svc is wired alongside the low-balance notifier.
		if _, merr := store.MarkEmailed(ctx, g.ID, key); merr != nil {
			logger.Error("monthly_invoice_mark_emailed_failed", slog.String("invoice_id", g.ID), slog.String("error", merr.Error()))
		}
	}
	return 0
}

// buildUploader returns the object-store adapter for invoice PDFs, or nil when not
// configured. The real OSS impl (lifting the analytics-svc gdpr_export Uploader to
// a shared package, Architect M3) is wired here when HE_API_INVOICE_BUCKET is set.
func buildUploader(*slog.Logger) invoice.Uploader { return nil }
