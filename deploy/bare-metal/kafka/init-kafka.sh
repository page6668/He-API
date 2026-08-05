#!/bin/bash
# =============================================================================
# init-kafka.sh — Kafka KRaft 初始化脚本
# 用途：首次启动前格式化存储、创建必要目录
# 注意：仅在首次安装时运行；重复运行会重新初始化导致数据丢失
# =============================================================================
set -euo pipefail

KAFKA_HOME="${KAFKA_HOME:-/opt/kafka}"
KRAFT_CONF="${KAFKA_HOME}/config/kraft/server.properties"
DATA_DIR="/opt/kafka/data"
LOGS_DIR="/opt/kafka/logs"
KAFKA_USER="kafka"
KAFKA_GROUP="kafka"

echo "==> [init-kafka] 开始 Kafka KRaft 初始化..."

# 1. 检查 Kafka 二进制是否存在
if [[ ! -x "${KAFKA_HOME}/bin/kafka-server-start.sh" ]]; then
  echo "ERROR: 未找到 Kafka 二进制文件于 ${KAFKA_HOME}"
  echo "请先运行 install-kafka.sh 或确认 Kafka 已正确安装"
  exit 1
fi

# 2. 检查是否已有 cluster ID（防止重复初始化）
if [[ -f "${DATA_DIR}/meta.properties" ]]; then
  echo "INFO: 检测到已有 meta.properties，跳过 cluster ID 初始化"
  echo "如需重新初始化，请先删除 ${DATA_DIR} 目录"
  CLUSTER_ID=$(grep '^cluster.id=' "${DATA_DIR}/meta.properties" | cut -d= -f2)
  echo "当前 Cluster ID: ${CLUSTER_ID}"
  exit 0
fi

# 3. 创建数据目录
echo "==> 创建数据目录: ${DATA_DIR}"
mkdir -p "${DATA_DIR}"
chown "${KAFKA_USER}:${KAFKA_GROUP}" "${DATA_DIR}"

# 4. 创建日志目录
echo "==> 创建日志目录: ${LOGS_DIR}"
mkdir -p "${LOGS_DIR}"
chown "${KAFKA_USER}:${KAFKA_GROUP}" "${LOGS_DIR}"

# 5. 生成 KRaft cluster ID 并格式化存储
echo "==> 生成 KRaft Cluster ID..."
CLUSTER_ID=$(uuidgen)
echo "Cluster UUID: ${CLUSTER_ID}"

echo "==> 格式化 Kafka 存储..."
"${KAFKA_HOME}/bin/kafka-storage.sh" format \
  -t "${CLUSTER_ID}" \
  -c "${KRAFT_CONF}" \
  --ignore-formatted

# 6. 验证 meta.properties 生成成功
if [[ -f "${DATA_DIR}/meta.properties" ]]; then
  echo "==> 存储格式化完成"
  grep '^cluster.id=' "${DATA_DIR}/meta.properties"
  echo ""
  echo "IMPORTANT: 请记录以上 Cluster ID，扩容时需用到"
  echo "格式: ${CLUSTER_ID}"
else
  echo "ERROR: meta.properties 未生成，请检查权限和配置"
  exit 1
fi

echo "==> [init-kafka] 初始化完成"
