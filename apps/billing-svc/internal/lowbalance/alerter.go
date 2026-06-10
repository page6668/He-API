// Package lowbalance is billing-svc's Story-7.7 AC2 低balance alert dispatcher. It
// consumes the post-deduction autorecharge.Result and decides whether to send a
// localized low-balance email, enforcing the once-per-episode dedupe (BR-A-1).
//
// Design note (Dev, within the Q-ALERT-CHANNEL ruling): the SETNX once-per-episode
// dedupe is CO-LOCATED here, in the balance-authority service that already computes
// suppression (autorecharge.Trigger), rather than split into notification-svc. This
// avoids a split-brain on the episode sentinel; the sentinel key is the spec'd
// `balancestate:user:{id}:low_balance_notified` (no TTL — cleared on recovery above
// the threshold, mirroring the 5.4 cap_threshold SETNX-dedupe semantics). The
// actual localized render + SendGrid dispatch is delegated to notification-svc via
// the Notifier adapter (direct-gRPC parity with 5.4).
package lowbalance

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/billing-svc/internal/autorecharge"
)

// Template variant slugs (mirror notification-svc/internal/templates).
const (
	TemplateLowBalance       = "low_balance"
	TemplateLowBalanceFailed = "low_balance_failed"
)

func episodeKey(userID string) string { return "balancestate:user:" + userID + ":low_balance_notified" }

// Redis is the minimal go-redis surface the dedupe needs.
type Redis interface {
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// Notifier renders + dispatches one localized low-balance email (implemented by a
// notification-svc client adapter; fire-and-forget — a failure must never roll
// back the already-committed debit, BR-A-4).
type Notifier interface {
	SendLowBalance(ctx context.Context, userID, templateSlug, currentBalance, threshold string) error
}

// Alerter dispatches low-balance alerts with the once-per-episode fence.
type Alerter struct {
	redis    Redis
	notifier Notifier
	logger   *slog.Logger
}

// New builds an Alerter. redis/notifier may be nil (the alert degrades to a no-op
// — the debit is already durable).
func New(rdb Redis, notifier Notifier, logger *slog.Logger) *Alerter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Alerter{redis: rdb, notifier: notifier, logger: logger}
}

// Handle acts on a post-deduction autorecharge.Result:
//   - AlertLowBalance / AlertFailed → fire the matching variant (once per episode).
//   - AlertNone with balance recovered ABOVE the threshold → clear the episode
//     sentinel so a later dip re-alerts (the episode boundary, BR-A-1).
func (a *Alerter) Handle(ctx context.Context, userID string, r autorecharge.Result) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	switch r.Alert {
	case autorecharge.AlertLowBalance:
		a.fire(ctx, userID, TemplateLowBalance, r.Balance, r.Threshold)
	case autorecharge.AlertFailed:
		a.fire(ctx, userID, TemplateLowBalanceFailed, r.Balance, r.Threshold)
	case autorecharge.AlertNone:
		if recovered(r.Balance, r.Threshold) {
			a.clearEpisode(ctx, userID)
		}
	}
}

// fire sends the email exactly once per low-balance episode (SETNX won → send).
func (a *Alerter) fire(ctx context.Context, userID, slug, balance, threshold string) {
	if a.notifier == nil {
		return
	}
	if a.redis != nil {
		won, err := a.redis.SetNX(ctx, episodeKey(userID), "1", 0).Result()
		if err == nil && !won {
			// Already alerted this episode — no duplicate email (BR-A-1).
			return
		}
		// On a SETNX error we fail-open (send) — at most a rare duplicate, never a
		// missed alert (parity with the 5.4 cap_threshold fail-open).
	}
	if err := a.notifier.SendLowBalance(ctx, userID, slug, balance, threshold); err != nil {
		// Fire-and-forget: log + roll back the sentinel so a later attempt can
		// still alert (BR-A-4 — the debit stands regardless).
		if a.redis != nil {
			_ = a.redis.Del(ctx, episodeKey(userID)).Err()
		}
		a.logger.WarnContext(ctx, "low_balance_alert_failed",
			slog.String("event", "low_balance_alert_failed"), slog.String("user_id", userID),
			slog.String("error", err.Error()))
	}
}

func (a *Alerter) clearEpisode(ctx context.Context, userID string) {
	if a.redis != nil {
		_ = a.redis.Del(ctx, episodeKey(userID)).Err()
	}
}

// ClearEpisode is the exported recovery hook — the credit path (a successful
// recharge crossing back above the threshold) calls it so the next dip re-alerts.
func (a *Alerter) ClearEpisode(ctx context.Context, userID string) {
	a.clearEpisode(ctx, strings.TrimSpace(userID))
}

// recovered reports whether the post-deduction balance is at/above the threshold.
func recovered(balanceStr, thresholdStr string) bool {
	bal, berr := decimal.NewFromString(strings.TrimSpace(balanceStr))
	thr, terr := decimal.NewFromString(strings.TrimSpace(thresholdStr))
	if berr != nil || terr != nil {
		return false
	}
	return bal.GreaterThanOrEqual(thr)
}
