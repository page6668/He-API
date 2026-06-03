// Story 5.4 AC2 — NotifyMonthlyCapThreshold handler.
//
// Fired fire-and-forget by the api-gateway keypolicy middleware on an 80%
// (WARNING_80) or 100% (TRIPPED) cap crossing. This handler owns the
// once-per-month SETNX dedupe so a duplicate fire (concurrent pods, retries)
// produces at most one email per (api_key_id, threshold). The user/key context
// is fetched from auth-svc over gRPC (Q-L Fix-A), then a localized SendGrid
// email is dispatched.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"

	"github.com/he-api/he-api/apps/notification-svc/internal/authsvcclient"
	"github.com/he-api/he-api/apps/notification-svc/internal/sendgrid"
	"github.com/he-api/he-api/apps/notification-svc/internal/templates"
)

// Redis dedupe-sentinel prefixes (no TTL — cron-cleared). MUST stay in lockstep
// with apps/api-gateway/internal/middleware/keypolicy/cap_state.go and
// apps/auth-svc/internal/redisclient/cap_state.go (the cron's SCAN prefixes).
const (
	capWarning80NotifiedPrefix = "keystate:apikey:cap_warning_80_notified:"
	capTrippedNotifiedPrefix   = "keystate:apikey:cap_tripped_notified:"
)

// CapContextLookup is the auth-svc gRPC surface the handler needs
// (*authsvcclient.Client satisfies it).
type CapContextLookup interface {
	GetCapNotificationContext(ctx context.Context, apiKeyID string) (authsvcclient.CapContext, error)
}

// CapDedupeStore is the once-per-month claim surface. SetNX returns true only
// for the first claimant; Del un-claims on a downstream lookup failure.
// *RedisDedupeStore is the production impl; tests inject a fake.
type CapDedupeStore interface {
	SetNX(ctx context.Context, key string) (bool, error)
	Del(ctx context.Context, key string) error
}

// RedisDedupeStore implements CapDedupeStore over go-redis. Value "1", no TTL.
type RedisDedupeStore struct{ rdb redis.Cmdable }

// NewRedisDedupeStore wraps a go-redis client as a CapDedupeStore.
func NewRedisDedupeStore(rdb redis.Cmdable) *RedisDedupeStore { return &RedisDedupeStore{rdb: rdb} }

// SetNX claims the dedupe sentinel (no TTL — cron-cleared).
func (s *RedisDedupeStore) SetNX(ctx context.Context, key string) (bool, error) {
	return s.rdb.SetNX(ctx, key, "1", 0).Result()
}

// Del removes the dedupe sentinel.
func (s *RedisDedupeStore) Del(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, key).Err()
}

// CapThresholdServer serves NotifyMonthlyCapThreshold. All deps are injectable
// so the handler unit-tests without real Redis / auth-svc / SendGrid.
type CapThresholdServer struct {
	Dedupe   CapDedupeStore
	AuthCtx  CapContextLookup
	Renderer TemplateRenderer
	Sender   EmailSender
	Logger   *slog.Logger
}

// NewCapThresholdServer wires the canonical Renderer + provided collaborators.
func NewCapThresholdServer(dedupe CapDedupeStore, authCtx CapContextLookup, sender EmailSender, logger *slog.Logger) *CapThresholdServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &CapThresholdServer{
		Dedupe:   dedupe,
		AuthCtx:  authCtx,
		Renderer: templates.NewRenderer(),
		Sender:   sender,
		Logger:   logger,
	}
}

