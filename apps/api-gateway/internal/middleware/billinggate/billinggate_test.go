package billinggate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func nextOK() (http.Handler, *bool) {
	called := new(bool)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served"))
	}), called
}

func doGate(t *testing.T, reader BalanceReader, userID string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	next, called := nextOK()
	gate := New(Options{Reader: reader})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := req.Context()
	if userID != "" {
		ctx = middleware.BearerWithUserID(ctx, userID)
	}
	rr := httptest.NewRecorder()
	gate(next).ServeHTTP(rr, req.WithContext(ctx))
	return rr, *called
}

// 7.1-INT-026 — balance ≤ 0 → 402 before dispatch (next NOT called).
func TestGate_RejectsNonPositive(t *testing.T) {
	reader := func(context.Context, string) (string, bool, error) { return "0.0000", true, nil }
	rr, called := doGate(t, reader, "u1")
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rr.Code)
	}
	if called {
		t.Fatal("next handler must NOT be called on insufficient balance")
	}
}

// 7.1-INT-029 — the 402 body matches the §5.1.2 envelope (code + type).
func TestGate_EnvelopeShape(t *testing.T) {
	reader := func(context.Context, string) (string, bool, error) { return "-0.0186", true, nil }
	rr, _ := doGate(t, reader, "u1")
	var env struct {
		Error struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			Type        string `json:"type"`
			HeRequestID string `json:"he_request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not a JSON envelope: %v (%s)", err, rr.Body.String())
	}
	if env.Error.Code != "402_balance_insufficient" {
		t.Fatalf("code = %q, want 402_balance_insufficient", env.Error.Code)
	}
	if env.Error.Type == "" || env.Error.HeRequestID == "" {
		t.Fatalf("envelope missing type/he_request_id: %+v", env.Error)
	}
}

// 7.1-INT-028 — balance > 0 → request proceeds (no false-reject).
func TestGate_AllowsPositive(t *testing.T) {
	reader := func(context.Context, string) (string, bool, error) { return "0.0157", true, nil }
	rr, called := doGate(t, reader, "u1")
	if rr.Code != http.StatusOK || !called {
		t.Fatalf("positive balance must proceed: status=%d called=%v", rr.Code, called)
	}
}

// 7.1-INT-027 — Redis error → FAIL-OPEN (request allowed).
func TestGate_FailOpenOnReaderError(t *testing.T) {
	reader := func(context.Context, string) (string, bool, error) {
		return "", false, errors.New("redis down")
	}
	rr, called := doGate(t, reader, "u1")
	if rr.Code != http.StatusOK || !called {
		t.Fatalf("reader error must FAIL-OPEN: status=%d called=%v", rr.Code, called)
	}
}

// Absent mirror (new/not-yet-deducted user) → allow.
func TestGate_FailOpenOnAbsent(t *testing.T) {
	reader := func(context.Context, string) (string, bool, error) { return "", false, nil }
	rr, called := doGate(t, reader, "u1")
	if rr.Code != http.StatusOK || !called {
		t.Fatalf("absent mirror must allow: status=%d called=%v", rr.Code, called)
	}
}

// 7.1-BLIND-BOUNDARY-004 — exact 0 → reject; 0.0001 → allow.
func TestGate_ZeroBoundary(t *testing.T) {
	rrZero, calledZero := doGate(t, func(context.Context, string) (string, bool, error) {
		return "0.0000", true, nil
	}, "u1")
	if rrZero.Code != http.StatusPaymentRequired || calledZero {
		t.Fatalf("balance=0 must reject (≤0): status=%d called=%v", rrZero.Code, calledZero)
	}
	rrEps, calledEps := doGate(t, func(context.Context, string) (string, bool, error) {
		return "0.0001", true, nil
	}, "u1")
	if rrEps.Code != http.StatusOK || !calledEps {
		t.Fatalf("balance=0.0001 must allow: status=%d called=%v", rrEps.Code, calledEps)
	}
}

// No bound user (gate cannot evaluate) → allow (defence-in-depth).
func TestGate_NoUserAllows(t *testing.T) {
	reader := func(context.Context, string) (string, bool, error) { return "0.0000", true, nil }
	rr, called := doGate(t, reader, "")
	if rr.Code != http.StatusOK || !called {
		t.Fatalf("missing user must allow: status=%d called=%v", rr.Code, called)
	}
}
