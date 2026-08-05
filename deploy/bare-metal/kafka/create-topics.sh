#!/bin/bash
# =============================================================================
# create-topics.sh — Kafka Topics 初始化脚本
# 用途：为 He-API 创建所有必需的 Kafka topics（topic 名与代码严格对应）
# 注意：仅需在 Kafka 首次启动后运行一次。topic 名来源于：
#   gateway  internal/billingemit/emitter.go   -> usage.recorded
#   gateway  internal/analyticslog/emitter.go   -> request.logged
#   auth-svc main.go                           -> audit.event
#   payment-svc internal/producer              -> payment.completed
#   billing-svc internal/consumer             -> usage.recorded (+ .dlq 消费)
#   billing-svc internal/credit/consumer       -> payment.completed 消费
#   notification-svc internal/events           -> gdpr.export.requested, usage.log.export.requested
#   analytics-svc internal/workers             -> request.logged(+.dlq), gdpr.export.requested, usage.log.export.requested 消费
# =============================================================================
set -euo pipefail

KAFKA_HOME="${KAFKA_HOME:-/opt/kafka}"
BROKER="${BROKER:-localhost:9092}"

info()  { echo -e "\033[0;32m[INFO]\033[0m $*"; }
error() { echo -e "\033[0;31m[ERROR]\033[0m $*" >&2; }

# 前置检查：Kafka 是否在线
info "检查 Kafka Broker 连接 (${BROKER}) ..."
if ! timeout 5 bash -c "echo > /dev/tcp/${BROKER%:*} ${BROKER##*:}" 2>/dev/null; then
  error "Kafka Broker 未在线，请先运行: sudo systemctl start kafka"
  exit 1
fi

create_topic() {
  local name="$1" part="$2" retain_ms="$3"
  info "创建 topic: ${name} (partitions=${part}, retention=$((retain_ms/86400000))d)"
  "${KAFKA_HOME}/bin/kafka-topics.sh" \
    --create \
    --if-not-exists \
    --bootstrap-server "${BROKER}" \
    --replication-factor 1 \
    --partitions "${part}" \
    --config retention.ms="${retain_ms}" \
    --config cleanup.policy=delete \
    --topic "${name}"
}

info "Kafka Broker 在线，开始创建 Topics..."

# 计费用量事件（网关 -> billing-svc 消费）
create_topic "usage.recorded"          6 604800000
# 计费用量 DLQ（失败死信，长保留）
create_topic "usage.recorded.dlq"      1 2592000000
# 支付完成事件（payment-svc -> billing-svc 消费）
create_topic "payment.completed"       3 604800000
# 请求日志事件（网关 -> analytics-svc 消费）
create_topic "request.logged"          6 604800000
# 请求日志 DLQ
create_topic "request.logged.dlq"      1 2592000000
# 审计事件（auth-svc -> notification-svc 消费）
create_topic "audit.event"             3 2592000000
# GDPR 导出请求（notification-svc -> analytics-svc 消费）
create_topic "gdpr.export.requested"   1 604800000
# 用量日志导出请求（notification-svc -> analytics-svc 消费）
create_topic "usage.log.export.requested" 1 604800000

echo ""
info "=== 当前所有 Topics ==="
"${KAFKA_HOME}/bin/kafka-topics.sh" --list --bootstrap-server "${BROKER}"

echo ""
info "Topics 创建完成！"
