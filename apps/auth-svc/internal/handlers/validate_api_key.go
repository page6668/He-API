// Story 3.2 AC2 — ValidateApiKey RPC handler.
//
// Thin pass-through to apikey.Service.Validate; the heavy lifting (regex
// gate, bcrypt iteration, fire-and-forget last_used_at, span attributes)
// lives in apps/auth-svc/internal/apikey. This handler exists only to
// satisfy authv1connect.AuthServiceHandler's method set and to keep the
// concrete apikey package off the public AuthServer field surface for
// unit-test ergonomics (test fakes can substitute APIKeyService directly
// without importing the apikey package).
package handlers

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

// errAPIKeyValidatorUnwired is surfaced when ValidateApiKey is called but
// AuthServer.APIKey is nil — a caller / wiring bug, not a request-time
// condition. The gateway translates connect.CodeInternal to a 500 envelope.
var errAPIKeyValidatorUnwired = errors.New("api_key_validator_unwired")

// APIKeyValidator is the broadened surface AuthServer.APIKey implements.
// Story-3.2 contributed Validate; Story-5.1 added CreateApiKey/ListApiKeys/
// RevokeApiKey. Production wires *apikey.Service which implements all 4;
// tests that exercise only Validate can substitute the same Story-3.2
// validate-only fake — the new methods will simply be unused (the gRPC
// handler short-circuits with CodeInternal when APIKey is nil, but a fake
// passed via AuthServer.APIKey must satisfy the broader contract).
type APIKeyValidator interface {
	Validate(ctx context.Context, req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)
	CreateApiKey(ctx context.Context, req *authv1.CreateApiKeyRequest) (*authv1.CreateApiKeyResponse, error)
	ListApiKeys(ctx context.Context, req *authv1.ListApiKeysRequest) (*authv1.ListApiKeysResponse, error)
	RevokeApiKey(ctx context.Context, req *authv1.RevokeApiKeyRequest) (*authv1.RevokeApiKeyResponse, error)
	UpdateApiKey(ctx context.Context, req *authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error)
}

// ValidateApiKey implements Story 3.2 AC2. Returns connect.CodeInternal
// when APIKey is unwired (caller config issue) so the gateway translates
// to a 503 rather than silently 401-ing every request.
func (s *AuthServer) ValidateApiKey(
	ctx context.Context,
	req *connect.Request[authv1.ValidateApiKeyRequest],
) (*connect.Response[authv1.ValidateApiKeyResponse], error) {
	if s.APIKey == nil {
		return nil, connect.NewError(connect.CodeInternal, errAPIKeyValidatorUnwired)
	}
	resp, err := s.APIKey.Validate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// CreateApiKey implements Story 5.1 AC1.
func (s *AuthServer) CreateApiKey(
	ctx context.Context,
	req *connect.Request[authv1.CreateApiKeyRequest],
) (*connect.Response[authv1.CreateApiKeyResponse], error) {
	if s.APIKey == nil {
		return nil, connect.NewError(connect.CodeInternal, errAPIKeyValidatorUnwired)
	}
	resp, err := s.APIKey.CreateApiKey(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ListApiKeys implements Story 5.1 AC2.
func (s *AuthServer) ListApiKeys(
	ctx context.Context,
	req *connect.Request[authv1.ListApiKeysRequest],
) (*connect.Response[authv1.ListApiKeysResponse], error) {
	if s.APIKey == nil {
		return nil, connect.NewError(connect.CodeInternal, errAPIKeyValidatorUnwired)
	}
	resp, err := s.APIKey.ListApiKeys(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// RevokeApiKey implements Story 5.1 AC3.
func (s *AuthServer) RevokeApiKey(
	ctx context.Context,
	req *connect.Request[authv1.RevokeApiKeyRequest],
) (*connect.Response[authv1.RevokeApiKeyResponse], error) {
	if s.APIKey == nil {
		return nil, connect.NewError(connect.CodeInternal, errAPIKeyValidatorUnwired)
	}
	resp, err := s.APIKey.RevokeApiKey(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// UpdateApiKey implements Story 5.2 AC1.
func (s *AuthServer) UpdateApiKey(
	ctx context.Context,
	req *connect.Request[authv1.UpdateApiKeyRequest],
) (*connect.Response[authv1.UpdateApiKeyResponse], error) {
	if s.APIKey == nil {
		return nil, connect.NewError(connect.CodeInternal, errAPIKeyValidatorUnwired)
	}
	resp, err := s.APIKey.UpdateApiKey(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
