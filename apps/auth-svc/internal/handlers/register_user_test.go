package handlers_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/password"
)

// --- fakes ---------------------------------------------------------------

type fakeHIBP struct {
	err error // nil = clean password; password.ErrPasswordBreached / ErrHIBPUnavailable for failure paths
	mu  sync.Mutex
	calls int
}

func (f *fakeHIBP) CheckBreached(_ context.Context, _ []byte) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.err
}

type fakeNotification struct {
	mu          sync.Mutex
	calls       int
	lastTo      string
	lastLocale  string
	lastToken   string
	lastLink    string
	err         error // notification.ErrTransient / ErrPermanent
}

func (f *fakeNotification) SendVerificationEmail(_ context.Context, to, locale, tok, link string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastTo, f.lastLocale, f.lastToken, f.lastLink = to, locale, tok, link
	return f.err
}

type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
	err    error
}

func (r *recordingAudit) Publish(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}

func (r *recordingAudit) only() audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 1 {
		panic("recordingAudit.only(): want exactly 1 event")
	}
	return r.events[0]
}

func (r *recordingAudit) byType(et audit.EventType) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.events {
		if e.EventType == et {
			out = append(out, e)
		}
	}
	return out
}

// --- harness -------------------------------------------------------------

type harness struct {
	srv    *handlers.AuthServer
	mock   pgxmock.PgxConnIface
	mr     *miniredis.Miniredis
	rdb    *redis.Client
	hibp   *fakeHIBP
	notif  *fakeNotification
	auditP *recordingAudit
}

// fixedNow is the deterministic timestamp every handler test uses for audit
// + token expiry calculations.
var fixedNow = time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)

func newHarness(t *testing.T) *harness {
	t.Helper()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet pgx expectations: %v", err)
		}
		mock.Close(context.Background())
	})
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	h := &harness{
		mock:   mock,
		mr:     mr,
		rdb:    rdb,
		hibp:   &fakeHIBP{},
		notif:  &fakeNotification{},
		auditP: &recordingAudit{},
	}
	h.srv = handlers.NewAuthServer(handlers.AuthServer{
		DB:             mock,
		Redis:          rdb,
		HIBP:           h.hibp,
		Notification:   h.notif,
		Audit:          h.auditP,
		Clock:          func() time.Time { return fixedNow },
		ConsoleBaseURL: "https://console.he-api.com",
		Logger:         slog.Default(),
	})
	return h
}

func (h *harness) call(t *testing.T, in *authv1.RegisterUserRequest) (*authv1.RegisterUserResponse, error) {
	t.Helper()
	resp, err := h.srv.RegisterUser(context.Background(), connect.NewRequest(in))
	if resp == nil {
		return nil, err
	}
	return resp.Msg, err
}

func validRequest() *authv1.RegisterUserRequest {
	return &authv1.RegisterUserRequest{
		Email:     "user@example.com",
		Password:  "correct horse battery staple",
		Locale:    "en",
		ClientIp:  "1.2.3.4",
		UserAgent: "Go-http-client/1.1",
	}
}

// --- tests ---------------------------------------------------------------

// Scenario: 2.2-UNIT-030
// Email failing mail.ParseAddress → InvalidArgument / 400_invalid_email.
func TestRegisterUser_RejectsMalformedEmail(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	in := validRequest()
	in.Email = "not-an-email"
	_, err := h.call(t, in)
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidEmail)
}

// Scenario: 2.2-UNIT-031
// Email length over 254 → 400_invalid_email.
func TestRegisterUser_RejectsEmailOver254Chars(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	in := validRequest()
	in.Email = strings.Repeat("a", 250) + "@x.io"
	_, err := h.call(t, in)
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidEmail)
}

// Scenario: 2.2-UNIT-031 (local part)
// Local part > 64 chars → 400_invalid_email.
func TestRegisterUser_RejectsLocalPartOver64Chars(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	in := validRequest()
	in.Email = strings.Repeat("a", 65) + "@x.io"
	_, err := h.call(t, in)
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidEmail)
}

