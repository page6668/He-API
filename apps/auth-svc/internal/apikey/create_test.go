// Story 5.1 — UNIT-006..010 (CreateApiKey RPC handler).
// See docs/qa/assessments/5.1-test-design-20260525.md.

package apikey

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
)

// recordingAuditPub captures every Publish call for assertion. Optionally
// returns a sentinel error so failure-path tests can assert WARN-log
// behaviour (UNIT-009).
type recordingAuditPub struct {
	mu       sync.Mutex
	events   []audit.Event
	failWith error
}

func (r *recordingAuditPub) Publish(_ context.Context, ev audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return r.failWith
}

func (r *recordingAuditPub) hits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recordingAuditPub) last() audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return audit.Event{}
	}
	return r.events[len(r.events)-1]
}

// stubSentinel is the SentinelStore stand-in for tests that need to assert
// sentinel-write counters. Default failOnSet=false so happy-path tests
// don't need to wire anything.
type stubSentinel struct {
	mu        sync.Mutex
	hits      int
	failOnSet error
}

func (s *stubSentinel) SetRevokedSentinel(_ context.Context, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	return s.failOnSet
}

func (s *stubSentinel) hitsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// newCreateService wires a Service for the Create/List/Revoke tests with
// the standard fake repo + recording audit pub + stub sentinel + a log
// buffer for SECURITY-001 plaintext-leak assertions.
func newCreateService(t *testing.T, repo Repository) (*Service, *recordingAuditPub, *stubSentinel, *bytes.Buffer) {
	t.Helper()
	svc, _, logBuf := newTestService(t, repo)
	auditPub := &recordingAuditPub{}
	sentinel := &stubSentinel{}
	svc.Audit = auditPub
	svc.Sentinel = sentinel
	svc.Clock = func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) }
	return svc, auditPub, sentinel, logBuf
}

