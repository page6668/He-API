// Package handlers implements the NotificationService gRPC interface.
//
// P1 scaffold: SendEmail returns CodeUnimplemented. Real SendGrid client +
// per-locale template renderer land in P2 (Story 2.2 T1, AC1).
package handlers

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"
)

type NotificationServer struct{}

func NewNotificationServer() *NotificationServer { return &NotificationServer{} }

func (s *NotificationServer) SendEmail(
	_ context.Context,
	_ *connect.Request[notificationv1.SendEmailRequest],
) (*connect.Response[notificationv1.SendEmailResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("SendEmail: pending P2 (T1, AC1)"))
}
