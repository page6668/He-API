// Package notification wraps the outbound Connect-go client to
// notification-svc (Wright Round 1 Q4 ruling option c: synchronous
// SendEmail; async Kafka migration is a documented Epic-9 follow-up).
//
// The package exposes:
//
//   - Sender — the narrow interface RegisterUser depends on. Lets the
//     handler unit-test swap in a fake without standing up a real
//     notification-svc.
//   - Client — concrete Connect-go-backed implementation; wraps the
//     generated notificationv1connect.NotificationServiceClient with a
//     SendVerificationEmail convenience that fills in the canonical
//     template enum + variable map shape.
package notification

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"
)

// ErrTransient indicates a retryable failure upstream (Connect Unavailable —
// notification-svc unreachable, SendGrid 5xx, timeout). RegisterUser
// surfaces this as `500_email_send_failed` per AC1 Error Handling row 7.
var ErrTransient = errors.New("notification: transient upstream failure")

// ErrPermanent indicates a non-retryable failure (Connect Internal +
// InvalidArgument). Programming or credentialing error — fail loud so CI
// catches it.
var ErrPermanent = errors.New("notification: permanent upstream failure")

// Sender is the surface RegisterUser depends on. Kept minimal so the
// handler test can stub out the dependency without pulling in Connect.
type Sender interface {
	SendVerificationEmail(ctx context.Context, to, locale, token, verificationLink string) error
	// SendSecurityAlert dispatches one of the Story 2.4 out-of-band 2FA
	// templates (BR-5.9). `template` is one of:
	//   - EMAIL_TEMPLATE_2FA_ENABLED              (AC1)
	//   - EMAIL_TEMPLATE_2FA_RECOVERY_USED        (AC3)
	//   - EMAIL_TEMPLATE_2FA_RECOVERY_REGENERATED (AC3)
	//   - EMAIL_TEMPLATE_2FA_DISABLED             (AC4)
	// `vars` carries template-specific keys: time, ip_summary, ua_summary,
	// and template-specific extras (remaining_count, disable_method).
	SendSecurityAlert(ctx context.Context, template SecurityAlertTemplate, to, locale string, vars map[string]string) error
}

// SecurityAlertTemplate enumerates the Story 2.4 2FA template ids. Defined
// as a typed wrapper so handler call sites cannot accidentally swap in an
// arbitrary EmailTemplate constant; the package's Send method maps to the
// protobuf enum internally.
type SecurityAlertTemplate int

const (
	Alert2FAEnabled SecurityAlertTemplate = iota + 1
	Alert2FARecoveryUsed
	Alert2FARecoveryRegenerated
	Alert2FADisabled
)

// Client is the production Sender — Connect-go client wrapper.
type Client struct {
	upstream notificationv1connect.NotificationServiceClient
}

// NewClient constructs a Connect-go-backed Sender. Pass the resolved
// notification-svc base URL (e.g., "http://notification-svc.he-api-staging:8080")
// and an HTTP client (use http.DefaultClient or a custom-tuned one).
func NewClient(httpClient connect.HTTPClient, baseURL string) *Client {
	return &Client{
		upstream: notificationv1connect.NewNotificationServiceClient(httpClient, baseURL),
	}
}

// SendVerificationEmail invokes notification-svc SendEmail with the
// email_verification template + canonical {token, verification_link} vars.
// Connect-go error codes are mapped to ErrTransient / ErrPermanent so the
// handler decides retry / surface.
func (c *Client) SendVerificationEmail(ctx context.Context, to, locale, token, verificationLink string) error {
	req := connect.NewRequest(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  to,
		Locale:   locale,
		Variables: map[string]string{
			"token":             token,
			"verification_link": verificationLink,
		},
	})
	if _, err := c.upstream.SendEmail(ctx, req); err != nil {
		return mapConnectErr(err)
	}
	return nil
}

// SendSecurityAlert maps the SecurityAlertTemplate wrapper onto the protobuf
// EmailTemplate enum and invokes notification-svc.
func (c *Client) SendSecurityAlert(ctx context.Context, template SecurityAlertTemplate, to, locale string, vars map[string]string) error {
	var tmpl notificationv1.EmailTemplate
	switch template {
	case Alert2FAEnabled:
		tmpl = notificationv1.EmailTemplate_EMAIL_TEMPLATE_2FA_ENABLED
	case Alert2FARecoveryUsed:
		tmpl = notificationv1.EmailTemplate_EMAIL_TEMPLATE_2FA_RECOVERY_USED
	case Alert2FARecoveryRegenerated:
		tmpl = notificationv1.EmailTemplate_EMAIL_TEMPLATE_2FA_RECOVERY_REGENERATED
	case Alert2FADisabled:
		tmpl = notificationv1.EmailTemplate_EMAIL_TEMPLATE_2FA_DISABLED
	default:
		return fmt.Errorf("%w: unknown SecurityAlertTemplate %d", ErrPermanent, template)
	}
	req := connect.NewRequest(&notificationv1.SendEmailRequest{
		Template:  tmpl,
		ToEmail:   to,
		Locale:    locale,
		Variables: vars,
	})
	if _, err := c.upstream.SendEmail(ctx, req); err != nil {
		return mapConnectErr(err)
	}
	return nil
}

// mapConnectErr translates Connect-go error codes into the package-level
// sentinels RegisterUser switches on.
func mapConnectErr(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return fmt.Errorf("%w: %v", ErrTransient, err)
	case connect.CodeInternal, connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return fmt.Errorf("%w: %v", ErrPermanent, err)
	default:
		// Unknown codes default to transient — safer to retry than to drop
		// a verification email silently.
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
}

// Ensure Client satisfies Sender at compile time.
var _ Sender = (*Client)(nil)

// Ensure connect.HTTPClient is satisfied by *http.Client (compile-time
// guard against an upstream Connect-go breaking change).
var _ connect.HTTPClient = (*http.Client)(nil)
