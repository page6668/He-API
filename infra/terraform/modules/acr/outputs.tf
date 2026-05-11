# ACR module outputs.
#
# IMPORTANT: acr_password is intentionally NOT exposed as a Terraform output.
# ACR EE temporary tokens (90-day TTL) are minted out-of-band by ops via
# `aliyun cr-ee CreateUserInfo` and the console, then placed into the
# `ACR_PASSWORD` GitHub Secret. Persisting the password into Terraform state
# would risk leakage even with KMS SSE on the OSS state bucket.

output "acr_endpoint" {
  description = "Registry endpoint for `docker login` / Helm image.repository prefix. EE Basic independent endpoint."
  value       = "${alicloud_cr_ee_instance.this.name}-registry.${var.region}.cr.aliyuncs.com"
}

output "acr_namespace" {
  description = "Registry namespace (path component between endpoint and repo)."
  value       = alicloud_cr_ee_namespace.he_api.name
}

output "acr_instance_id" {
  description = "EE instance ID."
  value       = alicloud_cr_ee_instance.this.id
}

output "acr_instance_name" {
  description = "EE instance name."
  value       = alicloud_cr_ee_instance.this.name
}

output "acr_username_path" {
  description = "Documentation pointer to where the ACR_USERNAME GitHub Secret is created from (sub-account name; password is minted out-of-band)."
  value       = "Create RAM sub-account 'cr-pusher' with AliyunContainerRegistryFullAccess scoped to ${alicloud_cr_ee_instance.this.id}; export the sub-account name as GitHub Secret ACR_USERNAME."
}
