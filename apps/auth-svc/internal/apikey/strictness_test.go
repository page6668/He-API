// Story 8.4 — auth-svc UpdateApiKey / ValidateApiKey content_safety_strictness
// persistence + read-back (8.4-UNIT-001..005 + defence-in-depth enum reject).
// See docs/qa/assessments/8.4-test-design-20260611.md.
package apikey

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

func strPtr(s string) *string { return &s }

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// 8.4-UNIT-001 — UpdateApiKey{content_safety_strictness:"loose"} persists the
// level via UpdateAPIKeyConfig, fires the EXISTING config_updated sentinel + the
// audit event (changed_fields carries content_safety_strictness), and echoes the
// level on the read-back (BR-1.3 / BR-1.5).
func TestUpdateApiKey_Strictness_PersistsSentinelAudit(t *testing.T) {
	userID := uuid.New()
	apiKeyID := uuid.New()

	var gotStrictness string
	repo := &fakeRepo{
		selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
			r := baseRow(apiKeyID, userID, `{}`)
			r.ContentSafetyStrictness = "strict" // existing value
			return r, nil
		},
		updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error) {
			gotStrictness = strictness
			r := baseRow(apiKeyID, userID, string(scope))
			r.ContentSafetyStrictness = strictness
			return r, nil
		},
	}
	svc, auditPub, sentinel := newUpdateService(t, repo)

	resp, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
		UserId:                  userID.String(),
		ApiKeyId:                apiKeyID.String(),
		ContentSafetyStrictness: strPtr("loose"),
	})
	if err != nil {
		t.Fatalf("UpdateApiKey err=%v want nil", err)
	}
	if gotStrictness != "loose" {
		t.Fatalf("persisted strictness=%q want loose", gotStrictness)
	}
	if sentinel.hitsCount() != 1 {
		t.Fatalf("config sentinel hits=%d want 1 (reused 5.2 sentinel)", sentinel.hitsCount())
	}
	cf, _ := auditPub.last().Metadata["changed_fields"].([]string)
	if !containsStr(cf, "content_safety_strictness") {
		t.Fatalf("changed_fields=%v missing content_safety_strictness", cf)
	}
	if resp.GetContentSafetyStrictness() != "loose" {
		t.Fatalf("read-back strictness=%q want loose", resp.GetContentSafetyStrictness())
	}
}

// 8.4-UNIT-002 — present-only: a cap-only patch PRESERVES the existing strictness
// (UpdateAPIKeyConfig receives the row's current value) and does NOT list it in
// changed_fields (mirrors the cap/scope present-arm semantics, BR-1.2).
func TestUpdateApiKey_Strictness_PresentOnlyPreserved(t *testing.T) {
	userID := uuid.New()
	apiKeyID := uuid.New()

	var gotStrictness string
	repo := &fakeRepo{
		selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
			r := baseRow(apiKeyID, userID, `{}`)
			r.ContentSafetyStrictness = "default" // existing value to be preserved
			return r, nil
		},
		updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error) {
			gotStrictness = strictness
			r := baseRow(apiKeyID, userID, string(scope))
			r.ContentSafetyStrictness = strictness
			return r, nil
		},
	}
	svc, auditPub, _ := newUpdateService(t, repo)

	capStr := "50.00"
	if _, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
		UserId:            userID.String(),
		ApiKeyId:          apiKeyID.String(),
		MonthlyCostCapUsd: &capStr,
	}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if gotStrictness != "default" {
		t.Fatalf("strictness=%q want preserved 'default' (cap-only patch must not change it)", gotStrictness)
	}
	if cf, _ := auditPub.last().Metadata["changed_fields"].([]string); containsStr(cf, "content_safety_strictness") {
		t.Fatalf("changed_fields=%v must NOT list content_safety_strictness on a cap-only patch", cf)
	}
}

// 8.4-UNIT-003 — a patch with ONLY content_safety_strictness satisfies the
// ≥1-field gate and mutates the column (BR-1.3).
func TestUpdateApiKey_Strictness_OnlyFieldSatisfiesGate(t *testing.T) {
	userID := uuid.New()
	apiKeyID := uuid.New()

	called := false
	repo := &fakeRepo{
		selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
			r := baseRow(apiKeyID, userID, `{}`)
			r.ContentSafetyStrictness = "strict"
			return r, nil
		},
		updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error) {
			called = true
			r := baseRow(apiKeyID, userID, string(scope))
			r.ContentSafetyStrictness = strictness
			return r, nil
		},
	}
	svc, auditPub, _ := newUpdateService(t, repo)

	if _, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
		UserId:                  userID.String(),
		ApiKeyId:                apiKeyID.String(),
		ContentSafetyStrictness: strPtr("default"),
	}); err != nil {
		t.Fatalf("only-strictness patch err=%v want nil (must satisfy ≥1-field gate)", err)
	}
	if !called {
		t.Fatal("UpdateAPIKeyConfig not invoked for a strictness-only patch")
	}
	cf, _ := auditPub.last().Metadata["changed_fields"].([]string)
	if len(cf) != 1 || cf[0] != "content_safety_strictness" {
		t.Fatalf("changed_fields=%v want [content_safety_strictness]", cf)
	}
}

// 8.4-UNIT (defence-in-depth) — an invalid strictness token is rejected with
// InvalidArgument BEFORE the SELECT/UPDATE (the gateway already validated, the DB
// CHECK is a third layer). BR-1.4.
func TestUpdateApiKey_Strictness_InvalidTokenRejected(t *testing.T) {
	userID := uuid.New()
	apiKeyID := uuid.New()

	selected := false
	repo := &fakeRepo{
		selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
			selected = true
			return baseRow(apiKeyID, userID, `{}`), nil
		},
	}
	svc, _, _ := newUpdateService(t, repo)

	_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
		UserId:                  userID.String(),
		ApiKeyId:                apiKeyID.String(),
		ContentSafetyStrictness: strPtr("off"),
	})
	if err == nil {
		t.Fatal("invalid strictness token must be rejected")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code=%v want InvalidArgument", connect.CodeOf(err))
	}
	if selected {
		t.Fatal("invalid token must be rejected BEFORE the SELECT (no DB side-effect)")
	}
}

// 8.4-UNIT-004 — ValidateApiKey carries the column on the hot path so the gateway
// gates the filter from the bearer cache (NOT NULL → always populated). BR-1.5.
func TestValidate_CarriesStrictness(t *testing.T) {
	plaintext := canonicalKey()
	hash := bcryptCost4(t, plaintext)
	apiKeyID := uuid.New()
	userID := uuid.New()

	repo := &fakeRepo{
		lookup: func(_ context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return []repository.ApiKeyRow{{
				ID:                      apiKeyID,
				UserID:                  userID,
				KeyPrefix:               prefix,
				KeyHash:                 hash,
				Scope:                   []byte(`{}`),
				ContentSafetyStrictness: "loose",
			}}, nil
		},
		touchDone: make(chan struct{}, 1),
	}
	svc, _, _ := newTestService(t, repo)

	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !resp.GetOk() {
		t.Fatal("ok=false want true")
	}
	if resp.GetContentSafetyStrictness() != "loose" {
		t.Fatalf("ValidateApiKeyResponse.content_safety_strictness=%q want loose", resp.GetContentSafetyStrictness())
	}
}
