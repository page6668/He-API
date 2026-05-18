// Package middleware — JWTVerify wires up Story 2.5+ JWT verification at
// the api-gateway. Activated by Story 2.4 for the JWT-protected 2FA
// endpoints (/v1/auth/2fa/enroll/*, /v1/auth/2fa/disable, /v1/auth/2fa/
// recovery-codes/regenerate).
//
// Design:
//   - Public key is loaded once at startup from the same K8s ConfigMap
//     auth-svc reads. cmd/server constructs a JWTVerifier; middlewares wrap
//     handlers that need authentication.
//   - Verification mirrors the auth-svc strict path: RS256 only (alg=none /
//     HS256 rejected at the keyfunc), `aud=he-api`, exp validated.
//   - Per Story 2.4 Architect Q2 (b), an mfa_token MUST NOT pass as an
//     access_token here. We sniff the `purpose` claim; non-empty → reject.
//
// User identity flows downstream via context.Context — the handler retrieves
// it with `UserIDFromContext(r.Context())`.
package middleware

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// Algorithm + audience constants — must match apps/auth-svc/internal/jwt.
const (
	jwtAlgorithm = "RS256"
	jwtAudience  = "he-api"
)

// AccessTokenCookieName is the canonical name of the access-token cookie
// (Story 2.2 cookie matrix BR-3.7).
const AccessTokenCookieName = "he_access"

// Errors surfaced by the verifier.
var (
	ErrJWTMissing       = errors.New("jwt: missing cookie")
	ErrJWTAlgInvalid    = errors.New("jwt: invalid algorithm")
	ErrJWTAudInvalid    = errors.New("jwt: invalid audience")
	ErrJWTExpired       = errors.New("jwt: expired")
	ErrJWTPurposeInvalid = errors.New("jwt: cross-token purpose claim (mfa_token rejected)")
	ErrJWTBadSignature  = errors.New("jwt: bad signature")
)

// JWTVerifier holds the RSA public key loaded at startup from the auth-svc
// ConfigMap. Safe for concurrent use.
type JWTVerifier struct {
	publicKey *rsa.PublicKey
}

// NewJWTVerifier constructs a verifier from an RSA public key. The caller
// is responsible for parsing the PEM (jwksFromPublicPEM in cmd/server does
// this for the JWKS endpoint; the public key is available alongside).
func NewJWTVerifier(pub *rsa.PublicKey) *JWTVerifier {
	return &JWTVerifier{publicKey: pub}
}

// claims is the minimal projection the verifier needs. Matches the auth-svc
// jwt.Claims wire shape.
type claims struct {
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	Purpose  string `json:"purpose,omitempty"` // Story 2.4 cross-token discrimination
	AAL      int32  `json:"aal,omitempty"`     // Story 2.4 T5.3 — NIST SP 800-63B AAL
	gojwt.RegisteredClaims
}

// GetExpirationTime delegates to RegisteredClaims (which is what gojwt
// validation reads).
func (c claims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return c.RegisteredClaims.GetExpirationTime()
}

// Verify parses + validates the supplied token string. Returns the `sub`
// claim (the user_id UUID) + the `aal` claim (Story 2.4 T5.3) on success.
//
// Cross-token rejection (Story 2.4 Architect Q2 hard constraint): any
// non-empty `purpose` claim is rejected — an mfa_token submitted as
// access_token MUST fail closed.
func (v *JWTVerifier) Verify(tokenString string) (sub string, aal int32, err error) {
	parsed, parseErr := gojwt.ParseWithClaims(tokenString, &claims{}, func(t *gojwt.Token) (any, error) {
		if t.Method.Alg() != jwtAlgorithm {
			return nil, ErrJWTAlgInvalid
		}
		return v.publicKey, nil
	})
	if parseErr != nil {
		switch {
		case errors.Is(parseErr, ErrJWTAlgInvalid):
			return "", 0, ErrJWTAlgInvalid
		case errors.Is(parseErr, gojwt.ErrTokenExpired):
			return "", 0, ErrJWTExpired
		case errors.Is(parseErr, gojwt.ErrTokenSignatureInvalid):
			return "", 0, ErrJWTBadSignature
		default:
			return "", 0, parseErr
		}
	}
	c, ok := parsed.Claims.(*claims)
	if !ok || !parsed.Valid {
		return "", 0, ErrJWTBadSignature
	}
	if c.Audience != jwtAudience {
		return "", 0, ErrJWTAudInvalid
	}
	if c.Purpose != "" {
		return "", 0, ErrJWTPurposeInvalid
	}
	if c.Subject == "" {
		return "", 0, ErrJWTBadSignature
	}
	return c.Subject, c.AAL, nil
}

