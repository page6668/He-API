package handlers_test

import (
	"net/http"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// 7.1-INT-016 — A/B dual-billing: both legs succeed → TWO usage.recorded events
// (one per leg), each is_ab_leg=true with a distinct ledger_key suffix and its
// OWN token totals (parity with the 6.4 TPM dual-billing invariant).
func TestEmit_ABDualBilling_TwoEvents(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A-answer", 10, 20)
	legB := legHandle("cmpl-b", "deepseek-v3", "B-answer", 30, 40)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	rec := &recordingEmitter{}
	h := abHandler(t, reg, &countingDeducter{}, handlers.WithUsageEmitter(rec))

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("emitted %d events, want 2 (one per leg)", len(events))
	}

	byModel := map[string]bool{}
	keys := map[string]bool{}
	for _, ev := range events {
		if !ev.GetIsAbLeg() {
			t.Fatalf("A/B leg event must have is_ab_leg=true: %+v", ev)
		}
		byModel[ev.GetModel()] = true
		keys[ev.GetLedgerKey()] = true
		switch ev.GetModel() {
		case "qwen-max":
			if ev.GetPromptTokens() != 10 || ev.GetCompletionTokens() != 20 {
				t.Fatalf("leg qwen-max tokens = %d/%d, want 10/20", ev.GetPromptTokens(), ev.GetCompletionTokens())
			}
		case "deepseek-v3":
			if ev.GetPromptTokens() != 30 || ev.GetCompletionTokens() != 40 {
				t.Fatalf("leg deepseek-v3 tokens = %d/%d, want 30/40", ev.GetPromptTokens(), ev.GetCompletionTokens())
			}
		}
	}
	if !byModel["qwen-max"] || !byModel["deepseek-v3"] {
		t.Fatalf("expected one event per leg model, got %+v", byModel)
	}
	// Q-ABKEY — each leg gets a DISTINCT ledger_key so they dedup independently.
	if len(keys) != 2 {
		t.Fatalf("expected 2 distinct ledger_keys, got %v", keys)
	}
}
