# oss-gdpr-exports module inputs.
#
# Story 2.6 AC4 BR-4.5 + TS-CONS-003 (compliance §9.1 数据不出境) +
# TS-CONS-015-shared-bucket-prefix (Architect R-3 — shared bucket + per-user
# prefix, NOT per-user bucket).

variable "bucket_name" {
  type        = string
  description = "OSS bucket name — defaults to he-api-gdpr-exports. Per AC4 BR-4.5."
  default     = "he-api-gdpr-exports"
}

variable "region" {
  type        = string
  description = "Aliyun region — MUST be cn-shanghai per TS-CONS-003 (data residency)."
  default     = "cn-shanghai"

  validation {
    condition     = startswith(var.region, "cn-")
    error_message = "region must be a 境内 (cn-*) region per TS-CONS-003 compliance §9.1."
  }
}

variable "kms_key_id" {
  type        = string
  description = "KMS CMK id for server-side encryption (mirrors Story 1.6 §3 envelope-encryption pattern)."
}

variable "analytics_svc_ram_role_arn" {
  type        = string
  description = "ACK-bound RAM role arn for analytics-svc — only principal allowed to PutObject + ListObjectsV2 (worker uploads ZIP)."
}

variable "notification_svc_ram_role_arn" {
  type        = string
  description = "ACK-bound RAM role arn for notification-svc — only principal allowed to GetObject (signed-URL backend; sign-only without read defeats the purpose of the URL)."
}
