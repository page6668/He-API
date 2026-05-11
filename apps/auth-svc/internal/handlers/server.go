// Package handlers implements the AuthService gRPC interface.
//
// P1 scaffold: every RPC returns CodeUnimplemented. Real logic lands in
// P2-P5 (Story 2.2 tasks T1-T4):
//
//   - RegisterUser       → T1 (P2, AC1)
//   - VerifyEmail        → T2 (P3, AC2)
//   - ResendVerification → T2 (P3, AC2)
//   - LoginUser          → T3 (P4, AC3)
//   - RefreshToken       → T3 (P4, AC3)
package handlers

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

// AuthServer satisfies authv1connect.AuthServiceHandler. P1 placeholder; real
// dependency wiring (repository, jwt, password, token, ratelimit, notification,
// audit) lands in P2-P5.
type AuthServer struct{}

func NewAuthServer() *AuthServer { return &AuthServer{} }

func (s *AuthServer) RegisterUser(
	_ context.Context,
	_ *connect.Request[authv1.RegisterUserRequest],
) (*connect.Response[authv1.RegisterUserResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("RegisterUser: pending P2 (T1, AC1)"))
}

func (s *AuthServer) VerifyEmail(
	_ context.Context,
	_ *connect.Request[authv1.VerifyEmailRequest],
) (*connect.Response[authv1.VerifyEmailResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("VerifyEmail: pending P3 (T2, AC2)"))
}

func (s *AuthServer) ResendVerification(
	_ context.Context,
	_ *connect.Request[authv1.ResendVerificationRequest],
) (*connect.Response[authv1.ResendVerificationResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ResendVerification: pending P3 (T2, AC2)"))
}

func (s *AuthServer) LoginUser(
	_ context.Context,
	_ *connect.Request[authv1.LoginUserRequest],
) (*connect.Response[authv1.LoginUserResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("LoginUser: pending P4 (T3, AC3)"))
}

func (s *AuthServer) RefreshToken(
	_ context.Context,
	_ *connect.Request[authv1.RefreshTokenRequest],
) (*connect.Response[authv1.RefreshTokenResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("RefreshToken: pending P4 (T3, AC3)"))
}