// Scenario: 2.2-UNIT-046
// Locale not in 10-MVP tuple → 400_invalid_locale.
func TestRegisterUser_RejectsInvalidLocale(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	in := validRequest()
	in.Locale = "klingon"
	_, err := h.call(t, in)
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidLocale)
}

// Scenario: 2.2-UNIT-035 + 2.2-UNIT-036
// Rate limit triggers BEFORE password validation (no HIBP call, no bcrypt
// hash, no DB INSERT). Returns 429_rate_limit_signup with a Retry-After
// metadata header.
func TestRegisterUser_RateLimitBeforePasswordValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Pre-fill the rate-limit key to its ceiling so the next call trips.
	for i := 0; i < 5; i++ {
		if err := h.rdb.Incr(context.Background(), "ratelimit:signup:ip:1.2.3.4").Err(); err != nil {
			t.Fatalf("seed incr: %v", err)
		}
	}
	// Set a TTL so RetryAfter has a useful value.
	if err := h.rdb.Expire(context.Background(), "ratelimit:signup:ip:1.2.3.4", 5*time.Minute).Err(); err != nil {
		t.Fatalf("seed expire: %v", err)
	}

	_, err := h.call(t, validRequest())
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusRateLimitSignup)

	// HIBP and notification must NOT have been called (UNIT-035 — early abort).
	if h.hibp.calls != 0 {
		t.Errorf("HIBP called %d times, want 0 (rate-limited path should abort early)", h.hibp.calls)
	}
	if h.notif.calls != 0 {
		t.Errorf("notification called %d times, want 0", h.notif.calls)
	}
	// Retry-After metadata MUST be set on the Connect error.
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if connectErr.Meta().Get(handlers.MetaRetryAfterSeconds) == "" {
			t.Errorf("Retry-After metadata not set on 429 response")
		}
	}
}

// Scenario: 2.2-UNIT-032
// Password < 10 chars → 400_password_too_short. No HIBP call (cheaper
// rejection).
func TestRegisterUser_RejectsShortPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	in := validRequest()
	in.Password = "shortpw"
	_, err := h.call(t, in)
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusPasswordTooShort)
	if h.hibp.calls != 0 {
		t.Errorf("HIBP called on too-short password; should reject before HIBP")
	}
}

// Scenario: 2.2-UNIT-033
// HIBP returns ErrPasswordBreached → 400_password_breached.
func TestRegisterUser_RejectsBreachedPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.hibp.err = password.ErrPasswordBreached
	_, err := h.call(t, validRequest())
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusPasswordBreached)
}

// Scenario: 2.2-UNIT-034
// HIBP unavailable → 503_hibp_unavailable (fail-closed). NOT a permissive
// 200 / pass-through.
func TestRegisterUser_HIBPUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.hibp.err = password.ErrHIBPUnavailable
	_, err := h.call(t, validRequest())
	assertConnectStatus(t, err, connect.CodeUnavailable, handlers.StatusHIBPUnavailable)
}

