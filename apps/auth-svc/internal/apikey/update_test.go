// Story 5.2 — UNIT-010..025 (UpdateApiKey RPC handler + config sentinel).
// See docs/qa/assessments/5.2-test-design-20260603.md.

package apikey

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// stubConfigSentinel is the ConfigSentinelStore stand-in (Story 5.2 BR-1.9).
type stubConfigSentinel struct {
	mu        sync.Mutex
	hits      int
	failOnSet error
}

func (s *stubConfigSentinel) SetConfigUpdatedSentinel(_ context.Context, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	return s.failOnSet
}

func (s *stubConfigSentinel) hitsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// newUpdateService wires a Service for the UpdateApiKey tests: fake repo +
// recording audit pub + config sentinel stub. Returns the wired pieces so
// tests assert side-effect counts.
func newUpdateService(t *testing.T, repo Repository) (*Service, *recordingAuditPub, *stubConfigSentinel) {
	t.Helper()
	svc, _, _ := newTestService(t, repo)
	auditPub := &recordingAuditPub{}
	cfgSentinel := &stubConfigSentinel{}
	svc.Audit = auditPub
	svc.ConfigSentinel = cfgSentinel
	svc.Clock = func() time.Time { return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) }
	return svc, auditPub, cfgSentinel
}

// numericFromString is a test helper producing a pgtype.Numeric from a
// decimal string (mirrors the production parse path).
func numericFromString(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("numericFromString(%q): %v", s, err)
	}
	return n
}

