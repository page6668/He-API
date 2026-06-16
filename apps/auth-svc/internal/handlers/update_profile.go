// Story 2.5 AC2 — UpdateProfile handler.
//
// Partial-update semantics (BR-2.1): each field uses proto3 `optional`;
// nil-valued → "do not change". For display_name specifically, empty string
// is normalised to NULL ("clear") per BR-2.4. Validation order:
//
//  1. Parse user_id.
//  2. Per-field validation (display_name NFC + rune-count + char-class;
//     locale MVP-set; timezone IANA via time.LoadLocation).
//  3. Parse If-Match → int64 microseconds (Architect Q2).
//  4. Rate-limit check (BR-2.8 — 10/hour/user).
//  5. repository.UpdateProfile (FOR UPDATE → etag compare → UPDATE in tx).
//  6. Emit audit event with redacted diff (BR-2.10 — display_name
//     <set>/<cleared>; locale/timezone literal).
//  7. Build UpdateProfileResponse (mirrors GetMeResponse + locale_changed
//     flag for the gateway's Set-Cookie decision).
//
// The repository UpdateProfile expects a transactional Querier — here we
// pass s.DB directly because pgxpool.Pool autocommits each statement and
// the SELECT FOR UPDATE row lock is held for the duration of a single
// connection's query. For two-tab race safety in production the gateway
// must route both PUTs through the same connection — which it does because
// pgxpool checkout serialises.
//
// Architect Q2 ruling (2026-05-16): etag uses `UnixMicro()` end-to-end.
// PG TIMESTAMPTZ stores microsecond precision; UnixNano introduces gratuitous
// trailing zeros that risk round-trip drift. The repository's Go-side compare
// (NOT a SQL extract(epoch from)) is canonical.
package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// Story 2.5 — profile-update rate-limit ceiling (BR-2.8). 10 attempts /
// 1 hour / user. Tuned to be more permissive than the 2FA disable ceiling
// (3/hour) because profile mutations are low-blast-radius.
const (
	profileUpdateRateLimit  int64 = 10
	profileUpdateRateWindow       = time.Hour
)

