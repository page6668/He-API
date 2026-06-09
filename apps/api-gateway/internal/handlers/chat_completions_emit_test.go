package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// recordingEmitter captures emitted usage events synchronously (the handler
// calls Emit inline; only the Kafka emitter detaches).
type recordingEmitter struct {
	mu     sync.Mutex
	events []*billingv1.UsageEvent
}

func (r *recordingEmitter) Emit(_ context.Context, ev *billingv1.UsageEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingEmitter) all() []*billingv1.UsageEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*billingv1.UsageEvent(nil), r.events...)
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// 7.1-INT-014 — a successful (mock) completion emits exactly ONE usage.recorded
// event carrying RAW token totals, the served model, and the billing subject.
func TestEmit_MockSuccess_OneEvent(t *testing.T) {
	rec := &recordingEmitter{}
	h := handlers.NewChatCompletionsHandler(discardLogger(), handlers.WithUsageEmitter(rec))

	rr := doRequest(t, h, validReqBody)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want exactly 1", len(events))
	}
	ev := events[0]
	if ev.GetUserId() != testUserID {
		t.Fatalf("user_id = %q, want %q", ev.GetUserId(), testUserID)
	}
	if ev.GetModel() != "qwen-max" {
		t.Fatalf("model = %q, want qwen-max", ev.GetModel())
	}
	// Mock usage is {10,20,30}.
	if ev.GetPromptTokens() != 10 || ev.GetCompletionTokens() != 20 || ev.GetTotalTokens() != 30 {
		t.Fatalf("tokens = %d/%d/%d, want 10/20/30", ev.GetPromptTokens(), ev.GetCompletionTokens(), ev.GetTotalTokens())
	}
	if ev.GetIsStreaming() || ev.GetIsAbLeg() {
		t.Fatalf("non-stream single request must have is_streaming=false is_ab_leg=false: %+v", ev)
	}
	if ev.GetBillingMode() != billingv1.BillingMode_BILLING_MODE_PER_TOKEN {
		t.Fatalf("billing_mode = %v, want PER_TOKEN", ev.GetBillingMode())
	}
}

// 7.1-INT-017 — a FAILED completion (rejected request) emits NO event → no
// charge (BR-D-7: you only bill for served tokens).
func TestEmit_FailedCompletion_NoEvent(t *testing.T) {
	rec := &recordingEmitter{}
	h := handlers.NewChatCompletionsHandler(discardLogger(), handlers.WithUsageEmitter(rec))

	// Empty model → 400 validation failure (no completion served).
	rr := doRequest(t, h, `{"model":"","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code == 200 {
		t.Fatal("expected a non-200 rejection")
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("failed completion emitted %d events, want 0", n)
	}
}
