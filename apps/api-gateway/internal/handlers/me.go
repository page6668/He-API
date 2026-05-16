// Story 2.5 AC1 — GET /v1/me REST handler.
//
// The route is wrapped by jwtVerifier.RequireJWT in cmd/server/main.go
// (Architect m-5 — stdlib net/http pattern). The middleware places the
// verified user_id in r.Context via WithUserID — read here via
// middleware.UserIDFromContext. BR-1.1 IDOR defence: the gRPC GetMeRequest
// carries user_id but we ALWAYS take it from the JWT-derived context, never
// from the request body / query string.
//
// Response shape (snake_case per coding-standards §12.3):
//
//   {
//     "user_id": "uuid",
//     "email": "user@example.com",
//     "display_name": "Alice" | null,
//     "locale": "en",
//     "timezone": "UTC",
//     "totp_enabled": false,
//     "oauth_provider": "google" | null,
//     "created_at": "RFC3339",
//     "updated_at": "RFC3339"
//   }
//
// Headers:
//   - ETag: "{UnixMicro}" (Architect Q2)
//   - Cache-Control: no-store (BR-1.7 — PII, never cached by proxies)
package handlers

import (
	"net/http"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// meResponseBody is the JSON envelope. Field names match coding-standards
// §12.3 snake_case. Use pointers for nullable fields so the JSON encoder
// emits `null` (not the zero value) when the backend returns absent.
type meResponseBody struct {
	UserID        string  `json:"user_id"`
	Email         string  `json:"email"`
	DisplayName   *string `json:"display_name"`
	Locale        string  `json:"locale"`
	Timezone      string  `json:"timezone"`
	TOTPEnabled   bool    `json:"totp_enabled"`
	OAuthProvider *string `json:"oauth_provider"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// GetMe implements GET /v1/me.
func (p *AuthProxy) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		// Should be impossible — RequireJWT wraps this handler. Defensive 401.
		writeError(w, http.StatusUnauthorized, "401_unauthorized", "missing access token")
		return
	}

	resp, err := p.Upstream.GetMe(r.Context(), connect.NewRequest(&authv1.GetMeRequest{
		UserId: userID,
	}))
	if err != nil {
		translateConnectError(w, err)
		return
	}
	msg := resp.Msg

	// BR-1.7 — PII response, never cached by intermediate proxies.
	w.Header().Set("Cache-Control", "no-store")
	// Architect Q2 — etag forwarded verbatim from auth-svc (already quoted).
	w.Header().Set("ETag", msg.GetEtag())

	body := meResponseBody{
		UserID:        msg.GetUserId(),
		Email:         msg.GetEmail(),
		DisplayName:   msg.DisplayName,
		Locale:        msg.GetLocale(),
		Timezone:      msg.GetTimezone(),
		TOTPEnabled:   msg.GetTotpEnabled(),
		OAuthProvider: msg.OauthProvider,
		CreatedAt:     msg.GetCreatedAt().AsTime().UTC().Format(time.RFC3339),
		UpdatedAt:     msg.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, body)
}
