# Terraform & provider version pins for the clickhouse module.

terraform {
  required_version = ">= 1.7"

  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}
