package pricing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"
)

var loadCols = []string{
	"model_id", "effective_at",
	"upstream_price_per_1k_input_tokens", "upstream_price_per_1k_output_tokens",
	"markup_percent", "per_call_price_usd",
	"price_per_minute_audio_usd",
}

// 7.1-UNIT-001 — boot-load resolves the latest-effective_at row per model.
func TestLoad_LatestEffectiveAtPerModel(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows(loadCols).
		AddRow("qwen-max", old, "0.0040", "0.0120", "10.00", (*string)(nil), (*string)(nil)).
		AddRow("qwen-max", newer, "0.0080", "0.0240", "10.00", (*string)(nil), (*string)(nil)). // latest wins
		AddRow("deepseek-v3", old, "0.0002", "0.0008", "15.00", (*string)(nil), (*string)(nil))
	mock.ExpectQuery("FROM he_api.model_pricing").WillReturnRows(rows)

	snap, err := Load(context.Background(), mock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Len() != 2 {
		t.Fatalf("snapshot len = %d, want 2", snap.Len())
	}
	row, ok := snap.Row("qwen-max")
	if !ok {
		t.Fatal("qwen-max missing")
	}
	if row.PriceIn.String() != "0.008" { // latest (newer) row, not the old 0.0040
		t.Fatalf("qwen-max price_in = %s, want 0.008 (latest effective_at)", row.PriceIn.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.1-UNIT-001b — nullable per_call_price_usd scans as nil (Q-PERCALL unwired).
func TestLoad_NullPerCall(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rows := pgxmock.NewRows(loadCols).
		AddRow("m", time.Now().UTC(), "0.0080", "0.0240", "10.00", (*string)(nil), (*string)(nil))
	mock.ExpectQuery("FROM he_api.model_pricing").WillReturnRows(rows)

	snap, err := Load(context.Background(), mock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row, _ := snap.Row("m")
	if row.PerCallPrice != nil {
		t.Fatalf("per_call should be nil, got %v", row.PerCallPrice)
	}
}

// 7.1-UNIT-003 — a loaded snapshot that misses a model → ComputeCost ErrNoPricing
// (never a per-event PG query, never a silent zero-charge).
func TestLoad_SnapshotMiss(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rows := pgxmock.NewRows(loadCols).
		AddRow("known", time.Now().UTC(), "0.0080", "0.0240", "10.00", (*string)(nil), (*string)(nil))
	mock.ExpectQuery("FROM he_api.model_pricing").WillReturnRows(rows)

	snap, _ := Load(context.Background(), mock)
	if _, err := snap.ComputeCost("missing", 100, 100, 0, perToken); err != ErrNoPricing {
		t.Fatalf("missing model err = %v, want ErrNoPricing", err)
	}
}

// 7.1-UNIT-002 — refresh replaces a stale row; the next snapshot read uses the
// new price (bounded staleness, 6.2 parity).
func TestProvider_RefreshSwapsSnapshot(t *testing.T) {
	boot := snap(t, "m", "0.0080", "0.0240", "10.00")
	next := snap(t, "m", "0.0160", "0.0480", "10.00") // doubled price

	calls := 0
	load := func(context.Context) (*Snapshot, error) {
		calls++
		return next, nil
	}
	p := NewProvider(boot, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		row, _ := p.Current().Row("m")
		if row.PriceIn.String() == "0.016" {
			return // refreshed
		}
		select {
		case <-deadline:
			t.Fatalf("snapshot not refreshed within deadline (calls=%d)", calls)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// 7.1-BLIND-ERROR-001 — a refresh error retains the last-good snapshot;
// deductions continue at the stale price.
func TestProvider_RefreshErrorKeepsLastGood(t *testing.T) {
	boot := snap(t, "m", "0.0080", "0.0240", "10.00")
	load := func(context.Context) (*Snapshot, error) {
		return nil, errors.New("pg unreachable")
	}
	p := NewProvider(boot, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	time.Sleep(40 * time.Millisecond) // several failed refreshes
	row, ok := p.Current().Row("m")
	if !ok || row.PriceIn.String() != "0.008" {
		t.Fatalf("last-good snapshot not retained after refresh errors: %+v ok=%v", row, ok)
	}
}

// 7.1-BLIND-RESOURCE-002 — the ticker goroutine stops on ctx cancel (no leak).
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
