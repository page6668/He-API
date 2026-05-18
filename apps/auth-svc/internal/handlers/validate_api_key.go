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

// APIKeyValidator is the narrow surface ValidateApiKey delegates to. The
// production wiring instantiates `*apikey.Service` and assigns it to
// AuthServer.APIKey; tests substitute a deterministic fake.
type APIKeyValidator interface {
	Validate(ctx context.Context, req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)
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
