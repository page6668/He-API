package workers

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	gdprv1 "github.com/he-api/he-api/packages/proto/gen/go/he/gdpr/v1"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
)

// ---- Fake interfaces under test ----

type fakeStore struct {
	mu          sync.Mutex
	claimed     bool
	claimReturn bool
	claimErr    error
	completed   []completion
	failed      []failure
	emailCtx    EmailContext
	emailErr    error
}

type completion struct {
	exportID, key string
	expires       time.Time
}
type failure struct{ exportID, reason string }

func (f *fakeStore) MarkProcessing(_ context.Context, exportID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimed = true
	return f.claimReturn, f.claimErr
}
func (f *fakeStore) MarkCompleted(_ context.Context, exportID, key string, exp time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, completion{exportID, key, exp})
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, exportID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, failure{exportID, reason})
	return nil
}
func (f *fakeStore) LookupEmailContext(_ context.Context, _ string) (EmailContext, error) {
	return f.emailCtx, f.emailErr
}

type fakeUploader struct {
	put     map[string][]byte
	signURL string
	putErr  error
	signErr error
	mu      sync.Mutex
}

func (f *fakeUploader) PutObject(_ context.Context, key string, body io.Reader) error {
	if f.putErr != nil {
		return f.putErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.put == nil {
		f.put = map[string][]byte{}
	}
	f.put[key] = b
	f.mu.Unlock()
	return nil
}
func (f *fakeUploader) SignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	if f.signErr != nil {
		return "", f.signErr
	}
	if f.signURL != "" {
		return f.signURL, nil
	}
	return "https://signed/example", nil
}

type fakeEmail struct {
	mu    sync.Mutex
	sends []string
	err   error
}

func (f *fakeEmail) Send(_ context.Context, exportID string, _ EmailContext, _ string, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, exportID)
	return nil
}

type fakeAudit struct {
	mu        sync.Mutex
	completed []string
	failed    []string
}

func (f *fakeAudit) EmitCompleted(_ context.Context, exportID, _ string, _ int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, exportID)
}
func (f *fakeAudit) EmitFailed(_ context.Context, exportID, _, _, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, exportID)
}

// recordingDumper writes a deterministic JSON body so the test asserts the
// zip contents.
type recordingDumper struct {
	name string
	body []byte
	err  error
}

func (d *recordingDumper) Name() string { return d.name }
func (d *recordingDumper) Dump(_ context.Context, _ string, w io.Writer) (int64, error) {
	if d.err != nil {
		return 0, d.err
	}
	n, err := w.Write(d.body)
	return int64(n), err
}

func sevenDumpers() []dumps.Dumper {
	names := []string{
		"users.json", "api_keys.json", "subscriptions.json",
		"balances.json", "recharge_orders.json",
		"content_safety_logs.json", "request_logs.json",
	}
	out := make([]dumps.Dumper, 0, 7)
	for _, n := range names {
		out = append(out, &recordingDumper{name: n, body: []byte(`[{"k":1}]`)})
	}
	return out
}

func newKafkaMessage(t *testing.T, exportID, userID string) kafka.Message {
	t.Helper()
	body, err := proto.Marshal(&gdprv1.DataExportRequestedEvent{
		ExportId:    exportID,
		UserId:      userID,
		RequestedAt: timestamppb.Now(),
	})
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return kafka.Message{Topic: "gdpr.export.requested", Key: []byte(userID), Value: body}
}

// ---- Tests ----

// 2.6-UNIT-090 — Happy path: BR-4.2 claim succeeds, all 7 dumps run, ZIP
// is uploaded with 7 entries, completed audit emitted, email sent.
func TestProcess_HappyPath_FullChain(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: true, emailCtx: EmailContext{UserID: "u-1", Email: "u@example.com", Locale: "en", Timezone: "UTC"}}
	up := &fakeUploader{signURL: "https://signed/example"}
	em := &fakeEmail{}
	au := &fakeAudit{}
	w := NewGdprExportWorker(&noopReader{}, store, up, sevenDumpers(), em, au, "test-bucket", slog.Default())
	w.Now = func() time.Time { return time.Unix(1700000000, 0).UTC() }

	msg := newKafkaMessage(t, "exp-1", "u-1")
	if err := w.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(store.completed) != 1 || store.completed[0].exportID != "exp-1" {
		t.Errorf("MarkCompleted: %+v", store.completed)
	}
	if len(au.completed) != 1 || au.completed[0] != "exp-1" {
		t.Errorf("EmitCompleted: %+v", au.completed)
	}
	if len(em.sends) != 1 {
		t.Errorf("Email.Send: %+v", em.sends)
	}
	if len(up.put) != 1 {
		t.Fatalf("expected 1 OSS PutObject, got %d", len(up.put))
	}
	for _, body := range up.put {
		r, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("zip open: %v", err)
		}
		if len(r.File) != 7 {
			t.Errorf("zip files: got %d, want 7 (GDPR completeness invariant)", len(r.File))
		}
	}
}

// 2.6-UNIT-091 — BR-4.2 double-consume guard: claimed=false → ack + skip,
// no downstream calls.
func TestProcess_AlreadyClaimed_SkipsWithoutWork(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: false}
	up := &fakeUploader{}
	em := &fakeEmail{}
	au := &fakeAudit{}
	w := NewGdprExportWorker(&noopReader{}, store, up, sevenDumpers(), em, au, "test-bucket", slog.Default())

	msg := newKafkaMessage(t, "exp-1", "u-1")
	if err := w.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(up.put) != 0 || len(em.sends) != 0 || len(au.completed) != 0 || len(au.failed) != 0 {
		t.Errorf("expected zero downstream calls on already-claimed; up=%d email=%d audit_ok=%d audit_fail=%d",
			len(up.put), len(em.sends), len(au.completed), len(au.failed))
	}
}

