-- Story 9.1 — FIRST ClickHouse business tables (golang-migrate paired up/down).
--
-- Realises data-models §4.2 `request_logs` (verbatim — MergeTree, Q-DEDUP keeps
-- the engine unchanged) + `request_logs_hourly_agg` WITH the Architect-ratified
-- Q-AGG amendment (H-2):
--   * ADD  success_count UInt64 = countIf(status_code < 400)
--          → month/quarter success_rate = sum(success_count)/sum(request_count)
--            is computable from the MV (a count is sum-compatible, so
--            SummingMergeTree merges it correctly).
--   * DROP avg_latency_ms / p95_latency_ms — avg() and quantile() stored as
--          plain SummingMergeTree columns are SUMMED on background merge →
--          statistical garbage. 9.1 consumes no latency aggregate, so the
--          realized MV omits them (a future latency story re-adds them as
--          avgState()/quantileState(0.95)() AggregateFunction columns).
-- This amendment is recorded as a data-models §4.5 Change Log row.
--
-- M-1: this migration runs under the migration-admin credential
-- (scripts/db-migrate.sh), NOT the `he_api` app user (which holds SELECT, INSERT
-- only — no DDL). CREATE TABLE / CREATE MATERIALIZED VIEW need DDL privileges.

-- 调用日志 (raw per-request store; realtime "今日" reads this directly per Q-RT).
CREATE TABLE IF NOT EXISTS he_api.request_logs (
  he_request_id String,
  user_id UUID,
  api_key_id UUID,
  team_id UUID,
  model String,
  upstream_model String,
  routing_strategy String,
  selected_by_strategy String,                  -- 策略实际选中模型
  status_code UInt16,
  prompt_tokens UInt32,
  completion_tokens UInt32,
  total_tokens UInt32,
  cost_usd Decimal64(6),
  latency_ms_total UInt32,
  latency_ms_gateway UInt32,
  latency_ms_upstream UInt32,
  ttfb_ms UInt32,                               -- 流式首字符
  is_streaming UInt8,
  client_ip IPv4,
  client_country FixedString(2),
  user_agent String,
  error_code String,
  error_message String,
  ts DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(ts)
ORDER BY (user_id, ts, he_request_id)
TTL ts + INTERVAL 90 DAY;                       -- 90 天保留 (durable SoT for Story 9.3 download)

-- 用量小时聚合 (month/quarter reads this — cheap over wide windows; H-2 amendment).
CREATE MATERIALIZED VIEW IF NOT EXISTS he_api.request_logs_hourly_agg
ENGINE = SummingMergeTree()
ORDER BY (user_id, model, hour)
AS SELECT
  user_id,
  model,
  toStartOfHour(ts) AS hour,
  count() AS request_count,
  countIf(status_code < 400) AS success_count,  -- H-2: success_rate denominator/numerator over the MV
  sum(prompt_tokens) AS prompt_tokens,
  sum(completion_tokens) AS completion_tokens,
  sum(total_tokens) AS total_tokens,
  sum(cost_usd) AS cost_usd
FROM he_api.request_logs
GROUP BY user_id, model, hour;
