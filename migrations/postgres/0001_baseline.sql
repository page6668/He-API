-- Story 1.6 — PostgreSQL baseline migration (Atlas versioned mode, Q1 ruling).
--
-- BR-3.6 — Baseline migration contains NO business tables. Only the minimal
-- bootstrap surface required by Epic 2+ services:
--   * pgcrypto extension (provides gen_random_uuid() per architecture §4.1).
--   * he_api schema (per architecture §4 namespacing).
--   * he_api application role with least-privilege grants scoped to that schema.
--
-- Application password (:'app_password') is injected via the psql -v
-- substitution mechanism. M-1 ruling: the value is sourced from K8s Secret
-- he-api-db-creds (he-api-staging namespace), NOT from Vault. Vault is
-- deferred to a future dedicated Story; see database-bootstrap.md §3.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE SCHEMA IF NOT EXISTS he_api;

-- Application role. Least-privilege per BR-2.2: USAGE+CREATE on the he_api
-- schema, and default ALL-on-future-tables only inside that schema. The role
-- is intentionally non-superuser, non-role-creator, non-database-creator (the
-- LOGIN attribute is the only elevated bit), per BR-2.2.
CREATE ROLE he_api LOGIN PASSWORD :'app_password';

GRANT USAGE, CREATE ON SCHEMA he_api TO he_api;
ALTER DEFAULT PRIVILEGES IN SCHEMA he_api GRANT ALL ON TABLES TO he_api;
ALTER DEFAULT PRIVILEGES IN SCHEMA he_api GRANT ALL ON SEQUENCES TO he_api;
