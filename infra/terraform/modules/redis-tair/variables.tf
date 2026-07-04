# redis-tair module inputs — Aliyun Tair (Redis 7.2 compatible) cluster edition.

variable "vpc_id" {
  type        = string
  description = "VPC ID — sourced from module.vpc.vpc_id."
}

variable "vswitch_id" {
  type        = string
  description = "vSwitch ID (single AZ for Tair — cluster edition spans multiple shards within the AZ). Sourced from module.vpc.vswitch_ids[0]."
}

variable "kms_key_id" {
  type        = string
  description = "KMS key ID for static encryption (BR-1.3)."
}

variable "instance_class" {
  type        = string
  description = "Redis 规格。香港 community 现售最小集群版(旧 redis.shard.small.ce 已下线)。"
  default     = "redis.logic.sharding.2g.2db.0rodb.4proxy.default"
}

variable "access_cidrs" {
  type        = list(string)
  description = "security_ips 白名单(provider 要求 ≥1 条)。默认覆盖 VPC(10.0.0.0/8)+ Pod/Service(172.16.0.0/12)私网段;实例无公网端点,仅 VPC 内可达。"
  default     = ["10.0.0.0/8", "172.16.0.0/12"]
}

variable "app_user" {
  type        = string
  description = "Application ACL user name (scoped to he-api:* key prefix)."
  default     = "he-api"
}

variable "tags" {
  type        = map(string)
  description = "Resource tags."
  default     = {}
}
