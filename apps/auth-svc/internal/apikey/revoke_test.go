// Story 5.1 — UNIT-021..025 (RevokeApiKey RPC handler + sentinel write).
// See docs/qa/assessments/5.1-test-design-20260525.md.

package apikey

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// TestRevokeApiKey covers UNIT-021..025.
// Source: T3.1 (story line 538-549) + design-doc §"Unit: RevokeApiKey RPC Handler".
func TestRevokeApiKey(t *testing.T) {
	userID := uuid.New()
	apiKeyID := uuid.New()
	now := time.Date(2026, 5, 25, 12, 5, 0, 0, time.UTC)

	t.Run("5.1-UNIT-021 happy path + pending_deletion permit + sentinel SET", func(t *testing.T) {
		// Scenario: 5.1-UNIT-021
		// Priority: P0
		// Input:    {UserId, ApiKeyId}; stub repo returns row with user_id==req.UserId,
		//           revoked_at=NULL, status='pending_deletion' (Q9 permit-revoke)
		// Expected: response {api_key_id, revoked_at=NOW, was_already_revoked=false};
		//           PG UPDATE issued once; Kafka emit once;
		//           Redis SET auth:apikey:revoked:{id} 1 EX 300 issued once.
		// AC3 + Q9 + BR-3.8 + BR-3.9
		repo := &fakeRepo{
			// Q9: pending_deletion PERMITS revoke (Architect ratified
			// asymmetric gating — revoke is a security-positive action).
			getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "pending_deletion", nil },
			selectForUpd: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return repository.ApiKeyRow{
					ID:        apiKeyID,
					UserID:    userID,
					KeyPrefix: "he-AA1BB2CC3",
					Name:      "Production",
					// revoked_at is the zero pgtype.Timestamptz → Valid=false
				}, nil
			},
			updateRevoked: func(_ context.Context, _ uuid.UUID) (time.Time, error) { return now, nil },
		}
		svc, auditPub, sentinel, _ := newCreateService(t, repo)

		resp, err := svc.RevokeApiKey(context.Background(), &authv1.RevokeApiKeyRequest{
			UserId:    userID.String(),
			ApiKeyId:  apiKeyID.String(),
			ClientIp:  "192.0.2.5",
			UserAgent: "curl/8.0",
		})
		if err != nil {
			t.Fatalf("RevokeApiKey err=%v want nil", err)
		}
		if resp.GetWasAlreadyRevoked() {
			t.Fatalf("was_already_revoked=true want false")
		}
		if resp.GetRevokedAt().AsTime().UTC() != now {
			t.Fatalf("revoked_at=%v want %v", resp.GetRevokedAt().AsTime().UTC(), now)
		}
		if resp.GetApiKeyId() != apiKeyID.String() {
			t.Fatalf("api_key_id=%q want %q", resp.GetApiKeyId(), apiKeyID.String())
		}
		// Side-effects
		if repo.updateRevHits != 1 {
			t.Fatalf("updateRevHits=%d want 1", repo.updateRevHits)
		}
		if auditPub.hits() != 1 {
			t.Fatalf("audit hits=%d want 1", auditPub.hits())
		}
		if ev := auditPub.last(); ev.EventType != audit.EventAPIKeyRevoked {
			t.Fatalf("audit event_type=%q want %q", ev.EventType, audit.EventAPIKeyRevoked)
		}
		if sentinel.hitsCount() != 1 {
			t.Fatalf("sentinel hits=%d want 1", sentinel.hitsCount())
		}
	})

	t.Run("5.1-UNIT-022 NotFound for nonexistent id (anti-enumeration)", func(t *testing.T) {
		// Scenario: 5.1-UNIT-022
		// Priority: P0
		// Input:    stub repo SELECT returns 0 rows
		// Expected: gRPC codes.NotFound; PG UPDATE counter == 0;
		//           Kafka emit counter == 0; Redis sentinel SET counter == 0.
		// BR-3.2 anti-enumeration NotFound path
		repo := &fakeRepo{
			selectForUpd: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return repository.ApiKeyRow{}, repository.ErrAPIKeyNotFound
			},
		}
		svc, auditPub, sentinel, _ := newCreateService(t, repo)

		_, err := svc.RevokeApiKey(context.Background(), &authv1.RevokeApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
		})
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("err=%v want *connect.Error", err)
		}
		if cerr.Code() != connect.CodeNotFound {
			t.Fatalf("code=%s want NotFound", cerr.Code())
		}
		if repo.updateRevHits != 0 {
			t.Fatalf("updateRevHits=%d want 0", repo.updateRevHits)
		}
		if auditPub.hits() != 0 {
			t.Fatalf("audit hits=%d want 0", auditPub.hits())
		}
		if sentinel.hitsCount() != 0 {
			t.Fatalf("sentinel hits=%d want 0", sentinel.hitsCount())
		}
	})

	t.Run("5.1-UNIT-023 NotFound for cross-user id (IDOR collapse)", func(t *testing.T) {
		// Scenario: 5.1-UNIT-023
		// Priority: P0
		// Input:    stub repo returns row with user_id != req.UserId
		// Expected: SAME codes.NotFound response as UNIT-022 (anti-enumeration collapse);
		//           PG UPDATE == 0; Kafka emit == 0; Redis SET == 0;
		//           slog records IDOR-attempt with hashed attempted_user_id + actual_user_id.
		// BR-3.2 anti-enumeration collapse + Q4 SM default
		actualOwner := uuid.New()
		repo := &fakeRepo{
			selectForUpd: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return repository.ApiKeyRow{
					ID:        apiKeyID,
					UserID:    actualOwner, // NOT userID
					KeyPrefix: "he-AAA111222",
					Name:      "OwnedByOther",
				}, nil
			},
		}
		svc, auditPub, sentinel, logBuf := newCreateService(t, repo)

		_, err := svc.RevokeApiKey(context.Background(), &authv1.RevokeApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
		})
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("err=%v want *connect.Error", err)
		}
		if cerr.Code() != connect.CodeNotFound {
			t.Fatalf("code=%s want NotFound (collapse with not-found path)", cerr.Code())
		}
		if repo.updateRevHits != 0 {
			t.Fatalf("updateRevHits=%d want 0", repo.updateRevHits)
		}
		if auditPub.hits() != 0 {
			t.Fatalf("audit hits=%d want 0", auditPub.hits())
		}
		if sentinel.hitsCount() != 0 {
			t.Fatalf("sentinel hits=%d want 0", sentinel.hitsCount())
		}
		if !strings.Contains(logBuf.String(), "apikey_revoke_idor_attempt") {
			t.Fatalf("expected IDOR-attempt slog record; got: %s", logBuf.String())
		}
		// Raw user IDs MUST NOT appear in slog (hashed only).
		if strings.Contains(logBuf.String(), userID.String()) {
			t.Fatalf("slog leaks raw attempted_user_id; want hash only")
		}
		if strings.Contains(logBuf.String(), actualOwner.String()) {
			t.Fatalf("slog leaks raw actual_user_id; want hash only")
		}
	})

	t.Run("5.1-UNIT-024 idempotent re-revoke returns historical revoked_at", func(t *testing.T) {
		// Scenario: 5.1-UNIT-024
		// Priority: P0
		// Input:    stub repo returns row with revoked_at=T0 (historical, non-NULL)
		// Expected: response {api_key_id, revoked_at=T0 (NOT NOW), was_already_revoked=true};
		//           PG UPDATE counter == 0; Kafka emit counter == 0 (BR-3.4);
		//           Redis sentinel re-SET allowed per T3.1 line 544.
		// BR-3.3 + BR-3.4 idempotent terminal state
		t0 := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
		repo := &fakeRepo{
			selectForUpd: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return repository.ApiKeyRow{
					ID:        apiKeyID,
					UserID:    userID,
					KeyPrefix: "he-AAA111222",
					Name:      "Production",
					RevokedAt: pgtype.Timestamptz{Time: t0, Valid: true},
				}, nil
			},
		}
		svc, auditPub, sentinel, _ := newCreateService(t, repo)

		resp, err := svc.RevokeApiKey(context.Background(), &authv1.RevokeApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
		})
		if err != nil {
			t.Fatalf("err=%v want nil (idempotent re-revoke is success)", err)
		}
		if !resp.GetWasAlreadyRevoked() {
			t.Fatalf("was_already_revoked=false want true")
		}
		if resp.GetRevokedAt().AsTime().UTC() != t0 {
			t.Fatalf("revoked_at=%v want historical T0=%v", resp.GetRevokedAt().AsTime().UTC(), t0)
		}
		if repo.updateRevHits != 0 {
			t.Fatalf("updateRevHits=%d want 0 on idempotent path", repo.updateRevHits)
		}
		if auditPub.hits() != 0 {
			t.Fatalf("audit hits=%d want 0 (BR-3.4 no new emit)", auditPub.hits())
		}
		// Sentinel re-SET allowed (defensive — T3.1 line 544).
		if sentinel.hitsCount() != 1 {
			t.Fatalf("sentinel hits=%d want 1 (defensive re-SET)", sentinel.hitsCount())
		}
	})

	t.Run("5.1-UNIT-025 redis unavailable still returns 200 + WARN log", func(t *testing.T) {
		// Scenario: 5.1-UNIT-025
		// Priority: P0
		// Input:    stub Redis returns connection error on SET sentinel
		// Expected: RevokeApiKey returns success (PG already UPDATEd + Kafka emitted);
		//           slog WARN event="apikey_revoke_sentinel_failed"; lag falls back to §8.2.1 baseline.
		// BR-3.13 fail-open default
		repo := &fakeRepo{
			selectForUpd: func(_ context.Context, _ uuid.UUID) (repository.ApiKeyRow, error) {
				return repository.ApiKeyRow{
					ID:        apiKeyID,
					UserID:    userID,
					KeyPrefix: "he-AAA111222",
					Name:      "Production",
				}, nil
			},
			updateRevoked: func(_ context.Context, _ uuid.UUID) (time.Time, error) { return now, nil },
		}
		svc, auditPub, sentinel, logBuf := newCreateService(t, repo)
		sentinel.failOnSet = errors.New("redis: connection refused")

		resp, err := svc.RevokeApiKey(context.Background(), &authv1.RevokeApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: apiKeyID.String(),
		})
		if err != nil {
			t.Fatalf("err=%v want nil (sentinel fail-open)", err)
		}
		if resp.GetWasAlreadyRevoked() {
			t.Fatalf("was_already_revoked=true want false")
		}
		if repo.updateRevHits != 1 {
			t.Fatalf("updateRevHits=%d want 1 (PG commit must succeed)", repo.updateRevHits)
		}
		if auditPub.hits() != 1 {
			t.Fatalf("audit hits=%d want 1", auditPub.hits())
		}
		if !strings.Contains(logBuf.String(), "apikey_revoke_sentinel_failed") {
			t.Fatalf("logBuf missing apikey_revoke_sentinel_failed WARN: %s", logBuf.String())
		}
	})

	t.Run("invalid_api_key_id rejects with InvalidArgument", func(t *testing.T) {
		svc, _, _, _ := newCreateService(t, &fakeRepo{})
		_, err := svc.RevokeApiKey(context.Background(), &authv1.RevokeApiKeyRequest{
			UserId:   userID.String(),
			ApiKeyId: "not-a-uuid",
		})
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("err=%v want *connect.Error", err)
		}
		if cerr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("code=%s want InvalidArgument", cerr.Code())
		}
	})
}
