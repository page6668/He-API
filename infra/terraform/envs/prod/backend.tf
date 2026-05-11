# Production backend — DIFFERENT bucket + prefix from staging.

terraform {
  required_version = ">= 1.7"

  backend "oss" {
    bucket              = "he-api-tfstate-prod-sh"
    prefix              = "envs/prod"
    region              = "cn-shanghai"
    tablestore_endpoint = "https://he-api-tfstate-prod.cn-shanghai.ots.aliyuncs.com"
    tablestore_table    = "terraform-lock"
    encrypt             = true
  }
}
