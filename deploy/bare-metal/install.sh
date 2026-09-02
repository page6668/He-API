#!/usr/bin/env bash
# ============================================================
# He-API 裸机部署脚本
# 用法: sudo ./install.sh [--release VERSION] [--local DIR]
#
#   --release VERSION   从 GitHub Release 下载指定版本二进制
#   --local   DIR        使用本地构建产物目录（构建产物在 _output/bin/）
#   无参数               交互式询问
# ============================================================
set -euo pipefail

# ---------- 颜色 ----------
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# ---------- 参数解析 ----------
RELEASE_VERSION=""
LOCAL_DIR=""
SOURCE_TYPE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --release) RELEASE_VERSION="$2"; SOURCE_TYPE="release"; shift 2 ;;
        --local)   LOCAL_DIR="$2"; SOURCE_TYPE="local"; shift 2 ;;
        *)         log_error "未知参数: $1"; exit 1 ;;
    esac
done

# ---------- 前置检查 ----------
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "请使用 root 或 sudo 运行此脚本"
        exit 1
    fi
}

check_deps() {
    for cmd in curl tar systemctl grep; do
        if ! command -v "$cmd" &>/dev/null; then
            log_error "缺少依赖命令: $cmd"
            exit 1
        fi
    done
}

# ---------- 用户与目录 ----------
create_user() {
    if id "he-api" &>/dev/null; then
        log_info "用户 he-api 已存在，跳过创建"
    else
        log_info "创建系统用户 he-api ..."
        useradd --system --no-create-home --shell /usr/sbin/nologin he-api
        log_info "用户 he-api 创建完成"
    fi
}

create_dirs() {
    log_info "创建目录结构 ..."
    mkdir -p /opt/he-api/{bin,env,logs,data,keys,console,secrets}
    chown -R he-api:he-api /opt/he-api
    log_info "目录 /opt/he-api/* 已创建并授权"
}

# ---------- JWT 密钥对生成 ----------
# gateway 用公钥验签，auth-svc 用私钥签名，两者必须配对。
# 若密钥已存在则跳过（不覆盖，避免重置已签发的 JWT）。
gen_jwt_keys() {
    local priv="/opt/he-api/keys/jwt_private.pem"
    local pub="/opt/he-api/keys/jwt_public.pem"
    if [[ -f "$priv" && -f "$pub" ]]; then
        log_info "JWT 密钥对已存在，跳过生成"
        return 0
    fi
    if ! command -v openssl &>/dev/null; then
        log_error "缺少 openssl，无法生成 JWT 密钥对。请先安装后重新运行。"
        exit 1
    fi
    log_info "生成 JWT RSA 密钥对 ..."
    openssl genrsa -out "$priv" 2048 2>/dev/null
    openssl rsa -in "$priv" -pubout -out "$pub" 2>/dev/null
    chown he-api:he-api "$priv" "$pub"
    chmod 600 "$priv"
    chmod 644 "$pub"
    log_info "JWT 密钥对已生成: $priv / $pub"
}

# ---------- AD-004: Provider API Key 加密密钥 ----------
# AES-256-GCM 主密钥，gateway admin 接口写入 provider_configs 表时加密存储。
# 若文件已存在则跳过（幂等）。
gen_provider_key() {
    local key_file="/opt/he-api/secrets/provider-encryption.key"
    if [[ -f "$key_file" ]]; then
        log_info "Provider 加密密钥已存在，跳过生成"
        return 0
    fi
    if ! command -v openssl &>/dev/null; then
        log_error "缺少 openssl，无法生成 Provider 加密密钥。"
        exit 1
    fi
    log_info "生成 Provider 加密密钥 (AES-256) ..."
    # 生成 32 字节随机密钥（base64 编码，无换行）
    openssl rand -base64 32 | tr -d '\n' > "$key_file"
    chown he-api:he-api "$key_file"
    chmod 600 "$key_file"
    log_info "Provider 加密密钥已生成: $key_file"
}

# ---------- 二进制下载 ----------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

