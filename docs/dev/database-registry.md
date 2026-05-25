# Database Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-25
**Total Stories Tracked**: 2
**Repository**: He-API
**Mode**: monolith

## Database Tables Registry

| Schema | Table | Story | Migration | Indices | FK Notes | Writers |
|--------|-------|-------|-----------|---------|----------|---------|
| `he_api` | `api_keys` | 3.2 | `0006_create_api_keys.sql` | `idx_api_keys_user_id` (user_id) · `idx_api_keys_hash` (key_hash) | `user_id → he_api.users(id) ON DELETE CASCADE`. `team_id` nullable, NO FK (Architect Q3 ratified DEFER — `he_api.teams` table creation + FK ALTER deferred to Epic 6+ when team-collaboration becomes a deliverable). | **Story 3.2**: READ-only on Validate hot path + fire-and-forget `UPDATE last_used_at`. **Story 5.1**: WRITE — `INSERT` (CreateApiKey RPC) + `UPDATE revoked_at=NOW()` (RevokeApiKey RPC). Story 5.1 explicitly OMITS `key_hash` from the ListApiKeys SELECT (BR-2.5 defence-in-depth at the SQL boundary). |

## Schema Evolution Timeline

| Date | Story | Migration | Change |
|------|-------|-----------|--------|
| 2026-05-18 | 3.2 | `0006_create_api_keys.sql` | CREATE TABLE `he_api.api_keys` (12 columns) + 2 indices. Forward-only; rollback via `atlas migrate down 1` (dynamic computation per `database-bootstrap.md §2`). |
| 2026-05-25 | 5.1 | _none_ | No DDL change (`cumulative_context_impact.db_schema=false`). Story 5.1 reuses the existing 3.2 schema; adds INSERT + UPDATE writers on `he_api.api_keys` (see Writers column above). |
