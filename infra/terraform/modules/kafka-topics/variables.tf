# kafka-topics module inputs — declarative Aliyun MSK topic provisioning.
#
# NEW module per Story 2.6 R-1 (TS-CONS-013). Story 2.6 seeds the module
# with `gdpr.export.requested` (AC2 BR-2.6 + T0.3). Future stories add
# additional topics by appending to the `topics` list at the env layer
# (infra/terraform/envs/<env>/main.tf).

variable "instance_id" {
  type        = string
  description = "Aliyun Kafka (MSK) instance id that owns these topics."
}

variable "topics" {
  type = list(object({
    name            = string
    partitions      = number
    replication     = number
    retention_hours = number
    description     = string
  }))
  description = <<-EOT
    Topic specs. Each entry produces one alicloud_alikafka_topic resource.

    Story 2.6 initial entry:
      - name=gdpr.export.requested, partitions=6, replication=3, retention_hours=168
        (matches data-models.md §4.4 async-task convention; AC2 BR-2.6).
  EOT
}
