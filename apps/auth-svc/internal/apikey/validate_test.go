package apikey

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// --- Test doubles ----------------------------------------------------------

type fakeRepo struct {
	// Story 3.2 hot path
	lookup     func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error)
	lookupHits int
	touch      func(ctx context.Context, id uuid.UUID) error
	touchHits  int
	touchDone  chan struct{}

	// Story 5.1 management surface — nil-safe; tests opt in by setting the
	// closure they care about.
	insert           func(ctx context.Context, userID uuid.UUID, name, prefix, hash string) (uuid.UUID, time.Time, error)
	insertHits       int
	list             func(ctx context.Context, userID uuid.UUID) ([]repository.ApiKeyRow, error)
	listHits         int
	selectForUpd     func(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error)
	selectForUpdHits int
	updateRevoked    func(ctx context.Context, apiKeyID uuid.UUID) (time.Time, error)
	updateRevHits    int
	getUserStatus    func(ctx context.Context, userID uuid.UUID) (string, error)
	getStatusHits    int

	// Story 5.2 — UpdateApiKey config-mutation surface.
	selectConfig     func(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error)
	selectConfigHits int
	updateConfig     func(ctx context.Context, apiKeyID, userID uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error)
	updateConfigHits int
}

func (f *fakeRepo) LookupAPIKeysByPrefix(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
	f.lookupHits++
	return f.lookup(ctx, prefix)
}

func (f *fakeRepo) TouchAPIKeyLastUsed(ctx context.Context, id uuid.UUID) error {
	f.touchHits++
	err := func() error {
		if f.touch == nil {
			return nil
		}
		return f.touch(ctx, id)
	}()
	if f.touchDone != nil {
		select {
		case f.touchDone <- struct{}{}:
		default:
		}
	}
	return err
}

func (f *fakeRepo) InsertAPIKey(ctx context.Context, userID uuid.UUID, name, prefix, hash string) (uuid.UUID, time.Time, error) {
	f.insertHits++
	if f.insert == nil {
		return uuid.Nil, time.Time{}, errors.New("fakeRepo.insert unwired")
	}
	return f.insert(ctx, userID, name, prefix, hash)
}

func (f *fakeRepo) ListAPIKeysByUser(ctx context.Context, userID uuid.UUID) ([]repository.ApiKeyRow, error) {
	f.listHits++
	if f.list == nil {
		return nil, errors.New("fakeRepo.list unwired")
	}
	return f.list(ctx, userID)
}

func (f *fakeRepo) SelectAPIKeyForUpdate(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error) {
	f.selectForUpdHits++
	if f.selectForUpd == nil {
		return repository.ApiKeyRow{}, errors.New("fakeRepo.selectForUpd unwired")
	}
	return f.selectForUpd(ctx, apiKeyID)
}

func (f *fakeRepo) UpdateAPIKeyRevokedAt(ctx context.Context, apiKeyID uuid.UUID) (time.Time, error) {
	f.updateRevHits++
	if f.updateRevoked == nil {
		return time.Time{}, errors.New("fakeRepo.updateRevoked unwired")
	}
	return f.updateRevoked(ctx, apiKeyID)
}

func (f *fakeRepo) GetUserStatus(ctx context.Context, userID uuid.UUID) (string, error) {
	f.getStatusHits++
	if f.getUserStatus == nil {
		return "active", nil
	}
	return f.getUserStatus(ctx, userID)
}

func (f *fakeRepo) SelectAPIKeyConfigForUpdate(ctx context.Context, apiKeyID uuid.UUID) (repository.ApiKeyRow, error) {
	f.selectConfigHits++
	if f.selectConfig == nil {
		return repository.ApiKeyRow{}, errors.New("fakeRepo.selectConfig unwired")
	}
	return f.selectConfig(ctx, apiKeyID)
}

func (f *fakeRepo) UpdateAPIKeyConfig(ctx context.Context, apiKeyID, userID uuid.UUID, scope []byte, cap pgtype.Numeric, strictness string) (repository.ApiKeyRow, error) {
	f.updateConfigHits++
	if f.updateConfig == nil {
		return repository.ApiKeyRow{}, errors.New("fakeRepo.updateConfig unwired")
	}
	return f.updateConfig(ctx, apiKeyID, userID, scope, cap, strictness)
}

