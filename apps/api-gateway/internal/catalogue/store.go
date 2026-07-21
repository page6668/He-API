// Package catalogue reads the model catalogue from PostgreSQL — the single
// source of truth per AD-002 (specs/model-catalogue-arch.md).
//
// Why this exists: the catalogue used to be compiled into the binary
// (packages/models-catalogue DefaultRegistry). Vendors retire model ids far
// faster than this project ships — by 2026-07 every compiled-in id had been
// removed upstream, so every live call returned 403 Model.AccessDenied with no
// alarm. The DB is now authoritative; the Go registry is a cold-start fallback.
//
// Serving posture (AD-002 failure modes): a refresh error NEVER fails a request.
// The last good snapshot keeps serving (stale but available) because /v1/models
// and /public/models are load-bearing for SEO and client integrations.
package catalogue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Capabilities mirrors the he_api.models.capabilities JSONB document.
type Capabilities struct {
	Chat                bool `json:"chat"`
	Streaming           bool `json:"streaming"`
	FunctionCalling     bool `json:"function_calling"`
	Vision              bool `json:"vision"`
	JSONMode            bool `json:"json_mode"`
	Transcription       bool `json:"transcription"`
	Speech              bool `json:"speech"`
	ContextWindowTokens int  `json:"context_window_tokens"`
	MaxOutputTokens     int  `json:"max_output_tokens"`
}

// Model is one catalogue row joined with its currently-effective price.
type Model struct {
	ID           string
	DisplayName  string
	Vendor       string
	Capabilities Capabilities
	// UpstreamModelID is the vendor's own id when it differs from ID (e.g.
	// deepseek-v3 → deepseek-chat). Empty means "pass ID through unchanged".
	UpstreamModelID string
	// Customer-facing price per 1K tokens in USD, already marked up.
	//
	// Carried as the exact decimal TEXT postgres produced — never float64.
	// This is the M-1 rule the billing cost engine is built on
	// (apps/billing-svc/internal/pricing): float is allowed where a price is
	// only RANKED, never where it is shown to or charged to a customer. The
	// gateway does no arithmetic on these; the markup is applied in SQL where
	// the type is still NUMERIC.
	//
	// Empty means unpriced — such a model MUST NOT be advertised (AD-002
	// invariant), which is why Load's JOIN filters it out before we get here.
	InputPricePer1K  string
	OutputPricePer1K string
}

// PriceCurrency is the unit of the two price fields. he_api.model_pricing is
// USD-denominated (migration 0007); display in another currency goes through
// he_api.fx_rates, never through a second price column.
const PriceCurrency = "USD"

// listActiveSQL returns active models that also carry an effective price.
//
// The INNER JOIN is the AD-002 invariant in SQL form: "active AND priced" is
// what makes a model publicly visible. An unpriced model would otherwise be
// advertised and then billed at zero — we would rather not list it at all.
// DISTINCT ON picks the newest price row at or before now (model_pricing is
// append-only for audit, so several rows can exist per model).
//
// The markup is applied HERE, while the values are still NUMERIC, then rounded
// to 6 places (the scale of the source columns) and cast to text. Postgres does
// the decimal arithmetic exactly; Go only carries the string.
const listActiveSQL = `
SELECT DISTINCT ON (m.id)
       m.id,
       m.display_name,
       m.vendor,
       m.capabilities,
       COALESCE(m.upstream_model_id, ''),
       ROUND(p.upstream_price_per_1k_input_tokens  * (1 + p.markup_percent / 100), 6)::text,
       ROUND(p.upstream_price_per_1k_output_tokens * (1 + p.markup_percent / 100), 6)::text
  FROM he_api.models m
  JOIN he_api.model_pricing p ON p.model_id = m.id AND p.effective_at <= NOW()
 WHERE m.status = 'active'
 ORDER BY m.id, p.effective_at DESC`

