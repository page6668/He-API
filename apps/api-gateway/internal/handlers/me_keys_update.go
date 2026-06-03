// Story 5.2 T4.1 — REST handler for PATCH /v1/me/keys/{api_key_id}.
//
//	PATCH /v1/me/keys/{api_key_id}   — HandleUpdate (AC1)
//
// JWT-only (wrapped by middleware.JWTVerifier.RequireJWT + CSRF at
// cmd/server/main.go). user_id is the JWT `sub` claim — the body/path MUST
// NOT carry it (BR-1.1 IDOR defence). The gateway is the authoritative
// validator: strict 2-level field validation + model-registry lookup + CIDR
// shape + cap range, all BEFORE the auth-svc RPC. Anti-enumeration: cross-user
// / nonexistent / revoked all collapse to 404 (BR-1.8 via auth-svc NotFound).

package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// capMinUSD / capMaxUSD are the Q-J inclusive bounds for monthly_cost_cap_usd.
const (
	capMinUSD = 0.01
	capMaxUSD = 999999.99
)

// HandleUpdate implements AC1 — PATCH /v1/me/keys/{api_key_id}.
func (h *MeKeysHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	apiKeyID := r.PathValue("api_key_id")
	if !uuidV4Re.MatchString(apiKeyID) {
		field := "api_key_id"
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "Invalid request parameters.", &field)
		return
	}

	// Body size cap — scope.models (≤11 ids) + ip_whitelist + cap is small;
	// 16 KiB is generous. Larger → 413.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	bodyBytes, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusRequestEntityTooLarge, "413_payload_too_large", "Request body too large.", nil)
		return
	}

	// Strict-field-validation at TWO nesting levels (BR-1.2). Go's
	// DisallowUnknownFields rejects unknown keys at every struct level, so a
	// stray top-level key OR a stray key within `scope` (incl. the read-only
	// current_month_cost_usd per BR-1.12) both surface here.
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.DisallowUnknownFields()
	var req UpdateKeyRequest
	if err := dec.Decode(&req); err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", err.Error(), nil)
		return
	}
	if dec.More() {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", "request body must contain a single JSON object", nil)
		return
	}

	protoReq := &authv1.UpdateApiKeyRequest{
		UserId:    userID,
		ApiKeyId:  apiKeyID,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}
	hasField := false

	// --- scope (BR-1.4 / BR-1.5 / BR-1.7) ------------------------------------
	if req.Scope != nil {
		if req.Scope.Models == nil && req.Scope.IPWhitelist == nil {
			// `{"scope":{}}` is degenerate (BR-1.7).
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request",
				"scope object must contain models or ip_whitelist", nil)
			return
		}
		sp := &authv1.ScopePatch{}
		if req.Scope.Models != nil {
			for i, m := range *req.Scope.Models {
				if !isKnownModel(m) {
					param := "scope.models[" + strconv.Itoa(i) + "]"
					_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "unknown model id: "+m, &param)
					return
				}
			}
			sp.Models = *req.Scope.Models
			sp.ModelsPresent = true
			hasField = true
		}
		if req.Scope.IPWhitelist != nil {
			for i, c := range *req.Scope.IPWhitelist {
				if !validGatewayCIDR(c) {
					param := "scope.ip_whitelist[" + strconv.Itoa(i) + "]"
					_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "invalid CIDR or IP: "+c, &param)
					return
				}
			}
			sp.IpWhitelist = *req.Scope.IPWhitelist
			sp.IpWhitelistPresent = true
			hasField = true
		}
		protoReq.Scope = sp
	}

	// --- monthly_cost_cap_usd (BR-1.6 / Q-J) ---------------------------------
	if req.MonthlyCostCapUSD != nil {
		raw := strings.TrimSpace(string(req.MonthlyCostCapUSD))
		switch {
		case raw == "null":
			protoReq.ClearMonthlyCap = true
			hasField = true
		case strings.HasPrefix(raw, `"`):
			var capStr string
			if err := json.Unmarshal(req.MonthlyCostCapUSD, &capStr); err != nil {
				_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "invalid monthly_cost_cap_usd", nil)
				return
			}
			if !validCapRange(capStr) {
				_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "invalid cap range", nil)
				return
			}
			protoReq.MonthlyCostCapUsd = &capStr
			hasField = true
		default:
			// JSON number (or anything non-string, non-null) → reject per BR-1.6.
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request",
				"monthly_cost_cap_usd must be a string decimal or null", nil)
			return
		}
	}

	// BR-1.3 — at least one field must be present.
	if !hasField {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request",
			"At least one configuration field must be provided", nil)
		return
	}

	resp, err := h.upstream.UpdateApiKey(r.Context(), connect.NewRequest(protoReq))
	if err != nil {
		h.translateUpstreamError(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, updateRespToJSON(resp.Msg))
}

// isKnownModel reports whether id is in the Story-4.7 in-process model
// registry (BR-1.4). Reuses the same authoritative map the /v1/models handler
// serves from — no second registry.
func isKnownModel(id string) bool {
	_, ok := capabilitiesByModelID[id]
	return ok
}

// validGatewayCIDR validates a scope.ip_whitelist entry as an IPv4/IPv6
// address or CIDR (BR-1.5). The degenerate `0.0.0.0/0` / `::/0` (Bits()==0)
// is rejected — it is equivalent to an empty whitelist with side effects.
func validGatewayCIDR(s string) bool {
	if strings.ContainsRune(s, '/') {
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			return false
		}
		return pfx.Bits() != 0
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// validCapRange validates a string-decimal cap is in [0.01, 999999.99] (Q-J).
func validCapRange(s string) bool {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	return f >= capMinUSD && f <= capMaxUSD
}

// updateRespToJSON projects the proto UpdateApiKeyResponse onto the JSON wire
// shape (BR-1.13 field order). Mirrors protoEntryToJSON (me_keys.go).
func updateRespToJSON(p *authv1.UpdateApiKeyResponse) UpdateKeyResponse {
	out := UpdateKeyResponse{
		APIKeyID:            p.GetApiKeyId(),
		Name:                p.GetName(),
		KeyPrefix:           p.GetKeyPrefix(),
		Scope:               json.RawMessage(stringOrEmpty(p.GetScope(), `{}`)),
		CurrentMonthCostUSD: p.GetCurrentMonthCostUsd(),
		CreatedAt:           p.GetCreatedAt().AsTime().UTC().Format(time.RFC3339),
	}
	if p.MonthlyCostCapUsd != nil {
		v := *p.MonthlyCostCapUsd
		out.MonthlyCostCapUSD = &v
	}
	if p.LastUsedAt != nil {
		s := p.LastUsedAt.AsTime().UTC().Format(time.RFC3339)
		out.LastUsedAt = &s
	}
	if p.RevokedAt != nil {
		s := p.RevokedAt.AsTime().UTC().Format(time.RFC3339)
		out.RevokedAt = &s
	}
	return out
}
