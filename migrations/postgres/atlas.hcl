# Atlas migration configuration — Story 1.6 Q1 ruling: PostgreSQL migrations
# use the **versioned-migrations** mode exclusively. Declarative apply via
# `atlas` non-versioned pathways is PROHIBITED in production (m-1 ruling)
# because it produces no audit trail and emits opaque diffs.
#
# Operator invocation (matches scripts/db-migrate.sh up):
#   atlas migrate apply --env staging --dir file://migrations/postgres
#
# Drift gate (matches scripts/db-migrate.sh diff + CI db-migrate-check.yml):
#   atlas migrate diff --env staging
#   atlas migrate status --env staging

env "staging" {
  url = getenv("HE_API_DB_POSTGRES_URI")

  # Versioned-migrations mode — Q1 + m-1 lock-in. The migration block below
  # selects versioned mode; declarative source loaders are intentionally NOT
  # declared here so the production path can only ever invoke
  # `atlas migrate apply` (audit-trail-preserving).
  migration {
    dir = "file://migrations/postgres"
  }

  # Atlas integrity check — the atlas.sum hash file in this directory MUST
  # match the directory state at every `atlas migrate apply`. Tampering with
  # migration files without re-running `atlas migrate hash` fails the gate.
  format {
    migrate {
      apply = "{{ json . }}"
    }
  }
}