// newTestService bundles a Service + in-memory tracetest exporter so each
// test can assert span attributes for the BR-2.10 / TC-9 plaintext-never-
// in-spans defence.
func newTestService(t *testing.T, repo Repository) (*Service, *tracetest.InMemoryExporter, *bytes.Buffer) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	tracer := tp.Tracer("apikey-test")

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := NewService(repo, tracer, logger)
	return svc, exp, &logBuf
}

func bcryptCost4(t *testing.T, plaintext string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), 4)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	return string(h)
}

func canonicalKey() string {
	// 12-char `he-` + 9-base62 prefix + 12 trailing chars. Matches the
	// regex `^he-[A-Za-z0-9]{10,253}$` and clears the KeyPrefixLength gate.
	return "he-ABCDEFGHIJ123"
}

// Assert that no span attribute or log line contains the plaintext key.
// Drives BR-2.10 / TC-9 — the manual code-review gate for plaintext-leak
// defence becomes an automated assertion here.
func assertNoPlaintextLeak(t *testing.T, exp *tracetest.InMemoryExporter, logBuf *bytes.Buffer, plaintext string) {
	t.Helper()
	for _, sp := range exp.GetSpans() {
		for _, a := range sp.Attributes {
			if strings.Contains(a.Value.AsString(), plaintext) {
				t.Fatalf("plaintext %q leaked into span attribute %q (value=%q)",
					plaintext, a.Key, a.Value.AsString())
			}
		}
	}
	if strings.Contains(logBuf.String(), plaintext) {
		t.Fatalf("plaintext leaked into log buffer: %q", logBuf.String())
	}
}

// --- UNIT-004 .. UNIT-010 --------------------------------------------------

// Scenario: 3.2-UNIT-004
// Validate returns ok=true, reason=UNSPECIFIED on a matching unrevoked key.
func TestValidate_OkOnMatchingUnrevokedKey(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	hash := bcryptCost4(t, plaintext)
	apiKeyID := uuid.New()
	userID := uuid.New()

	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return []repository.ApiKeyRow{{
				ID:        apiKeyID,
				UserID:    userID,
				KeyPrefix: prefix,
				KeyHash:   hash,
				Scope:     []byte(`{"models":["qwen-max"]}`),
			}}, nil
		},
		touchDone: make(chan struct{}, 1),
	}
	svc, exp, logBuf := newTestService(t, repo)

	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !resp.GetOk() {
		t.Fatalf("ok = false, want true")
	}
	if resp.GetReason() != authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_UNSPECIFIED {
		t.Errorf("reason = %v, want UNSPECIFIED", resp.GetReason())
	}
	if resp.GetApiKeyId() != apiKeyID.String() {
		t.Errorf("api_key_id mismatch")
	}
	if resp.GetUserId() != userID.String() {
		t.Errorf("user_id mismatch")
	}
	if resp.GetTeamId() != "" {
		t.Errorf("team_id = %q, want empty", resp.GetTeamId())
	}
	if resp.GetScope() != `{"models":["qwen-max"]}` {
		t.Errorf("scope mismatch: got %q", resp.GetScope())
	}
	// Fire-and-forget last_used_at touch should fire exactly once.
	select {
	case <-repo.touchDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("fire-and-forget last_used_at UPDATE did not occur within 2s")
	}
	assertNoPlaintextLeak(t, exp, logBuf, plaintext)
}

// Validate populates team_id (non-empty string) when the DB row carries a
// non-NULL team_id. Covers the BR-1.8 context-propagation contract.
func TestValidate_PopulatesTeamID(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	hash := bcryptCost4(t, plaintext)
	teamID := uuid.New()
	var teamPG pgtype.UUID
	_ = teamPG.Scan(teamID.String())

	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return []repository.ApiKeyRow{{
				ID:        uuid.New(),
				UserID:    uuid.New(),
				TeamID:    teamPG,
				KeyPrefix: prefix,
				KeyHash:   hash,
				Scope:     []byte(`{}`),
			}}, nil
		},
		touchDone: make(chan struct{}, 1),
	}
	svc, _, _ := newTestService(t, repo)
	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.GetTeamId() != teamID.String() {
		t.Errorf("team_id = %q, want %q", resp.GetTeamId(), teamID.String())
	}
	<-repo.touchDone
}

