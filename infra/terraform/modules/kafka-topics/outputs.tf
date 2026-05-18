# kafka-topics module outputs.

output "topic_names" {
  description = "List of provisioned topic names (sorted)."
  value       = sort([for t in alicloud_alikafka_topic.this : t.topic])
}

output "topic_ids" {
  description = "Map of topic name → Aliyun resource id."
  value       = { for t in alicloud_alikafka_topic.this : t.topic => t.id }
}
