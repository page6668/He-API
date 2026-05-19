package requestid

import (
	"context"
	"testing"
)

func TestFromContext_Empty(t *testing.T) {
	id, ok := FromContext(context.Background())
	if ok || id != "" {
		t.Fatalf("FromContext on bare ctx = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestWithRequestID_RoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req_abcdef012345")
	id, ok := FromContext(ctx)
	if !ok || id != "req_abcdef012345" {
		t.Fatalf("FromContext = (%q, %v), want (\"req_abcdef012345\", true)", id, ok)
	}
}

func TestContextWith_IsAliasOfWithRequestID(t *testing.T) {
	ctx := ContextWith(context.Background(), "req_aaaaaaaaaaaa")
	id, ok := FromContext(ctx)
	if !ok || id != "req_aaaaaaaaaaaa" {
		t.Fatalf("FromContext via ContextWith = (%q, %v), want (\"req_aaaaaaaaaaaa\", true)", id, ok)
	}
}

func TestHeaderName_Constant(t *testing.T) {
	if HeaderName != "X-He-Request-Id" {
		t.Fatalf("HeaderName = %q, want %q", HeaderName, "X-He-Request-Id")
	}
}

func TestSpanAttributeKey_Constant(t *testing.T) {
	if SpanAttributeKey != "he.request_id" {
		t.Fatalf("SpanAttributeKey = %q, want %q", SpanAttributeKey, "he.request_id")
	}
}
