# Aliyun Tair (Redis 7.2 compatible) module — staging baseline per Story 1.6 AC1.
#
# Architect Round 1 Q4 — three independent DB modules (this is one).
# BR-1.1 — VPC-private only.
# BR-1.2 — TLS enforced via ssl_enable=Open.
# BR-1.3 — KMS envelope encryption.
# BR-1.4 — instance_class default redis.shard.small.ce.
# BR-1.6 — Outputs expose endpoint + port + app_user; not password.
# m-5 — Endpoint resolves to VPC-private DNS FQDN.

resource "alicloud_kvstore_instance" "this" {
  instance_name = "he-api-staging-tair"
  instance_type = "Tair"
  # Tair Cluster Edition aligned with Redis 7.2 ACL model.
  instance_class    = var.instance_class
  engine_version    = "7.0"
  architecture_type = "cluster"
  vswitch_id        = var.vswitch_id
  payment_type      = "PostPaid"

  # BR-1.2 — TLS in transit enforced.
  tls_enabled = true
  ssl_enable  = "Open"

  # BR-1.3 — KMS envelope encryption.
  encryption_key = var.kms_key_id

  # BR-1.1 — no public network. security_ips empty means VPC-internal only.
  security_ips = []

  tags = var.tags
}
