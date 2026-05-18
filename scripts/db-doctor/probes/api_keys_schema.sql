-- Story 3.2 — schema probe for he_api.api_keys (AC3 BR-3.8).
-- Shared by scripts/db-doctor.sh AND infra/helm/db-doctor/templates/configmap.yaml.
--
-- Asserts: he_api.api_keys table exists AND both indices
-- (idx_api_keys_user_id + idx_api_keys_hash) exist in the he_api schema.
-- A failed probe (row count != 1 / != 2) trips db-doctor.sh exit=1.

SELECT 1 AS api_keys_table_present
  FROM pg_tables
 WHERE schemaname = 'he_api'
   AND tablename  = 'api_keys';

SELECT COUNT(*) AS api_keys_index_count
  FROM pg_indexes
 WHERE schemaname = 'he_api'
   AND tablename  = 'api_keys'
   AND indexname IN ('idx_api_keys_user_id', 'idx_api_keys_hash');
