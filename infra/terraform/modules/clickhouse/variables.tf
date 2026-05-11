# clickhouse module inputs — Aliyun ClickHouse 24+ managed cluster.

variable "vpc_id" {
  type        = string
  description = "VPC ID — sourced from module.vpc.vpc_id."
}

variable "vswitch_id" {
  type        = string
  description = "vSwitch ID — sourced from module.vpc.vswitch_ids[0]."
}

variable "kms_key_id" {
  type        = string
  description = "KMS key ID for static encryption (BR-1.3)."
}

variable "db_node_class" {
  type        = string
  description = "ClickHouse node class — staging baseline 2c8g per BR-1.4."
  default     = "S8"
}

variable "db_node_count" {
  type        = number
  description = "Number of ClickHouse nodes per shard."
  default     = 1
}

variable "app_user" {
  type        = string
  description = "Application user (scoped to he_api database)."
  default     = "he_api"
}

variable "tags" {
  type        = map(string)
  description = "Resource tags."
  default     = {}
}
