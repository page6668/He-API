// Story 5.4 — 5.4-UNIT-021..030 (NotifyMonthlyCapThreshold handler: SETNX
// dedupe, auth-svc lookup, locale render, HTML-escape, fail paths).
package handlers_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/authsvcclient"
	"github.com/he-api/he-api/apps/notification-svc/internal/handlers"
)

type fakeDedupe struct {
	setnxResult bool
	setnxErr    error
	setnxKeys   []string
	delKeys     []string
}

func (f *fakeDedupe) SetNX(_ context.Context, key string) (bool, error) {
	f.setnxKeys = append(f.setnxKeys, key)
	return f.setnxResult, f.setnxErr
}

func (f *fakeDedupe) Del(_ context.Context, key string) error {
	f.delKeys = append(f.delKeys, key)
	return nil
}

type fakeAuthCtx struct {
	cc    authsvcclient.CapContext
	err   error
	calls int
}

func (f *fakeAuthCtx) GetCapNotificationContext(_ context.Context, _ string) (authsvcclient.CapContext, error) {
	f.calls++
	return f.cc, f.err
}

const testKeyID = "0b6f3b2e-1c4a-4d5e-8f90-1a2b3c4d5e6f"

func capCtx() authsvcclient.CapContext {
	return authsvcclient.CapContext{
		UserEmail:            "alex@example.com",
		UserLocale:           "en",
		UserDisplayName:      "Alex",
		KeyName:              "prod-key",
		KeyMonthlyCostCapUSD: "50.00",
	}
}

func callNotify(t *testing.T, srv *handlers.CapThresholdServer, threshold notificationv1.ThresholdLevel, keyID string) (*notificationv1.NotifyMonthlyCapThresholdResponse, error) {
	t.Helper()
	resp, err := srv.NotifyMonthlyCapThreshold(context.Background(),
		connect.NewRequest(&notificationv1.NotifyMonthlyCapThresholdRequest{
			ApiKeyId: keyID, Threshold: threshold,
		}))
	if resp == nil {
		return nil, err
	}
	return resp.Msg, err
}

// UNIT-021: WARNING_80 first-fire → SETNX wins → fetch + send warning template.
func TestNotify_Warning80_FirstFire_Sends(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxResult: true}
	auth := &fakeAuthCtx{cc: capCtx()}
	sender := &fakeSender{id: "msg-1"}
	srv := handlers.NewCapThresholdServer(ded, auth, sender, nil)

	resp, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80, testKeyID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.GetEmailSent() || resp.GetWasAlreadyNotified() {
		t.Fatalf("want email_sent=true already_notified=false; got %+v", resp)
	}
	if len(ded.setnxKeys) != 1 || !strings.HasPrefix(ded.setnxKeys[0], "keystate:apikey:cap_warning_80_notified:") {
		t.Fatalf("dedupe key=%v", ded.setnxKeys)
	}
	if sender.last.To != "alex@example.com" {
		t.Fatalf("To=%q", sender.last.To)
	}
	// BR-2.7 money rendering + 80% threshold present in the warning body.
	if !strings.Contains(sender.last.TextBody, "50.00") || !strings.Contains(sender.last.TextBody, "80%") {
		t.Fatalf("warning body missing cap/threshold: %q", sender.last.TextBody)
	}
}

// UNIT-022: WARNING_80 second-fire → SETNX loses → already_notified, no send.
func TestNotify_Warning80_AlreadyNotified_NoSend(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxResult: false} // lost the claim
	auth := &fakeAuthCtx{cc: capCtx()}
	sender := &fakeSender{id: "msg-1"}
	srv := handlers.NewCapThresholdServer(ded, auth, sender, nil)

	resp, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80, testKeyID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.GetWasAlreadyNotified() || resp.GetEmailSent() {
		t.Fatalf("want already_notified=true email_sent=false; got %+v", resp)
	}
	if auth.calls != 0 {
		t.Fatalf("lost claim must skip auth lookup; calls=%d", auth.calls)
	}
	if sender.last.To != "" {
		t.Fatalf("lost claim must not send; To=%q", sender.last.To)
	}
}

// UNIT-023: TRIPPED first-fire → send tripped template; NO threshold_pct/precise spend.
func TestNotify_Tripped_FirstFire_Sends(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxResult: true}
	sender := &fakeSender{id: "msg-2"}
	srv := handlers.NewCapThresholdServer(ded, &fakeAuthCtx{cc: capCtx()}, sender, nil)

	resp, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED, testKeyID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.GetEmailSent() {
		t.Fatal("want email_sent=true")
	}
	if !strings.HasPrefix(ded.setnxKeys[0], "keystate:apikey:cap_tripped_notified:") {
		t.Fatalf("dedupe key=%v", ded.setnxKeys)
	}
	// BR-2.10: tripped body carries the cap but NOT an 80% threshold figure.
	if !strings.Contains(sender.last.TextBody, "50.00") {
		t.Fatalf("tripped body missing cap: %q", sender.last.TextBody)
	}
	if strings.Contains(sender.last.TextBody, "80%") {
		t.Fatalf("tripped body must not leak threshold pct: %q", sender.last.TextBody)
	}
}

