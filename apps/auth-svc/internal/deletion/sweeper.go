// Package deletion implements the Story 2.7 AC6 account-deletion sweeper core:
// the per-user physical-erasure pipeline invoked by the
// account-deletion-sweeper CronJob. The cross-store dependencies (ClickHouse,
// OSS, Stripe) are behind narrow seams so the PG-correctness — the
// CASCADE-on-UPDATE trap (BR-6.3) + the payment_methods Stripe-detach OVERRIDE
// (OQ-3/M-2) + the crash-safety reconcile gate (BR-6.6) — is fully unit-testable
// with pgxmock + fakes, with no live infra.
package deletion

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// ClickHouseAnonymizer anonymizes the user's request_logs.user_id (AC6 step 5).
// Nil-safe: a nil seam skips the step (the 90-day TTL still applies).
type ClickHouseAnonymizer interface {
	AnonymizeUser(ctx context.Context, userID uuid.UUID) error
}

// OSSPurger deletes the user's personal OSS objects under gdpr-exports/{id}/*
// (AC6 step 6).
type OSSPurger interface {
	PurgeUserObjects(ctx context.Context, userID uuid.UUID) error
}

// StripeDetacher detaches a live off-session PaymentMethod token (OQ-3 OVERRIDE
// / M-2). A detached account must never retain a chargeable instrument.
type StripeDetacher interface {
	DetachPaymentMethod(ctx context.Context, token string) error
}

// TxBeginner starts a per-user transaction. *pgxpool.Pool satisfies it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Deps bundles the sweeper's collaborators. CH / OSS / Stripe / Mailer / Audit
// are nil-safe (a nil seam skips its step) so tests exercise the PG core in
// isolation. Clock defaults to time.Now.
type Deps struct {
	DB     repository.Querier // pool — reads + finalize (non-per-user-tx)
	Tx     TxBeginner         // per-user transaction starter
	CH     ClickHouseAnonymizer
	OSS    OSSPurger
	Stripe StripeDetacher
	Mailer notification.Sender
	Audit  audit.Publisher
	Logger *slog.Logger
	Clock  func() time.Time
}

func (d Deps) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

