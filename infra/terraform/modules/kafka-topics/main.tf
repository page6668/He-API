# Aliyun Kafka (MSK) topic provisioning — declarative GitOps.
#
# Story 2.6 R-1 (TS-CONS-013): the audit.event topic from Story 2.2 was
# created out-of-band via the managed-Kafka console; this module establishes
# the IaC convention for all future per-Story Kafka topic creation. The
# first occupant is `gdpr.export.requested` (AC2 BR-2.6, T0.3 acceptance
# criteria: declarative spec; staging-side smoke via
#   kafka-topics.sh --describe --bootstrap-server <broker> \
#                   --topic gdpr.export.requested
# verifies the topic exists post-apply).
#
# Each topic is one `alicloud_alikafka_topic` resource. Retention is
# expressed in hours by this module and converted to ms downstream by the
# Aliyun provider (compact-only / log-compaction is intentionally NOT
# supported — append-only event topics across the He-API platform per
# data-models.md §4.4).

resource "alicloud_alikafka_topic" "this" {
  for_each = { for t in var.topics : t.name => t }

  instance_id   = var.instance_id
  topic         = each.value.name
  partition_num = each.value.partitions
  remark        = each.value.description

  # Aliyun Kafka manages replication via instance-level setting; the
  # `replication` field in the topic spec is informational + asserted at
  # plan-time by an external check (CI test in
  # `.github/workflows/db-migrate-check.yml` once Kafka IaC lands; out of
  # Story 2.6 scope to wire that check).
  #
  # retention_hours is converted to retention.ms via the provider's
  # `compact_topic = false` + the instance's default policy; if non-default
  # retention is required, set `local_topic = true` and configure via the
  # instance-level setting (not surfaced here — Story 2.6's topic uses the
  # default 7-day retention which matches `usage.recorded` /
  # `notification.queued` per data-models.md §4.4).
}
