# ACK module outputs.
#
# Note: `kubeconfig` is marked sensitive — Terraform stores it encrypted in
# OSS state (via KMS SSE). Operators retrieve it out-of-band via
# `aliyun cs DescribeClusterUserKubeconfig --ClusterId <id>` rather than
# from `terraform output -raw kubeconfig`. See dev-log §6.3.

output "cluster_id" {
  description = "ACK cluster ID. Required for `aliyun cs DescribeClusterUserKubeconfig` and as a Story 1.4 ArgoCD reference."
  value       = alicloud_cs_managed_kubernetes.this.id
}

output "kubeconfig" {
  description = "kubeconfig contents (sensitive). Stored encrypted in OSS state; prefer fetching via `aliyun cs` API."
  value       = try(alicloud_cs_managed_kubernetes.this.kube_config, "")
  sensitive   = true
}

output "api_server_endpoint_intranet" {
  description = "Private VPC-internal API server endpoint. Used by Story 1.4 in-cluster ArgoCD controller."
  value       = try(alicloud_cs_managed_kubernetes.this.connections["api_server_intranet"], "")
}

output "api_server_endpoint_internet" {
  description = "Public API server endpoint (non-empty under Path A2, gated by api_server_public_access_allowed_cidrs)."
  value       = try(alicloud_cs_managed_kubernetes.this.connections["api_server_internet"], "")
}

output "security_group_id" {
  description = "SG ID actually applied (passthrough from input — useful for downstream consumers)."
  value       = var.security_group_id
}
