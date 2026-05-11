-- Story 1.6 — ClickHouse baseline migration (golang-migrate, Q2 ruling).
--
-- BR-3.6 — No business tables. The baseline establishes:
--   * he_api database (per architecture §4 namespacing).
--   * he_api application user with least-privilege SELECT + INSERT
--     scoped to the he_api database (no destructive verbs, per BR-2.2).
--
-- Application password ({app_password:String}) is the golang-migrate parameter
-- substitution mechanism. The value is sourced from K8s Secret he-api-db-creds
-- (he-api-staging namespace) by scripts/db-migrate.sh; M-1 ruling: NOT Vault.

CREATE DATABASE IF NOT EXISTS he_api;

CREATE USER IF NOT EXISTS he_api IDENTIFIED WITH plaintext_password BY {app_password:String};

GRANT SELECT, INSERT ON he_api.* TO he_api;