// Store loads the catalogue from PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Load returns every publicly-visible model. A nil pool (HE_API_DB_POSTGRES_URI
// unset) returns an error rather than an empty list, so the caller can tell
// "no database" apart from "database says there are no models" — the latter
// must never silently blank the catalogue.
func (s *Store) Load(ctx context.Context) ([]Model, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("catalogue: no database pool")
	}
	rows, err := s.pool.Query(ctx, listActiveSQL)
	if err != nil {
		return nil, fmt.Errorf("catalogue: query: %w", err)
	}
	defer rows.Close()

	var out []Model
	for rows.Next() {
		var m Model
		var capsRaw []byte
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.Vendor, &capsRaw,
			&m.UpstreamModelID, &m.InputPricePer1K, &m.OutputPricePer1K); err != nil {
			return nil, fmt.Errorf("catalogue: scan: %w", err)
		}
		// A malformed capabilities document must not sink the whole catalogue:
		// keep the row with zero-valued capabilities and move on.
		if err := json.Unmarshal(capsRaw, &m.Capabilities); err != nil {
			m.Capabilities = Capabilities{}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalogue: rows: %w", err)
	}
	return out, nil
}

// Snapshot is the process-local cache the request path reads. Refreshes happen
// on a timer, never inside a request, so a slow database cannot add latency to
// /v1/models. Safe for concurrent use across the gateway's replicas-of-one.
type Snapshot struct {
	mu       sync.RWMutex
	models   []Model
	loadedAt time.Time
	stale    bool
	store    *Store
	fallback []Model
	logger   *slog.Logger
	ttl      time.Duration
}

// NewSnapshot seeds the cache with fallback (the compiled-in registry) so the
// gateway can serve before — or entirely without — a successful DB read.
func NewSnapshot(store *Store, fallback []Model, ttl time.Duration, logger *slog.Logger) *Snapshot {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Snapshot{
		models:   fallback,
		fallback: fallback,
		store:    store,
		logger:   logger,
		ttl:      ttl,
		stale:    true, // nothing loaded from the DB yet
	}
}

// Models returns the current snapshot. Never blocks on the database.
func (s *Snapshot) Models() []Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.models
}

// Stale reports whether the snapshot is fallback/last-good rather than fresh.
// Exposed so main can surface it as a metric — a catalogue silently serving
// stale data is exactly the failure that went unnoticed for months.
func (s *Snapshot) Stale() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stale
}

// Refresh loads once. On error the previous snapshot is kept (AD-002: stale but
// available beats a 5xx on a public endpoint).
func (s *Snapshot) Refresh(ctx context.Context) error {
	models, err := s.store.Load(ctx)
	if err != nil {
		s.mu.Lock()
		s.stale = true
		s.mu.Unlock()
		return err
	}
	// An empty result is treated as a fault, not as "we sell nothing": a
	// truncated table or a bad migration must not blank the public catalogue.
	if len(models) == 0 {
		s.mu.Lock()
		s.stale = true
		s.mu.Unlock()
		return fmt.Errorf("catalogue: database returned no active priced models")
	}
	s.mu.Lock()
	s.models = models
	s.loadedAt = time.Now()
	s.stale = false
	s.mu.Unlock()
	return nil
}

// Start kicks off the initial load and the refresh loop. It RETURNS IMMEDIATELY.
//
// The first load is deliberately asynchronous: a database that is slow or down
// must not delay process boot. The gateway has a cold-start P95 gate (Story 3.1
// BR-2.2, asserted by TestColdStart) and /health must answer before any
// dependency is reachable — blocking here made the binary miss that gate.
// Until the first load lands, requests are served from the fallback the caller
// seeded, which is exactly the AD-002 cold-start posture.
func (s *Snapshot) Start(ctx context.Context) {
	go func() {
		if err := s.Refresh(ctx); err != nil {
			s.logger.Warn("model catalogue: initial load failed — serving compiled-in fallback",
				slog.String("error", err.Error()))
		} else {
			s.logger.Info("model catalogue loaded from database",
				slog.Int("models", len(s.Models())))
		}
		t := time.NewTicker(s.ttl)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.Refresh(ctx); err != nil {
					s.logger.Warn("model catalogue: refresh failed — serving last good snapshot",
						slog.String("error", err.Error()))
				}
			}
		}
	}()
}
