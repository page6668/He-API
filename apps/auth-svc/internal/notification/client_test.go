package notification_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"

	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
)

// fakeUpstream is an in-process notificationv1connect.NotificationServiceHandler
// that records the last request and returns a programmable response.
type fakeUpstream struct {
	lastReq *notificationv1.SendEmailRequest
	respID  string
	connErr error
}

func (f *fakeUpstream) SendEmail(
	_ context.Context,
	r *connect.Request[notificationv1.SendEmailRequest],
) (*connect.Response[notificationv1.SendEmailResponse], error) {
	f.lastReq = r.Msg
	if f.connErr != nil {
		return nil, f.connErr
	}
	return connect.NewResponse(&notificationv1.SendEmailResponse{ProviderMessageId: f.respID}), nil
}

// Story 2.6 — the NotificationServiceHandler interface now requires
// RequestDataExport + GetCurrentExport (proto extension). auth-svc tests
// don't exercise those RPCs, so the fakes return Unimplemented.
func (f *fakeUpstream) RequestDataExport(
	_ context.Context,
	_ *connect.Request[notificationv1.RequestDataExportRequest],
) (*connect.Response[notificationv1.RequestDataExportResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("fakeUpstream: RequestDataExport not stubbed"))
}

func (f *fakeUpstream) GetCurrentExport(
	_ context.Context,
	_ *connect.Request[notificationv1.GetCurrentExportRequest],
) (*connect.Response[notificationv1.GetCurrentExportResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("fakeUpstream: GetCurrentExport not stubbed"))
}

// Story 5.4 — the NotificationServiceHandler interface now also requires
// NotifyMonthlyCapThreshold. auth-svc never calls it (the gateway does), so
// the stub is Unimplemented.
func (f *fakeUpstream) NotifyMonthlyCapThreshold(
	_ context.Context,
	_ *connect.Request[notificationv1.NotifyMonthlyCapThresholdRequest],
) (*connect.Response[notificationv1.NotifyMonthlyCapThresholdResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("fakeUpstream: NotifyMonthlyCapThreshold not stubbed"))
}

func newClientServer(t *testing.T, up *fakeUpstream) *notification.Client {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := notificationv1connect.NewNotificationServiceHandler(up)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return notification.NewClient(srv.Client(), srv.URL)
}

// Scenario: P2e / 5 — happy path. SendVerificationEmail issues SendEmail to
// notification-svc with the canonical template enum + snake_case variable
// map, then returns nil on 2xx.
func TestSendVerificationEmail_BuildsCanonicalRequest(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{respID: "msg-1"}
	c := newClientServer(t, up)

	err := c.SendVerificationEmail(context.Background(), "user@example.com", "en", "TOK", "https://console.he-api.com/en/verify-email?token=TOK")
	if err != nil {
		t.Fatalf("SendVerificationEmail: %v", err)
	}
	if up.lastReq == nil {
		t.Fatalf("upstream did not receive request")
	}
	if up.lastReq.GetTemplate() != notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION {
		t.Errorf("template = %v, want EMAIL_TEMPLATE_EMAIL_VERIFICATION", up.lastReq.GetTemplate())
	}
	if up.lastReq.GetToEmail() != "user@example.com" {
		t.Errorf("to_email = %q, want user@example.com", up.lastReq.GetToEmail())
	}
	if up.lastReq.GetLocale() != "en" {
		t.Errorf("locale = %q, want en", up.lastReq.GetLocale())
	}
	if up.lastReq.GetVariables()["token"] != "TOK" {
		t.Errorf("variables[token] = %q, want TOK", up.lastReq.GetVariables()["token"])
	}
	if up.lastReq.GetVariables()["verification_link"] != "https://console.he-api.com/en/verify-email?token=TOK" {
		t.Errorf("variables[verification_link] not propagated")
	}
}

// Scenario: P2e / 5 — Connect Unavailable (notification-svc unreachable
// or SendGrid 5xx upstream) → ErrTransient.
func TestSendVerificationEmail_UnavailableMapsToErrTransient(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{connErr: connect.NewError(connect.CodeUnavailable, errors.New("notification-svc down"))}
	c := newClientServer(t, up)
	err := c.SendVerificationEmail(context.Background(), "user@example.com", "en", "T", "https://x/v")
	if !errors.Is(err, notification.ErrTransient) {
		t.Fatalf("err = %v, want ErrTransient", err)
	}
}

// Scenario: P2e / 5 — Connect Internal (SendGrid permanent / programming
// error) → ErrPermanent.
func TestSendVerificationEmail_InternalMapsToErrPermanent(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{connErr: connect.NewError(connect.CodeInternal, errors.New("internal"))}
	c := newClientServer(t, up)
	err := c.SendVerificationEmail(context.Background(), "user@example.com", "en", "T", "https://x/v")
	if !errors.Is(err, notification.ErrPermanent) {
		t.Fatalf("err = %v, want ErrPermanent", err)
	}
}

// Scenario: P2e / 5 — Connect InvalidArgument → ErrPermanent (caller bug).
func TestSendVerificationEmail_InvalidArgumentMapsToErrPermanent(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{connErr: connect.NewError(connect.CodeInvalidArgument, errors.New("bad"))}
	c := newClientServer(t, up)
	err := c.SendVerificationEmail(context.Background(), "user@example.com", "en", "T", "https://x/v")
	if !errors.Is(err, notification.ErrPermanent) {
		t.Fatalf("err = %v, want ErrPermanent", err)
	}
}

// Compile-time check that Client satisfies Sender — already in client.go,
// duplicated here to surface as a test if the interface ever drifts.
func TestClientSatisfiesSender(t *testing.T) {
	t.Parallel()
	var _ notification.Sender = (*notification.Client)(nil)
}
