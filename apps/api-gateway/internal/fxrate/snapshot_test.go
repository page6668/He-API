package fxrate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"
)

var fxCols = []string{"base_currency", "quote_currency", "rate", "fetched_at"}

// 7.2-UNIT-010 P1 [L-1, BR-C-1] — boot-load resolves the latest fetched_at row
// per (USD,CNY) (active rate = ORDER BY fetched_at DESC LIMIT 1, realised as a
// Go reduce over the append-only table; mirrors model_pricing latest-effective).
func TestLoad_LatestFetchedAtPerPair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	old := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 10, 0, 0, 5, 0, time.UTC)
	rows := pgxmock.NewRows(fxCols).
		AddRow("USD", "CNY", "7.20000000", old).
		AddRow("USD", "CNY", "7.18000000", newer). // latest wins
		AddRow("USD", "EUR", "0.92000000", old)
	mock.ExpectQuery("FROM he_api.fx_rates").WillReturnRows(rows)

	snap, err := Load(context.Background(), mock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Len() != 2 {
		t.Fatalf("snapshot len = %d, want 2", snap.Len())
	}
	r, ok := snap.Lookup("USD", "CNY")
	if !ok {
		t.Fatal("USD:CNY missing")
	}
	if r.Rate.String() != "7.18" { // latest (newer) row, not the old 7.20
		t.Fatalf("USD:CNY rate = %s, want 7.18 (latest fetched_at)", r.Rate.String())
	}
	if !r.FetchedAt.Equal(newer) {
		t.Fatalf("fetched_at = %v, want %v", r.FetchedAt, newer)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.2-UNIT-012 P0 [BR-C-5] — the read path NEVER calls the FX provider; Lookup
// resolves from the in-memory snapshot only (no Querier, no I/O).
func TestSnapshot_LookupNoIO(t *testing.T) {
	snap := NewSnapshot(map[string]Rate{
		"USD:CNY": {Base: "USD", Quote: "CNY", Rate: d(t, "7.21000000"), FetchedAt: time.Now().UTC()},
	})
	if _, ok := snap.Lookup("usd", " cny "); !ok { // case-insensitive + trimmed
		t.Fatal("Lookup should normalize case/whitespace")
	}
	if _, ok := snap.Lookup("USD", "JPY"); ok {
		t.Fatal("missing pair should report not-found, not a zero rate")
	}
	// nil snapshot is safe (cold-start / boot-before-PG).
	var nilSnap *Snapshot
	if _, ok := nilSnap.Lookup("USD", "CNY"); ok {
		t.Fatal("nil snapshot must report not-found")
	}
}

// 7.2-UNIT-011 P1 — a ~refresh picks up a newer row; the next conversion uses
// the new rate AND fx_as_of advances.
func TestProvider_RefreshSwapsSnapshot(t *testing.T) {
	bootAt := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	nextAt := time.Date(2026, 6, 10, 0, 0, 5, 0, time.UTC)
	boot := NewSnapshot(map[string]Rate{"USD:CNY": {Base: "USD", Quote: "CNY", Rate: d(t, "7.20"), FetchedAt: bootAt}})
	next := NewSnapshot(map[string]Rate{"USD:CNY": {Base: "USD", Quote: "CNY", Rate: d(t, "7.18"), FetchedAt: nextAt}})

	load := func(context.Context) (*Snapshot, error) { return next, nil }
	p := NewProvider(boot, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		rate, asOf, _ := p.Lookup("USD", "CNY")
		if rate.String() == "7.18" {
			if !asOf.Equal(nextAt) {
				t.Fatalf("fx_as_of = %v, want advanced to %v", asOf, nextAt)
			}
			return // refreshed
		}
		select {
		case <-deadline:
			t.Fatal("snapshot not refreshed within deadline")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// 7.2-BLIND-ERROR-001 P1 — a refresh error retains the last-good snapshot; reads
// continue at the stale rate (a stale display rate is cosmetic, never blocks).
func TestProvider_RefreshErrorKeepsLastGood(t *testing.T) {
	boot := NewSnapshot(map[string]Rate{"USD:CNY": {Base: "USD", Quote: "CNY", Rate: d(t, "7.20"), FetchedAt: time.Now().UTC()}})
	load := func(context.Context) (*Snapshot, error) { return nil, errors.New("pg unreachable") }
	p := NewProvider(boot, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	time.Sleep(40 * time.Millisecond) // several failed refreshes
	rate, _, ok := p.Lookup("USD", "CNY")
	if !ok || rate.String() != "7.2" {
		t.Fatalf("last-good snapshot not retained after refresh errors: rate=%s ok=%v", rate, ok)
	}
}

// 7.2-BLIND-RESOURCE-002 P2 — the ticker goroutine stops on ctx cancel (no leak).
func TestProvider_RunStopsOnCancel(t *testing.T) {
	load := func(context.Context) (*Snapshot, error) { return NewSnapshot(nil), nil }
	p := NewProvider(nil, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel (goroutine leak)")
	}
}
