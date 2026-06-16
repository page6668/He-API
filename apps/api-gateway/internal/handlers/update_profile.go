// Story 2.5 AC2 — PUT /v1/me/profile REST handler.
//
// Wire contract:
//
//	PUT /v1/me/profile
//	Cookie:    he_access=<JWT>
//	If-Match:  "<UnixMicro etag>"
//	Body:      { "display_name"?: "Alice"|"", "locale"?: "en", "timezone"?: "UTC" }
//
// Strict-field validation: any key outside {display_name, locale, timezone}
// → 400_unknown_field (BR-2.2 — parity with Story 2.2 signup BR-2.4).
//
// Architect Q3 ruling: on a successful locale change, this handler invokes
// SetLocaleCookie(...) to rewrite he_locale; the attribute parity with the
// TS buildLocaleCookieOptions is enforced by the SetLocaleCookie helper +
// its cookies_test.go.
//
// Architect Q4 ruling: route wiring uses RequireJWT ONLY — NO RequireAAL(2,...)
// wrap. Profile mutation is low-blast-radius UX (BR-2.13). cmd/server/main.go
// is where this contract is materialised.
package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// updateProfileRequestBody is the strict-field PUT body. Pointers distinguish
// "field absent" from "field present with zero value". The JSON decoder
// runs in DisallowUnknownFields mode (BR-2.2) so extra keys → decode error.
type updateProfileRequestBody struct {
	DisplayName *string `json:"display_name,omitempty"`
	Locale      *string `json:"locale,omitempty"`
	Timezone    *string `json:"timezone,omitempty"`
	// DefaultRoutingStrategy (Story 6.5) needs the three-way trap disambiguated
	// at this layer — the ONLY layer that can tell HTTP `null` from `""` from
	// omitted (BLIND-BOUNDARY-002). A plain *string collapses null and omitted,
	// so a presence-tracking wrapper is required.
	DefaultRoutingStrategy nullableString `json:"default_routing_strategy"`
}

// nullableString distinguishes the three JSON states of an optional field:
//   - key absent           → Present=false              ("do not change")
//   - key present, null    → Present=true, Value=nil    ("clear")
//   - key present, "x"     → Present=true, Value=&"x"   ("set to x")
//
// UnmarshalJSON only fires when the key is present, so Present defaults to false.
type nullableString struct {
	Present bool
	Value   *string
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Present = true
	if string(b) == "null" {
		n.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// updateProfileResponseBody mirrors meResponseBody (Story 2.5 AC2 says "body
// mirrors GetMeResponse"). Same JSON shape so console reuses the same renderer.
type updateProfileResponseBody = meResponseBody

// UpdateProfile implements PUT /v1/me/profile.
func (p *AuthProxy) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	// If-Match header is REQUIRED (BR-2.7 Data Validation).
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		_ = openaierr.Write(w, r.Context(), http.StatusPreconditionRequired, "428_precondition_required", "If-Match header required", nil)
		return
	}

	// Cap body to defend against abuse; profile payload is < 1KB.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_body", "request body too large or unreadable", nil)
		return
	}

	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.DisallowUnknownFields() // BR-2.2 — strict field check
	var body updateProfileRequestBody
	if err := dec.Decode(&body); err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", err.Error(), nil)
		return
	}

	// Reject any trailing JSON content after the first object (defensive
	// against `{...}{...}` smuggling).
	if dec.More() {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", "request body must contain a single JSON object", nil)
		return
	}

	req := &authv1.UpdateProfileRequest{
		UserId:    userID,
		IfMatch:   ifMatch,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}
	if body.DisplayName != nil {
		req.DisplayName = body.DisplayName
	}
	if body.Locale != nil {
		req.Locale = body.Locale
	}
	if body.Timezone != nil {
		req.Timezone = body.Timezone
	}
	// Story 6.5 — map the three-way intent onto the proto3 optional field:
	//   absent  → leave req.DefaultRoutingStrategy nil (auth-svc: unchanged)
	//   null    → forward "" (auth-svc: clear to NULL)
	//   ""      → reject HERE (the only layer that can tell "" from null)
	//   "x"     → forward verbatim (auth-svc validates the enum)
	if body.DefaultRoutingStrategy.Present {
		switch v := body.DefaultRoutingStrategy.Value; {
		case v == nil:
			clear := ""
			req.DefaultRoutingStrategy = &clear
		case *v == "":
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request",
				"default_routing_strategy must not be empty (use null to clear)", nil)
			return
		default:
			req.DefaultRoutingStrategy = v
		}
	}

	resp, err := p.Upstream.UpdateProfile(r.Context(), connect.NewRequest(req))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	msg := resp.Msg

	// Architect Q3 ruling — on locale change, rewrite he_locale cookie so
	// the next page render's next-intl middleware reads the new locale
	// (BR-3.5 / BR-3.10). Cookie attribute parity with the TS helper is
	// enforced by SetLocaleCookie itself.
	if msg.GetLocaleChanged() {
		SetLocaleCookie(w, msg.GetLocale(), p.Env)
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", msg.GetEtag())

	respBody := updateProfileResponseBody{
		UserID:        msg.GetUserId(),
		Email:         msg.GetEmail(),
		DisplayName:   msg.DisplayName,
		Locale:        msg.GetLocale(),
		Timezone:      msg.GetTimezone(),
		TOTPEnabled:   msg.GetTotpEnabled(),
		OAuthProvider: msg.OauthProvider,
		CreatedAt:     msg.GetCreatedAt().AsTime().UTC().Format(time.RFC3339),
		UpdatedAt:     msg.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339),
		// Story 6.5 — echo the persisted value (nil → JSON null).
		DefaultRoutingStrategy: msg.DefaultRoutingStrategy,
	}
	writeJSON(w, http.StatusOK, respBody)
}
