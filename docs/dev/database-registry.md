# Database Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-06-03
**Total Stories Tracked**: 5
**Repository**: He-API
**Mode**: monolith

## Database Tables Registry

| Schema | Table | Story | Migration | Indices | FK Notes | Writers |
|--------|-------|-------|-----------|---------|----------|---------|
| `he_api` | `api_keys` | 3.2 | `0006_create_api_keys.sql` | `idx_api_keys_user_id` (user_id) · `idx_api_keys_hash` (key_hash) | `user_id → he_api.users(id) ON DELETE CASCADE`. `team_id` nullable, NO FK (Architect Q3 ratified DEFER — `he_api.teams` table creation + FK ALTER deferred to Epic 6+ when team-collaboration becomes a deliverable). | **Story 3.2**: READ-only on Validate hot path + fire-and-forget `UPDATE last_used_at`. **Story 5.1**: WRITE — `INSERT` (CreateApiKey RPC) + `UPDATE revoked_at=NOW()` (RevokeApiKey RPC). Story 5.1 explicitly OMITS `key_hash` from the ListApiKeys SELECT (BR-2.5 defence-in-depth at the SQL boundary). **Story 5.2**: WRITE — `UPDATE scope, monthly_cost_cap_usd WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL` (UpdateApiKey RPC; NO `updated_at` write — Architect Q-K, the column does not exist). The Validate hot-path SELECT now also reads `monthly_cost_cap_usd` so the cap rides the gateway bearer cache (AC4 enforcement without a per-request PG round-trip). |
| `he_api` | `models` | 6.2 | `0007_create_models_and_pricing.sql` | PK `id` | _(referenced BY `model_pricing.model_id`)_ | **Story 6.2**: seed-only WRITE in the migration (8 concrete catalogue models, idempotent `ON CONFLICT DO NOTHING`). NOT yet read at runtime — the in-process `packages/models-catalogue` remains the 6.2 candidate-set SoT (Q-D); the PG-backed `models.status` filter is a FUTURE story. |
| `he_api` | `model_pricing` | 6.2 | `0007_create_models_and_pricing.sql` | PK `(model_id, effective_at)` | `model_id → he_api.models(id)` | **Story 6.2**: seed-only WRITE in the migration (1 `effective_at` row per concrete model). **READ** by `routing-svc/internal/pricing` (Q-K read-only pgx pool) — boot snapshot + 60s refresh (Q-E), `SELECT model_id, effective_at, (input+output)::float8`; latest-`effective_at` per model reduced Go-side. The `cost` strategy ranks by the (input+output) sum ascending (Q-J). NEVER a per-request query (BR2-2). |

## Schema Evolution Timeline

| Date | Story | Migration | Change |
|------|-------|-----------|--------|
| 2026-05-18 | 3.2 | `0006_create_api_keys.sql` | CREATE TABLE `he_api.api_keys` (12 columns) + 2 indices. Forward-only; rollback via `atlas migrate down 1` (dynamic computation per `database-bootstrap.md §2`). |
| 2026-05-25 | 5.1 | _none_ | No DDL change (`cumulative_context_impact.db_schema=false`). Story 5.1 reuses the existing 3.2 schema; adds INSERT + UPDATE writers on `he_api.api_keys` (see Writers column above). |
| 2026-05-26 | 5.3 | _none_ | No DDL change (`cumulative_context_impact.db_schema=false`). Story 5.3 introduces 3-axis rate-limit state in Redis ONLY (`ratelimit:key:{api_key_id}:{qps,rpm,tpm}` per data-models.md §4.3 NEW rows); zero PostgreSQL touch. `ResolveCeilings` is a pure constant returning `FreeTierDefaults` per Architect H-1 remediation — no per-request PG SELECT. |
| 2026-06-03 | 5.2 | _none_ | No DDL change (`cumulative_context_impact.db_schema=false`; Architect Q-K ratified NO `updated_at` add). Story 5.2 adds an `UPDATE scope + monthly_cost_cap_usd` writer (UpdateApiKey RPC) + extends the Validate hot-path SELECT with `monthly_cost_cap_usd`. NEW Redis keys: `auth:apikey:config_updated:{api_key_id}` (TTL 300s, BR-1.9 sentinel) + `usage:apikey:{api_key_id}:month_cost_usd` (Q-D realtime cost counter, no TTL — cron-reset by Story 5.4; gateway READ-only, billing-svc WRITE in Epic 6+). |
| 2026-06-03 | 5.4 | _none_ | No DDL change (`cumulative_context_impact.db_schema=false`). Story 5.4 adds a cron-only `UPDATE he_api.api_keys SET current_month_cost_usd = 0 WHERE revoked_at IS NULL` writer (`repository.ResetMonthlyCosts`, run by the `monthly-cost-reset` K8s CronJob at `0 0 1 * *` UTC) + a READ-only single-statement JOIN `users ⨝ api_keys` (`repository.LookupCapNotificationContext`, feeding the `GetCapNotificationContext` gRPC RPC). NEW Redis keys (no TTL — cron-cleared): `keystate:apikey:cap_tripped:{api_key_id}` (sticky-trip breaker) + `keystate:apikey:cap_warning_80_notified:{api_key_id}` + `keystate:apikey:cap_tripped_notified:{api_key_id}` (email-dedupe sentinels). The cron also SCAN+DELs `usage:apikey:*:month_cost_usd` (the Story-5.2 counter family). |
| 2026-06-03 | 6.2 | `0007_create_models_and_pricing.sql` | CREATE TABLE `he_api.models` + `he_api.model_pricing` (per data-models.md §4.1) + idempotent seed (8 concrete models + 1 pricing row each). Additive (no ALTER/DROP of existing tables), REVERSIBLE (`atlas migrate down 1` drops both, FK order), NON-DESTRUCTIVE (BR2-1). FIRST routing-svc PG source: `model_pricing` READ by `internal/pricing` (Q-K). `he_api.models` seeded but not yet runtime-read (in-process catalogue is the 6.2 candidate SoT, Q-D). |
