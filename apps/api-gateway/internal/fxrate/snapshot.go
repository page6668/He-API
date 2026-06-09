package fxrate

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// DefaultRefreshInterval is the snapshot refresh cadence (parity with the
// billing-svc/routing-svc 60s pricing refresh). The FX rate changes once/day, so
// a per-pod ~60s-refresh snapshot of the latest row is sufficient and avoids a
// per-request PG hit (Q-FXCACHE — no Redis).
const DefaultRefreshInterval = 60 * time.Second

// loadSQL reads every fx_rates row; Load reduces to the latest fetched_at per
// (base,quote) pair in Go (the active-rate read is "ORDER BY fetched_at DESC
// LIMIT 1 per pair" — Architect L-1; a full-table read of this tiny, append-only
// table is cheap, mirroring pricing.Load). The NUMERIC rate is cast ::text and
// parsed with decimal.NewFromString — NEVER scanned as float8 (M-1).
const loadSQL = `SELECT base_currency, quote_currency, rate::text, fetched_at
FROM he_api.fx_rates`

// Rate is one (base,quote) pair's latest rate. FetchedAt is surfaced to clients
// as `fx_as_of` so staleness is visible (BR-B-8, ties to AC2 stale-serve).
type Rate struct {
	Base      string
	Quote     string
	Rate      decimal.Decimal
	FetchedAt time.Time
}

// Snapshot is an immutable (base:quote) → Rate view. The zero value (and a nil
// *Snapshot) is a valid empty snapshot — Lookup reports (_, false), which the
// read handler degrades gracefully (cold-start guard, BR — never a zero rate).
type Snapshot struct {
	rates map[string]Rate
}

func key(base, quote string) string {
	return strings.ToUpper(strings.TrimSpace(base)) + ":" + strings.ToUpper(strings.TrimSpace(quote))
}

// NewSnapshot builds an immutable Snapshot from a (base:quote) → Rate map. The
// input is defensively copied.
func NewSnapshot(rates map[string]Rate) *Snapshot {
	cp := make(map[string]Rate, len(rates))
	for k, v := range rates {
		cp[k] = v
	}
	return &Snapshot{rates: cp}
}

// Len reports the number of (base,quote) pairs held.
func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.rates)
}

// Lookup returns the latest rate for (base,quote) and whether one exists. Pure:
// reads the in-memory snapshot ONLY — never the FX provider (BR-C-5 hot-path
// isolation, UNIT-012).
func (s *Snapshot) Lookup(base, quote string) (Rate, bool) {
	if s == nil || s.rates == nil {
		return Rate{}, false
	}
	r, ok := s.rates[key(base, quote)]
	return r, ok
}

// Querier is the minimal pgx surface Load needs. Satisfied by *pgxpool.Pool
// (production) and pgxmock (unit tests).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Load reads the latest-fetched_at rate per (base,quote) and returns an
// immutable Snapshot. Zero rows is a valid empty Snapshot, not an error. A
// malformed NUMERIC text is a hard error (better to retain the last-good
// snapshot than to convert from a corrupt rate).
func Load(ctx context.Context, q Querier) (*Snapshot, error) {
	rows, err := q.Query(ctx, loadSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // RESOURCE: connection returned to the pool on every path

	acc := make(map[string]Rate)
	for rows.Next() {
		var (
			base, quote, rateText string
			fetchedAt             time.Time
		)
		if err := rows.Scan(&base, &quote, &rateText, &fetchedAt); err != nil {
			return nil, err
		}
		rate, perr := decimal.NewFromString(rateText)
		if perr != nil {
			return nil, perr
		}
		base = strings.ToUpper(strings.TrimSpace(base))
		quote = strings.ToUpper(strings.TrimSpace(quote))
		k := base + ":" + quote
		if cur, ok := acc[k]; !ok || fetchedAt.After(cur.FetchedAt) {
			acc[k] = Rate{Base: base, Quote: quote, Rate: rate, FetchedAt: fetchedAt}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &Snapshot{rates: acc}, nil
}

// LoadFunc loads a fresh Snapshot.
type LoadFunc func(ctx context.Context) (*Snapshot, error)

// Provider holds the current Snapshot behind an atomic pointer and refreshes it
// on a fixed cadence. Reads (Current/Lookup) are lock-free; a failed refresh
// keeps the last-good snapshot (BLIND-ERROR-001 — conversions continue at the
// stale rate, bounded by the refresh window; a stale display rate is a cosmetic,
// not financial, defect).
type Provider struct {
	cur      atomic.Pointer[Snapshot]
	load     LoadFunc
	interval time.Duration
	logger   *slog.Logger
}

// NewProvider seeds the Provider with a boot snapshot. boot may be nil/empty
// (resilient boot). interval <= 0 → DefaultRefreshInterval. logger may be nil.
func NewProvider(boot *Snapshot, load LoadFunc, interval time.Duration, logger *slog.Logger) *Provider {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	if boot == nil {
		boot = NewSnapshot(nil)
	}
	p := &Provider{load: load, interval: interval, logger: logger}
	p.cur.Store(boot)
	return p
}

// Current returns the latest snapshot. Never nil. Safe for concurrent use — the
// atomic pointer swap makes a refresh race read either the old OR the new
// snapshot, never a torn one (BLIND-CONCURRENCY-002).
func (p *Provider) Current() *Snapshot { return p.cur.Load() }

// Lookup is a convenience over Current().Lookup for the read-handler seam. It
// reads the in-process snapshot ONLY — it NEVER calls the FX provider (BR-C-5).
func (p *Provider) Lookup(base, quote string) (decimal.Decimal, time.Time, bool) {
	r, ok := p.Current().Lookup(base, quote)
	return r.Rate, r.FetchedAt, ok
}

// Run refreshes the snapshot every interval until ctx is cancelled (the ticker
// goroutine stops on ctx.Done — BLIND-RESOURCE-002, no goroutine leak). A
// refresh error retains the last-good snapshot (BLIND-ERROR-001). Call in a
// dedicated goroutine.
func (p *Provider) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snap, err := p.load(ctx)
			if err != nil {
				p.logger.WarnContext(ctx, "fx_rate_refresh_failed",
					slog.String("event", "fx_rate_refresh_failed"),
					slog.String("error", err.Error()),
				)
				continue // keep last-good
			}
			p.cur.Store(snap)
			p.logger.InfoContext(ctx, "fx_rate_refreshed",
				slog.String("event", "fx_rate_refreshed"),
				slog.Int("pairs", snap.Len()),
			)
		}
	}
}
