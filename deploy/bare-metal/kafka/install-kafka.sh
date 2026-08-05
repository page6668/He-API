#!/bin/bash
# =============================================================================
# install-kafka.sh — Kafka 3.7.0 KRaft 单节点一键安装脚本
# 用法: sudo ./install-kafka.sh
# =============================================================================
set -euo pipefail

# ─────────────────────────────────────────
# 参数
# ─────────────────────────────────────────
KAFKA_VERSION="3.7.0"
# Scala 版本 2.13，Kafka 3.7.0 对应 kafka_2.13-3.7.0
KAFKA_SCALA="2.13"
KAFKA_URL="https://downloads.apache.org/kafka/${KAFKA_VERSION}/kafka_2.13-${KAFKA_VERSION}.tgz"
INSTALL_DIR="/opt/kafka"
KAFKA_USER="kafka"
KAFKA_GROUP="kafka"
DATA_DIR="/opt/kafka/data"
LOGS_DIR="/opt/kafka/logs"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ─────────────────────────────────────────
# 颜色输出
# ─────────────────────────────────────────
RED='\033[0;31m'; GRN='\033[0;32m'; YEL='\033[1;33m'; NC='\033[0m'

info()    { echo -e "${GRN}[INFO]${NC} $*"; }
warn()    { echo -e "${YEL}[WARN]${NC} $*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }
require() { [[ "$EUID" -eq 0 ]] || { error "必须以 root 用户运行（如：sudo $0）"; exit 1; }; }

# ─────────────────────────────────────────
# 前置检查
# ─────────────────────────────────────────
require

# 已有 Kafka 则跳过下载
if [[ -d "${INSTALL_DIR}" && -x "${INSTALL_DIR}/bin/kafka-server-start.sh" ]]; then
  warn "检测到已有 Kafka 安装于 ${INSTALL_DIR}，跳过下载步骤"
else
  info "开始下载 Kafka ${KAFKA_VERSION}..."

  TMP_ARCHIVE="/tmp/kafka_${KAFKA_VERSION}.tgz"
  if [[ ! -f "${TMP_ARCHIVE}" ]]; then
    if command -v wget &>/dev/null; then
      wget --progress=bar:force:noscroll -O "${TMP_ARCHIVE}" "${KAFKA_URL}"
    elif command -v curl &>/dev/null; then
      curl -#Lo "${TMP_ARCHIVE}" "${KAFKA_URL}"
    else
      error "需要 wget 或 curl，请先安装"
      exit 1
    fi
  fi

  info "解压 Kafka 到 ${INSTALL_DIR}..."
  mkdir -p "${INSTALL_DIR}"
  tar -xzf "${TMP_ARCHIVE}" -C /tmp

  # 移动到目标位置（重命名为不含版本号的 kafka）
  rm -rf "${INSTALL_DIR}"
  mv "/tmp/kafka_2.13-${KAFKA_VERSION}" "${INSTALL_DIR}"
  info "Kafka 已安装到 ${INSTALL_DIR}"
fi

# ─────────────────────────────────────────
# 创建 kafka 系统用户
# ─────────────────────────────────────────
info "创建系统用户 ${KAFKA_USER}..."
if id "${KAFKA_USER}" &>/dev/null; then
  warn "用户 ${KAFKA_USER} 已存在，跳过"
else
  useradd --system --no-create-home --shell /usr/sbin/nologin "${KAFKA_USER"
  info "用户 ${KAFKA_USER} 创建完成"
fi

# ─────────────────────────────────────────
# 复制配置文件
# ─────────────────────────────────────────
info "部署 server.properties..."
mkdir -p "${INSTALL_DIR}/config/kraft"
cp "${SCRIPT_DIR}/kraft/server.properties" "${INSTALL_DIR}/config/kraft/server.properties"
chown "${KAFKA_USER}:${KAFKA_GROUP}" "${INSTALL_DIR}/config/kraft/server.properties"

info "部署 systemd unit..."
cp "${SCRIPT_DIR}/kafka.service" /etc/systemd/system/
systemctl daemon-reload

# ─────────────────────────────────────────
# 创建数据目录和日志目录
# ─────────────────────────────────────────
info "创建数据目录 ${DATA_DIR}..."
mkdir -p "${DATA_DIR}"
chown "${KAFKA_USER}:${KAFKA_GROUP}" "${DATA_DIR}"

info "创建日志目录 ${LOGS_DIR}..."
mkdir -p "${LOGS_DIR}"
chown "${KAFKA_USER}:${KAFKA_GROUP}" "${LOGS_DIR}"

# ─────────────────────────────────────────
# 初始化 KRaft cluster ID（如尚未初始化）
# ─────────────────────────────────────────
if [[ ! -f "${DATA_DIR}/meta.properties" ]]; then
  info "运行 init-kafka.sh 初始化 cluster ID..."
  # 临时提权运行 init-kafka.sh（因为需要 chown）
  chmod +x "${SCRIPT_DIR}/init-kafka.sh"
  bash "${SCRIPT_DIR}/init-kafka.sh"
else
  warn "检测到已有 cluster ID，跳过 init-kafka.sh"
fi

# ─────────────────────────────────────────
# 设置 Kafka 脚本可执行权限
# ─────────────────────────────────────────
chmod +x "${INSTALL_DIR}/bin/kafka-server-start.sh"
chmod +x "${INSTALL_DIR}/bin/kafka-server-stop.sh"

# ─────────────────────────────────────────
# 启用开机自启
# ─────────────────────────────────────────
info "启用 kafka 服务开机自启..."
systemctl enable kafka

# ─────────────────────────────────────────
# 总结
# ─────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════"
echo "  Kafka ${KAFKA_VERSION} KRaft 安装完成！"
echo "════════════════════════════════════════════════════════"
echo ""
echo "  配置路径:    ${INSTALL_DIR}/config/kraft/server.properties"
echo "  数据目录:    ${DATA_DIR}"
echo "  日志目录:    ${LOGS_DIR}"
echo "  systemd:     /etc/systemd/system/kafka.service"
echo ""
echo "  启动 Kafka:  sudo systemctl start kafka"
echo "  查看状态:    sudo systemctl status kafka"
echo "  查看日志:    sudo journalctl -u kafka -f"
echo "  停止 Kafka:  sudo systemctl stop kafka"
echo ""
echo "  初始化 Topics（首次启动后）:"
echo "    sudo ${SCRIPT_DIR}/create-topics.sh"
echo ""
echo "  He-API 环境变量配置:"
echo "    HE_API_KAFKA_BROKERS=localhost:9092"
echo ""
echo "════════════════════════════════════════════════════════"
