# redis-tair outputs — exposed to envs/staging.
# BR-1.6 — endpoint + port + app_user; NEVER password.
# m-5 — endpoint is VPC-private DNS FQDN.

output "endpoint" {
  description = "VPC-private DNS FQDN of the Tair instance (e.g., r-uf6xxx.redis.rds.aliyuncs.com)."
  value       = alicloud_kvstore_instance.this.connection_domain
}

output "port" {
  description = "Tair (Redis) port. Default 6379."
  value       = alicloud_kvstore_instance.this.port
}

output "app_user" {
  description = "Application ACL user name (scope ~he-api:* keys)."
  value       = var.app_user
}

output "instance_id" {
  description = "Tair instance ID."
  value       = alicloud_kvstore_instance.this.id
}
