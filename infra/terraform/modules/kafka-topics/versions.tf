# Terraform & provider version pins for the kafka-topics module.
#
# NEW module per Story 2.6 Architect Round 1 Ruling R-1 (TS-CONS-013).
# Sets the GitOps convention for all future per-Story Kafka-topic
# provisioning (audit.event was pre-existing managed topic; this module
# is the first IaC entry point for declarative topic creation).

terraform {
  required_version = ">= 1.7"

  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}
