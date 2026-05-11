# Staging environment composition — VPC + ACK + ACR.
#
# Bootstrap precondition (one-shot, ops): the OSS state backend, KMS key, and
# TableStore lock-table referenced in `backend.tf` MUST already exist; see
# scripts/infra/bootstrap-state-backend.sh.
#
# Invocation:
#   cd infra/terraform/envs/staging
#   terraform init
#   terraform plan -out=plan.tfplan
#   terraform apply plan.tfplan

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

# Common tags applied to every resource for cost-attribution + GitOps audit.
locals {
  common_tags = {
    project     = "he-api"
    environment = "staging"
    managed_by  = "terraform"
    epic        = "epic-1"
    story       = "1.3"
  }
}

# -----------------------------------------------------------------------------
# 1. VPC — VPC + 3-AZ vSwitches + NAT + EIP + default SG
# -----------------------------------------------------------------------------
module "vpc" {
  source             = "../../modules/vpc"
  vpc_cidr           = var.vpc_cidr
  vswitch_cidrs      = var.vswitch_cidrs
  availability_zones = var.availability_zones
  tags               = local.common_tags
}

# -----------------------------------------------------------------------------
# 2. ACK — Standard managed K8s cluster (Path A2 endpoint exposure)
# -----------------------------------------------------------------------------
module "ack" {
  source = "../../modules/ack"

  vswitch_ids       = module.vpc.vswitch_ids
  security_group_id = module.vpc.default_security_group_id

  k8s_version  = var.k8s_version
  service_cidr = var.service_cidr
  pod_cidr     = var.pod_cidr

  worker_count         = var.worker_count
  worker_instance_type = var.worker_instance_type

  # Architect Round 1 Q3 ruling — Path A2.
  api_server_public_access_enabled        = true
  api_server_public_access_allowed_cidrs  = var.api_server_public_access_allowed_cidrs

  tags = local.common_tags
}

# -----------------------------------------------------------------------------
# 3. ACR — Enterprise Edition Basic instance + he-api namespace + api-gateway repo
# -----------------------------------------------------------------------------
module "acr" {
  source        = "../../modules/acr"
  instance_name = var.acr_instance_name
  region        = var.region
  tags          = local.common_tags
}

# -----------------------------------------------------------------------------
# Outputs — for operator hand-off into dev-log T12 (retention status + 1.4 seeds)
# -----------------------------------------------------------------------------
output "cluster_id" {
  description = "ACK cluster ID — Story 1.4 ArgoCD reference."
  value       = module.ack.cluster_id
}

output "vpc_id" {
  description = "VPC ID."
  value       = module.vpc.vpc_id
}

output "acr_endpoint" {
  description = "ACR EE Basic endpoint — populates GitHub Secret ACR_REGISTRY."
  value       = module.acr.acr_endpoint
}

output "api_server_endpoint_intranet" {
  description = "Private ACK API server endpoint — Story 1.4 in-cluster controller target."
  value       = module.ack.api_server_endpoint_intranet
}

output "api_server_endpoint_internet" {
  description = "Public ACK API server endpoint (ACL-gated)."
  value       = module.ack.api_server_endpoint_internet
}
