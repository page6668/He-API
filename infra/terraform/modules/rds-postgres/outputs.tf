# rds-postgres outputs — exposed to envs/staging.
#
# BR-1.6 — expose endpoint + port + app_user. NEVER expose password
# (password is sourced from K8s Secret he-api-db-creds at consumer pods).
# m-5 — endpoint value resolves to VPC-private DNS hostname (FQDN), not IP.

output "endpoint" {
  description = "VPC-private DNS FQDN of the RDS PG instance (e.g., rm-uf6xxx.pg.rds.aliyuncs.com). IPs may rotate during managed-DB maintenance — always consume via FQDN."
  value       = alicloud_db_instance.this.connection_string
}

output "port" {
  description = "PostgreSQL port (always 5432 in this module)."
  value       = alicloud_db_instance.this.port
}

output "app_user" {
  description = "Application role name (least-privilege scope to he_api schema)."
  value       = var.app_user
}

output "instance_id" {
  description = "RDS instance ID — for operator runbook references."
  value       = alicloud_db_instance.this.id
}