// TestCreateApiKey covers UNIT-006..010.
// Source: T1.2 (story line 507-514) + design-doc §"Unit: CreateApiKey RPC Handler".
func TestCreateApiKey(t *testing.T) {
	userID := uuid.New()
	createdAt := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	t.Run("5.1-UNIT-006 happy path returns plaintext + persists hash + emits audit", func(t *testing.T) {
		// Scenario: 5.1-UNIT-006
		// Priority: P0
		// Input:    {UserId: valid uuid, Name: "My First Key"}, users.status='active'
		// Expected: response carries api_key_id (uuid v4), key_prefix (12 chars),
		//           name=="My First Key", created_at (RFC3339), plaintext (~46 chars "he-...");
		//           PG INSERT issued once; Kafka audit.event "api_key.created" emitted once
		//           with PII-safe payload (no plaintext, no key_hash).
		// AC1 §Scenario + BR-1.5 + BR-3.5
		var capturedHash, capturedPrefix, capturedName string
		var capturedUserID uuid.UUID
		fakeKeyID := uuid.New()
		repo := &fakeRepo{
			getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "active", nil },
			insert: func(_ context.Context, uid uuid.UUID, name, prefix, hash string) (uuid.UUID, time.Time, error) {
				capturedUserID = uid
				capturedName = name
				capturedPrefix = prefix
				capturedHash = hash
				return fakeKeyID, createdAt, nil
			},
		}
		svc, auditPub, _, logBuf := newCreateService(t, repo)

		resp, err := svc.CreateApiKey(context.Background(), &authv1.CreateApiKeyRequest{
			UserId:    userID.String(),
			Name:      "My First Key",
			ClientIp:  "192.0.2.1",
			UserAgent: "Mozilla/5.0",
		})
		if err != nil {
			t.Fatalf("CreateApiKey err=%v want nil", err)
		}
		if resp.GetApiKeyId() != fakeKeyID.String() {
			t.Fatalf("api_key_id=%q want %q", resp.GetApiKeyId(), fakeKeyID.String())
		}
		if got := resp.GetName(); got != "My First Key" {
			t.Fatalf("name=%q want %q", got, "My First Key")
		}
		if got := len(resp.GetKeyPrefix()); got != 12 {
			t.Fatalf("key_prefix len=%d want 12", got)
		}
		if !strings.HasPrefix(resp.GetPlaintext(), "he-") {
			t.Fatalf("plaintext=%q want prefix 'he-'", resp.GetPlaintext())
		}
		if resp.GetCreatedAt().AsTime().UTC() != createdAt {
			t.Fatalf("created_at=%v want %v", resp.GetCreatedAt().AsTime().UTC(), createdAt)
		}
		// Insert side-effects
		if repo.insertHits != 1 {
			t.Fatalf("insertHits=%d want 1", repo.insertHits)
		}
		if capturedUserID != userID {
			t.Fatalf("captured userID=%v want %v", capturedUserID, userID)
		}
		if capturedName != "My First Key" {
			t.Fatalf("captured name=%q want 'My First Key'", capturedName)
		}
		if capturedPrefix != resp.GetKeyPrefix() {
			t.Fatalf("captured prefix=%q want %q", capturedPrefix, resp.GetKeyPrefix())
		}
		// bcrypt-verify the captured hash against the returned plaintext
		if err := bcrypt.CompareHashAndPassword([]byte(capturedHash), []byte(resp.GetPlaintext())); err != nil {
			t.Fatalf("bcrypt verify err=%v", err)
		}
		// Audit emit
		if auditPub.hits() != 1 {
			t.Fatalf("audit hits=%d want 1", auditPub.hits())
		}
		ev := auditPub.last()
		if ev.EventType != audit.EventAPIKeyCreated {
			t.Fatalf("audit event_type=%q want %q", ev.EventType, audit.EventAPIKeyCreated)
		}
		if ev.UserID != userID.String() {
			t.Fatalf("audit user_id=%q want %q", ev.UserID, userID.String())
		}
		// PII-safe payload — no plaintext, no key_hash in Metadata or any field
		if md := ev.Metadata; md == nil || md["api_key_id"] != fakeKeyID.String() || md["key_prefix"] != resp.GetKeyPrefix() {
			t.Fatalf("audit metadata=%+v missing/wrong api_key_id/key_prefix", md)
		}
		for k, v := range ev.Metadata {
			if s, ok := v.(string); ok && (s == resp.GetPlaintext() || s == capturedHash) {
				t.Fatalf("audit metadata[%s] leaks plaintext or key_hash", k)
			}
		}
		// IP / UserAgent must be hashed (NOT the raw values)
		if ev.IP == "192.0.2.1" || ev.UserAgent == "Mozilla/5.0" {
			t.Fatalf("audit IP/UA carries plaintext (want hashes); ip=%q ua=%q", ev.IP, ev.UserAgent)
		}
		// slog buffer must NOT contain plaintext or key_hash (SECURITY-001 cross-ref)
		if strings.Contains(logBuf.String(), resp.GetPlaintext()) {
			t.Fatalf("slog buffer contains plaintext key — BR-1.5 violation")
		}
		if strings.Contains(logBuf.String(), capturedHash) {
			t.Fatalf("slog buffer contains key_hash — BR-1.5 violation")
		}
	})

	t.Run("5.1-UNIT-007 pending_deletion rejects with FailedPrecondition", func(t *testing.T) {
		// Scenario: 5.1-UNIT-007
		// Priority: P0
		// Input:    users.status='pending_deletion'
		// Expected: gRPC codes.FailedPrecondition with Reason="account_pending_deletion";
		//           PG INSERT counter == 0; Kafka emit counter == 0.
		// Q9 + Story-2.5 BR-1.9 cascade
		repo := &fakeRepo{
			getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "pending_deletion", nil },
		}
		svc, auditPub, _, _ := newCreateService(t, repo)

		_, err := svc.CreateApiKey(context.Background(), &authv1.CreateApiKeyRequest{
			UserId: userID.String(),
			Name:   "X",
		})
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("err=%v want *connect.Error", err)
		}
		if cerr.Code() != connect.CodeFailedPrecondition {
			t.Fatalf("code=%s want FailedPrecondition", cerr.Code())
		}
		if !strings.Contains(cerr.Message(), "account_pending_deletion") {
			t.Fatalf("message=%q want 'account_pending_deletion'", cerr.Message())
		}
		if repo.insertHits != 0 {
			t.Fatalf("insertHits=%d want 0", repo.insertHits)
		}
		if auditPub.hits() != 0 {
			t.Fatalf("audit hits=%d want 0", auditPub.hits())
		}
	})

	t.Run("5.1-UNIT-008 invalid name table-driven rejection", func(t *testing.T) {
		// Scenario: 5.1-UNIT-008
		// Priority: P0
		// Input:    7 rows — empty, whitespace-only, 101-rune, emoji-only "🎉",
		//           control char "\x00name", RTL override "‮", "💎 fine"
		// Expected: codes.InvalidArgument; PG INSERT counter == 0 for every row.
		// BR-1.7 / Q10 (Story-2.5 display_name parity)
		cases := []struct {
			name  string
			input string
		}{
			{"empty", ""},
			{"whitespace-only", "   "},
			{"too-long-101-runes", strings.Repeat("a", 101)},
			{"emoji-only", "🎉"},
			{"control-char-prefix", "\x00name"},
			{"rtl-override", "‮Hello"}, // ‮ = RIGHT-TO-LEFT OVERRIDE (class Cf)
			{"mixed-with-emoji", "💎 fine"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				repo := &fakeRepo{
					getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "active", nil },
				}
				svc, auditPub, _, _ := newCreateService(t, repo)
				_, err := svc.CreateApiKey(context.Background(), &authv1.CreateApiKeyRequest{
					UserId: userID.String(),
					Name:   c.input,
				})
				var cerr *connect.Error
				if !errors.As(err, &cerr) {
					t.Fatalf("err=%v want *connect.Error", err)
				}
				if cerr.Code() != connect.CodeInvalidArgument {
					t.Fatalf("name=%q code=%s want InvalidArgument", c.input, cerr.Code())
				}
				if repo.insertHits != 0 {
					t.Fatalf("name=%q insertHits=%d want 0", c.input, repo.insertHits)
				}
				if auditPub.hits() != 0 {
					t.Fatalf("name=%q audit hits=%d want 0", c.input, auditPub.hits())
				}
			})
		}
	})

	t.Run("5.1-UNIT-009 kafka emit failure still returns success", func(t *testing.T) {
		// Scenario: 5.1-UNIT-009
		// Priority: P1
		// Input:    stub Kafka producer returns kafka.ErrLeaderNotAvailable
		// Expected: CreateApiKey returns success (PG row already committed); slog WARN
		//           event="audit_emit_failed"; response carries plaintext as normal.
		// BR audit-graceful-degradation (Story 2.5 BR-2.9 pattern)
		fakeKeyID := uuid.New()
		repo := &fakeRepo{
			getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "active", nil },
			insert: func(_ context.Context, _ uuid.UUID, _, _, _ string) (uuid.UUID, time.Time, error) {
				return fakeKeyID, createdAt, nil
			},
		}
		svc, auditPub, _, logBuf := newCreateService(t, repo)
		auditPub.failWith = errors.New("kafka: leader not available")

		resp, err := svc.CreateApiKey(context.Background(), &authv1.CreateApiKeyRequest{
			UserId: userID.String(),
			Name:   "Production",
		})
		if err != nil {
			t.Fatalf("CreateApiKey err=%v want nil (audit failure must not bubble up)", err)
		}
		if resp.GetApiKeyId() != fakeKeyID.String() {
			t.Fatalf("api_key_id mismatch")
		}
		if !strings.Contains(logBuf.String(), "audit_emit_failed") {
			t.Fatalf("logBuf missing audit_emit_failed WARN: %s", logBuf.String())
		}
	})

	t.Run("5.1-UNIT-010 PG insert failure returns Internal without Kafka emit", func(t *testing.T) {
		// Scenario: 5.1-UNIT-010
		// Priority: P0
		// Input:    stub repository returns pgx wrap error on Insert
		// Expected: codes.Internal; Kafka emit counter == 0 (audit emits ONLY after PG commit);
		//           response carries NO plaintext.
		// Dev Notes §kafka post-commit + BR-1.5
		repo := &fakeRepo{
			getUserStatus: func(_ context.Context, _ uuid.UUID) (string, error) { return "active", nil },
			insert: func(_ context.Context, _ uuid.UUID, _, _, _ string) (uuid.UUID, time.Time, error) {
				return uuid.Nil, time.Time{}, errors.New("pgx: connection refused")
			},
		}
		svc, auditPub, _, _ := newCreateService(t, repo)
		resp, err := svc.CreateApiKey(context.Background(), &authv1.CreateApiKeyRequest{
			UserId: userID.String(),
			Name:   "X",
		})
		if resp != nil {
			t.Fatalf("resp=%+v want nil on insert failure", resp)
		}
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("err=%v want *connect.Error", err)
		}
		if cerr.Code() != connect.CodeInternal {
			t.Fatalf("code=%s want Internal", cerr.Code())
		}
		if auditPub.hits() != 0 {
			t.Fatalf("audit hits=%d want 0 (audit MUST NOT emit when PG fails)", auditPub.hits())
		}
	})
}
