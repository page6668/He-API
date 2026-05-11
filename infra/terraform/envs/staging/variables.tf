# Staging env variables — actual values supplied via terraform.tfvars (gitignored).
# See terraform.tfvars.example for the operator-fillable template.

variable "region" {
  type        = string
  description = "Aliyun region."
  default     = "cn-shanghai"
}

variable "vpc_cidr" {
  type    = string
  default = "10.20.0.0/16"
}

variable "vswitch_cidrs" {
  type    = list(string)
  default = ["10.20.1.0/24", "10.20.2.0/24", "10.20.3.0/24"]
}

variable "availability_zones" {
  type    = list(string)
  default = ["cn-shanghai-f", "cn-shanghai-g", "cn-shanghai-h"]
}

variable "k8s_version" {
  type    = string
  default = "1.29.1-aliyun.1"
}

variable "service_cidr" {
  type    = string
  default = "172.16.0.0/16"
}

variable "pod_cidr" {
  type    = string
  default = "172.20.0.0/16"
}

variable "worker_count" {
  type    = number
  default = 3
}

variable "worker_instance_type" {
  type    = string
  default = "ecs.c7.large"
}

variable "api_server_public_access_allowed_cidrs" {
  type        = list(string)
  description = "Path A2 ACL whitelist for the public ACK API endpoint. MUST be non-empty (module precondition rejects empty)."
  default     = []
}

variable "acr_instance_name" {
  type    = string
  default = "he-api"
}

# -----------------------------------------------------------------------------
# Story 1.6 — DB infrastructure variables.
# -----------------------------------------------------------------------------

variable "kubeconfig_path" {
  type        = string
  description = "Path to the ACK kubeconfig that the kubernetes provider consumes for Story 1.6 admin Secret + namespace + RBAC resources."
  default     = "~/.kube/he-api-staging.kubeconfig"
}

variable "kms_key_id" {
  type        = string
  description = "KMS key ID used for envelope encryption of RDS / Tair / ClickHouse at rest (BR-1.3). Same KMS key as the 1.3 OSS state backend."
}

variable "ops_admin_group" {
  type        = string
  description = "Kubernetes RBAC group name granted ops-cluster-admin via ClusterRoleBinding. Must match the OIDC group claim used by the cluster admin team."
  default     = "he-api-ops"
}

# Admin passwords — supplied via terraform.tfvars (gitignored under
# infra/terraform/**/*.tfvars) or via TF_VAR_* env vars. Marked sensitive so
# they do not appear in plan output or workflow logs. BR-2.3: never committed.
variable "postgres_admin_password" {
  type        = string
  description = "Admin password for the RDS PostgreSQL instance. Sourced from operator-controlled tfvars; never committed (BR-2.3)."
  sensitive   = true
}

variable "redis_admin_password" {
  type        = string
  description = "Admin password for the Tair (Redis) instance. Sourced from operator-controlled tfvars; never committed (BR-2.3)."
  sensitive   = true
}

variable "clickhouse_admin_password" {
  type        = string
  description = "Admin password for the ClickHouse cluster. Sourced from operator-controlled tfvars; never committed (BR-2.3)."
  sensitive   = true
}

# -----------------------------------------------------------------------------
# Story 2.2 — notification-svc SendGrid credentials.
# -----------------------------------------------------------------------------

variable "sendgrid_api_key" {
  type        = string
  description = "SendGrid API key for the staging sub-account (TS-CONS-010). Sourced from operator-controlled tfvars; never committed."
  sensitive   = true
}
