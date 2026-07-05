# ACK module inputs.

variable "vswitch_ids" {
  type        = list(string)
  description = "vSwitch IDs from the `vpc` module (3, one per AZ)."

  validation {
    condition     = length(var.vswitch_ids) >= 1
    error_message = "At least one vSwitch ID is required."
  }
}

variable "security_group_id" {
  type        = string
  description = "Default deny-all SG ID from the `vpc` module."
}

variable "k8s_version" {
  type        = string
  description = "Kubernetes minor version. MUST be ≥ 1.29 (tech-stack §2.1)."
  default     = "1.34.3-aliyun.1"   # 香港现售(旧 1.29 已下架 no-ros-component)

  validation {
    condition     = can(regex("^1\\.(29|30|31|32|33|34|35|36)\\.", var.k8s_version))
    error_message = "k8s_version must start with 1.29–1.36."
  }
}

variable "service_cidr" {
  type        = string
  description = "Kubernetes service CIDR. Default 172.16.0.0/16 (tech-stack §2.1)."
  default     = "172.16.0.0/16"
}

variable "pod_cidr" {
  type        = string
  description = "Kubernetes pod CIDR. Default 172.20.0.0/16 (tech-stack §2.1)."
  default     = "172.20.0.0/16"
}

# Path A2 endpoint exposure controls — Architect Round 1 Q3 ruling.
variable "api_server_public_access_enabled" {
  type        = bool
  description = "If true (default), the ACK API server is reachable over the public Internet, restricted to api_server_public_access_allowed_cidrs."
  default     = true
}

variable "api_server_public_access_allowed_cidrs" {
  type        = list(string)
  description = <<-EOT
    List of CIDR blocks allowed to reach the public API server endpoint.
    MUST be non-empty when api_server_public_access_enabled = true; module
    precondition rejects empty lists. Typical entries:
      * GitHub Actions hosted runner egress (api.github.com/meta — weekly refresh)
      * Operator workstation /32 IPs (≤ 5)
      * Office NAT egress /N (optional)
  EOT
  default     = []
}

variable "worker_count" {
  type        = number
  description = "Number of worker nodes."
  default     = 3
}

variable "worker_instance_type" {
  type        = string
  description = "ECS instance type for worker nodes (e.g., ecs.c7.large for staging, ecs.c7.xlarge for prod)."
  default     = "ecs.c7.large"
}

variable "tags" {
  type        = map(string)
  description = "Resource tags."
  default     = {}
}
