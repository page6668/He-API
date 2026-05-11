# Terraform & provider version pins for the rds-postgres module.
# Matches the 1.7+ baseline used by 1.3 modules (vpc / ack / acr / oss-state).

terraform {
  required_version = ">= 1.7"

  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}
