package pricing_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/routing-svc/internal/pricing"
)

// 6.2-INT-005 — Load reads model_pricing and PriceOf returns (sum, ok).
func TestLoad_ReadsPricingRows(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock.NewConn: %v", err)
	}
	defer func() { _ = mock.Close(context.Background()) }()

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT model_id, effective_at`).
		WillReturnRows(pgxmock.NewRows([]string{"model_id", "effective_at", "price_sum"}).
			AddRow("qwen-max", at, 0.016).
			AddRow("deepseek-v3", at, 0.001))

	snap, err := pricing.Load(context.Background(), mock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := snap.PriceOf("qwen-max"); !ok || got != 0.016 {
		t.Errorf("PriceOf(qwen-max) = %v, %v; want 0.016, true", got, ok)
	}
	if got, ok := snap.PriceOf("deepseek-v3"); !ok || got != 0.001 {
		t.Errorf("PriceOf(deepseek-v3) = %v, %v; want 0.001, true", got, ok)
	}
	if _, ok := snap.PriceOf("not-priced"); ok {
		t.Errorf("PriceOf(not-priced) ok = true, want false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// 6.2-UNIT-013 / 6.2-BLIND-DATA-001 — multiple effective_at rows per model →
// the LATEST effective_at row is used, consistently.
func TestLoad_LatestEffectiveAtWins(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock.NewConn: %v", err)
	}
	defer func() { _ = mock.Close(context.Background()) }()

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Rows intentionally out of order to prove Go-side latest-selection.
	mock.ExpectQuery(`SELECT model_id, effective_at`).
		WillReturnRows(pgxmock.NewRows([]string{"model_id", "effective_at", "price_sum"}).
			AddRow("qwen-max", older, 0.999).
			AddRow("qwen-max", newer, 0.016).
			AddRow("qwen-max", older, 0.500))

	snap, err := pricing.Load(context.Background(), mock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := snap.PriceOf("qwen-max"); got != 0.016 {
		t.Errorf("PriceOf(qwen-max) = %v, want 0.016 (latest effective_at)", got)
	}
}

// 6.2-INT-005 boundary — zero rows is a valid empty snapshot, not an error.
func TestLoad_EmptyIsNotError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock.NewConn: %v", err)
	}
	defer func() { _ = mock.Close(context.Background()) }()

	mock.ExpectQuery(`SELECT model_id, effective_at`).
		WillReturnRows(pgxmock.NewRows([]string{"model_id", "effective_at", "price_sum"}))

	snap, err := pricing.Load(context.Background(), mock)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if snap.Len() != 0 {
		t.Errorf("empty snapshot Len = %d, want 0", snap.Len())
	}
}

// 6.2-BLIND-ERROR-003 — a query error propagates (caller decides last-good vs
// fail); the returned snapshot is nil.
func TestLoad_QueryErrorPropagates(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock.NewConn: %v", err)
	}
	defer func() { _ = mock.Close(context.Background()) }()

	mock.ExpectQuery(`SELECT model_id, effective_at`).
		WillReturnError(errors.New("connection refused"))

	if _, err := pricing.Load(context.Background(), mock); err == nil {
		t.Fatalf("Load: expected error, got nil")
	}
}

// PriceOf on a nil/empty snapshot never panics (BLIND-ERROR-003 boot path).
func TestSnapshot_NilSafe(t *testing.T) {
	t.Parallel()
	var s *pricing.Snapshot
	if _, ok := s.PriceOf("anything"); ok {
		t.Errorf("nil snapshot PriceOf ok = true, want false")
	}
	if s.Len() != 0 {
		t.Errorf("nil snapshot Len = %d, want 0", s.Len())
	}
	empty := pricing.NewSnapshot(nil)
	if _, ok := empty.PriceOf("x"); ok {
		t.Errorf("empty snapshot PriceOf ok = true")
	}
}

// 6.2-BLIND-ERROR-004 — a refresh failure keeps the LAST-GOOD snapshot (no
// empty-snapshot flap).
func TestProvider_RefreshFailureKeepsLastGood(t *testing.T) {
	t.Parallel()
	boot := pricing.NewSnapshot(map[string]float64{"qwen-max": 0.016})

	var calls int
	load := func(ctx context.Context) (*pricing.Snapshot, error) {
		calls++
		return nil, errors.New("pg down")
	}
	p := pricing.NewProvider(boot, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	// Let a couple of refresh ticks fail.
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	if calls == 0 {
		t.Fatalf("refresh never ran")
	}
	if got, ok := p.Current().PriceOf("qwen-max"); !ok || got != 0.016 {
		t.Errorf("after failed refresh PriceOf = %v, %v; want last-good 0.016, true", got, ok)
	}
}

// Provider applies a successful refresh (Q-E cadence; read-through, not a
// per-request query).
func TestProvider_RefreshAppliesNewSnapshot(t *testing.T) {
	t.Parallel()
	boot := pricing.NewSnapshot(map[string]float64{"qwen-max": 0.999})
	updated := pricing.NewSnapshot(map[string]float64{"qwen-max": 0.016})

	load := func(ctx context.Context) (*pricing.Snapshot, error) { return updated, nil }
	p := pricing.NewProvider(boot, load, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	deadline := time.After(time.Second)
	for {
		if got, _ := p.Current().PriceOf("qwen-max"); got == 0.016 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("refresh did not apply updated snapshot")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	<-done
}

// 6.2-BLIND-CONCURRENCY-001 — concurrent Current()/PriceOf during snapshot
// swaps are torn-read-free (run with -race).
func TestProvider_ConcurrentReadDuringSwap(t *testing.T) {
	t.Parallel()
	snaps := []*pricing.Snapshot{
		pricing.NewSnapshot(map[string]float64{"m": 1}),
		pricing.NewSnapshot(map[string]float64{"m": 2}),
	}
	idx := 0
	load := func(ctx context.Context) (*pricing.Snapshot, error) {
		idx = (idx + 1) % len(snaps)
		return snaps[idx], nil
	}
	p := pricing.NewProvider(snaps[0], load, time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.Run(ctx) }()

	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 2000; j++ {
				if v, ok := p.Current().PriceOf("m"); ok && v != 1 && v != 2 {
					t.Errorf("torn read: %v", v)
				}
			}
		}()
	}
	readers.Wait()
	cancel()
	wg.Wait()
}
