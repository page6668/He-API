// Story 9.3 AC2 — UsageLogExportWorker tests.
//
//   - 9.3-UNIT-025 [P0] claim guard: already-claimed row → ack+skip, no dump
//   - 9.3-UNIT-024 [P0] signed URL never reaches failure_reason
//   - 9.3-UNIT-028     OSS key = usage-log-exports/{user}/{export}.{ext}
//   - 9.3-UNIT-032     failure stage attribution (ch_dump / oss_* / email_send)
//   - 9.3-UNIT-033     unknown format on the event → mark failed (don't guess)
//   - happy path: dump → upload → sign → complete → email(format,rowCount) → audit
package workers_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
	"github.com/he-api/he-api/apps/analytics-svc/internal/workers"
)

// --- fakes ---

type fakeStore struct {
	claimed       bool
	claimErr      error
	completedKey  string
	completedExp  time.Time
	failedReason  string
	markFailedHit bool
	emailCtx      workers.EmailContext
}

func (s *fakeStore) MarkProcessing(context.Context, string) (bool, error) {
	return s.claimed, s.claimErr
}
func (s *fakeStore) MarkCompleted(_ context.Context, _, key string, exp time.Time) error {
	s.completedKey, s.completedExp = key, exp
	return nil
}
func (s *fakeStore) MarkFailed(_ context.Context, _, reason string) error {
	s.markFailedHit = true
	s.failedReason = reason
	return nil
}
func (s *fakeStore) LookupEmailContext(context.Context, string) (workers.EmailContext, error) {
	return s.emailCtx, nil
}

type fakeUploader struct {
	putKey  string
	putErr  error
	signErr error
	signURL string
}

func (u *fakeUploader) PutObject(_ context.Context, key string, body io.Reader) error {
	u.putKey = key
	_, _ = io.Copy(io.Discard, body)
	return u.putErr
}
func (u *fakeUploader) SignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	if u.signErr != nil {
		return "", u.signErr
	}
	if u.signURL == "" {
		u.signURL = "https://oss.example/SECRET-SIGNED-URL?sig=abcdef"
	}
	return u.signURL, nil
}

type fakeEmail struct {
	calls     int
	gotFormat string
	gotRows   int64
	gotURL    string
	err       error
}

func (e *fakeEmail) Send(_ context.Context, _ string, _ workers.EmailContext, signedURL string, _ time.Time, format string, rowCount int64) error {
	e.calls++
	e.gotURL, e.gotFormat, e.gotRows = signedURL, format, rowCount
	return e.err
}

type fakeAudit struct {
	completed   int
	failed      int
	failedStage string
}

func (a *fakeAudit) EmitCompleted(context.Context, string, string, int64) { a.completed++ }
func (a *fakeAudit) EmitFailed(_ context.Context, _, _, _, stage string) {
	a.failed++
	a.failedStage = stage
}

type fakeFetcher struct {
	rows []dumps.LogRow
	err  error
}

func (f *fakeFetcher) FetchLogRows(context.Context, string, time.Time, time.Time) ([]dumps.LogRow, error) {
	return f.rows, f.err
}

type stubReader struct{}

func (stubReader) FetchMessage(context.Context) (kafka.Message, error) { return kafka.Message{}, nil }
func (stubReader) CommitMessages(context.Context, ...kafka.Message) error { return nil }
func (stubReader) Close() error                                          { return nil }

func eventMsg(t *testing.T, exportID, userID, format string) kafka.Message {
	t.Helper()
	b, err := proto.Marshal(&usagelogv1.UsageLogExportRequestedEvent{
		ExportId: exportID, UserId: userID, Format: format,
		RangeStart: timestamppb.New(time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC)),
		RangeEnd:   timestamppb.New(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return kafka.Message{Value: b}
}

func newWorker(t *testing.T, store *fakeStore, up *fakeUploader, fetch *fakeFetcher, email *fakeEmail, audit *fakeAudit) *workers.UsageLogExportWorker {
	t.Helper()
	w := workers.NewUsageLogExportWorker(
		stubReader{}, store, up, dumps.NewRequestLogsExportDumper(fetch), email, audit, nil,
	)
	w.Now = func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }
	return w
}

func sampleRows() []dumps.LogRow {
	return []dumps.LogRow{{HeRequestID: "req_1", Ts: time.Now().UTC(), Model: "qwen-max", StatusCode: 200}}
}

// 9.3-UNIT-025 [P0]: already-claimed row → ack+skip, dumper/uploader untouched.
func TestProcess_AlreadyClaimed_Skip(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimed: false}
	up := &fakeUploader{}
	fetch := &fakeFetcher{rows: sampleRows()}
	w := newWorker(t, store, up, fetch, &fakeEmail{}, &fakeAudit{})

	if err := w.Process(context.Background(), eventMsg(t, "ul-1", "u-1", "csv")); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if up.putKey != "" {
		t.Errorf("uploader must NOT run on an already-claimed row")
	}
}

