# Staging env outputs — Story 1.6 DB endpoints (m-5: FQDNs, NOT IPs).
#
# Consumers (Epic 2-10 services) read these via `terraform output -json` or via
# the K8s Secret `he-api-db-creds` (he-api-staging namespace) which embeds the
# fully formed URIs (HE_API_DB_*_URI). The Secret is rendered from the Helm
# chart infra/helm/db-doctor/templates/secret.yaml with values populated from
# Terraform-injected variables (M-1 ruling: NOT ExternalSecret / SealedSecret).

output "postgres_endpoint" {
  description = "VPC-private DNS FQDN of the PostgreSQL instance. Maintenance windows may rotate the underlying IP; always consume the FQDN (m-5 ruling)."
  value       = module.rds_postgres.endpoint
}

output "postgres_port" {
  description = "PostgreSQL port."
  value       = module.rds_postgres.port
}

output "redis_endpoint" {
  description = "VPC-private DNS FQDN of the Tair (Redis 7.2 compat) instance (m-5 ruling)."
  value       = module.redis_tair.endpoint
}

output "redis_port" {
  description = "Tair (Redis) port."
  value       = module.redis_tair.port
}

output "clickhouse_endpoint" {
  description = "VPC-private DNS FQDN of the ClickHouse cluster (m-5 ruling)."
  value       = module.clickhouse.endpoint
}

output "clickhouse_port" {
  description = "ClickHouse HTTPS port."
  value       = module.clickhouse.port
}
