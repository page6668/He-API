# rds-postgres module inputs — wired to 1.3 VPC + KMS from envs/staging.

variable "vpc_id" {
  type        = string
  description = "VPC ID — sourced from module.vpc.vpc_id (1.3)."
}

variable "vswitch_ids" {
  type        = list(string)
  description = "vSwitch IDs (one per AZ) — sourced from module.vpc.vswitch_ids (1.3). Multi-AZ HA picks the first two."
}

variable "kms_key_id" {
  type        = string
  description = "KMS key ID for static encryption (BR-1.3). Must be the same KMS key used by 1.3 OSS state backend."
}

variable "instance_class" {
  type        = string
  description = "RDS instance class. 香港现售最小规格(旧 pg.n2.medium.2c 已下线)。"
  default     = "pg.n2.2c.2m"
}

variable "instance_storage_gb" {
  type        = number
  description = "Allocated storage (GB) for the RDS instance."
  default     = 100
}

variable "zone_id" {
  type        = string
  description = <<-EOT
    Multi-AZ HA zone selector for the RDS instance (Aliyun region-specific string).
    Staging default is the cn-shanghai multi-AZ id MAZ2(f,g). To deploy in another
    region (e.g. cn-hongkong) supply that region's multi-AZ id — query the valid
    value with:  aliyun rds DescribeAvailableZones --RegionId <region> --Engine PostgreSQL
    (look for a multi-AZ ZoneId such as MAZ1(b,c)). Must be consistent with var.region
    and the two vSwitch AZs passed in vswitch_ids.
  EOT
  default     = "MAZ2(f,g)"
}

variable "app_user" {
  type        = string
  description = "Application role name (least-privilege scope to he_api schema)."
  default     = "he_api"
}

variable "tags" {
  type        = map(string)
  description = "Resource tags applied to the RDS instance."
  default     = {}
}
