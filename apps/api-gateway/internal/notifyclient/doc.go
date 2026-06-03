// Package notifyclient is the api-gateway's fire-and-forget wrapper around the
// notification-svc NotifyMonthlyCapThreshold RPC (Story 5.4 AC1/AC2).
//
// The gateway hot path MUST NOT block on notification-svc latency, so
// NotifyCapThresholdAsync detaches the parent context (context.WithoutCancel),
// applies a 500 ms deadline, and dispatches the gRPC call on its own goroutine
// (BR-1.6 / BR-2.2 / Q-E). Failures are logged at WARN and never surfaced to
// the caller — the threshold-crossing dedup lives inside notification-svc, so a
// dropped fire is re-attempted by the next request's detector.
package notifyclient