func (d Deps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// Result tallies a Run.
type Result struct {
	Processed int // users fully erased this run (anonymized_at set)
	Failed    int // users that errored (rolled back or left for reconcile)
}

// Run executes one sweeper pass: the due cohort (grace elapsed) followed by the
// reconcile cohort (PG-anonymized but cross-store-incomplete — BR-6.6). Each
// user is fail-isolated: one bad row never blocks the batch. Returns the tally;
// the caller exits 1 when Failed > 0 (ops-visible, BR-6.7).
func Run(ctx context.Context, d Deps) (Result, error) {
	var res Result

	due, err := repository.SelectDueDeletions(ctx, d.DB)
	if err != nil {
		return res, err
	}
	for _, u := range due {
		if err := d.processUser(ctx, u, false); err != nil {
			res.Failed++
			d.log().WarnContext(ctx, "account_deletion_sweep_user_failed",
				slog.String("user_id", u.ID.String()), slog.String("error", err.Error()))
			continue
		}
		res.Processed++
	}

	reconcile, err := repository.SelectReconcileDeletions(ctx, d.DB)
	if err != nil {
		return res, err
	}
	for _, u := range reconcile {
		if err := d.processUser(ctx, u, true); err != nil {
			res.Failed++
			d.log().WarnContext(ctx, "account_deletion_reconcile_user_failed",
				slog.String("user_id", u.ID.String()), slog.String("error", err.Error()))
			continue
		}
		res.Processed++
	}
	return res, nil
}

// processUser runs the full erasure for one user.
//
// First pass (reconcile=false): a single PG transaction soft-deletes the users
// row (anonymize, status='deleted', deleted_at, anonymized_at LEFT NULL) and
// EXPLICITLY deletes the PII children — CASCADE does NOT fire on the UPDATE
// (BR-6.3). payment_methods rows are RETAINED until Stripe-detach succeeds so a
// detach failure is retriable on reconcile. Then the cross-store scrub runs
// OUTSIDE the txn (Stripe detach → ClickHouse → OSS); only when ALL succeed are
// the payment_methods rows deleted and anonymized_at set (BR-6.6), followed by
// the HIGH audit + the completion email using the ORIGINAL email (BR-6.4).
//
// Reconcile pass (reconcile=true): the PG anonymize + child-delete already ran;
// only the cross-store scrub + finalize are retried.
func (d Deps) processUser(ctx context.Context, u repository.DueUser, reconcile bool) error {
	if !reconcile {
		tx, err := d.Tx.Begin(ctx)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()

		anonymized, err := repository.AnonymizeUserPG(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if !anonymized {
			// Lost a race / already deleted — treat as reconcile.
			_ = tx.Rollback(ctx)
			return d.finalize(ctx, u)
		}
		if err := repository.DeletePIIChildren(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		committed = true
	}
	return d.finalize(ctx, u)
}

// finalize runs the cross-store scrub and, on full success, deletes the
// payment_methods rows + closes the reconcile gate + audits + emails.
func (d Deps) finalize(ctx context.Context, u repository.DueUser) error {
	now := d.now()

	// Detach every live Stripe token BEFORE the rows are deleted (OQ-3 / M-2).
	// Rows are retained until detach succeeds so a failure is retried on the
	// next reconcile sweep (the tokens are still readable).
	tokens, err := repository.SelectPaymentMethodTokens(ctx, d.DB, u.ID)
	if err != nil {
		return err
	}
	for _, tok := range tokens {
		if d.Stripe == nil {
			break
		}
		if err := d.Stripe.DetachPaymentMethod(ctx, tok); err != nil {
			d.log().WarnContext(ctx, "account_deletion_stripe_detach_failed",
				slog.String("user_id", u.ID.String()), slog.String("error", err.Error()))
			return err // anonymized_at stays NULL → reconciled next run (BR-6.6)
		}
	}

	if d.CH != nil {
		if err := d.CH.AnonymizeUser(ctx, u.ID); err != nil {
			d.log().WarnContext(ctx, "account_deletion_ch_anonymize_failed",
				slog.String("user_id", u.ID.String()), slog.String("error", err.Error()))
			return err
		}
	}
	if d.OSS != nil {
		if err := d.OSS.PurgeUserObjects(ctx, u.ID); err != nil {
			d.log().WarnContext(ctx, "account_deletion_oss_purge_failed",
				slog.String("user_id", u.ID.String()), slog.String("error", err.Error()))
			return err
		}
	}

	// All cross-store scrubs succeeded — delete payment_methods rows + close the
	// reconcile gate in one transaction (OQ-3: detach THEN delete).
	tx, err := d.Tx.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := repository.DeletePaymentMethods(ctx, tx, u.ID); err != nil {
		return err
	}
	if err := repository.MarkAnonymizationComplete(ctx, tx, u.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true

	// Erasure is now fully done — exactly-once audit (HIGH) + completion email
	// (BR-6.4 — best-effort, original email captured at selection). Failures
	// here are non-fatal: the erasure stands.
	d.auditExecuted(ctx, u, now)
	if d.Mailer != nil && u.Email != "" {
		if err := d.Mailer.SendAccountDeletionEmail(ctx, notification.AccountDeletionCompleted, u.Email, u.Locale, map[string]string{
			"executed_at": now.UTC().Format(time.RFC3339),
		}); err != nil {
			d.log().WarnContext(ctx, "account_deletion_completed email send failed",
				slog.String("user_id", u.ID.String()), slog.String("error", err.Error()))
		}
	}
	return nil
}

// auditExecuted emits the BR-7.1 account.deletion.executed event (HIGH,
// PII-safe: only user_id + timestamps).
func (d Deps) auditExecuted(ctx context.Context, u repository.DueUser, now time.Time) {
	if d.Audit == nil {
		return
	}
	audit.PublishBestEffort(ctx, d.Audit, d.Logger, audit.Event{
		EventType: audit.EventAccountDeletionExecuted,
		UserID:    u.ID.String(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":    audit.SeverityAccountDeletion(audit.EventAccountDeletionExecuted),
			"executed_at": now.UTC().Format(time.RFC3339),
		},
	})
}

// ErrNoTx is returned when a transaction-requiring path is invoked without a
// TxBeginner wired (caller config error).
var ErrNoTx = errors.New("deletion: tx-capable DB not configured")
