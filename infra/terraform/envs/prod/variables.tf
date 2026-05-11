# Prod env variables.

variable "region" {
  type    = string
  default = "cn-shanghai"
}

variable "vpc_cidr" {
  type    = string
  default = "10.30.0.0/16"
}

variable "vswitch_cidrs" {
  type    = list(string)
  default = ["10.30.1.0/24", "10.30.2.0/24", "10.30.3.0/24"]
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

variable "api_server_public_access_allowed_cidrs" {
  type    = list(string)
  default = []
}

variable "acr_instance_name" {
  type    = string
  default = "he-api-prod"
}
