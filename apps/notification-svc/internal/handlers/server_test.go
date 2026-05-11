package handlers_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/handlers"
	"github.com/he-api/he-api/apps/notification-svc/internal/sendgrid"
)

// fakeSender records the last SendRequest + returns a programmable result.
type fakeSender struct {
	last sendgrid.SendRequest
	id   string
	err  error
}

func (f *fakeSender) Send(_ context.Context, req sendgrid.SendRequest) (string, error) {
	f.last = req
	return f.id, f.err
}

func newReq(req *notificationv1.SendEmailRequest) *connect.Request[notificationv1.SendEmailRequest] {
	return connect.NewRequest(req)
}

// Scenario: P2e / 4 — happy path. SendEmail validates → renders → sends;
// returns provider_message_id from the upstream stub.
func TestSendEmail_HappyPath_RoutesThroughRenderAndSend(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{id: "msg-123"}
	srv := handlers.NewNotificationServer(fs)

	resp, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  "user@example.com",
		Locale:   "en",
		Variables: map[string]string{
			"token":             "T-123",
			"verification_link": "https://console.he-api.com/en/verify-email?token=T-123",
		},
	}))
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if resp.Msg.GetProviderMessageId() != "msg-123" {
		t.Errorf("ProviderMessageId = %q, want msg-123", resp.Msg.GetProviderMessageId())
	}
	if fs.last.To != "user@example.com" {
		t.Errorf("sender saw To = %q, want user@example.com", fs.last.To)
	}
	if !strings.Contains(fs.last.TextBody, "https://console.he-api.com/en/verify-email?token=T-123") {
		t.Errorf("sender TextBody missing rendered verification_link:\n%s", fs.last.TextBody)
	}
	if !strings.Contains(fs.last.HTMLBody, "https://console.he-api.com/en/verify-email?token=T-123") {
		t.Errorf("sender HTMLBody missing rendered verification_link")
	}
	if fs.last.Subject != "Verify your He-API email" {
		t.Errorf("sender Subject = %q, want %q", fs.last.Subject, "Verify your He-API email")
	}
}

// Scenario: P2e / 4 — UNSPECIFIED template → InvalidArgument.
func TestSendEmail_UnspecifiedTemplateRejected(t *testing.T) {
	t.Parallel()
	srv := handlers.NewNotificationServer(&fakeSender{})
	_, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_UNSPECIFIED,
		ToEmail:  "user@example.com",
		Variables: map[string]string{
			"token":             "T",
			"verification_link": "https://x/v",
		},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v (code %v), want InvalidArgument", err, connect.CodeOf(err))
	}
}

// Scenario: P2e / 4 — empty to_email → InvalidArgument.
func TestSendEmail_EmptyToEmailRejected(t *testing.T) {
	t.Parallel()
	srv := handlers.NewNotificationServer(&fakeSender{})
	_, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  "",
		Variables: map[string]string{
			"token":             "T",
			"verification_link": "https://x/v",
		},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v (code %v), want InvalidArgument", err, connect.CodeOf(err))
	}
}

// Scenario: P2e / 4 — missing required variable for email_verification →
// InvalidArgument. Caught before reaching the renderer.
func TestSendEmail_MissingRequiredVariableRejected(t *testing.T) {
	t.Parallel()
	srv := handlers.NewNotificationServer(&fakeSender{})
	_, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  "user@example.com",
		Variables: map[string]string{
			"token": "T", // verification_link omitted
		},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v (code %v), want InvalidArgument", err, connect.CodeOf(err))
	}
}

// Scenario: P2e / 4 — SendGrid transient → Unavailable.
func TestSendEmail_SendGridTransientReturnsUnavailable(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{err: sendgrid.ErrTransient}
	srv := handlers.NewNotificationServer(fs)
	_, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  "user@example.com",
		Variables: map[string]string{
			"token":             "T",
			"verification_link": "https://x/v",
		},
	}))
	if err == nil {
		t.Fatalf("err = nil, want non-nil")
	}
	if got, want := connect.CodeOf(err), connect.CodeUnavailable; got != want {
		t.Fatalf("code = %v, want %v", got, want)
	}
	if !errors.Is(err, sendgrid.ErrTransient) {
		t.Errorf("error chain does not contain ErrTransient: %v", err)
	}
}

// Scenario: P2e / 4 — SendGrid permanent → Internal.
func TestSendEmail_SendGridPermanentReturnsInternal(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{err: sendgrid.ErrPermanent}
	srv := handlers.NewNotificationServer(fs)
	_, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  "user@example.com",
		Variables: map[string]string{
			"token":             "T",
			"verification_link": "https://x/v",
		},
	}))
	if err == nil {
		t.Fatalf("err = nil, want non-nil")
	}
	if got, want := connect.CodeOf(err), connect.CodeInternal; got != want {
		t.Fatalf("code = %v, want %v", got, want)
	}
	if !errors.Is(err, sendgrid.ErrPermanent) {
		t.Errorf("error chain does not contain ErrPermanent: %v", err)
	}
}

// Scenario: P2e / 4 — empty locale falls back to en (renderer's job).
// Handler does not need a special case — verify the integration still works.
func TestSendEmail_EmptyLocaleStillRenders(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{id: "m"}
	srv := handlers.NewNotificationServer(fs)
	_, err := srv.SendEmail(context.Background(), newReq(&notificationv1.SendEmailRequest{
		Template: notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION,
		ToEmail:  "user@example.com",
		Locale:   "",
		Variables: map[string]string{
			"token":             "T",
			"verification_link": "https://x/v",
		},
	}))
	if err != nil {
		t.Fatalf("SendEmail(empty locale): %v", err)
	}
	if !strings.Contains(fs.last.TextBody, "https://x/v") {
		t.Fatalf("empty locale did not render: %s", fs.last.TextBody)
	}
}
