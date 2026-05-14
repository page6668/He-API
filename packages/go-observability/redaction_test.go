package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// helper: build a logger that writes JSON to a buffer with the redaction
// handler in front. Returns the logger + buffer for assertion.
func loggerWithRedaction(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(NewRedactionHandler(inner, nil)), &buf
}

// Scenario: 2.4-SEC-006 — redaction handler replaces totp_secret / mfa_token /
// recovery_code / cookie / password / authorization values with [REDACTED].
func TestRedaction_DefaultKeysReplaced(t *testing.T) {
	t.Parallel()
	log, buf := loggerWithRedaction(t)
	log.Info("audit",
		slog.String("password", "p@ssw0rd"),
		slog.String("totp_secret", "ABCDEFG"),
		slog.String("mfa_token", "eyJabc"),
		slog.String("recovery_code", "ABCD-EFG-HJ2"),
		slog.String("cookie", "he_access=xxx"),
		slog.String("authorization", "Bearer xxx"),
		slog.String("user_id", "uuid-xyz"),
	)
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, buf.String())
	}
	for _, k := range []string{"password", "totp_secret", "mfa_token", "recovery_code", "cookie", "authorization"} {
		if rec[k] != RedactedValue {
			t.Errorf("key %q: got %v want %q", k, rec[k], RedactedValue)
		}
	}
	// Non-sensitive keys pass through.
	if rec["user_id"] != "uuid-xyz" {
		t.Errorf("user_id should be unchanged, got %v", rec["user_id"])
	}
}

// Scenario: 2.4-SEC-007 — case-insensitive matching (Password, MFA_Token, etc.).
func TestRedaction_CaseInsensitive(t *testing.T) {
	t.Parallel()
	log, buf := loggerWithRedaction(t)
	log.Info("event",
		slog.String("Password", "x"),
		slog.String("MFA_Token", "y"),
		slog.String("PASSWORD_HASH", "z"),
		slog.String("user_password", "w"),
	)
	for _, want := range []string{`"Password":"[REDACTED]"`, `"MFA_Token":"[REDACTED]"`, `"PASSWORD_HASH":"[REDACTED]"`, `"user_password":"[REDACTED]"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q\nout=%s", want, buf.String())
		}
	}
}

// Scenario: 2.4-SEC-008 — WithAttrs preserves redaction for pre-bound attrs.
func TestRedaction_WithAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	log := slog.New(NewRedactionHandler(inner, nil))
	// Pre-bind a sensitive attr — it must still be redacted on subsequent logs.
	bound := log.With(slog.String("secret", "shh"))
	bound.Info("hello")
	if !strings.Contains(buf.String(), `"secret":"[REDACTED]"`) {
		t.Errorf("bound attr not redacted: %s", buf.String())
	}
}

// Scenario: 2.4-SEC-008 — WithGroup preserves redaction inside the group.
func TestRedaction_WithGroup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	log := slog.New(NewRedactionHandler(inner, nil))
	log.WithGroup("auth").Info("ok",
		slog.String("mfa_token", "eyJ..."),
		slog.String("user_id", "u1"),
	)
	if !strings.Contains(buf.String(), `"mfa_token":"[REDACTED]"`) {
		t.Errorf("group attr not redacted: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"user_id":"u1"`) {
		t.Errorf("group non-sensitive should pass through: %s", buf.String())
	}
}

// Scenario: 2.4-SEC-008 — nested slog.Group attrs are recursed.
func TestRedaction_NestedGroup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	log := slog.New(NewRedactionHandler(inner, nil))
	log.Info("nested", slog.Group("auth",
		slog.String("password", "p"),
		slog.String("user_id", "u"),
	))
	out := buf.String()
	if !strings.Contains(out, `"password":"[REDACTED]"`) {
		t.Errorf("nested redaction missing: %s", out)
	}
	if !strings.Contains(out, `"user_id":"u"`) {
		t.Errorf("nested non-sensitive should pass: %s", out)
	}
}

// Scenario: 2.4-SEC-009 — across 1000 log lines containing secrets, zero leaks
// the plaintext value (post-incident scrape simulation).
func TestRedaction_ZeroLeaksAcross1000(t *testing.T) {
	t.Parallel()
	const N = 1000
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	log := slog.New(NewRedactionHandler(inner, nil))
	plaintext := "SECRET-VALUE-XYZ-NEVER-LEAK"
	for i := 0; i < N; i++ {
		log.Info("scrape",
			slog.String("totp_secret", plaintext),
			slog.String("mfa_token", plaintext),
			slog.String("recovery_code", plaintext),
		)
	}
	if strings.Contains(buf.String(), plaintext) {
		t.Fatalf("plaintext leaked across %d lines", N)
	}
}

// Scenario: 2.4-SEC-009 — Enabled delegates so log levels still work.
func TestRedaction_EnabledDelegates(t *testing.T) {
	t.Parallel()
	inner := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := NewRedactionHandler(inner, nil)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Errorf("Info should be disabled when inner level=Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Errorf("Error should be enabled")
	}
}
