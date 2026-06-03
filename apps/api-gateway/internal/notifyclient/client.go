package notifyclient

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"
)

// Threshold is the gateway-local crossing level, decoupled from the proto enum
// so keypolicy does not depend on the generated package directly.
type Threshold int

const (
	// ThresholdWarning80 = current/cap crossed 0.80 (still < 1.00).
	ThresholdWarning80 Threshold = iota + 1
	// ThresholdTripped = current/cap reached 1.00 (key paused).
	ThresholdTripped
)

func (t Threshold) String() string {
	switch t {
	case ThresholdWarning80:
		return "warning_80"
	case ThresholdTripped:
		return "tripped"
	default:
		return "unspecified"
	}
}

func (t Threshold) proto() notificationv1.ThresholdLevel {
	switch t {
	case ThresholdWarning80:
		return notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80
	case ThresholdTripped:
		return notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED
	default:
		return notificationv1.ThresholdLevel_THRESHOLD_LEVEL_UNSPECIFIED
	}
}

// DefaultFireDeadline bounds the detached gRPC call so the goroutine cannot
// leak (TC-4 — gateway hot path impact is capped at this wallclock).
const DefaultFireDeadline = 500 * time.Millisecond

// capNotifier is the slice of the generated NotificationServiceClient this
// package needs — kept narrow so tests can inject a fake.
type capNotifier interface {
	NotifyMonthlyCapThreshold(
		context.Context,
		*connect.Request[notificationv1.NotifyMonthlyCapThresholdRequest],
	) (*connect.Response[notificationv1.NotifyMonthlyCapThresholdResponse], error)
}

// Client wraps the notification-svc Connect client with fire-and-forget
// threshold-notification semantics.
type Client struct {
	upstream capNotifier
	logger   *slog.Logger
	deadline time.Duration
	// spawn runs f asynchronously; overridable in tests to run synchronously.
	spawn func(func())
}

// New builds a Client against the notification-svc base URL.
func New(httpClient connect.HTTPClient, baseURL string, logger *slog.Logger) *Client {
	return NewWithClient(notificationv1connect.NewNotificationServiceClient(httpClient, baseURL), logger)
}

// NewWithClient builds a Client around an arbitrary capNotifier (tests).
func NewWithClient(upstream capNotifier, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		upstream: upstream,
		logger:   logger,
		deadline: DefaultFireDeadline,
		spawn:    func(f func()) { go f() },
	}
}

// NotifyCapThresholdAsync fires the threshold notification on a detached
// goroutine and returns immediately. nil-safe.
func (c *Client) NotifyCapThresholdAsync(parent context.Context, apiKeyID string, threshold Threshold) {
	if c == nil || c.upstream == nil {
		return
	}
	// Snapshot the request id BEFORE detaching so cross-service correlation
	// survives (BR-1.6). The detached context drops the parent's cancellation
	// + deadline but keeps its values.
	reqID, _ := requestid.FromContext(parent)
	detached := context.WithoutCancel(parent)
	c.spawn(func() {
		ctx, cancel := context.WithTimeout(detached, c.deadline)
		defer cancel()
		req := connect.NewRequest(&notificationv1.NotifyMonthlyCapThresholdRequest{
			ApiKeyId:  apiKeyID,
			Threshold: threshold.proto(),
			RequestId: reqID,
		})
		if _, err := c.upstream.NotifyMonthlyCapThreshold(ctx, req); err != nil {
			c.logger.WarnContext(ctx, "apikey_cap_threshold_notify_failed",
				slog.String("api_key_id", apiKeyID),
				slog.String("threshold", threshold.String()),
				slog.Bool("deadline_exceeded", errors.Is(ctx.Err(), context.DeadlineExceeded)),
				slog.String("error", err.Error()))
		}
	})
}
