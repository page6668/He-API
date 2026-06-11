-- Story 9.1 — reverse of 002_create_request_logs.up.sql (golang-migrate paired).
-- Drop the MATERIALIZED VIEW first (it reads request_logs), then the table.
-- Reversible + non-destructive in the migration sense (drops only the objects
-- this migration created; the 001 baseline he_api database + user are untouched).
DROP VIEW IF EXISTS he_api.request_logs_hourly_agg;
DROP TABLE IF EXISTS he_api.request_logs;
