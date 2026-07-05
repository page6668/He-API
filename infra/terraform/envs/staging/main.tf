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
    # Story 1.6 — Kubernetes provider drives admin Secret + namespace + RBAC.
    # Configuration is wired against the ACK cluster created by module.ack.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.27.0"
    }
    # Story 2.2 — tls provider materializes the auth-svc JWT signing key
    # (Wright Round 1 Q3 ruling option a: K8s-Secret-only, Vault deferred).
    tls = {
      source  = "hashicorp/tls"
      version = ">= 4.0.0"
    }
  }
}

provider "alicloud" {
  region = var.region
}

# Kubernetes provider configured against the ACK kubeconfig that module.ack
# materializes. The kubeconfig path is exposed via module.ack.kubeconfig_path
# (provided in 1.3); operator must run `terraform apply` after the cluster is
# reachable. The provider is consumed only by Story 1.6 resources below.
provider "kubernetes" {
  config_path = var.kubeconfig_path
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
  db_tags = merge(local.common_tags, {
    story = "1.6"
  })
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

# -----------------------------------------------------------------------------
# 4. Story 1.6 — RDS PostgreSQL (Q4: three independent modules)
# -----------------------------------------------------------------------------
module "rds_postgres" {
  source = "../../modules/rds-postgres"

  vpc_id         = module.vpc.vpc_id
  vswitch_ids    = module.vpc.vswitch_ids
  kms_key_id     = var.kms_key_id
  zone_id        = var.postgres_multi_az_zone_id
  admin_password = var.postgres_admin_password

  tags = local.db_tags
}

# -----------------------------------------------------------------------------
# 5. Story 1.6 — Aliyun Tair (Redis 7.2 compat)
# -----------------------------------------------------------------------------
module "redis_tair" {
  source = "../../modules/redis-tair"

  vpc_id     = module.vpc.vpc_id
  vswitch_id = module.vpc.vswitch_ids[1]   # b 区(vsw[0])Redis 报 zone not supported,改用 c 区(vsw[1])
  kms_key_id = var.kms_key_id

  tags = local.db_tags
}

# -----------------------------------------------------------------------------
# 6. ClickHouse —— 香港最省档「先不建」(用户 2026-07 决策:早期无用户,省 ~¥1k+/月)。
#    用量分析(Epic 9 / analytics-svc)暂不可用;需要时把此模块 + outputs 恢复即可。
# -----------------------------------------------------------------------------

# -----------------------------------------------------------------------------
# 7. Story 1.6 — K8s namespaces + admin Secret (M-1 ruling: Vault deferred).
#    Two namespaces, separated by RBAC; both Secrets ACK-KMS envelope-encrypted.
# -----------------------------------------------------------------------------

resource "kubernetes_namespace" "he_api_ops" {
  metadata {
    name = "he-api-ops"
    labels = {
      "he-api/role"  = "ops"
      "he-api/story" = "1.6"
    }
  }
}

resource "kubernetes_namespace" "he_api_staging" {
  metadata {
    name = "he-api-staging"
    labels = {
      "he-api/role"  = "app"
      "he-api/story" = "1.6"
    }
  }
}

# ClusterRoleBinding granting ops-cluster-admin to operators only.
# The 'ops-cluster-admin' ClusterRole MUST be pre-created out-of-band by the
# cluster admin (binding to it is in-scope; ClusterRole authoring is not).
resource "kubernetes_cluster_role_binding" "he_api_ops_admin" {
  metadata {
    name = "he-api-ops-cluster-admin"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "ops-cluster-admin"
  }
  subject {
    kind      = "Group"
    name      = var.ops_admin_group
    api_group = "rbac.authorization.k8s.io"
  }
}

# Admin Secret: he-api-db-admin-creds in he-api-ops namespace.
# M-1.1 — namespace + RBAC separation (NOT tool separation).
# M-1.3 — ACK KMS envelope encryption annotation.
# BR-2.3 — passwords NEVER written to git; Terraform variables (sensitive=true)
# sourced from a tfvars file gitignored under infra/terraform/**/*.tfvars (see
# .gitignore). The K8s Secret is the operator-visible storage; Terraform state
# is OSS-backed + KMS-encrypted per 1.3.
resource "kubernetes_secret" "he_api_db_admin_creds" {
  metadata {
    name      = "he-api-db-admin-creds"
    namespace = "he-api-ops" # ops-only RBAC; matches kubernetes_namespace.he_api_ops
    annotations = {
      "cloud.alibaba.com/kms-encrypted" = "true"
    }
    labels = {
      "he-api/story" = "1.6"
    }
  }
  depends_on = [kubernetes_namespace.he_api_ops]
  type = "Opaque"
  data = {
    postgres_admin_password = var.postgres_admin_password
    redis_admin_password    = var.redis_admin_password
    # clickhouse_admin_password 已移除(ClickHouse 本档不建)
  }
}
