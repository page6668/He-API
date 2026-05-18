// Package handlers implements the NotificationService gRPC interface.
//
// SendEmail validates the request → renders the template via the package
// renderer → posts to SendGrid → returns the provider message id. Failures
// are categorized so auth-svc can decide retry / surface (BR-1.9 AC1 row 7).
package handlers

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/sendgrid"
	"github.com/he-api/he-api/apps/notification-svc/internal/templates"
)

// EmailSender is the SendGrid surface the handler depends on. The interface
// keeps the handler unit-testable without a real SendGrid endpoint.
type EmailSender interface {
	Send(ctx context.Context, req sendgrid.SendRequest) (string, error)
}

// TemplateRenderer is the renderer surface the handler depends on. Same
// rationale as EmailSender — injectable so tests can mock failures cleanly.
type TemplateRenderer interface {
	Render(name, locale string, vars map[string]string) (templates.Rendered, error)
}

// NotificationServer satisfies notificationv1connect.NotificationServiceHandler.
//
// Story 2.6 — embeds *DataExportServer so the RequestDataExport +
// GetCurrentExport RPCs from the extended proto contract are served from
// the same connect-go handler binding. The embedding gives us Go's method
// promotion: NotificationServer automatically satisfies the two new RPCs
// without explicit delegation. main.go uses
// NewNotificationServerWithDataExport in production; legacy unit tests
// continue to use NewNotificationServer (the embed is nil for those, and
// the test harness never invokes the new RPCs).
type NotificationServer struct {
	Renderer TemplateRenderer
	Sender   EmailSender
	*DataExportServer
}

// NewNotificationServer wires the canonical Renderer + provided EmailSender.
// Use sendgrid.NewClient(...) in production; tests pass a fake. The
// DataExportServer embed is left nil — Story 2.2 callers (auth-svc) only
// hit SendEmail.
func NewNotificationServer(sender EmailSender) *NotificationServer {
	return &NotificationServer{
		Renderer: templates.NewRenderer(),
		Sender:   sender,
	}
}

// NewNotificationServerWithDataExport wires SendEmail and the Story 2.6
// RequestDataExport / GetCurrentExport surface in a single binding.
func NewNotificationServerWithDataExport(sender EmailSender, dataExport *DataExportServer) *NotificationServer {
	return &NotificationServer{
		Renderer:         templates.NewRenderer(),
		Sender:           sender,
		DataExportServer: dataExport,
	}
}

// SendEmail validates inputs, renders the template, dispatches via SendGrid.
//
// Error mapping (preserves the codes auth-svc / api-gateway expect):
//
//   - empty/unspecified template enum     → InvalidArgument
//   - empty to_email / variables map nil  → InvalidArgument
//   - missing required vars per template  → InvalidArgument
//   - template not found in embed FS      → NotFound (programming error;
//                                            should be caught in CI tests)
//   - SendGrid transient (5xx/timeout)    → Unavailable (auth-svc translates
//                                            to 500_email_send_failed)
//   - SendGrid permanent (4xx)            → Internal (programming/cred error;
//                                            failing loudly is the right call)
func (s *NotificationServer) SendEmail(
	ctx context.Context,
	req *connect.Request[notificationv1.SendEmailRequest],
) (*connect.Response[notificationv1.SendEmailResponse], error) {
	in := req.Msg
	tpl, err := templateNameForEnum(in.GetTemplate())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if in.GetToEmail() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("send_email: to_email is required"))
	}
	if err := validateRequiredVars(tpl, in.GetVariables()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	rendered, err := s.Renderer.Render(tpl, in.GetLocale(), in.GetVariables())
	if err != nil {
		switch {
		case errors.Is(err, templates.ErrTemplateNotFound):
			return nil, connect.NewError(connect.CodeNotFound, err)
		case errors.Is(err, templates.ErrMissingVariable):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send_email: render: %w", err))
		}
	}

	id, err := s.Sender.Send(ctx, sendgrid.SendRequest{
		To:       in.GetToEmail(),
		Subject:  rendered.Subject,
		TextBody: rendered.TextBody,
		HTMLBody: rendered.HTMLBody,
	})
	if err != nil {
		switch {
		case errors.Is(err, sendgrid.ErrTransient):
			return nil, connect.NewError(connect.CodeUnavailable, err)
		case errors.Is(err, sendgrid.ErrPermanent):
			return nil, connect.NewError(connect.CodeInternal, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&notificationv1.SendEmailResponse{
		ProviderMessageId: id,
	}), nil
}

// templateNameForEnum bridges the proto enum to the templates-package name.
// Listed exhaustively so a new enum value flagged-but-not-implemented fails
// fast at the handler edge.
func templateNameForEnum(e notificationv1.EmailTemplate) (string, error) {
	switch e {
	case notificationv1.EmailTemplate_EMAIL_TEMPLATE_EMAIL_VERIFICATION:
		return templates.TemplateEmailVerification, nil
	case notificationv1.EmailTemplate_EMAIL_TEMPLATE_GDPR_EXPORT_READY:
		// Story 2.6 — Architect R-2: caller passes the enum value, this
		// handler maps to the file-naming slug.
		return templates.TemplateGDPRExportReady, nil
	case notificationv1.EmailTemplate_EMAIL_TEMPLATE_UNSPECIFIED:
		return "", errors.New("send_email: template is required (UNSPECIFIED)")
	default:
		return "", fmt.Errorf("send_email: unknown template enum value %d", e)
	}
}

// requiredVars[template] = list of map keys the template needs. Used to fail
// before the renderer (cleaner error messages than text/template's "no entry
// for key").
var requiredVars = map[string][]string{
	templates.TemplateEmailVerification: {"token", "verification_link"},
	// Story 2.6 AC5 BR-5.1 — the 4 vars the gdpr_export_ready templates
	// reference via {{.variable}} syntax. display_name MAY be empty when
	// the user hasn't set one — the email-trigger upstream substitutes a
	// "there" fallback (per Story 2.5 BR-2.4) so this map still treats it
	// as required at the wire-shape level.
	templates.TemplateGDPRExportReady: {"display_name", "signed_url", "expires_at", "requested_at"},
}

func validateRequiredVars(tpl string, vars map[string]string) error {
	for _, key := range requiredVars[tpl] {
		if vars[key] == "" {
			return fmt.Errorf("send_email: missing required variable %q for template %q", key, tpl)
		}
	}
	return nil
}
