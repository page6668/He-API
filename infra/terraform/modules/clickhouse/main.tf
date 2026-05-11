# Aliyun ClickHouse 24+ module — staging baseline per Story 1.6 AC1.
#
# Q4 — three independent DB modules (this is one).
# BR-1.1 — VPC-private only; no public_connection_string.
# BR-1.2 — TLS via enable_https + tls_enabled.
# BR-1.3 — KMS envelope encryption.
# BR-1.4 — staging baseline 2c8g via db_node_class S8 (Aliyun ClickHouse spec).
# BR-1.6 — outputs expose endpoint + port + app_user; not password.
# m-5 — endpoint is VPC-private DNS FQDN (*.clickhouseserver.aliyuncs.com).

resource "alicloud_click_house_db_cluster" "this" {
  db_cluster_class       = var.db_node_class
  db_cluster_network_type = "vpc"
  db_cluster_version     = "24.8"
  category               = "Basic"
  db_node_group_count    = 1
  db_node_storage        = 100
  payment_type           = "PayAsYouGo"
  storage_type           = "cloud_essd"
  vswitch_id             = var.vswitch_id
  vpc_id                 = var.vpc_id

  # BR-1.2 — TLS / HTTPS enforced.
  tls_enabled  = true
  enable_https = true

  # BR-1.3 — KMS envelope encryption.
  encryption_key  = var.kms_key_id
  encryption_type = "CloudDisk"

  db_cluster_description = "he-api-staging-clickhouse"

  # BR-1.1 — VPC private only. No public_connection_string set.
}
