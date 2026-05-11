# oss-state — reference-only outputs.
#
# This file documents the naming convention bootstrapped by
# `scripts/infra/bootstrap-state-backend.sh`. The outputs below are NOT
# computed from real resources (the module declares no resources by design);
# they serve as a literal, in-code reference that env compositions can
# eyeball-compare against their backend.tf hardcoded values.

output "oss_bucket_name_pattern" {
  description = "OSS bucket naming pattern used by the bootstrap script."
  value       = "he-api-tfstate-<env>-<region_short>"
}

output "kms_alias_pattern" {
  description = "KMS CMK alias naming pattern."
  value       = "alias/he-api-tfstate-<env>"
}

output "tablestore_table_name" {
  description = "Fixed TableStore table name used as the OSS-backend lock table."
  value       = "terraform-lock"
}

output "tablestore_pk_name" {
  description = "Fixed PK column name on the lock table."
  value       = "LockID"
}
