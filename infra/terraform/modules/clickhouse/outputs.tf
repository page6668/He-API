# clickhouse outputs — exposed to envs/staging.
# BR-1.6 — endpoint + port + app_user; NEVER password.
# m-5 — endpoint is VPC-private DNS FQDN.

output "endpoint" {
  description = "VPC-private DNS FQDN of the ClickHouse cluster (e.g., cc-uf6xxx.clickhouseserver.aliyuncs.com)."
  value       = alicloud_click_house_db_cluster.this.connection_string
}

output "port" {
  description = "ClickHouse HTTPS port (8443)."
  value       = alicloud_click_house_db_cluster.this.port
}

output "app_user" {
  description = "Application user (scoped to he_api database)."
  value       = var.app_user
}

output "instance_id" {
  description = "ClickHouse cluster ID."
  value       = alicloud_click_house_db_cluster.this.id
}
