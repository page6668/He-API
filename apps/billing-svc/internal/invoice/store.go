// Package invoice is billing-svc's Story-7.7 AC3 monthly-statement store. It
// aggregates a period's usage_ledger debits + paid recharge_orders credits per
// ACTIVE user and persists ONE invoice per (user, period) — the exactly-once fence
// (BR-I-1) is the invoices UNIQUE(user_id, period) + INSERT ... ON CONFLICT DO
// NOTHING, so a CronJob retry / multi-pod re-run inserts zero rows for an
// already-generated user-month. Skip-empty (BR-I-4): only users with activity in
// the period appear in the source set. Figures are FROZEN at generation (BR-I-2).
package invoice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the minimal pgx surface the store needs.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Generated is a freshly-inserted invoice the cron must render+email (the
// RETURNING rows from the idempotent insert — a re-run returns ZERO).
type Generated struct {
	ID             string
	UserID         string
	Period         string
	TotalDebitUSD  string
	TotalCreditUSD string
}

// Invoice is the DISPLAY projection for the list / retrieval RPCs.
type Invoice struct {
	ID             string `json:"id"`
	Period         string `json:"period"`
	TotalDebitUSD  string `json:"total_debit_usd"`
	TotalCreditUSD string `json:"total_credit_usd"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	HasPdf         bool   `json:"has_pdf"`
	CreatedAt      string `json:"created_at"`
}

// Owned is the owner-scoped fetch result for the PDF download (IDOR-guarded).
type Owned struct {
	Period         string
	TotalDebitUSD  string
	TotalCreditUSD string
	Status         string
	PdfObjectKey   string
}

// ErrNotFound — the invoice id is not the caller's (or does not exist). The
// gateway maps it to 404 (no existence disclosure, BR-I-6).
var ErrNotFound = errors.New("invoice: not found")

const (
	// One idempotent statement: skip-empty (only active users via the UNION) +
	// accuracy (SUM the period's ledger debits + paid-order credits) +
	// exactly-once (ON CONFLICT DO NOTHING). RETURNING yields ONLY newly-inserted
	// rows, so a re-run renders/emails nothing.
	generateSQL = `INSERT INTO he_api.invoices
		(user_id, period, total_debit_usd, total_credit_usd, currency, status, created_at)
	SELECT u.user_id, $1::char(7), COALESCE(d.debit, 0), COALESCE(c.credit, 0), 'USD', 'generated', NOW()
	FROM (
		SELECT user_id FROM he_api.usage_ledger WHERE ts >= $2 AND ts < $3
		UNION
		SELECT user_id FROM he_api.recharge_orders WHERE status = 'paid' AND paid_at >= $2 AND paid_at < $3
	) u
	LEFT JOIN (
		SELECT user_id, SUM(cost_usd) AS debit FROM he_api.usage_ledger
		WHERE ts >= $2 AND ts < $3 GROUP BY user_id
	) d ON d.user_id = u.user_id
	LEFT JOIN (
		SELECT user_id, SUM(amount) AS credit FROM he_api.recharge_orders
		WHERE status = 'paid' AND paid_at >= $2 AND paid_at < $3 GROUP BY user_id
	) c ON c.user_id = u.user_id
	ON CONFLICT (user_id, period) DO NOTHING
	RETURNING id::text, user_id::text, total_debit_usd::text, total_credit_usd::text`

	listSQL = `SELECT id::text, period, total_debit_usd::text, total_credit_usd::text, currency, status,
		(pdf_object_key IS NOT NULL) AS has_pdf,
		to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	FROM he_api.invoices WHERE user_id = $1 ORDER BY period DESC`

	getOwnedSQL = `SELECT period, total_debit_usd::text, total_credit_usd::text, status, COALESCE(pdf_object_key, '')
	FROM he_api.invoices WHERE id = $1 AND user_id = $2`

	// Advance generated→emailed exactly once (the guard prevents a duplicate email).
	markEmailedSQL = `UPDATE he_api.invoices
	SET pdf_object_key = $2, status = 'emailed', emailed_at = NOW()
	WHERE id = $1 AND status = 'generated'`

	setPdfKeySQL = `UPDATE he_api.invoices SET pdf_object_key = $2 WHERE id = $1`
)

// Store persists + reads invoices.
type Store struct{ db DB }

// New builds a Store.
func New(db DB) *Store { return &Store{db: db} }

// Generate runs the idempotent per-user-month aggregation for `period` (a "YYYY-MM"
// string), over the half-open UTC window [start, end). Returns the NEWLY-inserted
// invoices (a re-run returns an empty slice — exactly-once, BR-I-1).
func (s *Store) Generate(ctx context.Context, period string, start, end time.Time) ([]Generated, error) {
	rows, err := s.db.Query(ctx, generateSQL, period, start, end)
	if err != nil {
		return nil, fmt.Errorf("invoice: generate: %w", err)
	}
	defer rows.Close()
	out := make([]Generated, 0)
	for rows.Next() {
		g := Generated{Period: period}
		if err := rows.Scan(&g.ID, &g.UserID, &g.TotalDebitUSD, &g.TotalCreditUSD); err != nil {
			return nil, fmt.Errorf("invoice: scan generated: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("invoice: generate rows: %w", err)
	}
	return out, nil
}

// List returns a user's invoices most-recent-first (string-decimal money).
func (s *Store) List(ctx context.Context, userID string) ([]Invoice, error) {
	rows, err := s.db.Query(ctx, listSQL, strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("invoice: list: %w", err)
	}
	defer rows.Close()
	out := make([]Invoice, 0)
	for rows.Next() {
		var iv Invoice
		if err := rows.Scan(&iv.ID, &iv.Period, &iv.TotalDebitUSD, &iv.TotalCreditUSD, &iv.Currency, &iv.Status, &iv.HasPdf, &iv.CreatedAt); err != nil {
			return nil, fmt.Errorf("invoice: scan: %w", err)
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// GetForOwner fetches an invoice ONLY if it belongs to userID (IDOR guard); a
// foreign / missing id → ErrNotFound (the gateway returns 404, not 403).
func (s *Store) GetForOwner(ctx context.Context, userID, invoiceID string) (Owned, error) {
	var o Owned
	err := s.db.QueryRow(ctx, getOwnedSQL, strings.TrimSpace(invoiceID), strings.TrimSpace(userID)).
		Scan(&o.Period, &o.TotalDebitUSD, &o.TotalCreditUSD, &o.Status, &o.PdfObjectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owned{}, ErrNotFound
	}
	if err != nil {
		return Owned{}, fmt.Errorf("invoice: get owned: %w", err)
	}
	return o, nil
}

// SetPdfKey records the stored object key after a render+upload.
func (s *Store) SetPdfKey(ctx context.Context, invoiceID, key string) error {
	_, err := s.db.Exec(ctx, setPdfKeySQL, invoiceID, key)
	return err
}

// MarkEmailed advances generated→emailed exactly once (after notification-svc
// dispatch); a re-run on an already-emailed row affects zero rows (no dup email).
func (s *Store) MarkEmailed(ctx context.Context, invoiceID, pdfKey string) (bool, error) {
	tag, err := s.db.Exec(ctx, markEmailedSQL, invoiceID, pdfKey)
	if err != nil {
		return false, fmt.Errorf("invoice: mark emailed: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// PeriodBounds returns the "YYYY-MM" label + the half-open [start, end) UTC window
// for the calendar month that JUST ended relative to `now` (BR-I-3). E.g. now =
// 2026-07-01T00:00:00Z → ("2026-06", 2026-06-01, 2026-07-01).
func PeriodBounds(now time.Time) (period string, start, end time.Time) {
	now = now.UTC()
	end = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start = end.AddDate(0, -1, 0)
	return start.Format("2006-01"), start, end
}