// Scenario: 2.2-UNIT-040 + 2.2-UNIT-041 + 2.2-UNIT-042 + 2.2-UNIT-044
// Happy path: INSERT + token Store + notification SendEmail + audit auth.signup.
// Response shape is {status: "pending_verification"} — NO user_id.
func TestRegisterUser_HappyPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	uID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	h.mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("user@example.com", pgxmock.AnyArg(), "en").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uID))

	resp, err := h.call(t, validRequest())
	if err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if resp.GetStatus() != "pending_verification" {
		t.Errorf("Status = %q, want pending_verification", resp.GetStatus())
	}
	// UNIT-044: NO user_id in response — confirmed by the proto only carrying status.

	// UNIT-041: notification called with correct args.
	if h.notif.calls != 1 {
		t.Fatalf("notification.calls = %d, want 1", h.notif.calls)
	}
	if h.notif.lastTo != "user@example.com" {
		t.Errorf("notification.to = %q, want user@example.com", h.notif.lastTo)
	}
	if h.notif.lastLocale != "en" {
		t.Errorf("notification.locale = %q, want en", h.notif.lastLocale)
	}
	wantLinkPrefix := "https://console.he-api.com/en/verify-email?token="
	if !strings.HasPrefix(h.notif.lastLink, wantLinkPrefix) {
		t.Errorf("notification.link = %q, want prefix %q", h.notif.lastLink, wantLinkPrefix)
	}
	if !strings.HasSuffix(h.notif.lastLink, h.notif.lastToken) {
		t.Errorf("notification.link does not end with token; link=%s token=%s", h.notif.lastLink, h.notif.lastToken)
	}

	// UNIT-042: Redis key auth:email_verify:{sha256(token)} exists with payload + 24h TTL.
	keys := h.mr.Keys()
	var verifyKey string
	for _, k := range keys {
		if strings.HasPrefix(k, "auth:email_verify:") && !strings.HasPrefix(k, "auth:email_verify:dummy:") {
			verifyKey = k
			break
		}
	}
	if verifyKey == "" {
		t.Fatalf("no auth:email_verify:* key written to Redis; keys=%v", keys)
	}
	if ttl := h.mr.TTL(verifyKey); ttl < 23*time.Hour+59*time.Minute || ttl > 24*time.Hour+5*time.Second {
		t.Errorf("verify key TTL = %v, want ~24h", ttl)
	}

	// UNIT-040: audit auth.signup with success=true, no plaintext email.
	events := h.auditP.byType(audit.EventSignup)
	if len(events) != 1 {
		t.Fatalf("audit auth.signup events = %d, want 1; all=%+v", len(events), h.auditP.events)
	}
	e := events[0]
	if !e.Success {
		t.Errorf("audit Success = false, want true")
	}
	if e.EmailHash == "" {
		t.Errorf("audit EmailHash empty")
	}
	if e.UserID == "" {
		t.Errorf("audit UserID empty")
	}
	// Plaintext email MUST NOT appear in any field. Reflective scan would be
	// overkill; spot-check the JSON serialization.
	js := mustMarshalEvent(t, e)
	if strings.Contains(js, "user@example.com") {
		t.Errorf("audit event contains plaintext email: %s", js)
	}
	if strings.Contains(js, "correct horse battery staple") {
		t.Errorf("audit event contains plaintext password: %s", js)
	}
}

// Scenario: 2.2-UNIT-045
// Empty locale → defaults to 'en' (BR-1.9).
func TestRegisterUser_EmptyLocaleDefaultsToEn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	uID := uuid.New()
	h.mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("user@example.com", pgxmock.AnyArg(), "en").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uID))

	in := validRequest()
	in.Locale = ""
	if _, err := h.call(t, in); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if h.notif.lastLocale != "en" {
		t.Errorf("notification.locale = %q, want en (BR-1.9 default)", h.notif.lastLocale)
	}
}