// Scenario: 3.2-UNIT-005
// Validate returns ok=false, reason=REVOKED when the matched row is revoked.
// Anti-enumeration: response shape MUST equal the NOT_FOUND shape modulo
// the `reason` enum (BR-2.4).
func TestValidate_RevokedKey(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	hash := bcryptCost4(t, plaintext)
	var revokedAt pgtype.Timestamptz
	_ = revokedAt.Scan(time.Now())
	if !revokedAt.Valid {
		t.Fatal("revokedAt.Valid should be true")
	}

	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return []repository.ApiKeyRow{{
				ID:        uuid.New(),
				UserID:    uuid.New(),
				KeyPrefix: prefix,
				KeyHash:   hash,
				Scope:     []byte(`{}`),
				RevokedAt: revokedAt,
			}}, nil
		},
	}
	svc, _, _ := newTestService(t, repo)

	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.GetOk() {
		t.Fatalf("ok = true, want false")
	}
	if resp.GetReason() != authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_REVOKED {
		t.Errorf("reason = %v, want REVOKED", resp.GetReason())
	}
	// Anti-enumeration assertion — payload fields (api_key_id, user_id,
	// team_id, scope) MUST be zero so the gateway cannot accidentally
	// reveal which path triggered.
	if resp.GetApiKeyId() != "" || resp.GetUserId() != "" || resp.GetTeamId() != "" || resp.GetScope() != "" {
		t.Errorf("REVOKED response leaked identity: api_key_id=%q user_id=%q team_id=%q scope=%q",
			resp.GetApiKeyId(), resp.GetUserId(), resp.GetTeamId(), resp.GetScope())
	}
	// No touch on revoked path.
	if repo.touchHits != 0 {
		t.Errorf("touch_hits = %d, want 0", repo.touchHits)
	}
}

// Scenario: 3.2-UNIT-006
// Validate returns ok=false, reason=NOT_FOUND when the prefix matches zero
// rows.
func TestValidate_NotFoundNoPrefix(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return nil, nil
		},
	}
	svc, _, _ := newTestService(t, repo)

	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.GetOk() {
		t.Fatalf("ok = true, want false")
	}
	if resp.GetReason() != authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND {
		t.Errorf("reason = %v, want NOT_FOUND", resp.GetReason())
	}
}

// Scenario: 3.2-UNIT-007
// Validate rejects plaintext failing the BR-2.6 regex and emits NOT_FOUND
// without any DB query — proves the bcrypt-DoS-via-oversized-input gate.
func TestValidate_RegexRejectShortCircuits(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			t.Fatalf("DB MUST NOT be queried on regex reject; called for prefix %q", prefix)
			return nil, nil
		},
	}
	svc, exp, _ := newTestService(t, repo)

	for _, bad := range []string{
		"",
		"not-a-he-key",
		"he-",
		"he-short",
		"BEARER he-ABCDEF1234", // includes scheme — caller must strip first
		strings.Repeat("a", 1000),
	} {
		resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: bad})
		if err != nil {
			t.Fatalf("Validate(%q): %v", bad, err)
		}
		if resp.GetOk() {
			t.Errorf("ok = true on %q, want false", bad)
		}
		if resp.GetReason() != authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND {
			t.Errorf("reason on %q = %v, want NOT_FOUND", bad, resp.GetReason())
		}
	}
	if repo.lookupHits != 0 {
		t.Errorf("lookup_hits = %d, want 0 (regex must short-circuit)", repo.lookupHits)
	}
	// Span outcome attribute should be `not_found_syntax`.
	foundSyntax := false
	for _, sp := range exp.GetSpans() {
		for _, a := range sp.Attributes {
			if a.Key == "auth.outcome" && a.Value.AsString() == "not_found_syntax" {
				foundSyntax = true
			}
		}
	}
	if !foundSyntax {
		t.Errorf("expected at least one span with auth.outcome=not_found_syntax")
	}
}

// Scenario: 3.2-UNIT-008
// Validate fails closed (REASON_NOT_FOUND + ERROR log) when LookupByPrefix
// returns more than MaxAPIKeyCandidates rows (BR-2.3).
func TestValidate_CandidateCapExceeded(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	overflow := make([]repository.ApiKeyRow, repository.MaxAPIKeyCandidates+1)
	for i := range overflow {
		overflow[i] = repository.ApiKeyRow{
			ID:        uuid.New(),
			UserID:    uuid.New(),
			KeyPrefix: plaintext[:KeyPrefixLength],
			KeyHash:   "$2a$04$short", // intentionally non-matching — must never bcrypt
		}
	}
	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return overflow, nil
		},
	}
	svc, _, _ := newTestService(t, repo)

	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.GetOk() {
		t.Errorf("ok = true on overflow, want false (fail closed)")
	}
	if resp.GetReason() != authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND {
		t.Errorf("reason = %v, want NOT_FOUND", resp.GetReason())
	}
}

