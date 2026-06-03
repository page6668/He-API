// monthly-cost-reset — Story 5.4 AC3 K8s CronJob entrypoint (schedule
// `0 0 1 * *` UTC). Resets he_api.api_keys.current_month_cost_usd to 0 for
// every non-revoked row, then SCAN+DELs the three cost-cap Redis key families.
// Emits a Kafka audit.event on completion (3-retry best-effort).
//
// Env (reuses the auth-svc Deployment's secret, BR-3.11):
//
//	HE_API_DB_POSTGRES_URI      — required (PG DSN)
//	HE_API_DB_REDIS_URI         — required (Redis URI)
//	HE_API_AUDIT_KAFKA_BROKERS  — optional (audit emit; graceful-degrade)
//	HE_API_REDIS_CLUSTER_ADDRS  — optional (comma-sep; selects cluster client)
//	DRY_RUN=1                   — optional (skip writes; CI smoke)
//
// Flags:
//
//	--purge-redis-only          — skip the PG UPDATE; run only SCAN+DEL (Q-J
//	                              on-call remediation for a partial-fail run).
//
// Exit codes: 0 success (incl. empty state); 1 on any PG/Redis failure or the
// 5-minute deadline (backoffLimit=0 → on-call investigates).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/redisclient"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// runDeadline bounds the whole job (BR-3.13). 10× safety margin at 100k keys.
const runDeadline = 5 * time.Minute

// config is the validated environment for one run.
type config struct {
	pgURI        string
	redisURI     string
	clusterAddrs []string
	kafkaBrokers []string
	dryRun       bool
	purgeOnly    bool
}

// loadConfig reads + validates the environment. Returns an error naming the
// first missing required var (the caller logs the matching slog code + exit 1).
func loadConfig(getenv func(string) string, purgeOnly bool) (config, error) {
	c := config{purgeOnly: purgeOnly}
	c.pgURI = strings.TrimSpace(getenv("HE_API_DB_POSTGRES_URI"))
	c.redisURI = strings.TrimSpace(getenv("HE_API_DB_REDIS_URI"))
	c.dryRun = strings.TrimSpace(getenv("DRY_RUN")) == "1"
	if addrs := strings.TrimSpace(getenv("HE_API_REDIS_CLUSTER_ADDRS")); addrs != "" {
		c.clusterAddrs = splitNonEmpty(addrs)
	}
	if brokers := strings.TrimSpace(getenv("HE_API_AUDIT_KAFKA_BROKERS")); brokers != "" {
		c.kafkaBrokers = splitNonEmpty(brokers)
	}
	if c.purgeOnly {
		// purge-redis-only mode does not touch PG.
		if c.redisURI == "" {
			return c, errors.New("redis_url_missing")
		}
		return c, nil
	}
	if c.pgURI == "" {
		return c, errors.New("pg_dsn_missing")
	}
	if c.redisURI == "" {
		return c, errors.New("redis_url_missing")
	}
	return c, nil
}

func splitNonEmpty(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// newRedisClient builds a UniversalClient — *redis.ClusterClient when extra
// cluster addrs are configured, else a standalone *redis.Client. Both satisfy
// redisclient.PurgeMonthlyState's type switch (BR-3.6 m-1).
func newRedisClient(c config) (redis.UniversalClient, error) {
	opt, err := redis.ParseURL(c.redisURI)
	if err != nil {
		return nil, err
	}
	addrs := []string{opt.Addr}
	if len(c.clusterAddrs) > 0 {
		addrs = c.clusterAddrs
	}
	return redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:     addrs,
		Username:  opt.Username,
		Password:  opt.Password,
		DB:        opt.DB,
		TLSConfig: opt.TLSConfig,
		PoolSize:  10, // covers the 3 sequential SCAN passes + overhead (TC-8)
	}), nil
}

