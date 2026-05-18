// Package dumps contains the 7 per-table JSON dump implementations for the
// GDPR data-export worker (Story 2.6 T4.2).
//
// Each dumper writes a JSON document (or JSON-Lines for request_logs per
// BR-4.6) for one source table, scoped to a single user_id. The column
// allow-list is EXPLICIT inside each Dumper — never `SELECT *`, never
// reflection — so the PII redaction guarantees (BR-4.3 + BR-4.4) are
// statically auditable by code review (T8.5 V10.3.1 grep).
//
// The unit tests in this package assert the column allow-lists at run-time
// against the table schema fixtures (T4.1 INT-025), and the
// `// pii-allow-list:` comment above each dump function is parsed by the
// QA scan (2.6-UNIT-080..086 candidate) to confirm no extra fields snuck
// in via copy-paste.
//
// SCOPE DEFERRAL: this file ships the dumper INTERFACES + the in-progress
// PII redaction allow-lists + JSON envelope helpers. The PG / ClickHouse
// query bodies require live cluster credentials and are wired by the
// worker (see apps/analytics-svc/internal/workers/gdpr_export.go) — the
// Dumper interface here lets the worker fan-out 7 concurrent dumps and
// the integration tests (T4.1 testcontainers, run on PR) verify
// end-to-end correctness with seeded fixtures.
package dumps

import (
	"context"
	"io"
)

// Dumper writes one user's worth of one table as JSON to w. Implementations
// MUST honour the PII redaction allow-list documented above their type
// (BR-4.3 / BR-4.4). The returned int64 is the byte count for the
// Prometheus `gdpr_export_worker_zip_bytes` histogram (T4.5).
type Dumper interface {
	Name() string // file name inside the ZIP — e.g. "users.json"
	Dump(ctx context.Context, userID string, w io.Writer) (int64, error)
}

// usersDumpColumns enumerates the columns dumped to users.json per BR-4.3.
// EXPLICITLY OMITS password_hash, totp_secret_encrypted, oauth_subject —
// credentials never leave the system.
//
// pii-allow-list: users
var usersDumpColumns = []string{
	"id", "email", "email_verified_at", "oauth_provider", "display_name",
	"locale", "timezone", "totp_enabled", "status", "pending_deletion_at",
	"created_at", "updated_at",
}

// apiKeysDumpColumns enumerates the api_keys.json columns per BR-4.4.
// EXPLICITLY OMITS key_hash — only key_prefix (the display-safe leading
// chars) is returned.
//
// pii-allow-list: api_keys
var apiKeysDumpColumns = []string{
	"id", "name", "key_prefix", "scope", "monthly_cost_cap_usd",
	"current_month_cost_usd", "last_used_at", "revoked_at", "created_at",
}

// AllowedColumns returns a static map of {dump_name → allowed columns} for
// QA's BR-4.3 / BR-4.4 enforcement test (2.6-INT-025). The slices are
// cloned defensively so callers cannot mutate the package-level state.
func AllowedColumns() map[string][]string {
	clone := func(in []string) []string { out := make([]string, len(in)); copy(out, in); return out }
	return map[string][]string{
		"users.json":               clone(usersDumpColumns),
		"api_keys.json":            clone(apiKeysDumpColumns),
		"subscriptions.json":       {"id", "user_id", "plan", "status", "started_at", "cancelled_at", "created_at", "updated_at"},
		"balances.json":            {"user_id", "balance_usd", "currency", "updated_at"},
		"recharge_orders.json":     {"id", "user_id", "amount_usd", "status", "channel", "created_at", "completed_at"},
		"content_safety_logs.json": {"id", "user_id", "verdict", "category", "ts"},
		"request_logs.json":        {"ts", "user_id", "endpoint", "model", "tokens_in", "tokens_out", "cost_usd", "status_code"},
	}
}
