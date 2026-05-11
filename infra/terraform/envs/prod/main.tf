# Production environment composition — VPC + ACK + ACR.
#
# **NOT APPLIED IN STORY 1.3** — code-only delivery. Production apply is
# gated until Story 1.7+ rollout review.
#
# Lifecycle protection (Architect Round 1 T6):
#   * `lifecycle { prevent_destroy = true }` is applied via dedicated
#     `terraform_data` guard resources tied to each protected module's
#     primary output (`module.vpc.vpc_id` / `module.ack.cluster_id`).
#     A `terraform destroy` run will refuse to delete the guards, which
#     transitively blocks destruction of the entire prod workspace.
#   * Distinct CIDR plan per M-4 ruling (10.30.x.0/24, NOT 10.20.x).

terraform {
  required_version = ">= 1.7"
  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}

provider "alicloud" {
  region = var.region
}

locals {
  common_tags = {
    project     = "he-api"
    environment = "prod"
    managed_by  = "terraform"
    epic        = "epic-1"
    story       = "1.3"
  }
}

# -----------------------------------------------------------------------------
# 1. VPC — 10.30.0.0/16 (DISTINCT from staging 10.20.0.0/16 per M-4 ruling)
# -----------------------------------------------------------------------------
module "vpc" {
  source             = "../../modules/vpc"
  vpc_cidr           = var.vpc_cidr            # 10.30.0.0/16
  vswitch_cidrs      = var.vswitch_cidrs       # 10.30.{1,2,3}.0/24 — NOT 10.20.x
  availability_zones = var.availability_zones
  tags               = local.common_tags
}

# module "vpc" → lifecycle { prevent_destroy = true } enforced via terraform_data.vpc_destroy_guard below.

# -----------------------------------------------------------------------------
# 2. ACK — Standard managed K8s, prod-scale worker pool (6 nodes, c7.xlarge)
# -----------------------------------------------------------------------------
module "ack" {
  source = "../../modules/ack"

  vswitch_ids       = module.vpc.vswitch_ids
  security_group_id = module.vpc.default_security_group_id

  k8s_version  = var.k8s_version
  service_cidr = var.service_cidr
  pod_cidr     = var.pod_cidr

  worker_count         = 6                      # prod baseline — 2 per AZ
  worker_instance_type = "ecs.c7.xlarge"        # 4C8G prod tier

  api_server_public_access_enabled        = true
  api_server_public_access_allowed_cidrs  = var.api_server_public_access_allowed_cidrs

  tags = local.common_tags
}

# module "ack" → lifecycle { prevent_destroy = true } enforced via terraform_data.ack_destroy_guard below.

# -----------------------------------------------------------------------------
# 3. ACR — same EE Basic shape as staging (separate prod instance)
#    Story 1.7+ may upgrade to EE Standard / Advanced — out of scope here.
# -----------------------------------------------------------------------------
module "acr" {
  source        = "../../modules/acr"
  instance_name = var.acr_instance_name
  region        = var.region
  tags          = local.common_tags
}

# -----------------------------------------------------------------------------
# Destroy guards — `terraform destroy` blocked at the prod workspace level.
# -----------------------------------------------------------------------------

resource "terraform_data" "vpc_destroy_guard" {
  # Tying triggers to module.vpc.vpc_id binds the guard to the VPC's identity.
  input = {
    vpc_id   = module.vpc.vpc_id
    protects = "module.vpc"
  }
  lifecycle {
    prevent_destroy = true
  }
}

resource "terraform_data" "ack_destroy_guard" {
  input = {
    cluster_id = module.ack.cluster_id
    protects   = "module.ack"
  }
  lifecycle {
    prevent_destroy = true
  }
}

# -----------------------------------------------------------------------------
# Outputs
# -----------------------------------------------------------------------------
output "cluster_id" {
  value = module.ack.cluster_id
}

output "vpc_id" {
  value = module.vpc.vpc_id
}

output "acr_endpoint" {
  value = module.acr.acr_endpoint
}
