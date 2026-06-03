// Story 5.4 — 5.4-UNIT-046..050 (fire-and-forget threshold notifier: proto
// mapping, request-id propagation, detached-context isolation, error swallow).
package notifyclient

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"
)

type fakeUpstream struct {
	gotReq    *notificationv1.NotifyMonthlyCapThresholdRequest
	gotCtxErr error // ctx.Err() observed inside the call
	err       error
	calls     int
}

func (f *fakeUpstream) NotifyMonthlyCapThreshold(
	ctx context.Context,
	req *connect.Request[notificationv1.NotifyMonthlyCapThresholdRequest],
) (*connect.Response[notificationv1.NotifyMonthlyCapThresholdResponse], error) {
	f.calls++
	f.gotReq = req.Msg
	f.gotCtxErr = ctx.Err()
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&notificationv1.NotifyMonthlyCapThresholdResponse{}), nil
}

// newSync builds a Client that runs its goroutine synchronously for tests.
func newSync(up capNotifier) *Client {
	c := NewWithClient(up, nil)
	c.spawn = func(f func()) { f() }
	return c
}

// UNIT-046/047: proto mapping + request-id propagation.
func TestNotify_ProtoMapping_RequestIDPropagated(t *testing.T) {
	up := &fakeUpstream{}
	c := newSync(up)
	ctx := requestid.WithRequestID(context.Background(), "req_abc123")

	c.NotifyCapThresholdAsync(ctx, "key-1", ThresholdTripped)

	if up.calls != 1 {
		t.Fatalf("calls=%d want 1", up.calls)
	}
	if up.gotReq.GetApiKeyId() != "key-1" {
		t.Fatalf("api_key_id=%q", up.gotReq.GetApiKeyId())
	}
	if up.gotReq.GetThreshold() != notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED {
		t.Fatalf("threshold=%v want TRIPPED", up.gotReq.GetThreshold())
	}
	if up.gotReq.GetRequestId() != "req_abc123" {
		t.Fatalf("request_id=%q want req_abc123", up.gotReq.GetRequestId())
	}
}

func TestNotify_Warning80_Maps(t *testing.T) {
	up := &fakeUpstream{}
	newSync(up).NotifyCapThresholdAsync(context.Background(), "k", ThresholdWarning80)
	if up.gotReq.GetThreshold() != notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80 {
		t.Fatalf("threshold=%v want WARNING_80", up.gotReq.GetThreshold())
	}
}

// UNIT-048: detached context — a cancelled parent does NOT abort the call.
func TestNotify_DetachedContext_SurvivesParentCancel(t *testing.T) {
	up := &fakeUpstream{}
	c := newSync(up)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // parent already cancelled before the fire

	c.NotifyCapThresholdAsync(ctx, "k", ThresholdTripped)

	if up.calls != 1 {
		t.Fatalf("call must still happen on cancelled parent; calls=%d", up.calls)
	}
	if up.gotCtxErr != nil {
		t.Fatalf("detached ctx must not carry parent cancellation; ctx.Err()=%v", up.gotCtxErr)
	}
}

// UNIT-049: upstream error is swallowed (fire-and-forget; no panic).
func TestNotify_UpstreamError_Swallowed(t *testing.T) {
	up := &fakeUpstream{err: errors.New("unavailable")}
	c := newSync(up)
	c.NotifyCapThresholdAsync(context.Background(), "k", ThresholdTripped) // must not panic
	if up.calls != 1 {
		t.Fatalf("calls=%d want 1", up.calls)
	}
}

// UNIT-050: nil client / nil upstream are no-ops.
func TestNotify_NilSafe(t *testing.T) {
	var c *Client
	c.NotifyCapThresholdAsync(context.Background(), "k", ThresholdTripped) // nil receiver
	(&Client{}).NotifyCapThresholdAsync(context.Background(), "k", ThresholdTripped)
}
