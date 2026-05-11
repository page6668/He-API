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
  description = "Tair (Redis 7.2 compat) instance class. Staging baseline per BR-1.4."
  default     = "redis.shard.small.ce"
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
