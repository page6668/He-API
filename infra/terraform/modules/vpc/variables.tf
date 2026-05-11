# VPC module inputs.
#
# Note (Architect Round 1 Q3 ruling): vpn_allowed_cidrs is INTENTIONALLY ABSENT.
# Public K8s API access is owned by the ACK cluster endpoint ACL (see
# `ack` module's api_server_public_access_allowed_cidrs); the VPC SG layer
# forbids any public inbound. If VPN gateway is ever introduced in Story 1.7+,
# add a new variable then.

variable "vpc_cidr" {
  type        = string
  description = "CIDR block for the VPC. Must NOT overlap pod_cidr (172.20.0.0/16) or service_cidr (172.16.0.0/16)."

  validation {
    condition     = can(cidrnetmask(var.vpc_cidr))
    error_message = "vpc_cidr must be a valid IPv4 CIDR (e.g., 10.20.0.0/16)."
  }
}

variable "vswitch_cidrs" {
  type        = list(string)
  description = "List of vSwitch CIDR blocks; one per AZ. Must be subsets of vpc_cidr and non-overlapping."

  validation {
    condition     = length(var.vswitch_cidrs) == 3
    error_message = "Exactly 3 vSwitch CIDRs are required (one per AZ for multi-AZ ACK worker spread)."
  }
}

variable "availability_zones" {
  type        = list(string)
  description = "List of Aliyun availability zone IDs (e.g., cn-shanghai-f/g/h). Length must match vswitch_cidrs."

  validation {
    condition     = length(var.availability_zones) == 3
    error_message = "Exactly 3 availability zones are required (cross-AZ resilience)."
  }
}

variable "tags" {
  type        = map(string)
  description = "Resource tags applied to all VPC/vSwitch/NAT/EIP/SG resources."
  default     = {}
}
