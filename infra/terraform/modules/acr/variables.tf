# ACR module inputs.

variable "instance_name" {
  type        = string
  description = "EE instance name. Forms the independent endpoint: <instance_name>-registry.<region>.cr.aliyuncs.com"
  default     = "he-api"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,29}$", var.instance_name))
    error_message = "instance_name must be 3-30 chars, lower-case alphanumeric or '-', starting with a letter (Aliyun ACR EE naming rule)."
  }
}

variable "region" {
  type        = string
  description = "Aliyun region (e.g., cn-shanghai). Used to compose the EE registry endpoint."
  default     = "cn-shanghai"
}

variable "tags" {
  type        = map(string)
  description = "Resource tags."
  default     = {}
}
