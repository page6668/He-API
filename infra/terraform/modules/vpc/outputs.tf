# VPC module outputs — consumed by the `ack` module and env composition.

output "vpc_id" {
  description = "ID of the created VPC."
  value       = alicloud_vpc.this.id
}

output "vswitch_ids" {
  description = "IDs of the 3 vSwitches, ordered by AZ (matches input var.availability_zones)."
  value       = alicloud_vswitch.this[*].id
}

output "nat_gateway_id" {
  description = "ID of the NAT Gateway (worker-node egress)."
  value       = alicloud_nat_gateway.this.id
}

output "default_security_group_id" {
  description = "ID of the default deny-all SG with intra-VPC ingress only."
  value       = alicloud_security_group.default.id
}
