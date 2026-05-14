package obs

import (
	"context"
	"log/slog"
	"strings"
)

// RedactedValue replaces sensitive attribute values in structured log output.
// The literal string is what a post-incident log scrape will see when a
// developer accidentally passes a secret-bearing field to slog (Story 2.4
// BR-5.8 / m-2 ruling, "load-bearing" per QA risk profile §SEC-007).
const RedactedValue = "[REDACTED]"

// DefaultRedactKeys is the canonical denylist applied to slog attribute names.
// The handler matches by case-insensitive substring — so `password`,
// `Password`, `user_password`, `password_hash` all match.
//
// Maintenance: every Story that introduces a new secret-bearing concept MUST
// extend this list. Story 2.2 baseline: password / token / cookie / secret /
// authorization / bearer. Story 2.4 additions: totp_secret / mfa_token /
// recovery_code / otpauth.
var DefaultRedactKeys = []string{
	"password",
	"token",        // catches access_token, refresh_token, mfa_token, verification_token
	"cookie",
	"secret",       // catches totp_secret, totp_secret_encrypted, client_secret
	"authorization",
	"bearer",
	"recovery_code",
	"otpauth",      // catches otpauth_uri (per BR-5.8)
	// Intentionally NOT added: "email" / "ip" / "user_agent" — these have
	// dedicated hash representations (email_hash / ip_hash / ua_hash) per
	// Story 2.2/2.3 conventions; the raw forms are caller-controlled.
}

// RedactionHandler is a slog.Handler that walks every attribute on every
// record and replaces values whose KEY matches any entry in `redactKeys`
// (case-insensitive substring). The replacement value is the constant
// RedactedValue.
//
// Concurrency: safe under concurrent Handle calls; inner.Handle is the
// only mutation point and the underlying handler is responsible for its
// own safety.
//
// Composition: WithAttrs / WithGroup preserve the redaction policy through
// chained handlers (per m-2 spec) by re-wrapping the inner handler.
type RedactionHandler struct {
	inner      slog.Handler
	redactKeys []string // pre-lowercased for fast comparison
	preAttrs   []slog.Attr
}

// NewRedactionHandler returns a handler that wraps `inner`. If `keys` is nil
// the DefaultRedactKeys list applies; pass a custom slice to extend or
// override.
func NewRedactionHandler(inner slog.Handler, keys []string) *RedactionHandler {
	if keys == nil {
		keys = DefaultRedactKeys
	}
	lc := make([]string, len(keys))
	for i, k := range keys {
		lc[i] = strings.ToLower(k)
	}
	return &RedactionHandler{
		inner:      inner,
		redactKeys: lc,
	}
}

// Enabled delegates to the inner handler.
func (h *RedactionHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle scans every attribute on `r` and emits a new record with sensitive
// values replaced. WithAttrs-supplied pre-attrs are also scanned.
func (h *RedactionHandler) Handle(ctx context.Context, r slog.Record) error {
	// Build a new record so we don't mutate the original (records pass
	// through multiple handlers in a chain).
	clone := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clone.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clone)
}

// WithAttrs returns a new RedactionHandler with the supplied attrs applied
// to the inner handler. The redaction policy is also applied to the new
// attrs so subsequent records inherit the redacted form.
func (h *RedactionHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = h.redactAttr(a)
	}
	return &RedactionHandler{
		inner:      h.inner.WithAttrs(redacted),
		redactKeys: h.redactKeys,
	}
}

// WithGroup wraps the inner handler's WithGroup call; redaction policy
// is preserved across group boundaries.
func (h *RedactionHandler) WithGroup(name string) slog.Handler {
	return &RedactionHandler{
		inner:      h.inner.WithGroup(name),
		redactKeys: h.redactKeys,
	}
}

// redactAttr applies the policy to a single attribute. Groups recurse so
// nested structures (e.g., slog.Group("auth", slog.String("password", ...)))
// also have their leaf values replaced.
func (h *RedactionHandler) redactAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		inner := a.Value.Group()
		out := make([]slog.Attr, len(inner))
		for i, sub := range inner {
			out[i] = h.redactAttr(sub)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if h.matchKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	return a
}

// matchKey returns true if `name` contains (case-insensitive) any entry in
// the redact-keys list. We pre-lowercase the policy entries; the test is
// substring on the lowercase form of `name`.
func (h *RedactionHandler) matchKey(name string) bool {
	lc := strings.ToLower(name)
	for _, k := range h.redactKeys {
		if strings.Contains(lc, k) {
			return true
		}
	}
	return false
}
