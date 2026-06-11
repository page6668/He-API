// Package safetylog is the Story-8.5 persisting half of the §9.3 治理日志 seam
// that Stories 8.2/8.3/8.4 deliberately stubbed as a no-op contentsafety.Recorder.
// It turns every ACTED-ON content-safety block (8.2 入参 reject / 8.3 出参 redact /
// 8.3 stream terminate, both directions) into a he_api.content_safety_logs row.
//
// It lives in a SEPARATE package from contentsafety ON PURPOSE: contentsafety
// must stay lexicon-only and import-cycle-free (it depends ONLY on
// packages/safety-lexicon — recorder.go:9-11). safetylog imports contentsafety
// (for the SafetyEvent / Recorder types) + pgx; only cmd/server/main.go imports
// safetylog. The handler still references only contentsafety.Recorder, wired via
// the existing handlers.WithSafetyRecorder option — no handler change (BR-1.2).
//
// FIRE-AND-FORGET is the load-bearing contract (BR-1.3, recorder.go:56-62):
// Record does a non-blocking enqueue and returns immediately; a bounded async
// worker drains + INSERTs. The block decision (the 400 / redact / stream-
// terminate) is NEVER awaited on the DB — a DB error, timeout, or full buffer
// logs at WARN + increments a drop counter, and the user-facing block proceeds
// byte-identically. On ctx cancel the worker flushes in-flight events then exits
// (HARD graceful-drain, OQ-8.5-1) so a normal redeploy loses nothing.
package safetylog

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
)

// Tunables (OQ-8.5-1, Architect APPROVED: bounded async worker, cap ~1024).
const (
	// queueCap bounds the in-flight buffer. Acted-on blocks are rare; sustained
	// saturation implies a DB outage already surfaced by the drop counter.
	queueCap = 1024
	// insertTimeout bounds a single INSERT so one slow write cannot wedge the
	// single drain goroutine.
	insertTimeout = 5 * time.Second
	// drainTimeout bounds the graceful flush-then-exit on shutdown so a stuck DB
	// cannot block process teardown.
	drainTimeout = 10 * time.Second
)