// NotifyMonthlyCapThreshold implements AC2. Side-effect order is load-bearing:
//
//  1. Validate api_key_id (UUID v4) + threshold enum.
//  2. SETNX dedupe claim. Lost claim → was_already_notified=true, no email.
//     SETNX error → fail-OPEN (Q-F): proceed and risk a duplicate email.
//  3. auth-svc GetCapNotificationContext. On failure, un-claim the dedupe so
//     the next request re-fires (BR error table), and map the gRPC code.
//  4. Render in the user's locale + SendGrid send. On send failure the dedupe
//     stays claimed (BR-2.4 SETNX-on-claim) — the user misses the email.
func (s *CapThresholdServer) NotifyMonthlyCapThreshold(
	ctx context.Context,
	req *connect.Request[notificationv1.NotifyMonthlyCapThresholdRequest],
) (*connect.Response[notificationv1.NotifyMonthlyCapThresholdResponse], error) {
	in := req.Msg
	apiKeyID := strings.TrimSpace(in.GetApiKeyId())
	if _, err := uuid.Parse(apiKeyID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_api_key_id"))
	}

	var (
		dedupeKey    string
		slug         string
		thresholdPct string
	)
	switch in.GetThreshold() {
	case notificationv1.ThresholdLevel_THRESHOLD_LEVEL_WARNING_80:
		dedupeKey = capWarning80NotifiedPrefix + apiKeyID
		slug = templates.TemplateMonthlyCapWarning
		thresholdPct = "80"
	case notificationv1.ThresholdLevel_THRESHOLD_LEVEL_TRIPPED:
		dedupeKey = capTrippedNotifiedPrefix + apiKeyID
		slug = templates.TemplateMonthlyCapTripped
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_threshold"))
	}

	// === 2. SETNX dedupe claim (BR-2.3; no TTL — cron-cleared) ===
	claimed := true
	if s.Dedupe != nil {
		won, err := s.Dedupe.SetNX(ctx, dedupeKey)
		if err != nil {
			// Q-F fail-OPEN: proceed (risk one duplicate email on a Redis hiccup).
			s.Logger.WarnContext(ctx, "cap_notify_dedupe_setnx_failed",
				slog.String("api_key_id", apiKeyID), slog.String("error", err.Error()))
		} else {
			claimed = won
		}
	}
	if !claimed {
		return connect.NewResponse(&notificationv1.NotifyMonthlyCapThresholdResponse{
			WasAlreadyNotified: true,
		}), nil
	}

	// === 3. Fetch user + key context via auth-svc gRPC ===
	cc, err := s.AuthCtx.GetCapNotificationContext(ctx, apiKeyID)
	if err != nil {
		s.unclaim(ctx, dedupeKey) // BR error table: let the next request re-fire
		switch connect.CodeOf(err) {
		case connect.CodeNotFound:
			return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
		case connect.CodeUnavailable:
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("pg_unavailable"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cap_notify: lookup: %w", err))
		}
	}

	// === 4. Render (HTML auto-escape, BR-2.8) + send ===
	vars := map[string]string{
		"display_name": resolveDisplayName(cc.UserEmail, cc.UserDisplayName),
		"key_name":     cc.KeyName,
		"cap_usd":      cc.KeyMonthlyCostCapUSD,
	}
	if thresholdPct != "" {
		vars["threshold_pct"] = thresholdPct
	}

	rendered, err := s.Renderer.Render(slug, cc.UserLocale, vars)
	if err != nil {
		// Template errors are a build-time invariant (CI render test); surface
		// loudly. Dedupe stays claimed — a misconfigured template would otherwise
		// re-fire every request.
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cap_notify: render: %w", err))
	}

	if _, err := s.Sender.Send(ctx, sendgrid.SendRequest{
		To:       cc.UserEmail,
		Subject:  rendered.Subject,
		TextBody: rendered.TextBody,
		HTMLBody: rendered.HTMLBody,
	}); err != nil {
		// BR-2.4 SETNX-on-claim: leave the dedupe set; the user misses the email
		// this month. Log loudly for on-call.
		s.Logger.ErrorContext(ctx, "cap_email_send_failed_after_setnx",
			slog.String("api_key_id", apiKeyID), slog.String("error", err.Error()))
		return nil, connect.NewError(connect.CodeInternal, errors.New("email_send_failed"))
	}

	return connect.NewResponse(&notificationv1.NotifyMonthlyCapThresholdResponse{
		WasAlreadyNotified: false,
		EmailSent:          true,
	}), nil
}

// unclaim best-effort deletes the dedupe sentinel so a transient lookup failure
// does not permanently suppress the email for the month.
func (s *CapThresholdServer) unclaim(ctx context.Context, key string) {
	if s.Dedupe == nil {
		return
	}
	if err := s.Dedupe.Del(ctx, key); err != nil {
		s.Logger.WarnContext(ctx, "cap_notify_dedupe_unclaim_failed",
			slog.String("key", key), slog.String("error", err.Error()))
	}
}

// resolveDisplayName implements BR-2.5: display_name → email local-part →
// empty-string. Pure-Go; NEW in Story 5.4 (Architect Round 1 H-2).
func resolveDisplayName(userEmail, userDisplayName string) string {
	if userDisplayName != "" {
		return userDisplayName
	}
	if at := strings.IndexByte(userEmail, '@'); at > 0 {
		return userEmail[:at]
	}
	return ""
}
