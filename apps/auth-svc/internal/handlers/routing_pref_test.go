// Story 6.5 — auth-svc UpdateProfile default_routing_strategy validation,
// clear semantics, partial-update, write-through cache, and GetMe surfacing.
package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

// Scenario: 6.5-UNIT-009 + INT-005 — "cost" accepted, persisted, AND
// write-through to user:routing_pref:{uid} + sentinel user:pref_updated:{uid}.
func TestUpdateProfile_DefaultRoutingStrategy_AcceptedAndWriteThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	oldUpdatedAt := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)
	val := "cost"

	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:                id,
		oldUpdatedAt:      oldUpdatedAt,
		newUpdatedAt:      newUpdatedAt,
		oldLocale:         "en",
		newLocale:         "en",
		oldTimezone:       "UTC",
		newTimezone:       "UTC",
		newDefaultRouting: &val,
		updateSQLRegex:    `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), default_routing_strategy=\$1 WHERE id=\$2 RETURNING `,
		expectUpdateArgs:  []interface{}{&val, id},
	})

	resp, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:                 id.String(),
		DefaultRoutingStrategy: strp("cost"),
		IfMatch:                fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
	}))
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if resp.Msg.GetDefaultRoutingStrategy() != "cost" {
		t.Errorf("response default_routing_strategy = %q, want cost", resp.Msg.GetDefaultRoutingStrategy())
	}
	// Write-through value + sentinel present (auth-svc-owned keys — NOT entitlement:*).
	if got, _ := h.mr.Get("user:routing_pref:" + id.String()); got != "cost" {
		t.Errorf("cache user:routing_pref = %q, want cost", got)
	}
	if !h.mr.Exists("user:pref_updated:" + id.String()) {
		t.Errorf("invalidation sentinel user:pref_updated missing")
	}
}

// Scenario: 6.5-UNIT-010 / BLIND-BOUNDARY-001 — unknown value rejected with
// 400_invalid_default_routing_strategy and NO DB write (no pgx expectations).
func TestUpdateProfile_DefaultRoutingStrategy_UnknownRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()

	for _, bad := range []string{"fastest", "QUALITY", "Cost"} {
		_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
			UserId:                 id.String(),
			DefaultRoutingStrategy: strp(bad),
			IfMatch:                `"0"`,
		}))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument ||
			ce.Message() != "400_invalid_default_routing_strategy" {
			t.Fatalf("value %q: got code %v message %q, want 400_invalid_default_routing_strategy", bad, codeOf(ce), msgOf(ce))
		}
	}
	// No write-through on a rejected request.
	if h.mr.Exists("user:pref_updated:" + id.String()) {
		t.Errorf("sentinel set on a rejected request")
	}
}

// Scenario: 6.5-UNIT-011 — explicit "" clears to NULL (the gateway maps HTTP
// null → proto "" ); the SET clause binds SQL NULL and the cache stores "".
func TestUpdateProfile_DefaultRoutingStrategy_ClearToNull(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	oldUpdatedAt := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)
	oldVal := "quality"

	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:                id,
		oldUpdatedAt:      oldUpdatedAt,
		newUpdatedAt:      newUpdatedAt,
		oldLocale:         "en",
		newLocale:         "en",
		oldTimezone:       "UTC",
		newTimezone:       "UTC",
		oldDefaultRouting: &oldVal,
		newDefaultRouting: nil, // cleared
		updateSQLRegex:    `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), default_routing_strategy=\$1 WHERE id=\$2 RETURNING `,
		expectUpdateArgs:  []interface{}{(*string)(nil), id},
	})

	resp, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:                 id.String(),
		DefaultRoutingStrategy: strp(""), // clear
		IfMatch:                fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
	}))
	if err != nil {
		t.Fatalf("UpdateProfile clear: %v", err)
	}
	if resp.Msg.DefaultRoutingStrategy != nil {
		t.Errorf("response default_routing_strategy = %v, want nil (cleared)", resp.Msg.DefaultRoutingStrategy)
	}
	if got, _ := h.mr.Get("user:routing_pref:" + id.String()); got != "" {
		t.Errorf("cache after clear = %q, want empty", got)
	}
}

// Scenario: 6.5-UNIT-012 — field omitted → existing value unchanged; the SET
// clause must NOT mention default_routing_strategy and NO write-through fires.
func TestUpdateProfile_DefaultRoutingStrategy_OmittedLeavesUnchanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	oldUpdatedAt := time.Date(2026, 6, 16, 13, 0, 0, 0, time.UTC)
	newUpdatedAt := oldUpdatedAt.Add(time.Second)

	// Only locale participates — the UPDATE regex pins locale=$1 with NO
	// default_routing_strategy clause.
	expectFullProfileUpdate(t, h.mock, profileFlowOpts{
		id:               id,
		oldUpdatedAt:     oldUpdatedAt,
		newUpdatedAt:     newUpdatedAt,
		oldLocale:        "en",
		newLocale:        "de",
		oldTimezone:      "UTC",
		newTimezone:      "UTC",
		updateSQLRegex:   `(?s)UPDATE he_api\.users SET updated_at=NOW\(\), locale=\$1 WHERE id=\$2 RETURNING `,
		expectUpdateArgs: []interface{}{"de", id},
	})

	_, err := h.srv.UpdateProfile(context.Background(), connect.NewRequest(&authv1.UpdateProfileRequest{
		UserId:  id.String(),
		Locale:  strp("de"),
		IfMatch: fmt.Sprintf(`"%d"`, oldUpdatedAt.UnixMicro()),
	}))
	if err != nil {
		t.Fatalf("UpdateProfile locale-only: %v", err)
	}
	// No routing field touched → no write-through.
	if h.mr.Exists("user:pref_updated:" + id.String()) {
		t.Errorf("sentinel set when default_routing_strategy was omitted")
	}
}

// Scenario: 6.5-UNIT-015 — GetMe returns default_routing_strategy (always
// present in the proto when set; nullable when absent).
func TestGetMe_DefaultRoutingStrategy_Surfaced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	updatedAt := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	val := "latency"

	expectProfileSelect(t, h.mock, id, happyProfileRowWithRouting(id, updatedAt, nil, &val))

	resp, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{UserId: id.String()}))
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if resp.Msg.GetDefaultRoutingStrategy() != "latency" {
		t.Errorf("GetMe default_routing_strategy = %q, want latency", resp.Msg.GetDefaultRoutingStrategy())
	}

	// And nil when no default set.
	expectProfileSelect(t, h.mock, id, happyProfileRowWithRouting(id, updatedAt, nil, nil))
	resp2, err := h.srv.GetMe(context.Background(), connect.NewRequest(&authv1.GetMeRequest{UserId: id.String()}))
	if err != nil {
		t.Fatalf("GetMe(nil default): %v", err)
	}
	if resp2.Msg.DefaultRoutingStrategy != nil {
		t.Errorf("DefaultRoutingStrategy = %v, want nil (no default)", resp2.Msg.DefaultRoutingStrategy)
	}
}

// codeOf / msgOf are nil-safe accessors so a failed errors.As doesn't panic the
// format args in the assertions above.
func codeOf(ce *connect.Error) connect.Code {
	if ce == nil {
		return connect.CodeUnknown
	}
	return ce.Code()
}

func msgOf(ce *connect.Error) string {
	if ce == nil {
		return "<nil>"
	}
	return ce.Message()
}