// 2.6-UNIT-092 — Poison message (invalid proto) returns nil so the
// consumer ACKs and moves on; no MarkProcessing call.
func TestProcess_PoisonMessage_AcksAndSkips(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: true}
	w := NewGdprExportWorker(&noopReader{}, store, &fakeUploader{}, sevenDumpers(), &fakeEmail{}, &fakeAudit{}, "b", slog.Default())

	msg := kafka.Message{Value: []byte("not-a-proto")}
	if err := w.Process(context.Background(), msg); err != nil {
		t.Errorf("expected nil on poison message, got %v", err)
	}
	if store.claimed {
		t.Errorf("MarkProcessing should not be called on poison message")
	}
}

// 2.6-UNIT-093 — Empty export_id or user_id treated as poison.
func TestProcess_EmptyIDs_AcksAndSkips(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: true}
	w := NewGdprExportWorker(&noopReader{}, store, &fakeUploader{}, sevenDumpers(), &fakeEmail{}, &fakeAudit{}, "b", slog.Default())

	msg := newKafkaMessage(t, "", "u-1")
	if err := w.Process(context.Background(), msg); err != nil {
		t.Errorf("expected nil on empty export_id, got %v", err)
	}
	if store.claimed {
		t.Errorf("MarkProcessing should not be called on poison empty IDs")
	}
}

// 2.6-UNIT-094 — OSS upload failure → MarkFailed + EmitFailed (oss_upload
// stage). Email never sent. EmitCompleted not called.
func TestProcess_OSSUploadFailure_AuditsFailure(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: true}
	up := &fakeUploader{putErr: errors.New("oss put failed")}
	em := &fakeEmail{}
	au := &fakeAudit{}
	w := NewGdprExportWorker(&noopReader{}, store, up, sevenDumpers(), em, au, "b", slog.Default())

	msg := newKafkaMessage(t, "exp-1", "u-1")
	if err := w.Process(context.Background(), msg); err == nil {
		t.Fatal("expected error from OSS upload failure")
	}
	if len(store.failed) != 1 || store.failed[0].exportID != "exp-1" {
		t.Errorf("MarkFailed: %+v", store.failed)
	}
	if len(au.failed) != 1 || au.failed[0] != "exp-1" {
		t.Errorf("EmitFailed: %+v", au.failed)
	}
	if len(em.sends) != 0 {
		t.Errorf("Email.Send should not fire on upload failure: %+v", em.sends)
	}
	if len(au.completed) != 0 {
		t.Errorf("EmitCompleted should not fire on upload failure: %+v", au.completed)
	}
}

// 2.6-UNIT-095 — Email send failure → MarkFailed + EmitFailed (email_send
// stage) even though the upload + signed URL succeeded.
func TestProcess_EmailSendFailure_AuditsFailure(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: true, emailCtx: EmailContext{UserID: "u-1", Email: "u@example.com"}}
	up := &fakeUploader{}
	em := &fakeEmail{err: errors.New("sendgrid 5xx")}
	au := &fakeAudit{}
	w := NewGdprExportWorker(&noopReader{}, store, up, sevenDumpers(), em, au, "b", slog.Default())

	msg := newKafkaMessage(t, "exp-1", "u-1")
	if err := w.Process(context.Background(), msg); err == nil {
		t.Fatal("expected error from email send failure")
	}
	if len(au.failed) != 1 {
		t.Errorf("EmitFailed: %+v", au.failed)
	}
}

// 2.6-UNIT-096 — Dumper failure → fan-out errgroup short-circuits;
// MarkFailed + EmitFailed (pg_dump stage).
func TestProcess_DumperFailure_AuditsFailure(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claimReturn: true}
	up := &fakeUploader{}
	em := &fakeEmail{}
	au := &fakeAudit{}
	dumpers := sevenDumpers()
	dumpers[0] = &recordingDumper{name: "users.json", err: errors.New("pg dump query timeout")}
	w := NewGdprExportWorker(&noopReader{}, store, up, dumpers, em, au, "b", slog.Default())

	msg := newKafkaMessage(t, "exp-1", "u-1")
	if err := w.Process(context.Background(), msg); err == nil {
		t.Fatal("expected error from dumper failure")
	}
	if len(au.failed) != 1 {
		t.Errorf("EmitFailed: %+v", au.failed)
	}
	if len(up.put) != 0 {
		t.Errorf("OSS PutObject should not fire when dump fails: got %d uploads", len(up.put))
	}
}

// 2.6-UNIT-097 — Wrong dumper count panics at construction (GDPR
// completeness invariant — guards against partial dumps).
func TestNewGdprExportWorker_DumperCountInvariant(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on wrong dumper count")
		}
	}()
	NewGdprExportWorker(&noopReader{}, &fakeStore{}, &fakeUploader{}, sevenDumpers()[:3], &fakeEmail{}, &fakeAudit{}, "b", slog.Default())
}

// 2.6-UNIT-098 — ConsumerGroupID pinned (Kafka offset semantics depend
// on a stable group name; renaming silently resets offsets).
func TestConsumerGroupIDPinned(t *testing.T) {
	t.Parallel()
	if ConsumerGroupID != "analytics-svc.gdpr-export" {
		t.Errorf("ConsumerGroupID drift: got %q", ConsumerGroupID)
	}
}

// noopReader stub — we don't exercise Run() in unit tests; Process is the
// pure entry point.
type noopReader struct{}

func (*noopReader) FetchMessage(_ context.Context) (kafka.Message, error) {
	return kafka.Message{}, context.Canceled
}
func (*noopReader) CommitMessages(_ context.Context, _ ...kafka.Message) error { return nil }
func (*noopReader) Close() error                                                { return nil }
