// Package authsvcclient wraps the auth-svc AuthService gRPC client for the
// single call notification-svc needs: GetCapNotificationContext (Story 5.4
// Q-L Fix-A). notification-svc cannot import auth-svc's internal/repository
// across the go.work module boundary, so the PII-sensitive api_keys JOIN is
// reached over gRPC instead.
package authsvcclient

import (
	"context"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
)

// DefaultDeadline bounds the auth-svc round-trip. The call sits off the gateway
// hot path (the gateway already detached-context-fired), but a bounded deadline
// keeps the notification-svc handler from hanging on an auth-svc stall.
const DefaultDeadline = 500 * time.Millisecond

// CapContext is the projection notification-svc needs to render a cap email.
type CapContext struct {
	UserEmail            string
	UserLocale           string
	UserDisplayName      string
	KeyName              string
	KeyMonthlyCostCapUSD string // string-decimal "50.00" (BR-2.7)
}

// upstream is the slice of AuthServiceClient this package uses (narrow for tests).
type upstream interface {
	GetCapNotificationContext(
		context.Context,
		*connect.Request[authv1.GetCapNotificationContextRequest],
	) (*connect.Response[authv1.GetCapNotificationContextResponse], error)
}

// Client calls auth-svc GetCapNotificationContext.
type Client struct {
	up       upstream
	deadline time.Duration
}

// New builds a Client against the auth-svc base URL.
func New(httpClient connect.HTTPClient, baseURL string) *Client {
	return NewWithClient(authv1connect.NewAuthServiceClient(httpClient, baseURL))
}

// NewWithClient builds a Client around an arbitrary upstream (tests).
func NewWithClient(up upstream) *Client {
	return &Client{up: up, deadline: DefaultDeadline}
}

// GetCapNotificationContext fetches the user + key context for an api_key id.
// The connect error code is preserved (NotFound / Unavailable) so the caller
// can map it precisely.
func (c *Client) GetCapNotificationContext(ctx context.Context, apiKeyID string) (CapContext, error) {
	cctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	resp, err := c.up.GetCapNotificationContext(cctx,
		connect.NewRequest(&authv1.GetCapNotificationContextRequest{ApiKeyId: apiKeyID}))
	if err != nil {
		return CapContext{}, err
	}
	m := resp.Msg
	return CapContext{
		UserEmail:            m.GetUserEmail(),
		UserLocale:           m.GetUserLocale(),
		UserDisplayName:      m.GetUserDisplayName(),
		KeyName:              m.GetKeyName(),
		KeyMonthlyCostCapUSD: m.GetKeyMonthlyCostCapUsd(),
	}, nil
}
