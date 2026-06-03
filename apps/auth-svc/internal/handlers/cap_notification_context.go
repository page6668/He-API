// Story 5.4 (Architect Round 1 Q-L Fix-A) — GetCapNotificationContext RPC.
//
// notification-svc cannot import apps/auth-svc/internal/repository across the
// go.work module boundary (Go internal/ visibility), so auth-svc — the sole
// canonical reader of the PII-sensitive api_keys table — exposes the JOIN
// result over gRPC. Single PG round-trip; called fire-and-forget off the
// gateway hot path.
package handlers

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// errInvalidAPIKeyID / errAPIKeyNotFound carry stable, PII-free messages — the
// payload is internal-only, so a single NotFound for both missing + malformed
// preserves the Story-5.1 anti-enumeration collapse.
var (
	errInvalidAPIKeyID = errors.New("invalid_api_key_id")
	errCapKeyNotFound  = errors.New("api_key_not_found")
)

// GetCapNotificationContext returns the user email/locale/display_name + key
// name for one api_key id. Validates the UUID v4 shape, then a single JOIN.
func (s *AuthServer) GetCapNotificationContext(
	ctx context.Context,
	req *connect.Request[authv1.GetCapNotificationContextRequest],
) (*connect.Response[authv1.GetCapNotificationContextResponse], error) {
	apiKeyID, err := uuid.Parse(strings.TrimSpace(req.Msg.GetApiKeyId()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidAPIKeyID)
	}

	cc, err := repository.LookupCapNotificationContext(ctx, s.DB, apiKeyID)
	if err != nil {
		if errors.Is(err, repository.ErrAPIKeyNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errCapKeyNotFound)
		}
		return nil, internalErr(err, "lookup_cap_notification_context")
	}

	return connect.NewResponse(&authv1.GetCapNotificationContextResponse{
		UserEmail:            cc.UserEmail,
		UserLocale:           cc.UserLocale,
		UserDisplayName:      cc.UserDisplayName,
		KeyName:              cc.KeyName,
		KeyMonthlyCostCapUsd: cc.KeyMonthlyCostCapUSD,
	}), nil
}
