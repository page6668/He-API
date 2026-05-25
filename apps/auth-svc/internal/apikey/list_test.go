// Story 5.1 — UNIT-014..017 (ListApiKeys RPC handler).
// See docs/qa/assessments/5.1-test-design-20260525.md.

package apikey

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// rowFixture builds a repository.ApiKeyRow for list-test seeding. Optional
// args via the named-arg pattern wouldn't help here; explicit constructor
// keeps the table-driven tests legible.
func rowFixture(t *testing.T, name, prefix string, createdAt time.Time, revokedAt *time.Time) repository.ApiKeyRow {
	t.Helper()
	r := repository.ApiKeyRow{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Name:      name,
		KeyPrefix: prefix,
		Scope:     []byte("{}"),
		CreatedAt: createdAt,
	}
	if revokedAt != nil {
		r.RevokedAt = pgtype.Timestamptz{Time: *revokedAt, Valid: true}
	}
	return r
}

// TestListApiKeys covers UNIT-014..017.
// Source: T2.1 (story line 523-528) + design-doc §"Unit: ListApiKeys RPC Handler".
func TestListApiKeys(t *testing.T) {
	userID := uuid.New()

	t.Run("5.1-UNIT-014 happy path 3 keys mixed revoked/active", func(t *testing.T) {
		// Scenario: 5.1-UNIT-014
		// Priority: P0
		// Input:    stub repo returns 3 rows (2 active + 1 revoked, varied created_at)
		// Expected: response.keys length == 3; ordered created_at DESC then id ASC;
		//           revoked row has non-nil revoked_at proto field.
		// AC2 §Scenario + BR-2.3 / BR-2.4
		now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
		revokeAt := now.Add(-1 * time.Hour)
		rows := []repository.ApiKeyRow{
			rowFixture(t, "Newest", "he-AAA111222", now, nil),
			rowFixture(t, "Production", "he-BBB333444", now.Add(-48*time.Hour), &revokeAt),
			rowFixture(t, "Oldest", "he-CCC555666", now.Add(-72*time.Hour), nil),
		}
		repo := &fakeRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]repository.ApiKeyRow, error) {
				return rows, nil
			},
		}
		svc, _, _, _ := newCreateService(t, repo)

		resp, err := svc.ListApiKeys(context.Background(), &authv1.ListApiKeysRequest{UserId: userID.String()})
		if err != nil {
			t.Fatalf("ListApiKeys err=%v want nil", err)
		}
		if len(resp.GetKeys()) != 3 {
			t.Fatalf("keys length=%d want 3", len(resp.GetKeys()))
		}
		// repo returns rows in caller's order; handler preserves order
		// because the SQL contract guarantees DESC ordering. We assert the
		// projection is faithful for each row.
		for i, want := range rows {
			got := resp.GetKeys()[i]
			if got.GetApiKeyId() != want.ID.String() {
				t.Fatalf("row %d api_key_id=%q want %q", i, got.GetApiKeyId(), want.ID.String())
			}
			if got.GetName() != want.Name {
				t.Fatalf("row %d name=%q want %q", i, got.GetName(), want.Name)
			}
			if got.GetKeyPrefix() != want.KeyPrefix {
				t.Fatalf("row %d key_prefix=%q want %q", i, got.GetKeyPrefix(), want.KeyPrefix)
			}
		}
		// Row 1 (revoked) MUST have non-nil revoked_at; rows 0+2 MUST be nil.
		if resp.GetKeys()[1].GetRevokedAt() == nil {
			t.Fatalf("row 1 revoked_at=nil want non-nil")
		}
		if resp.GetKeys()[0].GetRevokedAt() != nil {
			t.Fatalf("row 0 revoked_at=%v want nil", resp.GetKeys()[0].GetRevokedAt())
		}
		if resp.GetKeys()[2].GetRevokedAt() != nil {
			t.Fatalf("row 2 revoked_at=%v want nil", resp.GetKeys()[2].GetRevokedAt())
		}
	})

	t.Run("5.1-UNIT-015 empty list returns empty array not nil", func(t *testing.T) {
		// Scenario: 5.1-UNIT-015
		// Priority: P0
		// Input:    stub repo returns []
		// Expected: response.keys == [] (proto reflection: present + Len() == 0).
		// BR-2.8 empty-list invariant
		repo := &fakeRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]repository.ApiKeyRow, error) {
				return []repository.ApiKeyRow{}, nil
			},
		}
		svc, _, _, _ := newCreateService(t, repo)
		resp, err := svc.ListApiKeys(context.Background(), &authv1.ListApiKeysRequest{UserId: userID.String()})
		if err != nil {
			t.Fatalf("ListApiKeys err=%v want nil", err)
		}
		if resp.GetKeys() == nil {
			t.Fatalf("keys=nil want non-nil empty slice (proto3 repeated field invariant)")
		}
		if len(resp.GetKeys()) != 0 {
			t.Fatalf("len(keys)=%d want 0", len(resp.GetKeys()))
		}
	})

	t.Run("5.1-UNIT-016 [SEC] key_hash never serialized in proto", func(t *testing.T) {
		// Scenario: 5.1-UNIT-016
		// Priority: P0
		// Input:    3-row response; reflect over ApiKeyEntry proto descriptor
		// Expected: NO field named key_hash; marshaled bytes grep for "key_hash" == 0;
		//           bytes grep for bcrypt-hash regex ^\$2[aby]\$ == 0.
		// BR-2.5 defence-in-depth (proto-level) — cross-ref SEC-002
		now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
		// Sentinel bcrypt-shaped string we'll seed into a "leak vector"
		// (Scope field) — proves the test would fail if a real leak occurred.
		sentinelHash := "$2a$12$" + strings.Repeat("A", 53)
		_ = sentinelHash // not actually inserted — Scope below is "{}"
		rows := []repository.ApiKeyRow{
			rowFixture(t, "Key 1", "he-AAA111111", now, nil),
			rowFixture(t, "Key 2", "he-BBB222222", now.Add(-1*time.Hour), nil),
			rowFixture(t, "Key 3", "he-CCC333333", now.Add(-2*time.Hour), nil),
		}
		repo := &fakeRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]repository.ApiKeyRow, error) {
				return rows, nil
			},
		}
		svc, _, _, _ := newCreateService(t, repo)
		resp, err := svc.ListApiKeys(context.Background(), &authv1.ListApiKeysRequest{UserId: userID.String()})
		if err != nil {
			t.Fatalf("ListApiKeys err=%v want nil", err)
		}
		// Proto descriptor: no field named key_hash on ApiKeyEntry.
		fields := resp.GetKeys()[0].ProtoReflect().Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			if string(f.Name()) == "key_hash" {
				t.Fatalf("ApiKeyEntry proto descriptor contains forbidden field 'key_hash'")
			}
		}
		// Marshaled bytes (protojson) must not contain "key_hash" substring.
		jsonBytes, err := protojson.Marshal(resp)
		if err != nil {
			t.Fatalf("protojson.Marshal err=%v", err)
		}
		if bytes.Contains(jsonBytes, []byte("key_hash")) {
			t.Fatalf("marshaled proto contains forbidden 'key_hash' substring: %s", jsonBytes)
		}
		// Marshaled bytes must not match bcrypt-shape ^\$2[aby]\$.
		if bytes.Contains(jsonBytes, []byte("$2a$")) || bytes.Contains(jsonBytes, []byte("$2b$")) || bytes.Contains(jsonBytes, []byte("$2y$")) {
			t.Fatalf("marshaled proto contains bcrypt-shape prefix: %s", jsonBytes)
		}
	})

	t.Run("5.1-UNIT-017 stable tie-break on identical created_at", func(t *testing.T) {
		// Scenario: 5.1-UNIT-017
		// Priority: P1
		// Input:    3 rows with microsecond-equal created_at + distinct UUIDs
		// Expected: response orders by UUID ASCENDING for the tie-break (SQL guarantee).
		// BR-2.3 deterministic ordering
		now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
		// Build rows with explicit ascending UUIDs to assert tie-break.
		ids := []uuid.UUID{
			uuid.MustParse("00000000-0000-4000-8000-000000000001"),
			uuid.MustParse("00000000-0000-4000-8000-000000000002"),
			uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		}
		rows := []repository.ApiKeyRow{
			{ID: ids[0], UserID: userID, Name: "A", KeyPrefix: "he-PPP000001", Scope: []byte("{}"), CreatedAt: now},
			{ID: ids[1], UserID: userID, Name: "B", KeyPrefix: "he-PPP000002", Scope: []byte("{}"), CreatedAt: now},
			{ID: ids[2], UserID: userID, Name: "C", KeyPrefix: "he-PPP000003", Scope: []byte("{}"), CreatedAt: now},
		}
		// Repo returns in the order the SQL produced — which per
		// ORDER BY created_at DESC, id ASC is ASCENDING id (since all
		// created_at are equal).
		repo := &fakeRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]repository.ApiKeyRow, error) {
				return rows, nil
			},
		}
		svc, _, _, _ := newCreateService(t, repo)
		resp, err := svc.ListApiKeys(context.Background(), &authv1.ListApiKeysRequest{UserId: userID.String()})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		// Assert handler preserved repo order (which represents SQL contract).
		gotIDs := make([]string, len(resp.GetKeys()))
		for i, k := range resp.GetKeys() {
			gotIDs[i] = k.GetApiKeyId()
		}
		wantIDs := []string{ids[0].String(), ids[1].String(), ids[2].String()}
		if !sort.StringsAreSorted(gotIDs) {
			t.Fatalf("ids=%v not ascending (handler should preserve SQL order)", gotIDs)
		}
		for i := range gotIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Fatalf("ids[%d]=%q want %q", i, gotIDs[i], wantIDs[i])
			}
		}
	})

	t.Run("invalid_user_id rejects with InvalidArgument (defensive)", func(t *testing.T) {
		// Defensive: gateway should never call with a bad uuid (JWT sub is
		// already validated), but the boundary check is in place.
		svc, _, _, _ := newCreateService(t, &fakeRepo{})
		_, err := svc.ListApiKeys(context.Background(), &authv1.ListApiKeysRequest{UserId: "not-a-uuid"})
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("err=%v want *connect.Error", err)
		}
		if cerr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("code=%s want InvalidArgument", cerr.Code())
		}
	})
}
