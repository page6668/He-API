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
