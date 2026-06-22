# RDS PostgreSQL 16 module — staging baseline per Story 1.6 AC1.
#
# Architect Round 1 rulings honored here:
#   * Q4 — three independent DB modules (this is one of three; mirrors
#     1.3's per-resource module pattern under modules/{vpc,ack,acr,oss-state}).
#   * BR-1.1 — VPC private network only; no public network attribute.
#   * BR-1.2 — TLS in transit enforced via parameter group + force_ssl.
#   * BR-1.3 — Static encryption via cloud KMS envelope.
#   * BR-1.4 — Instance class staging baseline pg.n2.medium.2c.
#   * BR-1.5 — Idempotent terraform apply (no triggers / random_id without keepers).
#   * BR-1.6 — Outputs expose endpoint + port + app_user; NEVER password.
#   * m-5 — Endpoint resolves to VPC-private DNS hostname (FQDN), not IP.

resource "alicloud_db_instance" "this" {
  engine               = "PostgreSQL"
  engine_version       = "16.0"
  instance_type        = var.instance_class
  instance_storage     = var.instance_storage_gb
  instance_charge_type = "Postpaid"

  # Multi-AZ HighAvailability category (BR-1.4 + AC1 main scenario "多 AZ").
  # zone_id is region-specific (var.zone_id) so the module is portable across
  # regions (e.g. cn-shanghai → cn-hongkong); default preserves staging behavior.
  category    = "HighAvailability"
  vswitch_id  = join(",", slice(var.vswitch_ids, 0, 2))
  zone_id     = var.zone_id
  monitoring_period = 60

  instance_name = "he-api-staging-pg"

  # BR-1.3 — KMS envelope encryption at rest.
  encryption_key = var.kms_key_id
  tde_status     = "Enabled"

  # BR-1.2 — TLS enforced via parameter group.
  parameters {
    name  = "rds_force_ssl"
    value = "on"
  }
  force_ssl = true

  # BR-1.1 — VPC-private only. security_ips remains empty so the instance is
  # reachable from VPC peers only; no public CIDR is ever permitted here.
  security_ips = []

  # AC1 Deliverables — 7-day automatic backup.
  backup_retention_period = 7

  # Db port pinned for predictable downstream connection string.
  port = 5432

  tags = var.tags
}

# Application role (he_api) is created by migrations/postgres/0001_baseline.sql
# at first `atlas migrate apply`. The Terraform module owns the instance + the
# admin endpoint; baseline migration owns roles + schema + extension grants.
# This separation keeps the module reusable for prod where admin credentials
# come from a different K8s Secret namespace.

variable "admin_password" {
  type        = string
  description = "Admin account secret. Wired from envs/staging variable postgres_admin_password (sensitive); never committed (BR-2.3). The K8s Secret he-api-db-admin-creds is the operator-visible store."
  sensitive   = true
}

resource "alicloud_db_account" "admin" {
  db_instance_id      = alicloud_db_instance.this.id
  account_name        = "he_admin"
  account_password    = var.admin_password
  account_type        = "Super"
  account_description = "Admin account for migration tooling. Supplied via K8s Secret he-api-db-admin-creds (Vault deferred per M-1 ruling)."
  lifecycle {
    # BR-2.3 — admin secret never lands in state in plaintext beyond the
    # initial apply; rotation happens out-of-band per database-bootstrap.md §3.
    ignore_changes = [account_password]
  }
}