download_release() {
    local version="$1"
    local repo="he-api/he-api"
    local base_url="https://github.com/${repo}/releases/download/v${version}"
    local os arch

    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)
    case "$arch" in
        x86_64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *)       log_error "不支持的架构: $arch"; exit 1 ;;
    esac

    local tarball="he-api-${version}-${os}-${arch}.tar.gz"
    local url="${base_url}/${tarball}"

    log_info "下载 He-API v${version} (${os}/${arch}) ..."
    log_info "URL: $url"

    cd /tmp
    if ! curl -L --fail --progress-bar -o "${tarball}" "$url"; then
        log_error "下载失败，请检查版本号是否正确: v${version}"
        exit 1
    fi

    log_info "解压到 /opt/he-api/bin/ ..."
    tar -xzf "${tarball}" -C /opt/he-api/bin/ --strip-components=1
    rm -f "${tarball}"

    # Console standalone 产物（与 Go 二进制同源分发）
    local console_tar="he-api-console-${version}-${os}-${arch}.tar.gz"
    local console_url="${base_url}/${console_tar}"
    log_info "下载 Console v${version} (${os}/${arch}) ..."
    log_info "URL: $console_url"
    cd /tmp
    if curl -L --fail --progress-bar -o "${console_tar}" "$console_url"; then
        log_info "解压到 /opt/he-api/console/ ..."
        tar -xzf "${console_tar}" -C /opt/he-api/
        rm -f "${console_tar}"
    else
        log_warn "未找到 Console 产物 ${console_tar}，跳过（可稍后单独部署）"
    fi
}