// baseRow returns an un-revoked, owned api_keys row with the given scope JSON.
func baseRow(apiKeyID, userID uuid.UUID, scope string) repository.ApiKeyRow {
	return repository.ApiKeyRow{
		ID:        apiKeyID,
		UserID:    userID,
		Name:      "Production",
		KeyPrefix: "he-AA1BB2CC3",
		Scope:     []byte(scope),
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestUpdateApiKey(t *testing.T) {
	userID := uuid.New()
	apiKeyID := uuid.New()

	t.Run("5.2-UNIT-010 scope-only patch merges + persists + sentinel + audit", func(t *testing.T) {
		var gotScope []byte
		var gotCap pgtype.Numeric
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{"models":["qwen-max"],"ip_whitelist":["10.0.0.0/8"]}`), nil
			},
			updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, cap pgtype.Numeric) (repository.ApiKeyRow, error) {
				gotScope = scope
				gotCap = cap
				r := baseRow(apiKeyID, userID, string(scope))
				return r, nil
			},
		}
		svc, auditPub, sentinel := newUpdateService(t, repo)

		resp, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{"deepseek-v3"}, ModelsPresent: true},
		})
		if err != nil {
			t.Fatalf("UpdateApiKey err=%v want nil", err)
		}
		// scope.models mutated; scope.ip_whitelist preserved (BR-1.7).
		if !strings.Contains(string(gotScope), `"models":["deepseek-v3"]`) {
			t.Fatalf("merged scope models not applied: %s", gotScope)
		}
		if !strings.Contains(string(gotScope), `"ip_whitelist":["10.0.0.0/8"]`) {
			t.Fatalf("ip_whitelist not preserved: %s", gotScope)
		}
		if gotCap.Valid {
			t.Fatalf("cap should be preserved (existing NULL) but got Valid=true")
		}
		if sentinel.hitsCount() != 1 {
			t.Fatalf("config sentinel hits=%d want 1", sentinel.hitsCount())
		}
		if auditPub.hits() != 1 || auditPub.last().EventType != audit.EventAPIKeyConfigUpdated {
			t.Fatalf("audit hits=%d type=%q", auditPub.hits(), auditPub.last().EventType)
		}
		if cf, ok := auditPub.last().Metadata["changed_fields"].([]string); !ok || len(cf) != 1 || cf[0] != "scope.models" {
			t.Fatalf("changed_fields=%v want [scope.models]", auditPub.last().Metadata["changed_fields"])
		}
		if resp.GetApiKeyId() != apiKeyID.String() {
			t.Fatalf("resp api_key_id=%q", resp.GetApiKeyId())
		}
	})

	t.Run("5.2-UNIT-011 cap-only patch sets numeric + changed_fields", func(t *testing.T) {
		var gotCap pgtype.Numeric
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{}`), nil
			},
			updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, cap pgtype.Numeric) (repository.ApiKeyRow, error) {
				gotCap = cap
				r := baseRow(apiKeyID, userID, string(scope))
				r.MonthlyCostCapUSD = cap
				return r, nil
			},
		}
		svc, auditPub, _ := newUpdateService(t, repo)
		capStr := "50.00"
		resp, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:            userID.String(),
			ApiKeyId:          apiKeyID.String(),
			MonthlyCostCapUsd: &capStr,
		})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if !gotCap.Valid {
			t.Fatalf("cap not set")
		}
		if resp.GetMonthlyCostCapUsd() != "50.00" {
			t.Fatalf("resp cap=%q want 50.00", resp.GetMonthlyCostCapUsd())
		}
		if cf := auditPub.last().Metadata["changed_fields"].([]string); len(cf) != 1 || cf[0] != "monthly_cost_cap_usd" {
			t.Fatalf("changed_fields=%v", cf)
		}
	})

	t.Run("5.2-UNIT-012 clear cap → NULL", func(t *testing.T) {
		var gotCap pgtype.Numeric
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				r := baseRow(apiKeyID, userID, `{}`)
				r.MonthlyCostCapUSD = numericFromString(t, "99.99")
				return r, nil
			},
			updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, cap pgtype.Numeric) (repository.ApiKeyRow, error) {
				gotCap = cap
				return baseRow(apiKeyID, userID, string(scope)), nil
			},
		}
		svc, _, _ := newUpdateService(t, repo)
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:          userID.String(),
			ApiKeyId:        apiKeyID.String(),
			ClearMonthlyCap: true,
		})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if gotCap.Valid {
			t.Fatalf("clear cap should store NULL (Valid=false)")
		}
	})

	t.Run("5.2-UNIT-013 NotFound on cross-user (IDOR)", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, uuid.New(), `{}`), nil // different owner
			},
		}
		svc, _, _ := newUpdateService(t, repo)
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{}, ModelsPresent: true},
		})
		assertConnectCode(t, err, connect.CodeNotFound)
	})

	t.Run("5.2-UNIT-014 NotFound on revoked key", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				r := baseRow(apiKeyID, userID, `{}`)
				r.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
				return r, nil
			},
		}
		svc, _, _ := newUpdateService(t, repo)
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{}, ModelsPresent: true},
		})
		assertConnectCode(t, err, connect.CodeNotFound)
	})

	t.Run("5.2-UNIT-015 pending_deletion → FailedPrecondition", func(t *testing.T) {
		repo := &fakeRepo{
			getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "pending_deletion", nil },
		}
		svc, _, _ := newUpdateService(t, repo)
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:            userID.String(),
			ApiKeyId:          apiKeyID.String(),
			MonthlyCostCapUsd: ptr("10.00"),
		})
		assertConnectCode(t, err, connect.CodeFailedPrecondition)
	})

	t.Run("5.2-UNIT-017 invalid CIDR → InvalidArgument", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{}`), nil
			},
		}
		svc, _, _ := newUpdateService(t, repo)
		for _, bad := range []string{"192.168.1.1/33", "2001:db8::/130", "not-an-ip", "0.0.0.0/0", "::/0"} {
			_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
				UserId:   userID.String(),
				ApiKeyId: apiKeyID.String(),
				Scope:    &authv1.ScopePatch{IpWhitelist: []string{bad}, IpWhitelistPresent: true},
			})
			assertConnectCode(t, err, connect.CodeInvalidArgument)
		}
	})

	t.Run("5.2-UNIT-018 cap out-of-range → InvalidArgument", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{}`), nil
			},
		}
		svc, _, _ := newUpdateService(t, repo)
		for _, bad := range []string{"0.00", "1000000.00", "-5.00", "abc"} {
			_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
				UserId:            userID.String(),
				ApiKeyId:          apiKeyID.String(),
				MonthlyCostCapUsd: ptr(bad),
			})
			assertConnectCode(t, err, connect.CodeInvalidArgument)
		}
	})

	t.Run("5.2-UNIT-019 sentinel failure → still success + audit", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{}`), nil
			},
			updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, _ pgtype.Numeric) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, string(scope)), nil
			},
		}
		svc, auditPub, sentinel := newUpdateService(t, repo)
		sentinel.failOnSet = context.DeadlineExceeded
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{}, ModelsPresent: true},
		})
		if err != nil {
			t.Fatalf("sentinel failure must NOT fail the request: %v", err)
		}
		if auditPub.hits() != 1 {
			t.Fatalf("audit still expected on sentinel failure")
		}
	})

	t.Run("5.2-UNIT-020 Kafka emit failure → still success", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{}`), nil
			},
			updateConfig: func(_ context.Context, _, _ uuid.UUID, scope []byte, _ pgtype.Numeric) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, string(scope)), nil
			},
		}
		svc, auditPub, _ := newUpdateService(t, repo)
		auditPub.failWith = context.DeadlineExceeded
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{}, ModelsPresent: true},
		})
		if err != nil {
			t.Fatalf("kafka failure must NOT fail the request: %v", err)
		}
	})

	t.Run("5.2-UNIT-016 empty patch → InvalidArgument", func(t *testing.T) {
		svc, _, _ := newUpdateService(t, &fakeRepo{})
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
		})
		assertConnectCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("5.2-UNIT-024 invalid uuids → InvalidArgument", func(t *testing.T) {
		svc, _, _ := newUpdateService(t, &fakeRepo{})
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   "not-a-uuid",
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{}, ModelsPresent: true},
		})
		assertConnectCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("5.2-UNIT-025 TOCTOU concurrent revoke → NotFound", func(t *testing.T) {
		repo := &fakeRepo{
			selectConfig: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return baseRow(apiKeyID, userID, `{}`), nil
			},
			updateConfig: func(_ context.Context, _, _ uuid.UUID, _ []byte, _ pgtype.Numeric) (repository.ApiKeyRow, error) {
				return repository.ApiKeyRow{}, repository.ErrAPIKeyNotFound
			},
		}
		svc, _, _ := newUpdateService(t, repo)
		_, err := svc.UpdateApiKey(context.Background(), &authv1.UpdateApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
			Scope:    &authv1.ScopePatch{Models: []string{}, ModelsPresent: true},
		})
		assertConnectCode(t, err, connect.CodeNotFound)
	})
}

func ptr(s string) *string { return &s }

func assertConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("err=nil want connect code %v", want)
	}
	if got := connect.CodeOf(err); got != want {
		t.Fatalf("connect code=%v want %v (err=%v)", got, want, err)
	}
}