// userIDKey is the context key for the verified user UUID. Unexported so
// callers MUST use UserIDFromContext (preventing collision with other
// middleware-injected values).
type ctxKey struct{ name string }

var (
	userIDKey = ctxKey{name: "userID"}
	aalKey    = ctxKey{name: "aal"}
)

// WithUserID attaches the verified user_id to the request context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFromContext retrieves the verified user_id; returns ("", false) if
// the middleware did not run (programming bug — handlers MUST be wrapped).
func UserIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(userIDKey).(string)
	return v, ok && v != ""
}

// WithAAL attaches the verified Authentication Assurance Level to the
// request context. The middleware stores the raw integer; aal_check
// compares against the threshold.
func WithAAL(ctx context.Context, aal int32) context.Context {
	return context.WithValue(ctx, aalKey, aal)
}

// AALFromContext returns the verified AAL claim, defaulting to 1 (NIST
// SP 800-63B AAL1 = single-factor) when the claim was absent / 0. Story 2.4
// BR-2.8 — older access tokens issued before the aal claim existed are
// treated as AAL1.
func AALFromContext(ctx context.Context) int32 {
	v, ok := ctx.Value(aalKey).(int32)
	if !ok || v == 0 {
		return 1
	}
	return v
}

// RequireJWT is an http.Handler middleware that enforces a valid he_access
// cookie. On failure it writes a 401 envelope and short-circuits.
//
// The handler chain pattern:
//
//	mux.Handle("/v1/auth/2fa/enroll/init", verifier.RequireJWT(http.HandlerFunc(authProxy.EnrollTOTPInit)))
func (v *JWTVerifier) RequireJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(AccessTokenCookieName)
		if err != nil || cookie.Value == "" {
			writeJWTError(w, http.StatusUnauthorized, "401_unauthenticated", "missing access token")
			return
		}
		userID, aal, err := v.Verify(cookie.Value)
		if err != nil {
			code, msg := "401_unauthenticated", "invalid access token"
			switch {
			case errors.Is(err, ErrJWTExpired):
				code, msg = "401_access_token_expired", "access token expired"
			case errors.Is(err, ErrJWTPurposeInvalid):
				// Surface a distinct code so audit / SIEM can detect
				// attempted mfa_token-as-access_token confusion.
				code, msg = "401_cross_token_rejected", "wrong token purpose"
			}
			writeJWTError(w, http.StatusUnauthorized, code, msg)
			return
		}
		ctx := WithUserID(r.Context(), userID)
		ctx = WithAAL(ctx, aal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAAL gates handlers behind a minimum NIST SP 800-63B AAL (Story 2.4
// BR-4.1 / T5.2). MUST be chained AFTER RequireJWT — depends on the AAL
// claim being placed in context by the JWT middleware. Caller passes the
// minimum acceptable AAL; on insufficient AAL the gateway emits
// 403_aal2_required and short-circuits before the proxy handler.
//
// Usage:
//
//	mux.Handle("POST /v1/auth/2fa/disable",
//	    jwtVerifier.RequireJWT(jwtVerifier.RequireAAL(2, http.HandlerFunc(auth.DisableTOTP))))
func (v *JWTVerifier) RequireAAL(minAAL int32, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := AALFromContext(r.Context())
		if got < minAAL {
			writeJWTError(w, http.StatusForbidden, "403_aal2_required",
				"This operation requires two-factor authentication. Please sign in again with 2FA.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJWTError emits the same envelope shape the rest of the gateway uses
// (TS-CONS-014). Kept package-private — this is the only place in the
// middleware that emits errors.
func writeJWTError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
	_, _ = w.Write(body)
}

// StripBearer extracts the token portion of an `Authorization: Bearer <token>`
// header per RFC 6750 §2.1. The scheme prefix match is case-insensitive
// ("bearer", "BEARER", "BeArEr" all accepted); the token portion is treated
// as opaque case-sensitive. Returns "" when the header does not carry a
// Bearer-scheme token.
//
// Story 3.2 OQ3 ruling: promoted from the package-private `stripBearer`
// stub. Story 2.4's `jwt_verify.go` does not currently consume this helper;
// it exists for the Story 3.2 bearer_auth middleware (and future
// Authorization-header consumers).
func StripBearer(h string) string {
	const scheme = "Bearer"
	if len(h) <= len(scheme) {
		return ""
	}
	if !strings.EqualFold(h[:len(scheme)], scheme) {
		return ""
	}
	// Accept one-or-more whitespace chars (space / tab) after the scheme.
	rest := h[len(scheme):]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	if i == 0 {
		// Scheme not followed by whitespace — malformed.
		return ""
	}
	return rest[i:]
}
