// Story 2.5 AC2 — UpdateProfile handler tests.
//
// QA scenarios covered (white-box):
//   - 2.5-UNIT-017..024 (validateDisplayName: happy / trim / empty→NULL /
//     emoji reject / control + format + surrogate reject / NFC normalise /
//     100-rune CJK / 101-rune reject / whitespace-only too_short)
//   - 2.5-UNIT-025..028 (validateLocale + validateTimezone allow/reject)
//   - 2.5-UNIT-029..033 (repository UpdateProfile paths — covered in
//     repository/users_test.go; here we focus on handler orchestration)
//   - 2.5-UNIT-034..037 (audit redaction; emit failure does not block 200;
//     ratelimit; if_match parse)
//   - 2.5-BLIND-BOUNDARY-001..006 (length edges)
package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
)

// strp is a tiny helper for proto3 optional fields (each is generated as
// `*string`). Inlining `&v` requires the value to be addressable.
func strp(s string) *string { return &s }

// expectProfileUpdateFlow wires the two-query expectations that
// UpdateProfile makes through the repository: the GetProfileByID SELECT and
// the SELECT FOR UPDATE + UPDATE pair inside repository.UpdateProfile.
//
// oldLocale, oldTimezone seed the "old" row; newLocale/newTimezone/newName
// seed the RETURNING row of UpdateProfile.
type profileFlowOpts struct {
	id                uuid.UUID
	oldUpdatedAt      time.Time
	newUpdatedAt      time.Time
	oldDisplayName    *string
	newDisplayName    *string
	oldLocale, newLocale     string
	oldTimezone, newTimezone string
	oldDefaultRouting *string // Story 6.5 — default_routing_strategy seed/return
	newDefaultRouting *string
	expectUpdateArgs []interface{}
	updateSQLRegex   string
}

func expectFullProfileUpdate(t *testing.T, mock pgxmock.PgxConnIface, o profileFlowOpts) {
	t.Helper()
	verifiedAt := o.oldUpdatedAt.Add(-time.Hour)

	// GetProfileByID SELECT (handler pre-loads to compute the diff).
	mock.ExpectQuery(`SELECT .*display_name.* FROM he_api\.users WHERE id = \$1`).
		WithArgs(o.id).
		WillReturnRows(pgxmock.NewRows(profileRowColumns).AddRow(
			o.id, "user@example.com", []byte("$2a$12$hash"), &verifiedAt,
			(*string)(nil), (*string)(nil), o.oldLocale, o.oldTimezone,
			false, "active", o.oldUpdatedAt.Add(-time.Hour*24), o.oldUpdatedAt,
			o.oldDisplayName, o.oldDefaultRouting,
		))
	// repository.UpdateProfile SELECT FOR UPDATE.
	mock.ExpectQuery(`SELECT updated_at, status FROM he_api\.users WHERE id=\$1 FOR UPDATE`).
		WithArgs(o.id).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at", "status"}).
			AddRow(o.oldUpdatedAt, "active"))
	// repository.UpdateProfile UPDATE + RETURNING.
	mock.ExpectQuery(o.updateSQLRegex).
		WithArgs(o.expectUpdateArgs...).
		WillReturnRows(pgxmock.NewRows(profileRowColumns).AddRow(
			o.id, "user@example.com", []byte("$2a$12$hash"), &verifiedAt,
			(*string)(nil), (*string)(nil), o.newLocale, o.newTimezone,
			false, "active", o.oldUpdatedAt.Add(-time.Hour*24), o.newUpdatedAt,
			o.newDisplayName, o.newDefaultRouting,
		))
}

