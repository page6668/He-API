# oss-gdpr-exports module outputs.

output "bucket_name" {
  description = "OSS bucket name (e.g., he-api-gdpr-exports)."
  value       = alicloud_oss_bucket.this.bucket
}

output "endpoint" {
  description = "Region-bound OSS endpoint (e.g., oss-cn-shanghai.aliyuncs.com)."
  value       = "oss-${var.region}.aliyuncs.com"
}

output "bucket_arn" {
  description = "Aliyun resource arn for the bucket (handy for downstream RAM policy attachments)."
  value       = "acs:oss:*:*:${alicloud_oss_bucket.this.bucket}"
}