// UNIT-024: invalid threshold → InvalidArgument, no dedupe/send.
func TestNotify_InvalidThreshold(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxResult: true}
	srv := handlers.NewCapThresholdServer(ded, &fakeAuthCtx{}, &fakeSender{}, nil)
	_, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_UNSPECIFIED, testKeyID)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code=%v want InvalidArgument", connect.CodeOf(err))
	}
	if len(ded.setnxKeys) != 0 {
		t.Fatal("invalid threshold must not touch dedupe")
	}
}

// UNIT-024b: invalid UUID → InvalidArgument.
func TestNotify_InvalidUUID(t *testing.T) {
	t.Parallel()
	srv := handlers.NewCapThresholdServer(&fakeDedupe{}, &fakeAuthCtx{}, &fakeSender{}, nil)
	_, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED, "not-a-uuid")
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code=%v want InvalidArgument", connect.CodeOf(err))
	}
}

// UNIT-025: lookup NotFound → NotFound + dedupe unclaimed (re-fire next time).
func TestNotify_LookupNotFound_Unclaims(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxResult: true}
	auth := &fakeAuthCtx{err: connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))}
	srv := handlers.NewCapThresholdServer(ded, auth, &fakeSender{}, nil)

	_, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED, testKeyID)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code=%v want NotFound", connect.CodeOf(err))
	}
	if len(ded.delKeys) != 1 {
		t.Fatalf("lookup failure must un-claim the dedupe; delKeys=%v", ded.delKeys)
	}
}

// UNIT-027: SendGrid failure after claim → Internal; dedupe STAYS claimed (BR-2.4).
func TestNotify_SendFails_KeepsClaim(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxResult: true}
	sender := &fakeSender{err: errors.New("sendgrid down")}
	srv := handlers.NewCapThresholdServer(ded, &fakeAuthCtx{cc: capCtx()}, sender, nil)

	_, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED, testKeyID)
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code=%v want Internal", connect.CodeOf(err))
	}
	if len(ded.delKeys) != 0 {
		t.Fatalf("send failure must KEEP the claim (BR-2.4); delKeys=%v", ded.delKeys)
	}
}

// UNIT-028: SETNX error (Redis down) → fail-OPEN, proceed to send.
func TestNotify_SetNXError_FailOpen_Sends(t *testing.T) {
	t.Parallel()
	ded := &fakeDedupe{setnxErr: errors.New("redis down")}
	sender := &fakeSender{id: "msg-3"}
	srv := handlers.NewCapThresholdServer(ded, &fakeAuthCtx{cc: capCtx()}, sender, nil)

	resp, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80, testKeyID)
	if err != nil {
		t.Fatalf("fail-open expected; err=%v", err)
	}
	if !resp.GetEmailSent() {
		t.Fatal("fail-open must still send")
	}
}

// UNIT-029: HTML-escape — a malicious key name renders escaped in the HTML body.
func TestNotify_HTMLEscape(t *testing.T) {
	t.Parallel()
	cc := capCtx()
	cc.KeyName = "<script>alert(1)</script>"
	sender := &fakeSender{id: "msg-4"}
	srv := handlers.NewCapThresholdServer(&fakeDedupe{setnxResult: true}, &fakeAuthCtx{cc: cc}, sender, nil)

	if _, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED, testKeyID); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(sender.last.HTMLBody, "<script>alert(1)</script>") {
		t.Fatalf("HTML body must escape the key name; got %q", sender.last.HTMLBody)
	}
	if !strings.Contains(sender.last.HTMLBody, "&lt;script&gt;") {
		t.Fatalf("HTML body must contain escaped form; got %q", sender.last.HTMLBody)
	}
}

// UNIT-030: locale resolution — ja renders the ja template ([en-pending] subject).
func TestNotify_LocaleJa(t *testing.T) {
	t.Parallel()
	cc := capCtx()
	cc.UserLocale = "ja"
	sender := &fakeSender{id: "msg-5"}
	srv := handlers.NewCapThresholdServer(&fakeDedupe{setnxResult: true}, &fakeAuthCtx{cc: cc}, sender, nil)

	if _, err := callNotify(t, srv, notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80, testKeyID); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(sender.last.Subject, "[en-pending]") {
		t.Fatalf("ja template subject should carry the pending marker; got %q", sender.last.Subject)
	}
}
