# ACR module — Aliyun Container Registry Enterprise Edition (EE) Basic.
#
# Architect Round 1 H-1 ruling — Fix A:
#   The Personal-tier resource (`alicloud_cr_namespace` / `alicloud_cr_repo`)
#   was rejected; Personal is now flagged legacy and lacks image signing /
#   scanning that Story 9 hardening requires. Enterprise Basic at ~¥100/mo
#   provides an independent registry endpoint and meets compliance early.
#
# Endpoint format (Q3 + H-1):
#   <instance_name>-registry.<region>.cr.aliyuncs.com
#   ≠ the shared `registry.<region>.aliyuncs.com` used by Personal.

terraform {
  required_version = ">= 1.7"
  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}

resource "alicloud_cr_ee_instance" "this" {
  instance_name  = var.instance_name
  instance_type  = "Basic"
  payment_type   = "Subscription"
  period         = 1
  renewal_status = "AutoRenewal"
  renew_period   = 1
  # No cross-region replication at Basic tier — would require Advanced.
}

resource "alicloud_cr_ee_namespace" "he_api" {
  instance_id        = alicloud_cr_ee_instance.this.id
  name               = "he-api"
  auto_create        = false
  default_visibility = "PRIVATE"
}

resource "alicloud_cr_ee_repo" "api_gateway" {
  instance_id = alicloud_cr_ee_instance.this.id
  namespace   = alicloud_cr_ee_namespace.he_api.name
  name        = "api-gateway"
  summary     = "API Gateway service"
  repo_type   = "PRIVATE"
  detail      = "He-API API Gateway container image. Built by .github/workflows/build-images.yml and pushed by deploy-staging.yml after Story 1.3 merges."
}