use_local_build() {
    local src_dir="$1"
    if [[ ! -d "$src_dir" ]]; then
        log_error "本地目录不存在: $src_dir"
        exit 1
    fi
    log_info "从本地构建目录复制二进制: $src_dir"
    cp -r "${src_dir}"/* /opt/he-api/bin/
    # 本地构建产物若含 console/ 子目录，一并复制
    if [[ -d "${src_dir}/console" ]]; then
        log_info "复制本地 Console 产物: ${src_dir}/console"
        cp -r "${src_dir}/console" /opt/he-api/
    fi
}

# ---------- Env 模板 ----------
install_env_templates() {
    log_info "部署 env 模板到 /opt/he-api/env/ ..."
    local env_src="${SCRIPT_DIR}/env"
    if [[ -d "$env_src" ]]; then
        for f in "${env_src}"/*.env; do
            [[ -e "$f" ]] || continue
            local name
            name=$(basename "$f")
            if [[ -f "/opt/he-api/env/${name}" ]]; then
                log_warn "跳过已有配置文件: $name"
            else
                cp "$f" "/opt/he-api/env/${name}"
                log_info "  → $name"
            fi
        done
    else
        log_warn "未找到 env 模板目录: $env_src"
    fi
}

# ---------- Systemd Unit ----------
install_systemd_units() {
    log_info "部署 systemd unit 文件 ..."
    local unit_src="${SCRIPT_DIR}/systemd"
    if [[ -d "$unit_src" ]]; then
        cp "${unit_src}"/*.service /etc/systemd/system/
        log_info "已复制 unit 文件到 /etc/systemd/system/"
    else
        log_error "未找到 systemd 目录: $unit_src"
        exit 1
    fi
    # console unit 的 ExecStart 用真实 node 路径替换占位符 /usr/bin/node
    if [[ -f /etc/systemd/system/he-api-console.service ]]; then
        if command -v node >/dev/null 2>&1; then
            local node_bin
            node_bin="$(command -v node)"
            sed -i "s#^ExecStart=/usr/bin/node #ExecStart=${node_bin} #" \
                /etc/systemd/system/he-api-console.service
            log_info "console unit ExecStart 已指向 ${node_bin}"
        else
            log_warn "未找到 node，he-api-console.service 仍用占位 /usr/bin/node（请安装 node 20+）"
        fi
    fi
}

reload_systemd() {
    log_info "重载 systemd 守护进程 ..."
    systemctl daemon-reload
}

enable_services() {
    log_info "注册开机自启（暂不启动，等待 env 配置完成后手动 start）..."
    local services=(
        he-api-gateway
        he-adapter-deepseek
        he-adapter-qwen
        he-adapter-doubao
        he-adapter-glm
        he-adapter-ernie
        he-adapter-kimi
        he-auth-svc
        he-billing-svc
        he-payment-svc
        he-notification-svc
        he-routing-svc
        he-analytics-svc
        he-sample-grpc-app
        he-sample-otel-app
        he-api-console
    )
    for svc in "${services[@]}"; do
        systemctl enable "${svc}" &>/dev/null || true
        log_info "  → systemctl enable $svc"
    done
}

# ---------- 安装完成 ----------
print_summary() {
    echo ""
    echo "============================================================"
    echo " He-API 裸机部署安装完成！"
    echo "============================================================"
    echo ""
    echo " 【下一步：配置环境变量】"
    echo ""
    echo "  请编辑以下文件，填写真实值（已部署到 /opt/he-api/env/）："
    echo ""
    echo "  必填："
    echo "    /opt/he-api/env/gateway.env         ← JWT_SECRET, DB, Redis"
    echo "    /opt/he-api/env/auth.env            ← JWT_SECRET（与 gateway 一致）"
    echo "    /opt/he-api/env/billing.env         ← DB, Redis"
    echo "    /opt/he-api/env/payment.env         ← 支付渠道密钥"
    echo "    /opt/he-api/env/notification.env   ← SendGrid API Key"
    echo "    /opt/he-api/env/analytics.env       ← ClickHouse, OSS"
    echo "    /opt/he-api/env/adapter-*.env      ← 各 LLM 提供方 API Key"
    echo ""
    echo "  启动所有服务："
    echo "    sudo systemctl start he-api-gateway"
    echo "    # 或一次性启动所有:"
    echo "    sudo systemctl start he-api-gateway he-auth-svc he-billing-svc \\"
    echo "        he-payment-svc he-notification-svc he-routing-svc he-analytics-svc \\"
    echo "        he-adapter-deepseek he-adapter-qwen he-adapter-doubao \\"
    echo "        he-adapter-glm he-adapter-ernie he-adapter-kimi \\"
    echo "        he-sample-grpc-app he-sample-otel-app he-api-console"
    echo ""
    echo "  查看日志："
    echo "    sudo journalctl -u he-api-gateway -f"
    echo ""
    echo "  健康检查："
    echo "    curl http://localhost:8080/healthz"
    echo "    curl -I http://localhost:3000/   # console"
    echo ""
    echo "  console 前端："
    echo "    - 已部署到 /opt/he-api/console（pnpm deploy --prod + next start，systemd: he-api-console）"
    echo "    - 构建期变量 NEXT_PUBLIC_* 已烘焙；如需改域名，重新构建："
    echo "        bash ${SCRIPT_DIR}/build-console.sh <repo-root> /tmp/console-out"
    echo "      tar -czf console.tgz -C /tmp/console-out console && 解压到 /opt/he-api/"
    echo ""
    echo "  nginx 配置（同源单域：/ → console，/v1 → gateway）："
    echo "    sudo cp ${SCRIPT_DIR}/nginx/he-api.conf /etc/nginx/conf.d/"
    echo "    sudo nginx -t && sudo systemctl reload nginx"
    echo ""
    echo " 【数据库迁移（启动服务前必做）】"
    echo ""
    echo "  1. 准备 PostgreSQL（建议阿里云 RDS 或同机自建），创建库 he_api"
    echo "  2. 执行迁移脚本（首次用超级用户连接以创建 he_api 角色与 schema）："
    echo "    export HE_API_DB_POSTGRES_URI='postgres://postgres:密码@localhost:5432/he_api?sslmode=disable'"
    echo "    sudo -E bash ${SCRIPT_DIR}/migrate.sh up"
    echo "  3. 校验："
    echo "    sudo -E bash ${SCRIPT_DIR}/migrate.sh status"
    echo ""
    echo "  （ClickHouse 为可选项：设置 HE_API_DB_CLICKHOUSE_URI 后同上执行）"
    echo ""
    echo "============================================================"
}

# ---------- 主流程 ----------
main() {
    echo ""
    echo "============================================================"
    echo "  He-API Bare-Metal Installer"
    echo "============================================================"

    check_root
    check_deps
    create_user
    create_dirs
    gen_jwt_keys
    gen_provider_key

    if [[ "$SOURCE_TYPE" == "release" ]]; then
        download_release "$RELEASE_VERSION"
    elif [[ "$SOURCE_TYPE" == "local" ]]; then
        use_local_build "$LOCAL_DIR"
    else
        echo ""
        echo "请选择二进制来源："
        echo "  1) 从 GitHub Release 下载"
        echo "  2) 使用本地构建产物（需指定目录）"
        echo ""
        read -rp "请输入选项 [1]: " choice
        choice="${choice:-1}"
        if [[ "$choice" == "1" ]]; then
            read -rp "请输入版本号（如 1.0.0）: " version
            download_release "${version:-1.0.0}"
        else
            read -rp "请输入本地构建目录路径: " ldir
            use_local_build "${ldir}"
        fi
    fi

    log_info "设置二进制可执行权限 ..."
    chmod +x /opt/he-api/bin/* 2>/dev/null || true
    ls -lh /opt/he-api/bin/

    install_env_templates
    install_systemd_units
    reload_systemd
    enable_services

    print_summary
}

main
