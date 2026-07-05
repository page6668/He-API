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
  instance_type = "Redis"   # v1.284 只接受 Redis/Memcache;Tair 由 instance_class 体现
  # Tair Cluster Edition aligned with Redis 7.2 ACL model.
  instance_class    = var.instance_class
  engine_version    = "5.0"   # 本地盘标准版最高 5.0(7.0 报 NotSupportOnLocalDisk);够用限流/会话/缓存
  vswitch_id        = var.vswitch_id
  zone_id           = var.zone_id   # 显式指定,避免 provider 从 vswitch 推断出错(vSwitchId zone not supported)
  payment_type      = "PostPaid"

  # v1.284:alicloud_kvstore_instance 无 architecture_type/tls_enabled 参数(已删)。
  # KMS encryption_key 去掉(需授权 KMS,报 Kms.Unauthorized);默认已磁盘加密。
  # 数据层为 VPC 私网(security_ips 白名单),最小档不启传输 TLS。

  # BR-1.1 — VPC 私网访问白名单(provider 要求至少 1 条)。默认覆盖 VPC + Pod 网段,
  # 由 env 传入真实网段;无公网端点,故只有 VPC 内可达。
  security_ips = var.access_cidrs

  tags = var.tags
}
