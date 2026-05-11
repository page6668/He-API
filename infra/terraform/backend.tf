# Canonical Terraform state backend declaration (staging).
#
# Why a shared backend.tf at infra/terraform/ root?
#   This file is the single-source-of-truth for the staging backend config.
#   `infra/terraform/envs/staging/backend.tf` re-declares the same block (it
#   has to be in the root module for Terraform to read it); operators MUST
#   keep both files in sync OR replace envs/staging/backend.tf with a symlink
#   to this file. Re-run `terraform init -reconfigure` after editing.
#
# Why OSS + TableStore + KMS?
#   * OSS bucket — durable, versioned, KMS-encrypted state object.
#   * TableStore  — distributed lock for concurrent `terraform apply` safety
#                   (Architect Round 1 Q2 ruling — Aliyun-native, no S3-compat).
#   * encrypt = true — forces SSE on every state write (defense in depth on
#                      top of bucket-level KMS).
#
# Bootstrap order:
#   1. scripts/infra/bootstrap-state-backend.sh staging   (ops-only, one-shot)
#   2. cd envs/staging && terraform init
#
# Restrictions per Architect Round 1 ruling H-3 + Q4:
#   The OSS bucket / KMS key / TableStore lock-table created by the bootstrap
#   script MUST NEVER be included in any `terraform destroy` run — they are
#   Epic-level shared resources. Deleting any of them destroys ALL stored
#   environment state files. See README.md "Rollback predicate".

terraform {
  required_version = ">= 1.7"

  backend "oss" {
    bucket              = "he-api-tfstate-staging-sh"
    prefix              = "envs/staging"
    region              = "cn-shanghai"
    tablestore_endpoint = "https://he-api-tfstate-staging.cn-shanghai.ots.aliyuncs.com"
    tablestore_table    = "terraform-lock"
    encrypt             = true
  }
}
