package password_test

import (
	"context"
	"crypto/sha1" //nolint:gosec // HIBP API uses SHA-1 by spec (k-anonymity range query); not a security primitive.
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/auth-svc/internal/password"
)

const breachedSuffixCount = "5\r\n"

// Helper — builds a server that returns the expected suffix list for the
// SHA-1 prefix of `breachedPlaintext`, or an empty body for any other prefix.
func newHIBPStub(t *testing.T, breachedPlaintext string, lastPath *string) *httptest.Server {
	t.Helper()
	breachedHash := strings.ToUpper(fmt.Sprintf("%x", sha1.Sum([]byte(breachedPlaintext)))) //nolint:gosec // see file comment
	breachedPrefix := breachedHash[:5]
	breachedSuffix := breachedHash[5:]
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastPath != nil {
			*lastPath = r.URL.Path
		}
		// Expected form: /range/{PREFIX}
		const prefix = "/range/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		req := strings.ToUpper(strings.TrimPrefix(r.URL.Path, prefix))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if req == breachedPrefix {
			_, _ = fmt.Fprintf(w, "%s:%s", breachedSuffix, breachedSuffixCount)
			return
		}
		// Some other suffixes for body realism — none match the queried password.
		_, _ = fmt.Fprintln(w, "0000000000000000000000000000000000000:1")
	}))
}

// Scenario: 2.2-UNIT-007
// CheckBreached sends only SHA-1(pw)[0:5] to HIBP (k-anonymity, zero leak).
// Outbound URL path matches `/range/[0-9A-F]{5}$`.
func TestCheckBreached_KAnonymityPath(t *testing.T) {
	t.Parallel()
	var lastPath string
	srv := newHIBPStub(t, "password123", &lastPath)
	defer srv.Close()

	c := &password.HIBPClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	_ = c.CheckBreached(context.Background(), []byte("password123"))

	re := regexp.MustCompile(`^/range/[0-9A-F]{5}$`)
	if !re.MatchString(lastPath) {
		t.Fatalf("outbound path = %q, want match %s (TS-CONS-004 k-anonymity)", lastPath, re.String())
	}
}

// Scenario: 2.2-UNIT-008
// CheckBreached returns ErrPasswordBreached when the suffix is in the response.
func TestCheckBreached_BreachedSuffixMatches(t *testing.T) {
	t.Parallel()
	srv := newHIBPStub(t, "password123", nil)
	defer srv.Close()

	c := &password.HIBPClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	err := c.CheckBreached(context.Background(), []byte("password123"))
	if !errors.Is(err, password.ErrPasswordBreached) {
		t.Fatalf("CheckBreached(breached) = %v, want ErrPasswordBreached", err)
	}
}

// Scenario: 2.2-UNIT-008b — non-breached password returns nil.
func TestCheckBreached_CleanPassword(t *testing.T) {
	t.Parallel()
	srv := newHIBPStub(t, "different-password", nil)
	defer srv.Close()

	c := &password.HIBPClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	if err := c.CheckBreached(context.Background(), []byte("correct horse battery staple")); err != nil {
		t.Fatalf("CheckBreached(clean) = %v, want nil", err)
	}
}

// Scenario: 2.2-UNIT-009
// Fail-closed on HTTP 5xx / timeout / DNS failure — returns ErrHIBPUnavailable
// (NOT a permissive nil-pass).
func TestCheckBreached_FailClosedOn5xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &password.HIBPClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	err := c.CheckBreached(context.Background(), []byte("any-password"))
	if !errors.Is(err, password.ErrHIBPUnavailable) {
		t.Fatalf("CheckBreached(500) = %v, want ErrHIBPUnavailable (fail-closed)", err)
	}
}

func TestCheckBreached_FailClosedOnConnError(t *testing.T) {
	t.Parallel()
	// Closed server → connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	c := &password.HIBPClient{BaseURL: addr, HTTPClient: &http.Client{Timeout: 500 * time.Millisecond}}
	err := c.CheckBreached(context.Background(), []byte("any-password"))
	if !errors.Is(err, password.ErrHIBPUnavailable) {
		t.Fatalf("CheckBreached(conn-refused) = %v, want ErrHIBPUnavailable", err)
	}
}

func TestCheckBreached_FailClosedOnTimeout(t *testing.T) {
	t.Parallel()
	// Server holds the request open longer than the client timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			return
		}
	}))
	defer srv.Close()

	c := &password.HIBPClient{BaseURL: srv.URL, HTTPClient: &http.Client{Timeout: 200 * time.Millisecond}}
	err := c.CheckBreached(context.Background(), []byte("any-password"))
	if !errors.Is(err, password.ErrHIBPUnavailable) {
		t.Fatalf("CheckBreached(timeout) = %v, want ErrHIBPUnavailable", err)
	}
}

// Scenario: 2.2-UNIT-010
// Default HIBPClient (NewHIBPClient) has a 3s timeout per TS-CONS-004.
func TestNewHIBPClient_DefaultTimeout(t *testing.T) {
	t.Parallel()
	c := password.NewHIBPClient()
	if c.HTTPClient == nil {
		t.Fatalf("NewHIBPClient: HTTPClient is nil")
	}
	if got, want := c.HTTPClient.Timeout, 3*time.Second; got != want {
		t.Fatalf("default HTTP client timeout = %v, want %v (TS-CONS-004)", got, want)
	}
	if c.BaseURL == "" {
		t.Fatalf("NewHIBPClient: BaseURL is empty")
	}
}
