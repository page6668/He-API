# Staging backend — duplicate of infra/terraform/backend.tf (Terraform reads
# the backend block from each env's own root module; keep this in sync with
# the canonical reference, or replace this file with a symlink to ../../backend.tf).

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