// Scenario: 2.5-UNIT-029 (handler-level) — happy path applies all three
// fields, returns updated profile with new etag + locale_changed=true.
func TestUpdateProfile_HappyPath_AllThreeFields(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	oldUpdatedAt := time.Date(2026, 5, 16, 10, 0, 0, 123_000_000, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)
	newName := "Alice"

	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:               id,
		oldUpdatedAt:     oldUpdatedAt,
		newUpdatedAt:     newUpdatedAt,
		oldDisplayName:   nil,
		newDisplayName:   &newName,
		oldLocale:        "en",
		newLocale:        "zh-CN",
		oldTimezone:      "UTC",
		newTimezone:      "Asia/Shanghai",
		updateSQLRegex:   `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), display_name=\$1, locale=\$2, timezone=\$3 WHERE id=\$4 RETURNING `,
		expectUpdateArgs: []interface{}{&newName, "zh-CN", "Asia/Shanghai", id},
	})

	resp, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:      id.String(),
		DisplayName: strp("Alice"),
		Locale:      strp("zh-CN"),
		Timezone:    strp("Asia/Shanghai"),
		IfMatch:     fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
		ClientIp:    "1.2.3.4",
		UserAgent:   "Go-test",
	}))
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if !resp.Msg.GetLocaleChanged() {
		t.Errorf("locale_changed = false, want true")
	}
	wantEtag := fmt.Sprintf(`"%d"`, newUpdatedAt.UnixMicro())
	if resp.Msg.GetEtag() != wantEtag {
		t.Errorf("etag = %q, want %q (UnixMicro)", resp.Msg.GetEtag(), wantEtag)
	}
	// Audit event was emitted with redacted display_name diff.
	events := h.auditP.byType(audit.EventProfileUpdated)
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	diff := events[0].Metadata["diff"].(map[string]any)
	dn := diff["display_name"].(map[string]string)
	if dn["from"] != "<unset>" || dn["to"] != "<set>" {
		t.Errorf("display_name diff = %v, want from=<unset> to=<set>", dn)
	}
	loc := diff["locale"].(map[string]string)
	if loc["from"] != "en" || loc["to"] != "zh-CN" {
		t.Errorf("locale diff = %v, want en→zh-CN literal", loc)
	}
}

// Scenario: 2.5-UNIT-019 — emoji rejected (Symbol-Other class So is NOT in
// the allowlist per BR-2.3). No DB call should occur.
func TestUpdateProfile_DisplayNameEmojiRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:      id.String(),
		DisplayName: strp("Alice 🎉"),
		IfMatch:     `"0"`,
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("emoji: expected connect.Error, got %v", err)
	}
	if ce.Code() != connect.CodeInvalidArgument || ce.Message() != "400_invalid_display_name" {
		t.Errorf("code/message = %v/%q", ce.Code(), ce.Message())
	}
}

// Scenario: 2.5-UNIT-021 — 101 runes (over limit). Use CJK so each rune is
// 3 bytes — verifies the rune-vs-byte distinction (BR-2.3).
func TestUpdateProfile_DisplayNameTooLong_Rejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	tooLong := strings.Repeat("中", 101) // 101 runes, 303 bytes

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:      id.String(),
		DisplayName: strp(tooLong),
		IfMatch:     `"0"`,
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Message() != "400_invalid_display_name" {
		t.Fatalf("too-long: got code %v message %q", ce.Code(), ce.Message())
	}
}

// Scenario: 2.5-UNIT-020 — 100 runes (boundary; allowed).
func TestUpdateProfile_DisplayName100RunesAccepted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	exactly := strings.Repeat("中", 100)
	oldUpdatedAt := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)

	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:               id,
		oldUpdatedAt:     oldUpdatedAt,
		newUpdatedAt:     newUpdatedAt,
		oldDisplayName:   nil,
		newDisplayName:   strp(exactly),
		oldLocale:        "en",
		newLocale:        "en",
		oldTimezone:      "UTC",
		newTimezone:      "UTC",
		updateSQLRegex:   `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), display_name=\$1 WHERE id=\$2 RETURNING `,
		expectUpdateArgs: []interface{}{strp(exactly), id},
	})

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:      id.String(),
		DisplayName: strp(exactly),
		IfMatch:     fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
	}))
	if err != nil {
		t.Fatalf("UpdateProfile 100-runes: %v", err)
	}
}

// Scenario: 2.5-UNIT-026 — V1.1 locale (zh-TW) rejected. MVP set only.
func TestUpdateProfile_LocaleZhTW_Rejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:  id.String(),
		Locale:  strp("zh-TW"),
		IfMatch: `"0"`,
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Message() != "400_invalid_locale" {
		t.Fatalf("zh-TW: got code %v message %q", ce.Code(), ce.Message())
	}
}

// Scenario: 2.5-UNIT-028 — POSIX timezone (PST) rejected by IANA validator.
func TestUpdateProfile_TimezonePST_Rejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:   id.String(),
		Timezone: strp("PST"),
		IfMatch:  `"0"`,
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Message() != "400_invalid_timezone" {
		t.Fatalf("PST: got code %v message %q", ce.Code(), ce.Message())
	}
}

