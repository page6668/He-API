package fx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 7.2-UNIT-015 P0 — FxProvider.Get success → a positive decimal USD→CNY.
func Test7_2_UNIT015_HTTPProviderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"base_code":"USD","rates":{"CNY":7.21,"EUR":0.92}}`))
	}))
	defer srv.Close()

	rate, err := NewHTTPProvider(srv.URL, time.Second).Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rate.String() != "7.21" {
		t.Fatalf("rate = %s, want 7.21", rate.String())
	}
}

// 7.2-UNIT-018 P0 / BLIND-RESOURCE-001 — the provider call carries a HARD
// context deadline; a hung upstream is cut off (bounded fetch underpins stale-serve).
func Test7_2_UNIT018_BoundedFetchDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second): // hang well past the provider timeout
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	start := time.Now()
	_, err := NewHTTPProvider(srv.URL, 30*time.Millisecond).Get(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error on a hung upstream")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("fetch not bounded: took %v (timeout was 30ms)", elapsed)
	}
}

// 7.2-UNIT-019 P1 — the secret base-URL is NEVER embedded in an error (a key in
// the URL must not leak to logs). The fetched rate value MAY be logged.
func Test7_2_UNIT019_SecretNeverInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	secretURL := srv.URL + "/v6/SECRETKEY123/latest/USD"

	_, err := NewHTTPProvider(secretURL, time.Second).Get(context.Background())
	if err == nil {
		t.Fatal("expected an error on 503")
	}
	if strings.Contains(err.Error(), "SECRETKEY123") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaked the secret URL: %q", err.Error())
	}
}

// 7.2-UNIT-020 P0 — rate validation at the parse layer: a NaN/Inf/non-numeric
// rate cannot become a Decimal → ErrMalformed (feeds stale-serve, never persisted).
func Test7_2_UNIT020_RateValidation(t *testing.T) {
	cases := map[string]string{
		"non-numeric": `{"rates":{"CNY":"not-a-number"}}`,
		"NaN":         `{"rates":{"CNY":"NaN"}}`,
		"missing":     `{"rates":{"EUR":0.92}}`,
		"wrong-type":  `{"rates":{"CNY":{"nested":1}}}`,
	}
	for name, body := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := NewHTTPProvider(srv.URL, time.Second).Get(context.Background())
		if err == nil {
			t.Errorf("%s: expected ErrMalformed, got nil", name)
		}
		srv.Close()
	}
	// A valid positive rate passes validRate; zero/negative do not.
	if validRate(mustDec(t, "0")) || validRate(mustDec(t, "-1")) {
		t.Fatal("zero/negative must be invalid")
	}
	if !validRate(mustDec(t, "7.21")) {
		t.Fatal("positive rate must be valid")
	}
}

// 7.2-BLIND-ERROR-003 P0 — provider 503 → error (drives stale-serve at the
// refresh layer).
func Test7_2_BLIND_ERROR003_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := NewHTTPProvider(srv.URL, time.Second).Get(context.Background()); err == nil {
		t.Fatal("expected error on 503")
	}
}

// 7.2-BLIND-ERROR-002 P0 — network failure (connection refused) → error.
func Test7_2_BLIND_ERROR002_NetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // server is down → connection refused
	if _, err := NewHTTPProvider(url, 200*time.Millisecond).Get(context.Background()); err == nil {
		t.Fatal("expected error on connection refused")
	}
}
