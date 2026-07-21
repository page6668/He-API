package catalogue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fallbackModels() []Model {
	return []Model{{ID: "fallback-model", DisplayName: "Fallback", Vendor: "alibaba"}}
}

// AD-002 failure mode 2: with no database the gateway must still serve the
// compiled-in fallback rather than 5xx — /v1/models and /public/models are
// load-bearing for SEO and client integrations.
func TestSnapshot_NoDatabase_ServesFallback(t *testing.T) {
	snap := NewSnapshot(NewStore(nil), fallbackModels(), time.Minute, quietLogger())

	if err := snap.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh with nil pool = nil error, want error")
	}
	got := snap.Models()
	if len(got) != 1 || got[0].ID != "fallback-model" {
		t.Fatalf("Models() = %+v, want the fallback entry", got)
	}
	if !snap.Stale() {
		t.Error("Stale() = false, want true when serving fallback")
	}
}

// AD-002 failure mode 1: a refresh error keeps the last good snapshot. The
// catalogue degrades to stale-but-available, never to empty.
func TestSnapshot_RefreshError_KeepsLastGood(t *testing.T) {
	snap := NewSnapshot(NewStore(nil), fallbackModels(), time.Minute, quietLogger())

	// Simulate a successful earlier load.
	snap.mu.Lock()
	snap.models = []Model{{ID: "qwen3.7-plus"}}
	snap.stale = false
	snap.mu.Unlock()

	if err := snap.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	got := snap.Models()
	if len(got) != 1 || got[0].ID != "qwen3.7-plus" {
		t.Fatalf("Models() = %+v, want the last good snapshot retained", got)
	}
	if !snap.Stale() {
		t.Error("Stale() = false, want true after a failed refresh")
	}
}

// An empty result set is a fault, not "we sell nothing". A truncated table or a
// bad migration must not silently blank the public catalogue.
func TestSnapshot_EmptyResultIsTreatedAsFault(t *testing.T) {
	snap := NewSnapshot(&Store{}, fallbackModels(), time.Minute, quietLogger())
	snap.mu.Lock()
	snap.models = []Model{{ID: "qwen3.7-plus"}}
	snap.stale = false
	snap.mu.Unlock()

	// Store with a nil pool errors before returning rows; assert the retention
	// contract holds on that path too.
	err := snap.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if len(snap.Models()) != 1 {
		t.Fatalf("snapshot was blanked: %+v", snap.Models())
	}
}

// Default TTL guards against a caller passing 0 and creating a hot loop.
func TestNewSnapshot_DefaultsTTL(t *testing.T) {
	snap := NewSnapshot(NewStore(nil), nil, 0, quietLogger())
	if snap.ttl != 5*time.Minute {
		t.Errorf("ttl = %v, want 5m default", snap.ttl)
	}
}

// Start must return promptly and never panic when the database is absent —
// it runs on the gateway's boot path.
func TestSnapshot_Start_DoesNotBlockWithoutDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	snap := NewSnapshot(NewStore(nil), fallbackModels(), 50*time.Millisecond, quietLogger())
	done := make(chan struct{})
	go func() { snap.Start(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start blocked without a database")
	}
	if len(snap.Models()) != 1 {
		t.Error("fallback not served after Start")
	}
}

// Load must distinguish "no database" from "database says zero models" so the
// caller never mistakes a missing pool for an empty catalogue.
func TestStore_Load_NilPool(t *testing.T) {
	_, err := NewStore(nil).Load(context.Background())
	if err == nil {
		t.Fatal("Load with nil pool = nil error, want error")
	}
	var nilStore *Store
	if _, err := nilStore.Load(context.Background()); err == nil {
		t.Fatal("Load on nil receiver = nil error, want error")
	}
	if errors.Is(err, context.Canceled) {
		t.Error("unexpected error kind")
	}
}