// happy path → upload key format-aware, complete(now+24h), email(format,rows), audit.
func TestProcess_HappyPath_Completed(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimed: true, emailCtx: workers.EmailContext{Email: "a@b.co", Locale: "en"}}
	up := &fakeUploader{}
	email := &fakeEmail{}
	audit := &fakeAudit{}
	w := newWorker(t, store, up, &fakeFetcher{rows: sampleRows()}, email, audit)

	if err := w.Process(context.Background(), eventMsg(t, "ul-9", "u-7", "csv")); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if up.putKey != "usage-log-exports/u-7/ul-9.csv" {
		t.Errorf("OSS key: got %q", up.putKey)
	}
	if !store.completedExp.Equal(w.Now().Add(24 * time.Hour)) {
		t.Errorf("expiry: got %v", store.completedExp)
	}
	if email.calls != 1 || email.gotFormat != "csv" || email.gotRows != 1 {
		t.Errorf("email: %+v", email)
	}
	if audit.completed != 1 {
		t.Errorf("audit completed not emitted")
	}
}

// 9.3-UNIT-024 [P0]: a sign-URL / email failure NEVER puts the signed URL into
// failure_reason. (Also covers oss_sign_url stage attribution.)
func TestProcess_FailureReason_NeverLeaksSignedURL(t *testing.T) {
	t.Parallel()
	const secret = "https://oss.example/SECRET-SIGNED-URL?sig=abcdef"
	cases := []struct {
		name      string
		up        *fakeUploader
		email     *fakeEmail
		wantStage string
	}{
		{"oss_upload", &fakeUploader{putErr: errors.New("net down")}, &fakeEmail{}, "oss_upload"},
		{"oss_sign_url", &fakeUploader{signErr: errors.New("sts denied")}, &fakeEmail{}, "oss_sign_url"},
		{"email_send", &fakeUploader{signURL: secret}, &fakeEmail{err: errors.New("sendgrid 500")}, "email_send"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{claimed: true, emailCtx: workers.EmailContext{Email: "a@b.co"}}
			audit := &fakeAudit{}
			w := newWorker(t, store, tc.up, &fakeFetcher{rows: sampleRows()}, tc.email, audit)
			_ = w.Process(context.Background(), eventMsg(t, "ul-x", "u-1", "json"))

			if !store.markFailedHit {
				t.Fatalf("expected MarkFailed")
			}
			if strings.Contains(store.failedReason, secret) || strings.Contains(store.failedReason, "SECRET") {
				t.Errorf("failure_reason LEAKED the signed URL: %q", store.failedReason)
			}
			if audit.failedStage != tc.wantStage {
				t.Errorf("stage: got %q want %q", audit.failedStage, tc.wantStage)
			}
		})
	}
}

// 9.3-UNIT-032: ch_dump failure attributed + reason sanitized.
func TestProcess_DumpFailure_ChDumpStage(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimed: true}
	audit := &fakeAudit{}
	w := newWorker(t, store, &fakeUploader{}, &fakeFetcher{err: errors.New("clickhouse unreachable")}, &fakeEmail{}, audit)
	_ = w.Process(context.Background(), eventMsg(t, "ul-x", "u-1", "csv"))
	if audit.failedStage != "ch_dump" || !strings.Contains(store.failedReason, "ch dump failed") {
		t.Errorf("ch_dump failure mis-attributed: stage=%q reason=%q", audit.failedStage, store.failedReason)
	}
}

// 9.3-UNIT-033: unknown format on the event → mark failed before any claim/dump.
func TestProcess_UnknownFormat_Failed(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimed: true}
	audit := &fakeAudit{}
	up := &fakeUploader{}
	w := newWorker(t, store, up, &fakeFetcher{rows: sampleRows()}, &fakeEmail{}, audit)
	_ = w.Process(context.Background(), eventMsg(t, "ul-x", "u-1", "xml"))
	if !store.markFailedHit || audit.failedStage != "validate" {
		t.Errorf("unknown format not failed cleanly: hit=%v stage=%q", store.markFailedHit, audit.failedStage)
	}
	if up.putKey != "" {
		t.Errorf("uploader must not run on a bad-format event")
	}
}