// Scenario: 2.2-UNIT-037 + 2.2-UNIT-039 — anti-enumeration on duplicate email.
// Returns SAME success-shaped response; emits auth.signup_duplicate_attempt
// audit; the bcrypt + dummy Redis SETEX both run (equivalent CPU/IO budget);
// notification-svc is NOT called; no real verify-token key is written.
func TestRegisterUser_DuplicateEmail_AntiEnumeration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "idx_users_email"}
	h.mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("user@example.com", pgxmock.AnyArg(), "en").
		WillReturnError(pgErr)

	resp, err := h.call(t, validRequest())
	if err != nil {
		t.Fatalf("RegisterUser (duplicate) = %v, want nil (anti-enumeration success response)", err)
	}
	if resp.GetStatus() != "pending_verification" {
		t.Errorf("Status = %q, want pending_verification (response shape must match success)", resp.GetStatus())
	}

	// HIBP + bcrypt must run (equivalent CPU work).
	if h.hibp.calls != 1 {
		t.Errorf("HIBP.calls = %d, want 1 (duplicate path keeps full validation work)", h.hibp.calls)
	}
	// notification-svc must NOT be invoked.
	if h.notif.calls != 0 {
		t.Errorf("notification.calls = %d, want 0 (duplicate path never re-emails)", h.notif.calls)
	}
	// No real verify-token key; only the dummy key.
	for _, k := range h.mr.Keys() {
		if strings.HasPrefix(k, "auth:email_verify:") && !strings.HasPrefix(k, "auth:email_verify:dummy:") {
			t.Errorf("duplicate path wrote real verify-token key %q; should only be dummy", k)
		}
	}
	// Dummy SETEX should have produced exactly one key under auth:email_verify:dummy:*.
	dummyCount := 0
	for _, k := range h.mr.Keys() {
		if strings.HasPrefix(k, "auth:email_verify:dummy:") {
			dummyCount++
		}
	}
	if dummyCount != 1 {
		t.Errorf("dummy SETEX key count = %d, want 1 (anti-enumeration timing parity)", dummyCount)
	}

	// Audit: auth.signup_duplicate_attempt with email_hash, NOT raw email.
	dupEvents := h.auditP.byType(audit.EventSignupDuplicateAttempt)
	if len(dupEvents) != 1 {
		t.Fatalf("audit auth.signup_duplicate_attempt events = %d, want 1", len(dupEvents))
	}
	if dupEvents[0].EmailHash == "" {
		t.Errorf("audit duplicate event missing email_hash")
	}
	js := mustMarshalEvent(t, dupEvents[0])
	if strings.Contains(js, "user@example.com") {
		t.Errorf("audit duplicate event leaked plaintext email: %s", js)
	}
	// success=true MUST NOT appear on the duplicate event (it was not a real signup).
	if dupEvents[0].Success {
		t.Errorf("audit duplicate event Success=true, want false")
	}
}

// Scenario: 2.2-UNIT-043 — partial-failure handling.
// Notification SendVerificationEmail fails → 500_email_send_failed; user row
// was already INSERTed (handler doesn't roll back PG); audit emits
// email_send_failed event.
func TestRegisterUser_NotificationFailureReturns500EmailSendFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	uID := uuid.New()
	h.mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("user@example.com", pgxmock.AnyArg(), "en").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uID))
	h.notif.err = notification.ErrTransient

	_, err := h.call(t, validRequest())
	assertConnectStatus(t, err, connect.CodeInternal, handlers.StatusEmailSendFailed)

	// Audit must record email_send_failed.
	if len(h.auditP.byType(audit.EventEmailSendFailed)) != 1 {
		t.Errorf("audit email_send_failed events = %d, want 1; all=%+v", len(h.auditP.byType(audit.EventEmailSendFailed)), h.auditP.events)
	}
}

// Scenario: 2.2-UNIT-043 (PG failure path).
// PG INSERT non-23505 error → 500 (no Redis writes happen).
func TestRegisterUser_PGInsertFailureNoSideEffects(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mock.ExpectQuery(`INSERT INTO he_api\.users`).
		WithArgs("user@example.com", pgxmock.AnyArg(), "en").
		WillReturnError(errors.New("pg unreachable"))

	_, err := h.call(t, validRequest())
	if err == nil {
		t.Fatalf("err = nil, want non-nil on PG failure")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
	// No Redis writes (no verify token, no dummy).
	for _, k := range h.mr.Keys() {
		if strings.HasPrefix(k, "auth:email_verify") {
			t.Errorf("PG failure path wrote Redis key %q; should have NO side effects", k)
		}
	}
	// No notification call.
	if h.notif.calls != 0 {
		t.Errorf("notification.calls = %d, want 0 on PG failure", h.notif.calls)
	}
}

// Helper: parse a Connect error and assert its code + the canonical
// status code embedded in the message.
func assertConnectStatus(t *testing.T, err error, wantCode connect.Code, wantStatus string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want non-nil (code=%v, status=%s)", wantCode, wantStatus)
	}
	if got := connect.CodeOf(err); got != wantCode {
		t.Errorf("Connect code = %v, want %v (err=%v)", got, wantCode, err)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("err is not *connect.Error: %T %v", err, err)
	}
	if !strings.Contains(connectErr.Message(), wantStatus) {
		t.Errorf("Connect message = %q, want to contain %q", connectErr.Message(), wantStatus)
	}
}

func mustMarshalEvent(t *testing.T, e audit.Event) string {
	t.Helper()
	b, err := e.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	return string(b)
}
