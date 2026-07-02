#!/usr/bin/env bash
# 阶段 0 — 在跳板机上一键安装部署工具(terraform / kubectl / helm / aliyun CLI)。
#
# 自动识别包管理器(dnf/yum/apt),x86_64 Linux 通用。安全:只装工具、不碰云、不花钱。
# 用法:  bash scripts/deploy/00-install-tools.sh
# 之后:  照 docs/runbooks/staging-deploy-hongkong-minimal.md 阶段 0.2/0.5 建 AccessKey + export 凭据。
set -euo pipefail

TF_VER="1.9.5"
KUBECTL_VER="v1.29.1"
say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

# --- 前置:unzip/curl/tar/jq(jq 供阶段1 bootstrap 解析 aliyun 输出)----------
say "安装前置(unzip/curl/tar/jq)"
if   command -v dnf >/dev/null; then sudo dnf install -y unzip curl tar jq
elif command -v yum >/dev/null; then sudo yum install -y unzip curl tar jq
elif command -v apt-get >/dev/null; then sudo apt-get update && sudo apt-get install -y unzip curl tar jq
else echo "未识别的包管理器(非 dnf/yum/apt)。请手动装 unzip/curl/tar/jq 后重跑。" >&2; exit 1
fi

ARCH="$(uname -m)"; [ "$ARCH" = "x86_64" ] || { echo "本脚本按 x86_64 写;你的架构是 $ARCH,请手动调整下载链接。" >&2; exit 1; }
TMP="$(mktemp -d)"; cd "$TMP"

# --- terraform --------------------------------------------------------------
if command -v terraform >/dev/null; then say "terraform 已存在,跳过"; else
  say "安装 terraform ${TF_VER}"
  curl -fsSLO "https://releases.hashicorp.com/terraform/${TF_VER}/terraform_${TF_VER}_linux_amd64.zip"
  unzip -o "terraform_${TF_VER}_linux_amd64.zip"; sudo mv terraform /usr/local/bin/
fi

# --- kubectl ----------------------------------------------------------------
if command -v kubectl >/dev/null; then say "kubectl 已存在,跳过"; else
  say "安装 kubectl ${KUBECTL_VER}"
  curl -fsSLO "https://dl.k8s.io/release/${KUBECTL_VER}/bin/linux/amd64/kubectl"
  sudo install -m 0755 kubectl /usr/local/bin/kubectl
fi

# --- helm -------------------------------------------------------------------
if command -v helm >/dev/null; then say "helm 已存在,跳过"; else
  say "安装 helm 3"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

# --- aliyun CLI -------------------------------------------------------------
if command -v aliyun >/dev/null; then say "aliyun CLI 已存在,跳过"; else
  say "安装 aliyun CLI"
  curl -fsSLO https://aliyuncli.alicdn.com/aliyun-cli-linux-latest-amd64.tgz
  tar xzf aliyun-cli-linux-latest-amd64.tgz; sudo mv aliyun /usr/local/bin/
fi

cd - >/dev/null; rm -rf "$TMP"

# --- 验证 -------------------------------------------------------------------
say "验证(每行应打印版本号)"
terraform version | head -1
kubectl version --client 2>/dev/null | head -1
helm version --short
aliyun version | head -1

say "跳板机公网出口 IP(阶段 2 白名单要用,记下来)"
curl -fsS ifconfig.me 2>/dev/null || curl -fsS https://api.ipify.org || echo "(取不到,手动查阿里云 ECS 公网 IP)"
echo

say "✅ 阶段 0 工具就绪。下一步:建 RAM AccessKey,然后 export ALICLOUD_ACCESS_KEY/SECRET_KEY/REGION(见 runbook 0.2/0.5)"