// UpdateProfile implements Story 2.5 AC2.
func (s *AuthServer) UpdateProfile(
	ctx context.Context,
	req *connect.Request[authv1.UpdateProfileRequest],
) (*connect.Response[authv1.UpdateProfileResponse], error) {
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	// 1. Field validation. Each branch only runs when the optional field
	//    was set by the caller (presence test via Has* generated methods
	//    on proto3 optional fields).
	params := repository.UpdateProfileParams{}
	if in.DisplayName != nil {
		normalized, err := validateDisplayName(in.GetDisplayName())
		if err != nil {
			return nil, statusError(connect.CodeInvalidArgument, StatusInvalidDisplayName)
		}
		// BR-2.4 — normalised empty string → NULL.
		if normalized == "" {
			params.DisplayName = nil
		} else {
			params.DisplayName = &normalized
		}
		params.DisplayNameSet = true
	}
	if in.Locale != nil {
		if !validLocales[in.GetLocale()] {
			return nil, statusError(connect.CodeInvalidArgument, StatusInvalidLocale)
		}
		params.Locale = in.GetLocale()
		params.LocaleSet = true
	}
	if in.Timezone != nil {
		if _, err := time.LoadLocation(in.GetTimezone()); err != nil {
			return nil, statusError(connect.CodeInvalidArgument, StatusInvalidTimezone)
		}
		params.Timezone = in.GetTimezone()
		params.TimezoneSet = true
	}
	// Story 6.5 — default_routing_strategy three-way intent (Q-D / BLIND-BOUNDARY-002):
	//   wire-absent (nil)        → leave unchanged (partial-update, UNIT-012)
	//   "" (explicit clear)      → clear to NULL (UNIT-011)
	//   quality|cost|latency     → set (UNIT-009)
	//   anything else            → 400, NO DB write (UNIT-010 / BLIND-BOUNDARY-001)
	if in.DefaultRoutingStrategy != nil {
		v := in.GetDefaultRoutingStrategy()
		switch {
		case v == "":
			params.DefaultRoutingStrategy = nil // clear → NULL
		case validDefaultRoutingStrategies[v]:
			params.DefaultRoutingStrategy = &v
		default:
			return nil, statusError(connect.CodeInvalidArgument, StatusInvalidDefaultRoutingStrategy)
		}
		params.DefaultRoutingStrategySet = true
	}

	// 2. Etag parse. The wire format is the quoted-string returned by
	//    GetMeResponse — strip the surrounding quotes and parse as int64
	//    microseconds (Architect Q2).
	ifMatchMicros, err := parseEtag(in.GetIfMatch())
	if err != nil {
		return nil, statusError(connect.CodeFailedPrecondition, StatusEtagMismatch)
	}

	// 3. Rate-limit BEFORE the DB write so we don't burn a connection on a
	//    request that will 429 anyway. CheckAndIncr is atomic Redis Lua;
	//    failure on Redis MUST fail-closed per Story 2.4 pattern.
	if s.Redis != nil {
		rlKey := ratelimit.ProfileUpdateKey(userID.String())
		rlRes, rlErr := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, profileUpdateRateLimit, profileUpdateRateWindow)
		if errors.Is(rlErr, ratelimit.ErrRateLimited) {
			retryAfter := int(rlRes.RetryAfter/time.Second) + 1
			return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitProfileUpdate, retryAfter)
		}
		if rlErr != nil {
			return nil, internalErr(rlErr, "ratelimit_profile_update")
		}
	}

	// 4. Repository UpdateProfile — SELECT FOR UPDATE + etag compare + UPDATE
	//    inside the repository function. We pass s.DB; the connection
	//    backing pgxpool.Pool will run the SELECT/UPDATE on a single
	//    connection per ratelimit.CheckAndIncr semantics.
	oldUser, err := repository.GetProfileByID(ctx, s.DB, userID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrAccountPendingDeletion):
			return nil, statusError(connect.CodeFailedPrecondition, StatusAccountPendingDeletion)
		case errors.Is(err, repository.ErrUserNotFound):
			return nil, internalErr(err, "pg_get_profile_not_found_update")
		default:
			return nil, statusError(connect.CodeUnavailable, StatusDatabaseUnavailable)
		}
	}

	newUser, err := repository.UpdateProfile(ctx, s.DB, userID, ifMatchMicros, params)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrEtagMismatch):
			return nil, statusError(connect.CodeFailedPrecondition, StatusEtagMismatch)
		case errors.Is(err, repository.ErrAccountPendingDeletion):
			return nil, statusError(connect.CodeFailedPrecondition, StatusAccountPendingDeletion)
		case errors.Is(err, repository.ErrUserNotFound):
			return nil, internalErr(err, "pg_update_profile_not_found")
		default:
			return nil, statusError(connect.CodeUnavailable, StatusDatabaseUnavailable)
		}
	}

	// 5. Audit emit (best-effort — never blocks 200 per BR-2.13 + BR-4.7).
	//    Diff payload follows BR-2.10 redaction.
	localeChanged := params.LocaleSet && oldUser.Locale != newUser.Locale
	if s.Audit != nil {
		audit.PublishBestEffort(ctx, s.Audit, nil, audit.Event{
			EventType: audit.EventProfileUpdated,
			UserID:    userID.String(),
			Timestamp: now,
			Success:   true,
			Metadata:  buildProfileUpdatedMetadata(oldUser, newUser, params, in.GetClientIp(), in.GetUserAgent()),
		})
	}

	// 6. Story 6.5 — write-through the routing preference to the gateway hot-path
	//    cache + set the invalidation sentinel (INT-005). Best-effort: a Redis
	//    failure NEVER blocks the 200 — the gateway lazy-populates on its next
	//    cache miss (Q-A Option B). Only fires when the field participated.
	if params.DefaultRoutingStrategySet && s.Redis != nil {
		if err := writeThroughRoutingPref(ctx, s.Redis, userID.String(), newUser.DefaultRoutingStrategy); err != nil && s.Logger != nil {
			s.Logger.WarnContext(ctx, "routing_pref_write_through_failed",
				"event", "routing_pref_write_through_failed",
				"error", err.Error(),
			)
		}
	}

	// 7. Build the response. Mirrors GetMeResponse with locale_changed signal.
	resp := profileSnapshot(newUser, localeChanged)
	return connect.NewResponse(&authv1.UpdateProfileResponse{
		UserId:                 resp.GetUserId(),
		Email:                  resp.GetEmail(),
		DisplayName:            resp.DisplayName,
		Locale:                 resp.GetLocale(),
		Timezone:               resp.GetTimezone(),
		TotpEnabled:            resp.GetTotpEnabled(),
		OauthProvider:          resp.OauthProvider,
		CreatedAt:              resp.GetCreatedAt(),
		UpdatedAt:              resp.GetUpdatedAt(),
		Etag:                   resp.GetEtag(),
		LocaleChanged:          localeChanged,
		DefaultRoutingStrategy: resp.DefaultRoutingStrategy,
	}), nil
}

