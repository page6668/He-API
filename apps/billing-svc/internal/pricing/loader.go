package pricing

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// DefaultRefreshInterval is the snapshot refresh cadence (parity with the
// Story-6.2 routing-svc 60s refresh).
const DefaultRefreshInterval = 60 * time.Second

// loadSQL reads every pricing row; Load reduces to the latest effective_at per
// model in Go. The NUMERIC columns are cast to ::text and parsed with
// decimal.NewFromString — NEVER scanned as float8 (M-1: the money path is
// Decimal end-to-end). per_call_price_usd is nullable (Q-PERCALL) → scanned into
// a *string. The catalogue is bounded (~8 models), so a full-table read at
// boot/refresh is cheap.
const loadSQL = `SELECT model_id, effective_at,
	upstream_price_per_1k_input_tokens::text,
	upstream_price_per_1k_output_tokens::text,
	markup_percent::text,
	per_call_price_usd::text,
	price_per_minute_audio_usd::text,
	price_per_1k_chars_audio_usd::text
FROM he_api.model_pricing`

// Querier is the minimal pgx surface Load needs. Satisfied by *pgxpool.Pool
// (production) and pgxmock (unit tests).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Load reads the latest-effective_at pricing row per model and returns an
// immutable Snapshot. A query/scan/parse error is returned verbatim (the caller
// decides boot fail-fast vs. last-good retention). Zero rows is a valid empty
// Snapshot, not an error. A malformed NUMERIC text is a hard error (better to
// retain the last-good snapshot than to bill from a corrupt price).
func Load(ctx context.Context, q Querier) (*Snapshot, error) {
	rows, err := q.Query(ctx, loadSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // RESOURCE: connection returned to the pool on every path

	type latest struct {
		at  time.Time
		row Row
	}
	acc := make(map[string]latest)
	for rows.Next() {
		var (
			id                        string
			at                        time.Time
			priceIn, priceOut, markup string
			perCall                   *string
			perMinuteAudio            *string
			perCharsAudio             *string
		)
		if err := rows.Scan(&id, &at, &priceIn, &priceOut, &markup, &perCall, &perMinuteAudio, &perCharsAudio); err != nil {
			return nil, err
		}
		row, perr := parseRow(priceIn, priceOut, markup, perCall, perMinuteAudio, perCharsAudio)
		if perr != nil {
			return nil, perr
		}
		if cur, ok := acc[id]; !ok || at.After(cur.at) {
			acc[id] = latest{at: at, row: row}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[string]Row, len(acc))
	for id, l := range acc {
		out[id] = l.row
	}
	return &Snapshot{rows: out}, nil
}

// parseRow parses the text-scanned NUMERIC columns into exact Decimals.
func parseRow(priceIn, priceOut, markup string, perCall, perMinuteAudio, perCharsAudio *string) (Row, error) {
	in, err := decimal.NewFromString(priceIn)
	if err != nil {
		return Row{}, err
	}
	out, err := decimal.NewFromString(priceOut)
	if err != nil {
		return Row{}, err
	}
	mk, err := decimal.NewFromString(markup)
	if err != nil {
		return Row{}, err
	}
	r := Row{PriceIn: in, PriceOut: out, Markup: mk}
	if perCall != nil {
		pc, err := decimal.NewFromString(*perCall)
		if err != nil {
			return Row{}, err
		}
		r.PerCallPrice = &pc
	}
	// Story 9.6 — nullable per-minute audio price (NULL for every token model).
	if perMinuteAudio != nil {
		pm, err := decimal.NewFromString(*perMinuteAudio)
		if err != nil {
			return Row{}, err
		}
		r.PricePerMinuteAudio = &pm
	}
	// Story 9.7 — nullable per-1k-chars audio price (NULL for token + ASR models).
	if perCharsAudio != nil {
		pc, err := decimal.NewFromString(*perCharsAudio)
		if err != nil {
			return Row{}, err
		}
		r.PricePerCharsAudio = &pc
	}
	return r, nil
}

// LoadFunc loads a fresh Snapshot.
type LoadFunc func(ctx context.Context) (*Snapshot, error)

// Provider holds the current Snapshot behind an atomic pointer and refreshes it
// on a fixed cadence. Reads (Current) are lock-free; a failed refresh keeps the
// last-good snapshot (BLIND-ERROR-001 — deductions continue at the stale price,
// bounded by the 60s window, BR-C-4).
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

// Current returns the latest snapshot. Never nil. Safe for concurrent use.
func (p *Provider) Current() *Snapshot { return p.cur.Load() }

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
				p.logger.WarnContext(ctx, "pricing_refresh_failed",
					slog.String("event", "pricing_refresh_failed"),
					slog.String("error", err.Error()),
				)
				continue // keep last-good
			}
			p.cur.Store(snap)
			p.logger.InfoContext(ctx, "pricing_refreshed",
				slog.String("event", "pricing_refreshed"),
				slog.Int("models_priced", snap.Len()),
			)
		}
	}
}