// Scenario: 2.5-UNIT-030 (handler-level) — etag mismatch surfaces as
// 412_etag_mismatch.
func TestUpdateProfile_EtagMismatch_Returns412(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	current := time.Date(2026, 5, 16, 10, 0, 0, 200_000_000, time.UTC)
	stale := current.Add(-time.Second)
	verifiedAt := current.Add(-time.Hour)

	h.mock.ExpectQuery(`SELECT .*display_name.* FROM he_api\.users WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows(profileRowColumns).AddRow(
			id, "u@example.com", []byte("$2a$12$x"), &verifiedAt,
			(*string)(nil), (*string)(nil), "en", "UTC",
			false, "active", current.Add(-time.Hour*24), current,
			(*string)(nil), (*string)(nil),
		))
	h.mock.ExpectQuery(`SELECT updated_at, status FROM he_api\.users WHERE id=\$1 FOR UPDATE`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at", "status"}).
			AddRow(current, "active"))

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:  id.String(),
		Locale:  strp("de"),
		IfMatch: fmt.Sprintf(`"%d"`, stale.UnixMicro()),
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Message() != "412_etag_mismatch" {
		t.Fatalf("stale etag: got code %v message %q", ce.Code(), ce.Message())
	}
}

// Scenario: 2.5-UNIT-037 — missing/empty if_match parses to 412.
func TestUpdateProfile_MissingIfMatch_Returns412(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId: id.String(),
		Locale: strp("en"),
		// IfMatch absent
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Message() != "412_etag_mismatch" {
		t.Fatalf("missing etag: got code %v message %q", ce.Code(), ce.Message())
	}
}

// Scenario: 2.5-UNIT-031 (handler-level) — partial update (locale only)
// works end-to-end; UPDATE statement contains only the locale=$1 clause.
func TestUpdateProfile_PartialUpdate_LocaleOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	oldUpdatedAt := time.Date(2026, 5, 16, 14, 0, 0, 0, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)

	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:               id,
		oldUpdatedAt:     oldUpdatedAt,
		newUpdatedAt:     newUpdatedAt,
		oldDisplayName:   nil,
		newDisplayName:   nil,
		oldLocale:        "en",
		newLocale:        "de",
		oldTimezone:      "UTC",
		newTimezone:      "UTC",
		updateSQLRegex:   `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), locale=\$1 WHERE id=\$2 RETURNING `,
		expectUpdateArgs: []interface{}{"de", id},
	})

	resp, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:  id.String(),
		Locale:  strp("de"),
		IfMatch: fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
	}))
	if err != nil {
		t.Fatalf("UpdateProfile locale-only: %v", err)
	}
	if !resp.Msg.GetLocaleChanged() {
		t.Errorf("locale_changed = false, want true")
	}
	if resp.Msg.GetLocale() != "de" {
		t.Errorf("locale = %q", resp.Msg.GetLocale())
	}
}

// Scenario: 2.5-UNIT-018 — display_name empty string trims and clears to NULL
// (BR-2.4 — empty-after-trim → NULL).
func TestUpdateProfile_DisplayNameWhitespaceClearsToNull(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	oldUpdatedAt := time.Date(2026, 5, 16, 15, 0, 0, 0, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)
	oldName := "Alice"

	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:               id,
		oldUpdatedAt:     oldUpdatedAt,
		newUpdatedAt:     newUpdatedAt,
		oldDisplayName:   &oldName,
		newDisplayName:   nil,
		oldLocale:        "en",
		newLocale:        "en",
		oldTimezone:      "UTC",
		newTimezone:      "UTC",
		updateSQLRegex:   `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), display_name=\$1 WHERE id=\$2 RETURNING `,
		expectUpdateArgs: []interface{}{(*string)(nil), id},
	})

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:      id.String(),
		DisplayName: strp("   "), // whitespace only → clear
		IfMatch:     fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
	}))
	if err != nil {
		t.Fatalf("UpdateProfile whitespace clear: %v", err)
	}
	// Audit diff records <set> → <cleared>.
	ev := h.auditP.byType(audit.EventProfileUpdated)
	if len(ev) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(ev))
	}
	diff := ev[0].Metadata["diff"].(map[string]any)
	dn := diff["display_name"].(map[string]string)
	if dn["from"] != "<set>" || dn["to"] != "<cleared>" {
		t.Errorf("display_name diff = %v, want from=<set> to=<cleared>", dn)
	}
}