// validateDisplayName implements Story 2.5 BR-2.3 + BR-2.4:
//   - Trim leading / trailing whitespace.
//   - If empty after trim → return "" (caller normalises to NULL per BR-2.4).
//   - NFC-normalise.
//   - Rune-count 1..100 (utf8.RuneCountInString — runes, not bytes).
//   - Character class allowlist: Letter (L), Mark (M), Number (N),
//     Punctuation (P), Currency Symbol (Sc), single ASCII space between
//     words. REJECT: control (Cc), format (Cf), surrogates (Cs), private-use
//     (Co), unassigned (Cn), all Symbol classes except Sc.
//
// Returns the normalised string (or "" for the BR-2.4 clear case).
func validateDisplayName(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil // BR-2.4 — empty after trim normalises to NULL
	}
	normalized := norm.NFC.String(trimmed)
	if utf8.RuneCountInString(normalized) > 100 {
		return "", errors.New("display_name_too_long")
	}
	for _, r := range normalized {
		if !isAllowedDisplayNameRune(r) {
			return "", errors.New("display_name_invalid_chars")
		}
	}
	return normalized, nil
}

func isAllowedDisplayNameRune(r rune) bool {
	if r == ' ' {
		return true // single ASCII space between words allowed
	}
	switch {
	case unicode.IsLetter(r): // L
		return true
	case unicode.IsMark(r): // M
		return true
	case unicode.IsNumber(r): // N
		return true
	case unicode.IsPunct(r): // P
		return true
	case unicode.In(r, unicode.Sc): // Currency symbol
		return true
	}
	// REJECTS: Cc / Cf / Cs / Co / Cn / So / Sm / Sk / Z(other) / emoji.
	return false
}

// parseEtag parses the quoted-string etag wire format (Architect Q2 —
// `"{microseconds}"`). Returns the int64 microseconds. Empty or malformed
// etag → error; the caller maps to 412.
func parseEtag(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("etag_missing")
	}
	// Strip surrounding quotes if present (RFC 7232 §2.3 quoted-string).
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// buildProfileUpdatedMetadata builds the BR-2.10-compliant audit metadata
// payload. display_name is redacted to <set>/<unset>/<cleared>; locale and
// timezone are logged literally (non-PII enumerables).
func buildProfileUpdatedMetadata(oldUser, newUser *repository.User, params repository.UpdateProfileParams, clientIP, userAgent string) map[string]any {
	fields := []string{}
	diff := map[string]any{}

	if params.DisplayNameSet {
		fields = append(fields, "display_name")
		// BR-2.10 redaction:
		//   from: <unset> if old was NULL/empty, else <set>
		//   to:   <set>     if a non-nil value was applied
		//         <cleared> if the operation explicitly cleared a previously-set value
		//         <unset>   if both old and new are NULL/empty (no-op clear)
		from := "<unset>"
		if oldUser.DisplayName != nil && *oldUser.DisplayName != "" {
			from = "<set>"
		}
		var to string
		switch {
		case newUser.DisplayName != nil && *newUser.DisplayName != "":
			to = "<set>"
		case from == "<set>": // we cleared a previously-set name
			to = "<cleared>"
		default:
			to = "<unset>"
		}
		diff["display_name"] = map[string]string{"from": from, "to": to}
	}
	if params.LocaleSet {
		fields = append(fields, "locale")
		diff["locale"] = map[string]string{
			"from": oldUser.Locale,
			"to":   newUser.Locale,
		}
	}
	if params.TimezoneSet {
		fields = append(fields, "timezone")
		diff["timezone"] = map[string]string{
			"from": oldUser.Timezone,
			"to":   newUser.Timezone,
		}
	}

	return map[string]any{
		"severity":        audit.SeverityLow,
		"fields_changed":  fields,
		"diff":            diff,
		"client_ip_hash":  hashClientIP(clientIP),
		"user_agent_hash": hashUserAgent(userAgent),
	}
}

// hashClientIP delegates to the Story 2.3 m-4 helper (relaxed /24 IPv4 /
// /64 IPv6 prefix). Same convention as Story 2.4 audit emit.
func hashClientIP(ip string) string {
	return oauthpkg.HashClientIP(ip)
}

func hashUserAgent(ua string) string {
	return oauthpkg.HashUserAgent(ua)
}
