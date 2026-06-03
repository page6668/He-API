// Package pricing is routing-svc's read-only view over he_api.model_pricing
// (Story 6.2 AC2, Q-K / Q-E). It exposes:
//
//   - Snapshot — an immutable model_id → (input+output price) map the cost
//     strategy ranks over (BR2-2: read a snapshot, never a per-request query);
//   - Load    — reads the latest-effective_at pricing row per model from PG;
//   - Provider — holds the current Snapshot behind an atomic pointer with a
//     boot load + 60s periodic refresh (Q-E), keeping the last-good snapshot on
//     a refresh error (BLIND-ERROR-004) and serving torn-read-free concurrent
//     reads during a swap (BLIND-CONCURRENCY-001).
//
// The pricing READ is the ONLY new persistent source 6.2 introduces; staleness
// is bounded + non-billing-authoritative (billing reads model_pricing
// independently — Epic 7), so a stale snapshot yields slightly-suboptimal cost
// routing, never a wrong charge.
package pricing

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultRefreshInterval is the Q-E pricing-snapshot refresh cadence.
const DefaultRefreshInterval = 60 * time.Second

// loadSQL reads every pricing row; Load reduces to the latest effective_at per
// model in Go (so the latest-row selection is unit-testable without PG, and PG
// does no per-model DISTINCT ON work). The (input+output) sum is cast to
// float8 server-side so it scans cleanly into a Go float64 (NUMERIC otherwise
// needs a pgtype codec). The catalogue is bounded (~8 models, few price rows),
// so a full-table read at boot/refresh is cheap.
const loadSQL = `SELECT model_id, effective_at,
	(upstream_price_per_1k_input_tokens + upstream_price_per_1k_output_tokens)::float8
FROM he_api.model_pricing`

// Querier is the minimal pgx surface pricing.Load needs. It is satisfied by
// *pgxpool.Pool (production) and pgxmock (unit tests) — mirroring the auth-svc
// repository.Querier seam.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Snapshot is an immutable model_id → (input+output price sum) view. The zero
// value (and a nil *Snapshot) is a valid empty snapshot: PriceOf always reports
// ok=false, so the cost strategy degrades every candidate to ranked-last rather
// than panicking (BLIND-ERROR-003: PG-unavailable-at-boot still serves).
type Snapshot struct {
	sums map[string]float64
}

// NewSnapshot builds an immutable Snapshot from a model_id → price-sum map. The
// input is defensively copied so a caller mutating its map after construction
// cannot race a concurrent reader.
func NewSnapshot(sums map[string]float64) *Snapshot {
	cp := make(map[string]float64, len(sums))
	for k, v := range sums {
		cp[k] = v
	}
	return &Snapshot{sums: cp}
}

// PriceOf returns the (input+output) price sum for modelID and whether a
// pricing row exists. ok=false → the cost strategy ranks the candidate last
// (BR2-3 missing-pricing degradation), never a panic.
func (s *Snapshot) PriceOf(modelID string) (sum float64, ok bool) {
	if s == nil || s.sums == nil {
		return 0, false
	}
	v, ok := s.sums[modelID]
	return v, ok
}

// Len reports the number of priced models in the snapshot.
func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.sums)
}

// Load reads the latest-effective_at pricing row per model from PG and returns
// an immutable Snapshot. A query/scan error is returned verbatim (the caller
// decides boot fail-fast vs. last-good retention). A successful read with zero
// rows is a valid empty Snapshot, not an error.
func Load(ctx context.Context, q Querier) (*Snapshot, error) {
	rows, err := q.Query(ctx, loadSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // RESOURCE-001: connection returned to the pool on every path

	type latest struct {
		at  time.Time
		sum float64
	}
	acc := make(map[string]latest)
	for rows.Next() {
		var (
			id  string
			at  time.Time
			sum float64
		)
		if err := rows.Scan(&id, &at, &sum); err != nil {
			return nil, err
		}
		if cur, ok := acc[id]; !ok || at.After(cur.at) {
			acc[id] = latest{at: at, sum: sum}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sums := make(map[string]float64, len(acc))
	for id, l := range acc {
		sums[id] = l.sum
	}
	return &Snapshot{sums: sums}, nil
}

// LoadFunc loads a fresh Snapshot. *pgxpool.Pool-backed production wiring closes
// over the pool; tests inject a deterministic fake.
type LoadFunc func(ctx context.Context) (*Snapshot, error)

// Provider holds the current pricing Snapshot behind an atomic pointer and
// refreshes it on a fixed cadence (Q-E). Reads (Current) are lock-free and
// torn-read-free; a failed refresh keeps the last-good snapshot.
type Provider struct {
	cur      atomic.Pointer[Snapshot]
	load     LoadFunc
	interval time.Duration
	logger   *slog.Logger
}

// NewProvider seeds the Provider with a boot snapshot and the refresh inputs.
// boot may be nil/empty (BLIND-ERROR-003) — Current then returns an empty
// snapshot and the next successful refresh fills it. interval <= 0 falls back
// to DefaultRefreshInterval. logger may be nil (slog.Default()).
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

// Run blocks refreshing the snapshot every interval until ctx is cancelled. A
// refresh error is logged and the last-good snapshot is retained (no
// empty-snapshot flap — BLIND-ERROR-004). Call in a dedicated goroutine.
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
