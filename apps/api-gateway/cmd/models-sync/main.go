// models-sync — discovers upstream model ids and records them for human review.
//
// AD-002 (specs/model-catalogue-arch.md): this job is DISCOVERY ONLY. Upstream
// /v1/models returns nothing but id/created/owned_by — no price, no context
// window, no capability flags — so a model it finds is written as
// status='pending' and stays invisible to customers until an operator fills in
// pricing and capabilities. Automating the part we cannot know would put
// unpriced models on sale.
//
// Why it exists: model ids compiled into the binary rotted silently. By 2026-07
// every id the gateway advertised had been retired upstream and every live call
// returned 403 Model.AccessDenied, with nothing watching. This job is the watch.
//
// Writes are strictly additive: insert new ids as pending, refresh
// last_seen_upstream_at, and flip vanished ids to deprecated. It never deletes a
// row (usage_ledger references models; historical invoices must stay traceable)
// and never promotes anything to active — only a human does that.
//
// Env:
//
//	HE_API_DB_POSTGRES_URI     — required
//	MODELS_SYNC_UPSTREAM_URL   — required, e.g. https://dashscope.aliyuncs.com/compatible-mode/v1/models
//	MODELS_SYNC_API_KEY        — required, upstream bearer token
//	MODELS_SYNC_VENDOR         — vendor recorded on newly discovered rows (default "alibaba")
//	MODELS_SYNC_DRY_RUN        — "true" logs the plan without writing
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type upstreamModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

type upstreamList struct {
	Data []upstreamModel `json:"data"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := run(ctx, logger); err != nil {
		logger.Error("models-sync failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	dbURI := os.Getenv("HE_API_DB_POSTGRES_URI")
	upstreamURL := os.Getenv("MODELS_SYNC_UPSTREAM_URL")
	apiKey := os.Getenv("MODELS_SYNC_API_KEY")
	vendor := envOr("MODELS_SYNC_VENDOR", "alibaba")
	dryRun := os.Getenv("MODELS_SYNC_DRY_RUN") == "true"

	if dbURI == "" || upstreamURL == "" || apiKey == "" {
		return fmt.Errorf("HE_API_DB_POSTGRES_URI, MODELS_SYNC_UPSTREAM_URL and MODELS_SYNC_API_KEY are all required")
	}

	ids, err := fetchUpstreamIDs(ctx, upstreamURL, apiKey)
	if err != nil {
		return err
	}
	// Guard rail: an empty list is treated as an upstream fault. Without this a
	// blip would mark the ENTIRE catalogue deprecated and take the product down.
	if len(ids) == 0 {
		return fmt.Errorf("upstream returned zero models — refusing to deprecate the catalogue")
	}
	logger.Info("upstream models fetched", slog.Int("count", len(ids)))

	if dryRun {
		logger.Info("dry run — no writes", slog.String("ids", strings.Join(ids, ",")))
		return nil
	}

	pool, err := pgxpool.New(ctx, dbURI)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	pendingBacklog, err := upsertPending(ctx, pool, ids, vendor)
	if err != nil {
		return err
	}
	deprecated, err := markVanished(ctx, pool, ids)
	if err != nil {
		return err
	}

	logger.Info("models-sync done",
		slog.Int("upstream_total", len(ids)),
		slog.Int("pending_backlog", pendingBacklog),
		slog.Int("marked_deprecated", deprecated))
	if pendingBacklog > 0 {
		// Surfaced deliberately: a pending model earns no revenue until someone
		// prices it, so this line is the prompt for that human step.
		logger.Warn("models awaiting pricing + capabilities before they can go live",
			slog.Int("pending_backlog", pendingBacklog))
	}
	return nil
}

func fetchUpstreamIDs(ctx context.Context, url, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}

	var list upstreamList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode upstream body: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// upsertPending inserts unknown ids as pending. Existing rows only get their
// last_seen_upstream_at refreshed — an operator's curated display_name,
// capabilities and status are never overwritten by the job.
func upsertPending(ctx context.Context, pool *pgxpool.Pool, ids []string, vendor string) (int, error) {
	const sql = `
INSERT INTO he_api.models (id, display_name, vendor, capabilities, status, last_seen_upstream_at)
SELECT unnest($1::text[]), unnest($1::text[]), $2, '{}'::jsonb, 'pending', NOW()
ON CONFLICT (id) DO UPDATE
   SET last_seen_upstream_at = NOW(),
       updated_at            = NOW()`
	if _, err := pool.Exec(ctx, sql, ids, vendor); err != nil {
		return 0, fmt.Errorf("upsert models: %w", err)
	}
	// Report the pending BACKLOG rather than this run's insert count: what an
	// operator needs to act on is "how many models are waiting to be priced",
	// which persists across runs until someone works through it.
	var pending int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM he_api.models WHERE status = 'pending'`).Scan(&pending); err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	return pending, nil
}

// markVanished flips ids absent from upstream to deprecated. Rows are kept:
// usage_ledger references them and past invoices must remain explainable.
func markVanished(ctx context.Context, pool *pgxpool.Pool, ids []string) (int, error) {
	const sql = `
UPDATE he_api.models
   SET status = 'deprecated', updated_at = NOW()
 WHERE status <> 'deprecated'
   AND last_seen_upstream_at IS NOT NULL
   AND NOT (id = ANY($1::text[]))`
	tag, err := pool.Exec(ctx, sql, ids)
	if err != nil {
		return 0, fmt.Errorf("deprecate vanished models: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