// insertSQL appends one governance row. created_at defaults to NOW() in the
// table (0014). Column order matches rowArgs.
const insertSQL = `INSERT INTO he_api.content_safety_logs
	(user_id, api_key_id, he_request_id, direction, matched_rule, action, strictness, excerpt_redacted)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

// Querier is the minimal pgx surface the recorder needs — satisfied by
// *pgxpool.Pool and pgxmock (mirrors handlers.billing_write's write seam).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PersistingRecorder implements contentsafety.Recorder by writing each acted-on
// block to he_api.content_safety_logs via a bounded async worker.
type PersistingRecorder struct {
	db     Querier
	logger *slog.Logger
	queue  chan contentsafety.SafetyEvent
	done   chan struct{} // closed when the worker has exited (post-drain)

	// Observability counters (atomic; read in tests + exposed via DroppedTotal).
	inserted atomic.Int64 // INSERT attempts that returned nil error
	failed   atomic.Int64 // INSERT attempts that errored (logged + dropped)
	dropped  atomic.Int64 // events dropped on a full buffer (safety_log_dropped_total)
}

// compile-time assertion: PersistingRecorder satisfies the 8.2 seam.
var _ contentsafety.Recorder = (*PersistingRecorder)(nil)

// NewPersistingRecorder builds the recorder and starts its single drain
// goroutine bound to ctx (the gateway root signal context). A nil db returns a
// nil contentsafety.Recorder so handlers.WithSafetyRecorder keeps the NopRecorder
// default — degraded-but-serving, mirroring the billing-endpoints-disabled
// posture (main.go:488 / BR-1.1). Returning the interface's nil (not a typed
// nil) is intentional so the != nil check in WithSafetyRecorder behaves.
func NewPersistingRecorder(ctx context.Context, db Querier, logger *slog.Logger) contentsafety.Recorder {
	if db == nil {
		logger.Warn("safety_log: no pool — using NopRecorder (content_safety_logs disabled)")
		return nil
	}
	r := newRecorder(db, logger)
	go r.run(ctx)
	return r
}

// newRecorder builds the struct WITHOUT starting the worker. Production goes
// through NewPersistingRecorder; tests use this to drive the queue/worker
// deterministically (white-box) and avoid async flakiness (Testing Requirements).
func newRecorder(db Querier, logger *slog.Logger) *PersistingRecorder {
	return &PersistingRecorder{
		db:     db,
		logger: logger,
		queue:  make(chan contentsafety.SafetyEvent, queueCap),
		done:   make(chan struct{}),
	}
}

// Record is the fire-and-forget seam (BR-1.3). It does a NON-BLOCKING enqueue and
// returns immediately — it NEVER blocks or fails the user-facing block. On a full
// buffer the event is dropped with a WARN + the safety_log_dropped_total counter;
// the block proceeds unchanged. The request ctx is intentionally NOT carried into
// the queue: the worker writes under its own (root) context so a request whose
// ctx is cancelled the instant the response flushes still persists.
func (r *PersistingRecorder) Record(_ context.Context, ev contentsafety.SafetyEvent) {
	select {
	case r.queue <- ev:
	default:
		r.dropped.Add(1)
		r.logger.Warn("safety_log: buffer full, event dropped",
			slog.String("direction", ev.Direction),
			slog.Int64("dropped_total", r.dropped.Load()))
	}
}

// run is the single drain goroutine. It INSERTs queued events until ctx is
// cancelled, then flushes whatever remains (bounded by drainTimeout) and exits —
// the HARD graceful-drain so a normal redeploy does not silently lose in-flight
// events (OQ-8.5-1). done is closed on exit (no goroutine leak).
func (r *PersistingRecorder) run(ctx context.Context) {
	defer close(r.done)
	for {
		select {
		case ev := <-r.queue:
			r.insert(ev)
		case <-ctx.Done():
			r.drain()
			return
		}
	}
}

// drain flushes every currently-queued event after the run ctx is cancelled,
// bounded overall by drainTimeout so a stuck DB cannot block teardown.
func (r *PersistingRecorder) drain() {
	deadline := time.Now().Add(drainTimeout)
	for {
		select {
		case ev := <-r.queue:
			r.insert(ev)
			if time.Now().After(deadline) {
				return
			}
		default:
			return
		}
	}
}

// insert writes one row under a fresh bounded context. It is deliberately NOT
// derived from the run/shutdown ctx: a single block must persist even when the
// gateway is tearing down (the graceful-drain contract), so each write gets its
// own background-rooted deadline. A failure is logged at WARN and dropped —
// NEVER surfaced (fire-and-forget; the block already completed). Counters feed
// the tests + observability.
func (r *PersistingRecorder) insert(ev contentsafety.SafetyEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()
	if _, err := r.db.Exec(ctx, insertSQL, rowArgs(ev)...); err != nil {
		r.failed.Add(1)
		r.logger.Warn("safety_log: persist failed",
			slog.String("direction", ev.Direction),
			slog.String("error", err.Error()))
		return
	}
	r.inserted.Add(1)
}

// rowArgs projects a SafetyEvent onto the ordered INSERT arguments (pure; the
// 8.5-UNIT-005 field-projection assertion targets this). Empty optional fields
// become SQL NULL via nullable; user_id / he_request_id / direction / action are
// NOT-NULL and always populated by the call sites. The excerpt is the masked
// matched span (maskedMatchExcerpt) — never raw text / the literal term.
func rowArgs(ev contentsafety.SafetyEvent) []any {
	return []any{
		ev.UserID,
		nullable(ev.APIKeyID),
		ev.HeRequestID,
		ev.Direction,
		nullable(ev.MatchedRule),
		ev.Action,
		ev.Strictness,
		nullable(maskedMatchExcerpt(ev.MatchedRule)),
	}
}

// nullable maps "" → nil (SQL NULL) so optional columns are not stored as empty
// strings; pgx encodes a nil any as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// DroppedTotal reports events dropped on a full buffer (safety_log_dropped_total).
func (r *PersistingRecorder) DroppedTotal() int64 { return r.dropped.Load() }
