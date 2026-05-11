-- Story 1.6 — ClickHouse health probe (Q5 single-source-of-truth).
-- Shared by scripts/db-doctor.sh AND infra/helm/db-doctor/templates/configmap.yaml.

SELECT 1 AS health_check;
