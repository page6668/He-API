// Story 2.5 AC1 — GetMe handler.
//
// Reads a single users row and returns a profile snapshot for the
// Settings → Profile page. api-gateway populates user_id from the verified
// JWT `sub` claim — clients NEVER supply it directly (BR-1.1 IDOR defence).
//
// Error mapping:
//   - ErrUserNotFound → CodeInternal (impossible in normal flow; the JWT
//     guarantees an existing user_id; if we reach this branch the JWT
//     pubkey / signing key has drifted or the row was deleted out-of-band).
//   - ErrAccountPendingDeletion → CodeFailedPrecondition with reason
//     `account_pending_deletion` (api-gateway translates to 403 — BR-1.9).
//   - any other DB error → CodeUnavailable (api-gateway → 503).
//
// etag format (Architect Q2 ruling 2026-05-16): `"{UpdatedAt.UnixMicro()}"`
// — quoted-string per RFC 7232 §2.3, microsecond precision matches PG's
// native TIMESTAMPTZ resolution exactly.
package handlers

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// GetMe implements Story 2.5 AC1.
func (s *AuthServer) GetMe(
	ctx context.Context,
	req *connect.Request[authv1.GetMeRequest],
) (*connect.Response[authv1.GetMeResponse], error) {
	userID, err := uuid.Parse(req.Msg.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	user, err := repository.GetProfileByID(ctx, s.DB, userID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrAccountPendingDeletion):
			return nil, statusError(connect.CodeFailedPrecondition, "403_account_pending_deletion")
		case errors.Is(err, repository.ErrUserNotFound):
			// Defensive: should be impossible in normal flow because
			// api-gateway extracts user_id from a verified JWT — if we hit
			// this branch the row was deleted out-of-band. Map to Internal
			// (api-gateway → 500) per Story 2.5 AC1 Error Handling table.
			return nil, internalErr(err, "pg_get_profile_not_found")
		default:
			return nil, statusError(connect.CodeUnavailable, "503_database_unavailable")
		}
	}

	return connect.NewResponse(profileSnapshot(user, false)), nil
}

// profileSnapshot maps repository.User → GetMeResponse. Reused by
// UpdateProfile (T2) which returns the same shape with `locale_changed`
// optionally set. The localeChanged param is ignored for GetMe (kept as
// the canonical envelope for renderer reuse).
func profileSnapshot(u *repository.User, localeChanged bool) *authv1.GetMeResponse {
	resp := &authv1.GetMeResponse{
		UserId:      u.ID.String(),
		Email:       u.Email,
		Locale:      u.Locale,
		Timezone:    u.Timezone,
		TotpEnabled: u.TOTPEnabled,
		CreatedAt:   timestamppb.New(u.CreatedAt),
		UpdatedAt:   timestamppb.New(u.UpdatedAt),
		// Architect Q2 — UnixMicro() quoted-string format. PG TIMESTAMPTZ
		// stores microsecond precision so this round-trips losslessly.
		Etag: fmt.Sprintf(`"%d"`, u.UpdatedAt.UnixMicro()),
	}
	if u.DisplayName != nil {
		resp.DisplayName = u.DisplayName
	}
	if u.OAuthProvider != nil {
		resp.OauthProvider = u.OAuthProvider
	}
	// Story 6.5 — surface the persisted default routing strategy (nil → absent →
	// no default → STRATEGY_DEFAULT passthrough).
	if u.DefaultRoutingStrategy != nil {
		resp.DefaultRoutingStrategy = u.DefaultRoutingStrategy
	}
	_ = localeChanged // UpdateProfile encodes via UpdateProfileResponse.LocaleChanged separately
	return resp
}
