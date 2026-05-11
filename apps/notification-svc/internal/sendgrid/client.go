// Package sendgrid wraps the SendGrid v3 Mail Send API
// (https://docs.sendgrid.com/api-reference/mail-send/mail-send).
//
// We avoid the official sendgrid-go SDK on purpose: the API surface we need
// is a single POST, and the SDK pulls in transitive deps + opinionated
// abstractions (helpers, builders) that complicate testing and balloon the
// distroless image.
//
// Credentials: SENDGRID_API_KEY from K8s Secret he-api-notification-creds
// (TS-CONS-010, Story 2.2 T0.6). Staging consumes a sub-account distinct
// from prod.
package sendgrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL = "https://api.sendgrid.com"
	defaultTimeout = 10 * time.Second
)

// ErrTransient is returned for failures the caller should retry: HTTP 5xx,
// network timeouts, DNS failures. The auth-svc handler translates this into
// `500_email_send_failed` per AC1 Error Handling row 7.
var ErrTransient = errors.New("sendgrid: transient failure")

// ErrPermanent is returned for non-retryable failures (4xx). These usually
// indicate a programming bug (malformed payload, bad credentials) — surfaced
// up so they fail loudly in CI / staging rather than silently swallowed.
var ErrPermanent = errors.New("sendgrid: permanent failure")

// Address is the SendGrid sender/recipient shape.
type Address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// Client posts to SendGrid v3 Mail Send. Tests inject a fake HTTPClient
// pointing at httptest.Server; prod uses the default.
type Client struct {
	APIKey     string
	BaseURL    string  // defaults to https://api.sendgrid.com
	From       Address // canonical sender ("noreply@he-api.com", "He-API")
	HTTPClient *http.Client
}

// NewClient returns a Client with sensible defaults. APIKey is required.
func NewClient(apiKey string, from Address) *Client {
	return &Client{
		APIKey:     apiKey,
		BaseURL:    defaultBaseURL,
		From:       from,
		HTTPClient: &http.Client{Timeout: defaultTimeout},
	}
}

// SendRequest carries the per-email payload assembled by the handler.
type SendRequest struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

// Send posts to /v3/mail/send and returns the SendGrid X-Message-Id (or an
// empty string if SendGrid did not emit one). Errors are categorized via
// errors.Is into ErrTransient / ErrPermanent so the handler can choose its
// own retry policy.
func (c *Client) Send(ctx context.Context, req SendRequest) (string, error) {
	body, err := buildPayload(c.From, req)
	if err != nil {
		return "", fmt.Errorf("sendgrid: build payload: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v3/mail/send", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("sendgrid: new request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// SendGrid returns 202 Accepted with empty body and X-Message-Id
		// header. The header may be absent on staging; treat that as ""
		// rather than an error.
		return resp.Header.Get("X-Message-Id"), nil
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrTransient, resp.StatusCode, truncate(string(respBody), 256))
	default: // 4xx
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrPermanent, resp.StatusCode, truncate(string(respBody), 256))
	}
}

// buildPayload assembles the SendGrid v3 Mail Send JSON body. Both
// text/plain and text/html parts are sent; SendGrid's order convention is
// text first, HTML second.
func buildPayload(from Address, req SendRequest) ([]byte, error) {
	type personalization struct {
		To []Address `json:"to"`
	}
	type content struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	payload := struct {
		Personalizations []personalization `json:"personalizations"`
		From             Address           `json:"from"`
		Subject          string            `json:"subject"`
		Content          []content         `json:"content"`
	}{
		Personalizations: []personalization{{To: []Address{{Email: req.To}}}},
		From:             from,
		Subject:          req.Subject,
		Content: []content{
			{Type: "text/plain", Value: req.TextBody},
			{Type: "text/html", Value: req.HTMLBody},
		},
	}
	return json.Marshal(payload)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
