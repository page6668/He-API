#!/bin/bash
# =============================================================================
# create-topics.sh — Kafka Topics 初始化脚本
# 用途：为 He-API 创建所有必需的 Kafka topics
# 注意：仅需在 Kafka 首次启动后运行一次
# =============================================================================
set -euo pipefail

KAFKA_HOME="${KAFKA_HOME:-/opt/kafka}"
BROKER="${BROKER:-localhost:9092}"

info()    { echo -e "\033[0;32m[INFO]\033[0m $*"; }
error()   { echo -e "\033[0;31m[ERROR]\033[0m $*" >&2; }

# 前置检查：Kafka 是否在线
info "检查 Kafka Broker 连接..."
if ! timeout 5 bash -c "echo > /dev/tcp/localhost/9092" 2>/dev/null; then
  error "Kafka Broker 未在线，请先运行: sudo systemctl start kafka"
  exit 1
fi

info "Kafka Broker 在线，开始创建 Topics..."

# ─────────────────────────────────────────
# Topic: billing.usage
# 用途：计费用量事件
# 保留期：7 天（604800000 ms）
# 分区：6（高并发写入）
# ─────────────────────────────────────────
info "创建 topic: billing.usage"
"${KAFKA_HOME}/bin/kafka-topics.sh" \
  --create \
  --if-not-exists \
  --bootstrap-server "${BROKER}" \
  --replication-factor 1 \
  --partitions 6 \
  --config retention.ms=604800000 \
  --config cleanup.policy=delete \
  --topic billing.usage

# ─────────────────────────────────────────
# Topic: audit.event
# 用途：审计日志事件
# 保留期：30 天（2592000000 ms）
# 分区：3
# ─────────────────────────────────────────
info "创建 topic: audit.event"
"${KAFKA_HOME}/bin/kafka-topics.sh" \
  --create \
  --if-not-exists \
  --bootstrap-server "${BROKER}" \
  --replication-factor 1 \
  --partitions 3 \
  --config retention.ms=2592000000 \
  --config cleanup.policy=delete \
  --topic audit.event

# ─────────────────────────────────────────
# Topic: gdpr.export.requested
# 用途：GDPR 数据导出请求通知
# 保留期：7 天（604800000 ms）
# 分区：1
# ─────────────────────────────────────────
info "创建 topic: gdpr.export.requested"
"${KAFKA_HOME}/bin/kafka-topics.sh" \
  --create \
  --if-not-exists \
  --bootstrap-server "${BROKER}" \
  --replication-factor 1 \
  --partitions 1 \
  --config retention.ms=604800000 \
  --config cleanup.policy=delete \
  --topic gdpr.export.requested

# ─────────────────────────────────────────
# Topic: notification.events
# 用途：系统通知事件
# 保留期：7 天（604800000 ms）
# 分区：3
# ─────────────────────────────────────────
info "创建 topic: notification.events"
"${KAFKA_HOME}/bin/kafka-topics.sh" \
  --create \
  --if-not-exists \
  --bootstrap-server "${BROKER}" \
  --replication-factor 1 \
  --partitions 3 \
  --config retention.ms=604800000 \
  --config cleanup.policy=delete \
  --topic notification.events

# ─────────────────────────────────────────
# 列出所有 Topic
# ─────────────────────────────────────────
echo ""
info "=== 当前所有 Topics ==="
"${KAFKA_HOME}/bin/kafka-topics.sh" \
  --list \
  --bootstrap-server "${BROKER}"

echo ""
info "Topics 创建完成！"
