-- Story 1.6 — PostgreSQL health probe (Q5 single-source-of-truth).
-- Shared by scripts/db-doctor.sh AND infra/helm/db-doctor/templates/configmap.yaml.
-- No connection string / password embedded — caller supplies credentials.

SELECT 1 AS health_check;