// Scenario: 3.2-UNIT-009
// Fire-and-forget last_used_at UPDATE failure does NOT fail the validate
// response (BR-2.5).
func TestValidate_FireAndForgetTouchFailsSilent(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	hash := bcryptCost4(t, plaintext)
	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return []repository.ApiKeyRow{{
				ID:        uuid.New(),
				UserID:    uuid.New(),
				KeyPrefix: prefix,
				KeyHash:   hash,
				Scope:     []byte(`{}`),
			}}, nil
		},
		touch: func(ctx context.Context, id uuid.UUID) error {
			return errors.New("PG transient")
		},
		touchDone: make(chan struct{}, 1),
	}
	svc, _, _ := newTestService(t, repo)

	resp, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("ok = false despite successful primary validate; must not fail on touch error")
	}
	<-repo.touchDone
}

// Scenario: 3.2-UNIT-010
// Plaintext MUST NOT appear in any span attribute or returned error
// message — defence in depth (TC-9 / BR-2.10).
func TestValidate_NoPlaintextLeakOnDBError(t *testing.T) {
	t.Parallel()
	plaintext := canonicalKey()
	repo := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return nil, errors.New("conn refused")
		},
	}
	svc, exp, logBuf := newTestService(t, repo)
	_, err := svc.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintext})
	// Expect connect.CodeUnavailable
	if err == nil {
		t.Fatalf("expected error")
	}
	if cerr := new(connect.Error); errors.As(err, &cerr) {
		if cerr.Code() != connect.CodeUnavailable {
			t.Errorf("connect.Code = %v, want Unavailable", cerr.Code())
		}
	} else {
		t.Errorf("err is not connect.Error: %v", err)
	}
	if strings.Contains(err.Error(), plaintext) {
		t.Errorf("plaintext leaked into error message: %v", err)
	}
	assertNoPlaintextLeak(t, exp, logBuf, plaintext)
}

// Anti-enumeration parity: NOT_FOUND and REVOKED MUST be byte-identical
// modulo the internal `reason` enum (BR-2.4 / SEC-002).
func TestValidate_AntiEnumerationParity(t *testing.T) {
	t.Parallel()
	plaintextOK := canonicalKey()
	plaintextRevoked := "he-XYZABC1234567"

	hashOK := bcryptCost4(t, plaintextOK)
	hashRev := bcryptCost4(t, plaintextRevoked)
	var revokedAt pgtype.Timestamptz
	_ = revokedAt.Scan(time.Now())

	repoNF := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return nil, nil
		},
	}
	repoRev := &fakeRepo{
		lookup: func(ctx context.Context, prefix string) ([]repository.ApiKeyRow, error) {
			return []repository.ApiKeyRow{{
				ID:        uuid.New(),
				UserID:    uuid.New(),
				KeyPrefix: prefix,
				KeyHash:   hashRev,
				Scope:     []byte(`{}`),
				RevokedAt: revokedAt,
			}}, nil
		},
	}
	_ = hashOK
	svcNF, _, _ := newTestService(t, repoNF)
	svcRev, _, _ := newTestService(t, repoRev)

	respNF, _ := svcNF.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintextOK})
	respRev, _ := svcRev.Validate(context.Background(), &authv1.ValidateApiKeyRequest{PlaintextKey: plaintextRevoked})

	if respNF.GetOk() != respRev.GetOk() {
		t.Errorf("ok parity broken: nf=%v rev=%v", respNF.GetOk(), respRev.GetOk())
	}
	if respNF.GetApiKeyId() != respRev.GetApiKeyId() ||
		respNF.GetUserId() != respRev.GetUserId() ||
		respNF.GetTeamId() != respRev.GetTeamId() ||
		respNF.GetScope() != respRev.GetScope() {
		t.Errorf("payload parity broken: nf=%+v rev=%+v", respNF, respRev)
	}
}
