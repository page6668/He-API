package sendgrid_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/notification-svc/internal/sendgrid"
)

// Scenario: P2e / 3 — happy path. POSTs to /v3/mail/send with Bearer auth +
// JSON payload; returns X-Message-Id header verbatim.
func TestClient_Send_HappyPath(t *testing.T) {
	t.Parallel()
	var capturedAuth, capturedContentType, capturedPath string
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedContentType = r.Header.Get("Content-Type")
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Message-Id", "stub-msg-id-123")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := sendgrid.NewClient("SG.test-key", sendgrid.Address{Email: "noreply@he-api.com", Name: "He-API"})
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()

	id, err := c.Send(context.Background(), sendgrid.SendRequest{
		To:       "user@example.com",
		Subject:  "Verify your He-API email",
		TextBody: "Open the link: https://example/v",
		HTMLBody: `<html><a href="https://example/v">Verify</a></html>`,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "stub-msg-id-123" {
		t.Errorf("returned message id = %q, want stub-msg-id-123", id)
	}

	if capturedPath != "/v3/mail/send" {
		t.Errorf("path = %q, want /v3/mail/send", capturedPath)
	}
	if capturedAuth != "Bearer SG.test-key" {
		t.Errorf("Authorization = %q, want Bearer SG.test-key", capturedAuth)
	}
	if capturedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", capturedContentType)
	}

	// Body shape: personalizations[0].to[0].email = recipient; from.email matches
	// constructor; content has text/plain THEN text/html.
	var got map[string]any
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("body JSON: %v\n%s", err, capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"to":[{"email":"user@example.com"}]`) {
		t.Errorf("body does not carry recipient correctly: %s", capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"from":{"email":"noreply@he-api.com","name":"He-API"}`) {
		t.Errorf("body does not carry sender correctly: %s", capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"subject":"Verify your He-API email"`) {
		t.Errorf("body missing subject: %s", capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"type":"text/plain"`) ||
		!strings.Contains(string(capturedBody), `"type":"text/html"`) {
		t.Errorf("body missing one of text/plain or text/html: %s", capturedBody)
	}
	// text/plain MUST appear BEFORE text/html (SendGrid convention).
	idxPlain := strings.Index(string(capturedBody), `"type":"text/plain"`)
	idxHTML := strings.Index(string(capturedBody), `"type":"text/html"`)
	if idxPlain == -1 || idxHTML == -1 || idxPlain > idxHTML {
		t.Errorf("body content order wrong: text/plain @%d, text/html @%d", idxPlain, idxHTML)
	}
}

// Scenario: P2e / 3 — 5xx → ErrTransient.
func TestClient_Send_5xxReturnsErrTransient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"errors":[{"message":"upstream down"}]}`))
	}))
	defer srv.Close()

	c := sendgrid.NewClient("SG.k", sendgrid.Address{Email: "n@he-api.com"})
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()

	_, err := c.Send(context.Background(), sendgrid.SendRequest{To: "u@e.com", Subject: "s", TextBody: "t", HTMLBody: "<p>t</p>"})
	if !errors.Is(err, sendgrid.ErrTransient) {
		t.Fatalf("err = %v, want ErrTransient on 503", err)
	}
}

// Scenario: P2e / 3 — 4xx → ErrPermanent.
func TestClient_Send_4xxReturnsErrPermanent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bad to address"}]}`))
	}))
	defer srv.Close()

	c := sendgrid.NewClient("SG.k", sendgrid.Address{Email: "n@he-api.com"})
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()

	_, err := c.Send(context.Background(), sendgrid.SendRequest{To: "u@e.com", Subject: "s", TextBody: "t", HTMLBody: "t"})
	if !errors.Is(err, sendgrid.ErrPermanent) {
		t.Fatalf("err = %v, want ErrPermanent on 400", err)
	}
}

// Scenario: P2e / 3 — 401 (bad API key) → ErrPermanent. This is the most
// common 4xx in practice (rotated keys, mis-mounted secret).
func TestClient_Send_401ReturnsErrPermanent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := sendgrid.NewClient("SG.bad", sendgrid.Address{Email: "n@he-api.com"})
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()

	_, err := c.Send(context.Background(), sendgrid.SendRequest{To: "u@e.com", Subject: "s", TextBody: "t", HTMLBody: "t"})
	if !errors.Is(err, sendgrid.ErrPermanent) {
		t.Fatalf("err = %v, want ErrPermanent on 401", err)
	}
}

// Scenario: P2e / 3 — timeout / network failure → ErrTransient.
func TestClient_Send_TimeoutReturnsErrTransient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusAccepted)
		case <-r.Context().Done():
			return
		}
	}))
	defer srv.Close()

	c := sendgrid.NewClient("SG.k", sendgrid.Address{Email: "n@he-api.com"})
	c.BaseURL = srv.URL
	c.HTTPClient = &http.Client{Timeout: 100 * time.Millisecond}

	_, err := c.Send(context.Background(), sendgrid.SendRequest{To: "u@e.com", Subject: "s", TextBody: "t", HTMLBody: "t"})
	if !errors.Is(err, sendgrid.ErrTransient) {
		t.Fatalf("err = %v, want ErrTransient on timeout", err)
	}
}

// Scenario: P2e / 3 — default constructor wires sensible defaults.
func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()
	c := sendgrid.NewClient("SG.k", sendgrid.Address{Email: "n@he-api.com"})
	if c.HTTPClient == nil || c.HTTPClient.Timeout != 10*time.Second {
		t.Errorf("default HTTPClient.Timeout = %v, want 10s", c.HTTPClient.Timeout)
	}
	if c.BaseURL == "" {
		t.Errorf("BaseURL is empty")
	}
}

// Scenario: P2e / 3 — empty X-Message-Id header in response is treated as
// "" (no error). Some staging endpoints don't emit it.
func TestClient_Send_EmptyMessageIDIsOK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := sendgrid.NewClient("SG.k", sendgrid.Address{Email: "n@he-api.com"})
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()

	id, err := c.Send(context.Background(), sendgrid.SendRequest{To: "u@e.com", Subject: "s", TextBody: "t", HTMLBody: "t"})
	if err != nil {
		t.Fatalf("Send (no message id): %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty string when header absent", id)
	}
}
