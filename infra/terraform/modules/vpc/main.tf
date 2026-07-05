# VPC module — VPC + 3-AZ vSwitch + NAT Gateway + EIP + default deny-all SG.
#
# Network topology:
#   * 1× VPC (CIDR = var.vpc_cidr)
#   * 3× vSwitch across var.availability_zones[*] (one per AZ)
#   * 1× NAT Gateway with associated EIP (for worker node egress: image pull,
#     external API calls — staging unified egress for cost control)
#   * 1× Security Group, default deny-all, with VPC-internal-only ingress rules
#
# Security stance per Architect Round 1 Q3 ruling:
#   * NO 0.0.0.0/0:22 (public SSH)
#   * NO 0.0.0.0/0:6443 (public K8s API at SG layer — that's owned by the ACK
#     cluster endpoint ACL via var.api_server_public_access_allowed_cidrs in
#     the `ack` module)
#   * The only ingress rules permitted at the VPC SG layer are intra-VPC
#     (cidr_ip = var.vpc_cidr).

terraform {
  required_version = ">= 1.7"
  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = ">= 1.220.0"
    }
  }
}

# -------------------------------------------------------------------------
# VPC + vSwitches
# -------------------------------------------------------------------------

resource "alicloud_vpc" "this" {
  vpc_name   = "he-api-${terraform.workspace}-vpc"
  cidr_block = var.vpc_cidr
  tags       = var.tags
}

resource "alicloud_vswitch" "this" {
  count        = length(var.vswitch_cidrs)
  vpc_id       = alicloud_vpc.this.id
  cidr_block   = var.vswitch_cidrs[count.index]
  zone_id      = var.availability_zones[count.index]
  vswitch_name = "he-api-${terraform.workspace}-vsw-${count.index + 1}"
  tags         = var.tags
}

# -------------------------------------------------------------------------
# NAT Gateway + EIP — unified egress for worker nodes
# -------------------------------------------------------------------------

resource "alicloud_nat_gateway" "this" {
  vpc_id           = alicloud_vpc.this.id
  vswitch_id       = alicloud_vswitch.this[0].id
  nat_gateway_name = "he-api-${terraform.workspace}-nat"
  nat_type         = "Enhanced"
  payment_type     = "PayAsYouGo"
  tags             = var.tags
}

resource "alicloud_eip" "nat" {
  address_name         = "he-api-${terraform.workspace}-nat-eip"
  bandwidth            = "100"
  internet_charge_type = "PayByTraffic"
  payment_type         = "PayAsYouGo"
  tags                 = var.tags
}

# Bind EIP to NAT Gateway.
resource "alicloud_eip_association" "nat" {
  allocation_id = alicloud_eip.nat.id
  instance_id   = alicloud_nat_gateway.this.id
  instance_type = "Nat"
}

# SNAT entries — 让每个子网的出网流量真正经 NAT 的 EIP 出去(节点拉镜像/调模型必需)。
# 缺这个则 NAT 建了却不 SNAT → 节点连公网 i/o timeout(本次部署踩到)。
resource "alicloud_snat_entry" "this" {
  count             = length(alicloud_vswitch.this)
  snat_table_id     = alicloud_nat_gateway.this.snat_table_ids
  source_vswitch_id = alicloud_vswitch.this[count.index].id
  snat_ip           = alicloud_eip.nat.ip_address
  depends_on        = [alicloud_eip_association.nat]
}

# -------------------------------------------------------------------------
# Default Security Group — deny-all baseline + intra-VPC ingress only
# -------------------------------------------------------------------------

resource "alicloud_security_group" "default" {
  name        = "he-api-${terraform.workspace}-default-sg"
  description = "Default deny-all; intra-VPC ingress only. Public K8s API ACL handled by ACK endpoint, NOT here."
  vpc_id      = alicloud_vpc.this.id
  tags        = var.tags
}

# Intra-VPC ingress — TCP across the full VPC CIDR. Covers k8s pod-to-pod,
# pod-to-service, and node-to-node communication. NO public ranges permitted.
resource "alicloud_security_group_rule" "intra_vpc_tcp" {
  type              = "ingress"
  ip_protocol       = "tcp"
  port_range        = "1/65535"
  cidr_ip           = var.vpc_cidr
  security_group_id = alicloud_security_group.default.id
  description       = "Intra-VPC TCP — pod/node/service mesh traffic"
}

# Intra-VPC ingress — UDP (DNS, kube-proxy IPVS health).
resource "alicloud_security_group_rule" "intra_vpc_udp" {
  type              = "ingress"
  ip_protocol       = "udp"
  port_range        = "1/65535"
  cidr_ip           = var.vpc_cidr
  security_group_id = alicloud_security_group.default.id
  description       = "Intra-VPC UDP — DNS, kube-proxy"
}

# Intra-VPC ingress — ICMP for diagnostics.
resource "alicloud_security_group_rule" "intra_vpc_icmp" {
  type              = "ingress"
  ip_protocol       = "icmp"
  port_range        = "-1/-1"
  cidr_ip           = var.vpc_cidr
  security_group_id = alicloud_security_group.default.id
  description       = "Intra-VPC ICMP — diagnostics"
}
