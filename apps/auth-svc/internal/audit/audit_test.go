package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
)

// Scenario: 2.2-UNIT-182 + 2.2-UNIT-183
// Event struct supports the 8 Story 2.2 event types + matches the BR-4.5
// JSON wire shape.
func TestEvent_AllEightStoryEventTypesDefined(t *testing.T) {
	t.Parallel()
	want := []audit.EventType{
		"auth.signup",
		"auth.signup_duplicate_attempt",
		"auth.verify_email",
		"auth.verify_email_brute_force",
		"auth.signin_success",
		"auth.signin_failure",
		"auth.account_locked",
		"auth.email_send_failed",
	}
	defined := []audit.EventType{
		audit.EventSignup,
		audit.EventSignupDuplicateAttempt,
		audit.EventVerifyEmail,
		audit.EventVerifyEmailBruteForce,
		audit.EventSigninSuccess,
		audit.EventSigninFailure,
		audit.EventAccountLocked,
		audit.EventEmailSendFailed,
	}
	if !reflect.DeepEqual(want, defined) {
		t.Fatalf("audit.Event* constants drifted from TS-CONS-015 list:\n want: %v\n got:  %v", want, defined)
	}
}

// Scenario: 2.2-UNIT-183
// Event JSON shape uses snake_case keys per BR-4.5 (data-models.md §4.4).
func TestEvent_JSONShapeMatchesBR4_5(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	e := audit.Event{
		EventType: audit.EventSignup,
		UserID:    "11111111-1111-1111-1111-111111111111",
		EmailHash: "a1b2c3",
		IP:        "1.2.3.4",
		UserAgent: "Go-http-client/1.1",
		Timestamp: now,
		Success:   true,
		ErrorCode: "",
		Metadata:  map[string]any{"foo": "bar"},
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, raw)
	}
	for _, key := range []string{"event_type", "user_id", "email_hash", "ip", "ua", "ts", "success", "metadata"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q; got %v", key, got)
		}
	}
	// camelCase / PascalCase fields MUST NOT appear.
	for _, banned := range []string{"EventType", "UserID", "EmailHash", "userAgent", "UserAgent", "errorCode"} {
		if _, ok := got[banned]; ok {
			t.Errorf("JSON has unexpected key %q (snake_case only per BR-4.5)", banned)
		}
	}
}

// Scenario: 2.2-UNIT-184
// AuditEvent struct has NO field with type `string` named like email or
// password / token / cleartext. Static reflection scan; future regressions
// that try to add `Email string` or `Password []byte` will trip the check.
func TestEvent_NoPlaintextSensitiveFields(t *testing.T) {
	t.Parallel()
	bannedNames := []string{
		"Email",      // use EmailHash
		"Password",
		"PasswordHash",
		"Token",      // tokens are package-local in token/
		"Plaintext",
		"Cleartext",
		"Secret",
	}
	typ := reflect.TypeOf(audit.Event{})
	for i := 0; i < typ.NumField(); i++ {
		fname := typ.Field(i).Name
		for _, banned := range bannedNames {
			if fname == banned {
				t.Errorf("audit.Event has banned field %q — sensitive data must not enter audit payload (TS-CONS-005)", fname)
			}
		}
	}
}

// Static source scan rejects accidental new fields with sensitive names.
func TestSource_NoPlaintextSensitiveAccessors(t *testing.T) {
	t.Parallel()
	src := readPackageSource(t)
	// Reject: any struct field declaration whose name is in the banned list.
	bannedField := regexp.MustCompile(`(?m)^\s*(Email|Password|PasswordHash|Plaintext|Cleartext|Secret)\s+(string|\[\]byte)\b`)
	if loc := bannedField.FindStringIndex(src); loc != nil {
		t.Errorf("package source declares banned field: %q", src[loc[0]:loc[1]])
	}
}

// Scenario: P2f / 1 — NoOpPublisher emits structured log + never errors.
func TestNoOpPublisher_LogsAndNeverErrors(t *testing.T) {
	t.Parallel()
	var captured strings.Builder
	logger := slog.New(slog.NewTextHandler(&captured, nil))
	p := audit.NewNoOpPublisher(logger)
	err := p.Publish(context.Background(), audit.Event{
		EventType: audit.EventSignup,
		EmailHash: "abc",
		Success:   true,
	})
	if err != nil {
		t.Fatalf("NoOpPublisher.Publish returned err = %v, want nil", err)
	}
	out := captured.String()
	if !strings.Contains(out, "event_type=auth.signup") {
		t.Errorf("log line missing event_type: %s", out)
	}
	if !strings.Contains(out, "email_hash=abc") {
		t.Errorf("log line missing email_hash: %s", out)
	}
}

// Scenario: P2f / 1 — PublishBestEffort never blocks on publisher failure.
// TS-CONS-009: Kafka outage MUST NOT block signin/signup success.
func TestPublishBestEffort_SwallowsErrors(t *testing.T) {
	t.Parallel()
	var capt strings.Builder
	logger := slog.New(slog.NewTextHandler(&capt, nil))
	failing := &failingPublisher{err: errors.New("kafka broker down")}

	// Must not panic; must not return anything (signature is void).
	audit.PublishBestEffort(context.Background(), failing, logger, audit.Event{
		EventType: audit.EventSignup,
		EmailHash: "abc",
		ErrorCode: "",
	})
	if failing.calls.Load() != 1 {
		t.Fatalf("Publish call count = %d, want 1", failing.calls.Load())
	}
	out := capt.String()
	if !strings.Contains(out, "audit publish failed") {
		t.Errorf("expected warn-level log of audit publish failure; got: %s", out)
	}
}

type failingPublisher struct {
	calls atomic.Int64
	err   error
}

func (p *failingPublisher) Publish(_ context.Context, _ audit.Event) error {
	p.calls.Add(1)
	return p.err
}

// helper — read package source for static scans
func readPackageSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		t.Fatalf("no .go source under %s", dir)
	}
	return sb.String()
}
