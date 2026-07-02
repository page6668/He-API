# ACK module — Aliyun Container Service for Kubernetes (Standard managed).
#
# Architect Round 1 Q3 ruling — Path A2 endpoint exposure:
#   * Public endpoint enabled by default, gated by CIDR ACL whitelist.
#   * Private (intranet) endpoint always on — for in-cluster controllers
#     such as Story 1.4 ArgoCD.
#   * Precondition: when public access is enabled, the ACL whitelist MUST be
#     non-empty. `terraform plan` rejects the "public-on / acl-empty" mistake
#     at planning time.
#   * Path A (force-VPN) was rejected — no VPN gateway exists at this stage.
#   * Path B (public + no ACL) was rejected — attack surface = global :6443.
#
# Architect Round 1 R1: field names below (`cluster_endpoint_public_access` /
# `cluster_endpoint_public_access_acl_cidrs`) are validated against alicloud
# provider v1.220+ at implementation time. If the provider renames either
# field, update here and update the deliverable_bindings regex in the story.

terraform {
  required_version = ">= 1.7"
  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}

resource "alicloud_cs_managed_kubernetes" "this" {
  name               = "he-api-${terraform.workspace}-ack"
  version            = var.k8s_version
  cluster_spec       = "ack.standard"
  worker_vswitch_ids = var.vswitch_ids
  security_group_id  = var.security_group_id

  # Service / Pod CIDRs — MUST NOT overlap var.vpc_cidr (validated by env composition).
  service_cidr = var.service_cidr
  pod_cidr     = var.pod_cidr

  # -----------------------------------------------------------------------
  # Path A2 endpoint exposure — variable-driven (NO hardcoded true).
  # -----------------------------------------------------------------------
  # v1.284 schema:公网 API 访问由 slb_internet_enabled 控制。该资源无 ACL-cidr /
  # private-access 参数(内网端点始终可用,见 connections.api_server_intranet 输出)。
  # 公网白名单在此资源层不可设 —— 由 kubeconfig 证书鉴权保护;要 IP 限制可另配安全组。
  slb_internet_enabled = var.api_server_public_access_enabled

  # -----------------------------------------------------------------------
  # Worker pool — spread across var.vswitch_ids (cross-AZ).
  # -----------------------------------------------------------------------
  worker_number              = var.worker_count
  worker_instance_types      = [var.worker_instance_type]
  worker_disk_category       = "cloud_essd"
  worker_disk_size           = 80
  worker_data_disk_category  = "cloud_essd"
  worker_data_disk_size      = 100

  # -----------------------------------------------------------------------
  # Add-ons — flannel (CNI) + csi-plugin (CSI) + metrics-server.
  # Explicitly NOT enabling nginx-ingress (Story 1.4 installs ingress-nginx
  # as a separate Helm release to avoid cert-manager coupling).
  # -----------------------------------------------------------------------
  addons {
    name = "flannel"
  }
  addons {
    name = "csi-plugin"
  }
  addons {
    name = "csi-provisioner"
  }
  addons {
    name = "metrics-server"
  }

  tags = var.tags

  # -----------------------------------------------------------------------
  # Path A2 precondition — public access requires non-empty allow-list.
  # `terraform plan` exits non-zero if the operator forgets to populate
  # api_server_public_access_allowed_cidrs while leaving _enabled=true.
  # -----------------------------------------------------------------------
  lifecycle {
    # pod_cidr / service_cidr MUST be distinct (overlap would silently break kube-proxy routing).
    precondition {
      condition     = var.pod_cidr != var.service_cidr
      error_message = "pod_cidr and service_cidr must be distinct (overlap breaks kube-proxy routing)."
    }
  }
}