func main() {
	purgeOnly := flag.Bool("purge-redis-only", false, "skip the PG UPDATE; run only the Redis SCAN+DEL purge")
	flag.Parse()

	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	cfg, err := loadConfig(os.Getenv, *purgeOnly)
	if err != nil {
		logger.Error("monthly_cost_reset_config_invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if cfg.purgeOnly {
		logger.Info("monthly_cost_reset_redis_only_mode_engaged")
	}

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()

	if code := run(ctx, logger, cfg); code != 0 {
		os.Exit(code)
	}
}

// run executes the reset. Returns the process exit code (0 success). Factored
// out of main so the orchestration is unit-testable.
func run(ctx context.Context, logger *slog.Logger, cfg config) int {
	started := time.Now()

	// --- Redis client (both modes need it) ---
	rdb, err := newRedisClient(cfg)
	if err != nil {
		logger.Error("monthly_cost_reset_redis_connect_failed", slog.String("error", err.Error()))
		return 1
	}
	defer func() { _ = rdb.Close() }()

	var rowsAffected int64

	// --- PG UPDATE (skipped in --purge-redis-only) ---
	if !cfg.purgeOnly {
		pool, perr := pgxpool.New(ctx, cfg.pgURI)
		if perr != nil {
			logger.Error("monthly_cost_reset_pg_connect_failed", slog.String("error", perr.Error()))
			return 1
		}
		defer pool.Close()

		if cfg.dryRun {
			logger.Info("monthly_cost_reset_dry_run_pg_update_skipped")
		} else {
			rowsAffected, perr = repository.ResetMonthlyCosts(ctx, pool)
			if perr != nil {
				logger.Error("monthly_cost_reset_pg_update_failed", slog.String("error", perr.Error()))
				return 1
			}
		}
		logger.Info("monthly_cost_reset_pg_update_complete", slog.Int64("rows_affected", rowsAffected))
	}

	// --- Redis SCAN+DEL ---
	var counts redisclient.PurgeCounts
	if cfg.dryRun {
		logger.Info("monthly_cost_reset_dry_run_redis_purge_skipped")
	} else {
		counts, err = redisclient.PurgeMonthlyState(ctx, rdb)
		if err != nil {
			logger.Error("monthly_cost_reset_redis_scan_failed", slog.String("error", err.Error()))
			return 1
		}
	}
	logger.Info("monthly_cost_reset_redis_scan_complete",
		slog.Int64("counter_keys", counts.CounterKeys),
		slog.Int64("sentinel_keys", counts.SentinelKeys),
		slog.Int64("dedupe_keys", counts.DedupeKeys))

	// --- Kafka audit emit (best-effort, 3-retry; BR-3.9) ---
	emitAudit(ctx, logger, cfg, started, rowsAffected, counts)

	if err := ctx.Err(); errors.Is(err, context.DeadlineExceeded) {
		logger.Error("monthly_cost_reset_timeout")
		return 1
	}

	logger.Info("monthly_cost_reset_complete",
		slog.Float64("total_runtime_seconds", time.Since(started).Seconds()),
		slog.Int64("rows_affected", rowsAffected),
		slog.Int64("redis_keys_deleted", counts.Total()))
	return 0
}

// auditRetryBackoff is the BR-3.9 m-3 3-attempt schedule.
var auditRetryBackoff = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}

// emitAudit publishes the completion (or redis-only) audit event with a
// 3-attempt exponential backoff. NON-FATAL — on all-3 failures it escalates to
// ERROR slog and returns; the cron still exits 0 because the data-plane work is
// already complete (BR-3.9).
func emitAudit(ctx context.Context, logger *slog.Logger, cfg config, started time.Time, rows int64, counts redisclient.PurgeCounts) {
	if len(cfg.kafkaBrokers) == 0 {
		logger.Warn("monthly_cost_reset_kafka_brokers_missing_audit_skipped")
		return
	}
	eventType := audit.EventType("monthly_cost_reset.completed")
	if cfg.purgeOnly {
		eventType = audit.EventType("monthly_cost_reset.redis_only.completed")
	}
	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.kafkaBrokers...),
		Topic:        "audit.event",
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
	}
	defer func() { _ = writer.Close() }()
	pub := audit.NewKafkaPublisher(writer, logger)

	evt := audit.Event{
		EventType: eventType,
		Timestamp: started,
		Success:   true,
		Metadata: map[string]any{
			"rows_affected":         rows,
			"counter_keys_deleted":  counts.CounterKeys,
			"sentinel_keys_deleted": counts.SentinelKeys + counts.DedupeKeys,
			"started_at":            started.UTC().Format(time.RFC3339),
			"completed_at":          time.Now().UTC().Format(time.RFC3339),
		},
	}

	var lastErr error
	for attempt, backoff := range auditRetryBackoff {
		if lastErr = pub.Publish(ctx, evt); lastErr == nil {
			if attempt > 0 {
				logger.Info("monthly_cost_reset_audit_emit_succeeded_on_retry", slog.Int("attempt", attempt+1))
			}
			return
		}
		logger.Warn("monthly_cost_reset_audit_emit_failed",
			slog.Int("attempt", attempt+1), slog.String("error", lastErr.Error()))
		if attempt < len(auditRetryBackoff)-1 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				lastErr = ctx.Err()
			}
		}
	}
	logger.Error("monthly_cost_reset_audit_emit_failed_all_retries",
		slog.Int("attempts", len(auditRetryBackoff)),
		slog.String("last_error", lastErr.Error()))
}
