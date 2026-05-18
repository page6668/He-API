# Database Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-18
**Total Stories Tracked**: 1
**Repository**: He-API
**Mode**: monolith

## Database Tables Registry

| Schema | Table | Story | Migration | Indices | FK Notes |
|--------|-------|-------|-----------|---------|----------|
| `he_api` | `api_keys` | 3.2 | `0006_create_api_keys.sql` | `idx_api_keys_user_id` (user_id) · `idx_api_keys_hash` (key_hash) | `user_id → he_api.users(id) ON DELETE CASCADE`. `team_id` nullable, NO FK (Epic 5 ALTER lands the `→ he_api.teams(id)` constraint when the teams table is created). |

## Schema Evolution Timeline

| Date | Story | Migration | Change |
|------|-------|-----------|--------|
| 2026-05-18 | 3.2 | `0006_create_api_keys.sql` | CREATE TABLE `he_api.api_keys` (12 columns) + 2 indices. Forward-only; rollback via `atlas migrate down 1` (dynamic computation per `database-bootstrap.md §2`). |
