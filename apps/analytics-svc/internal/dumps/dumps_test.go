// Unit tests for the PII redaction allow-list contract — BR-4.3 / BR-4.4.
// These tests run on every PR (no testcontainers required) and fail loud
// the moment someone adds a forbidden column to the dump set.
package dumps_test

import (
	"testing"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
)

// Scenario: 2.6-UNIT-080 — BR-4.3 users.json MUST NOT contain
// password_hash / totp_secret_encrypted / oauth_subject.
func TestUsersDump_PIIRedaction_BR43(t *testing.T) {
	t.Parallel()
	cols := dumps.AllowedColumns()["users.json"]
	for _, forbidden := range []string{"password_hash", "totp_secret_encrypted", "oauth_subject"} {
		for _, c := range cols {
			if c == forbidden {
				t.Errorf("users.json column allow-list contains forbidden field %q (BR-4.3)", forbidden)
			}
		}
	}
	// Sanity: at least the user_id-scoping fields are present.
	want := []string{"id", "email", "display_name", "locale", "timezone"}
	for _, w := range want {
		found := false
		for _, c := range cols {
			if c == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("users.json column allow-list missing required field %q", w)
		}
	}
}

// Scenario: 2.6-UNIT-081 — BR-4.4 api_keys.json MUST NOT contain key_hash.
func TestApiKeysDump_PIIRedaction_BR44(t *testing.T) {
	t.Parallel()
	cols := dumps.AllowedColumns()["api_keys.json"]
	for _, c := range cols {
		if c == "key_hash" {
			t.Errorf("api_keys.json column allow-list contains key_hash — BR-4.4 violation")
		}
	}
	want := []string{"id", "key_prefix", "name"}
	for _, w := range want {
		found := false
		for _, c := range cols {
			if c == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("api_keys.json column allow-list missing required field %q", w)
		}
	}
}

// Scenario: 2.6-UNIT-082 — BR-4.4 enumeration: 7 dump files exist exactly,
// matching the GDPR completeness invariant (`len(zipReader.File) == 7` in
// T4.1 INT-020).
func TestDumpSet_SevenFiles(t *testing.T) {
	t.Parallel()
	got := dumps.AllowedColumns()
	wantNames := []string{
		"users.json", "api_keys.json", "subscriptions.json", "balances.json",
		"recharge_orders.json", "content_safety_logs.json", "request_logs.json",
	}
	if len(got) != len(wantNames) {
		t.Fatalf("dump count: got %d want %d", len(got), len(wantNames))
	}
	for _, n := range wantNames {
		if _, ok := got[n]; !ok {
			t.Errorf("missing dump file %q from AllowedColumns()", n)
		}
	}
}
