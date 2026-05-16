// Story 2.5 AC1 — GetMe handler tests.
//
// QA scenarios covered:
//   - 2.5-UNIT-004 (etag format `"{UnixMicro}"` per Architect Q2)
//   - 2.5-UNIT-005 (FailedPrecondition mapping on pending_deletion)
//   - 2.5-UNIT-006 (Unavailable mapping on DB error)
//   - 2.5-UNIT-001/002/003 paths exercised at the repository layer
package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

// profileRowColumns mirrors the projection of the Story 2.5 getProfileByIDSQL
// (repository.scanProfileRow scans 13 columns: id, email, password_hash,
// email_verified_at, oauth_provider, oauth_subject, locale, timezone,
// totp_enabled, status, created_at, updated_at, display_name).
var profileRowColumns = []string{
	"id", "email", "password_hash", "email_verified_at",
	"oauth_provider", "oauth_subject", "locale", "timezone",
	"totp_enabled", "status", "created_at", "updated_at",
	"display_name",
}

// expectProfileSelect wires the pgxmock expectation for repository.GetProfileByID's
// SELECT. Callers supply the row values (or a zero-row builder to simulate
// ErrUserNotFound).
func expectProfileSelect(t *testing.T, mock pgxmock.PgxConnIface, id uuid.UUID, rows *pgxmock.Rows) {
	t.Helper()
	mock.ExpectQuery(`SELECT .*display_name.* FROM he_api\.users WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(rows)
}

// happyProfileRow builds a populated profile row.
func happyProfileRow(id uuid.UUID, updatedAt time.Time, displayName *string) *pgxmock.Rows {
	verifiedAt := updatedAt.Add(-time.Hour)
	return pgxmock.NewRows(profileRowColumns).AddRow(
		id, "user@example.com", []byte("$2a$12$hash"), &verifiedAt,
		(*string)(nil), (*string)(nil), "en", "UTC",
		false, "active", updatedAt.Add(-time.Hour*24), updatedAt,
		displayName,
	)
}

// Scenario: 2.5-UNIT-004 — GetMe happy path returns hydrated GetMeResponse
// with etag formatted as quoted-string `"{UpdatedAt.UnixMicro()}"`. The
// microsecond precision matches PG TIMESTAMPTZ exactly (Architect Q2).
func TestGetMe_HappyPath_EtagIsUnixMicroQuoted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	// Use a timestamp with non-zero nanoseconds — UnixMicro truncates the
	// trailing 3 digits per PG's stored precision; the test pins the
	// expected etag to the truncated value to catch any future refactor
	// that accidentally uses UnixNano.
	updatedAt := time.Date(2026, 5, 16, 10, 0, 0, 123_456_789, time.UTC)
	displayName := "Alice"

	expectProfileSelect(t, h.mock, id, happyProfileRow(id, updatedAt, &displayName))

	resp, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{
		UserId: id.String(),
	}))
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	wantEtag := fmt.Sprintf(`"%d"`, updatedAt.UnixMicro())
	if resp.Msg.GetEtag() != wantEtag {
		t.Errorf("etag = %q, want %q (Architect Q2 ruling — UnixMicro)", resp.Msg.GetEtag(), wantEtag)
	}
	if resp.Msg.GetEmail() != "user@example.com" {
		t.Errorf("email = %q", resp.Msg.GetEmail())
	}
	if resp.Msg.GetDisplayName() != "Alice" {
		t.Errorf("display_name = %q, want Alice", resp.Msg.GetDisplayName())
	}
	if resp.Msg.GetLocale() != "en" {
		t.Errorf("locale = %q, want en", resp.Msg.GetLocale())
	}
	if resp.Msg.GetTimezone() != "UTC" {
		t.Errorf("timezone = %q, want UTC", resp.Msg.GetTimezone())
	}
	if resp.Msg.GetTotpEnabled() {
		t.Errorf("totp_enabled = true, want false")
	}
}

// Scenario: 2.5-UNIT-004b — NULL display_name surfaces as absent in the
// proto (proto3 optional → nil-valued pointer). The Default getter (Go
// generated) returns empty string for absent — but we want the Has* form
// to flag absence so the client can render the placeholder UX.
func TestGetMe_NullDisplayName_OmitsField(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	updatedAt := time.Date(2026, 5, 16, 11, 0, 0, 0, time.UTC)

	expectProfileSelect(t, h.mock, id, happyProfileRow(id, updatedAt, nil))

	resp, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{
		UserId: id.String(),
	}))
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if resp.Msg.DisplayName != nil {
		t.Errorf("DisplayName = %v, want nil (proto3 optional absent)", resp.Msg.DisplayName)
	}
}

// Scenario: 2.5-UNIT-005 — pending_deletion → CodeFailedPrecondition with
// reason "403_account_pending_deletion" (api-gateway translates to 403).
func TestGetMe_PendingDeletion_ReturnsFailedPrecondition(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	rows := pgxmock.NewRows(profileRowColumns).AddRow(
		id, "u@example.com", []byte{}, &now,
		(*string)(nil), (*string)(nil), "en", "UTC",
		false, "pending_deletion", now, now,
		(*string)(nil),
	)
	expectProfileSelect(t, h.mock, id, rows)

	_, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{
		UserId: id.String(),
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("GetMe pending_deletion: expected connect.Error, got %v", err)
	}
	if ce.Code() != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", ce.Code())
	}
	if ce.Message() != "403_account_pending_deletion" {
		t.Errorf("message = %q, want 403_account_pending_deletion", ce.Message())
	}
}

// Scenario: 2.5-UNIT-006 — DB error → CodeUnavailable (api-gateway → 503).
func TestGetMe_DBError_ReturnsUnavailable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()

	h.mock.ExpectQuery(`SELECT .*display_name.* FROM he_api\.users WHERE id = \$1`).
		WithArgs(id).
		WillReturnError(errors.New("connection refused"))

	_, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{
		UserId: id.String(),
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("GetMe DB error: expected connect.Error, got %v", err)
	}
	if ce.Code() != connect.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", ce.Code())
	}
}

// Scenario: BR-1.1 IDOR defence — non-UUID user_id → InvalidArgument.
func TestGetMe_NonUUID_UserID_ReturnsInvalidArgument(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{
		UserId: "not-a-uuid",
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("GetMe non-UUID: expected connect.Error, got %v", err)
	}
	if ce.Code() != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", ce.Code())
	}
}
