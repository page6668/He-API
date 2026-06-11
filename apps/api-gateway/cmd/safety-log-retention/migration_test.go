package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// migrationPath resolves 0014 relative to this test file (cmd/safety-log-retention
// → repo root → migrations/postgres).
const migrationPath = "../../../../migrations/postgres/0014_create_content_safety_logs.sql"

func readMigration(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(migrationPath))
	if err != nil {
		t.Fatalf("read migration 0014: %v", err)
	}
	return string(b)
}

// ddlOnly strips `--` line comments so the no-FK / no-cascade assertions inspect
// the executable DDL, not the explanatory prose (which deliberately NAMES the
// FK/CASCADE it omits).
func ddlOnly(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// 8.5-MIG-001 / 8.5-BLIND-DATA-001 (HARD) — GDPR-EXEMPTION mechanism: the
// content_safety_logs.user_id column carries NO `REFERENCES he_api.users(id)` FK,
// so a `DELETE FROM users` cannot cascade-purge the compliance log (§9.3 合规优先
// 于个人删除). This is the regression LOCK against a future "tidy up the FK"
// refactor that would silently re-introduce a cascade and breach 备案 retention.
func Test8_5_MIG001_NoUserForeignKey(t *testing.T) {
	raw := readMigration(t)
	if !strings.Contains(raw, "CREATE TABLE he_api.content_safety_logs") {
		t.Fatal("0014 must CREATE he_api.content_safety_logs")
	}
	ddl := ddlOnly(raw) // inspect executable DDL, not the explanatory comments
	if regexp.MustCompile(`(?i)REFERENCES\s+he_api\.users`).MatchString(ddl) {
		t.Fatalf("content_safety_logs must NOT reference he_api.users (GDPR-exemption breached): %s", ddl)
	}
	if regexp.MustCompile(`(?i)ON\s+DELETE\s+CASCADE`).MatchString(ddl) {
		t.Fatal("content_safety_logs must NOT carry ON DELETE CASCADE (would purge logs on user deletion)")
	}
	// user_id is still NOT NULL (a row always attributes to a user).
	if !regexp.MustCompile(`(?i)user_id\s+UUID\s+NOT\s+NULL`).MatchString(ddl) {
		t.Fatal("user_id must be UUID NOT NULL")
	}
}

// 8.5-BR-1.1 — both indexes (the pre-declared per-user read path + the NEW
// retention range index) are present; the OQ-8.5-7 strictness column ships.
func Test8_5_MIG_IndexesAndStrictnessColumn(t *testing.T) {
	sql := readMigration(t)
	for _, want := range []string{
		"idx_safety_logs_user_time",
		"idx_safety_logs_created_at", // retention sweep (BR-1.1)
		"strictness",                 // OQ-8.5-7 attribution column
		"excerpt_redacted",
		"CHECK (direction IN ('input', 'output'))",
		"CHECK (action IN ('blocked', 'warned'))",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("0014 missing %q", want)
		}
	}
}

// 8.5-BR-4.4 — forward-only convention: NO paired .down.sql (atlas dynamic down).
func Test8_5_MIG_ForwardOnlyNoDownFile(t *testing.T) {
	down := strings.TrimSuffix(filepath.Clean(migrationPath), ".sql") + ".down.sql"
	if _, err := os.Stat(down); err == nil {
		t.Fatalf("forward-only convention violated: %s should not exist", down)
	}
}
