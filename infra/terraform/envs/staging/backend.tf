# Staging backend — duplicate of infra/terraform/backend.tf (Terraform reads
# the backend block from each env's own root module; keep this in sync with
# the canonical reference, or replace this file with a symlink to ../../backend.tf).

terraform {
  required_version = ">= 1.7"

  # 香港单节点部署:TableStore 状态锁已去掉(单人操作不需要;aliyun CLI 也不便建表)。
  # 多人协作再加回 tablestore_endpoint + tablestore_table。encrypt=true 仍启用 OSS 端加密。
  backend "oss" {
    bucket   = "he-api-tfstate-staging-hk"
    prefix   = "envs/staging"
    region   = "cn-hongkong"
    encrypt  = true
  }
}
