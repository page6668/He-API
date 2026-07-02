#!/usr/bin/env bash
# 阶段 2 准备 — 自动处理易错的机械步骤,不碰密钥、不 apply、不花钱。
# 做四件事:① 两个 backend.tf 同步改香港 ② 复制 tfvars 模板 ③ 自动把本机公网 IP
# 填进 ACK 白名单 ④ 查香港 PostgreSQL 可用区并打印。之后你只需填密钥 + init/plan/apply。
#
# 用法(在仓库根目录,且已 export ALICLOUD_ACCESS_KEY/SECRET_KEY/REGION):
#   bash scripts/deploy/02-prepare-terraform.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENVDIR="$ROOT/infra/terraform/envs/staging"
say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

# ① backend.tf 改香港(两个文件保持一致)------------------------------------
say "① 把 backend 状态后端指向香港(2 个文件)"
for f in "$ROOT/infra/terraform/backend.tf" "$ENVDIR/backend.tf"; do
  sed -i \
    -e 's/he-api-tfstate-staging-sh/he-api-tfstate-staging-hk/g' \
    -e 's/region              = "cn-shanghai"/region              = "cn-hongkong"/g' \
    -e 's#he-api-tfstate-staging\.cn-shanghai\.ots#he-api-tfstate-staging.cn-hongkong.ots#g' \
    "$f"
  echo "  改好: $f"
done
grep -h -E 'bucket|region|tablestore_endpoint' "$ENVDIR/backend.tf" | sed 's/^/    /'

# ② 复制 tfvars 模板(不覆盖已存在的)---------------------------------------
say "② 准备 terraform.tfvars"
cd "$ENVDIR"
if [ -f terraform.tfvars ]; then
  echo "  terraform.tfvars 已存在,不覆盖(保留你已填的值)"
else
  cp terraform.tfvars.hongkong-minimal.example terraform.tfvars
  echo "  已从 hongkong-minimal 模板生成 terraform.tfvars"
fi

# ③ 自动把本机公网 IP 填进白名单 ---------------------------------------------
say "③ 自动填本机公网 IP 到 ACK 白名单"
MYIP="$(curl -fsS ifconfig.me 2>/dev/null || curl -fsS https://api.ipify.org || true)"
if [ -n "$MYIP" ]; then
  sed -i "s#203.0.113.42/32#${MYIP}/32#g" terraform.tfvars
  echo "  已填: ${MYIP}/32"
else
  echo "  ⚠️ 取公网 IP 失败,请手动把 terraform.tfvars 里的 203.0.113.42/32 换成本机 IP"
fi

# ④ 查香港 PostgreSQL 可用区 --------------------------------------------------
say "④ 香港 PostgreSQL 可用区(填进 postgres_multi_az_zone_id,选一个 MAZ 值)"
if [ -n "${ALICLOUD_ACCESS_KEY:-}" ] && [ -n "${ALICLOUD_SECRET_KEY:-}" ]; then
  ALIBABA_CLOUD_ACCESS_KEY_ID="$ALICLOUD_ACCESS_KEY" ALIBABA_CLOUD_ACCESS_KEY_SECRET="$ALICLOUD_SECRET_KEY" \
    aliyun rds DescribeAvailableZones --RegionId cn-hongkong --Engine PostgreSQL 2>&1 | head -40 || \
    echo "  ⚠️ 查询失败(检查凭据/权限);手动填一个香港多AZ值,如 MAZ1(b,c)"
else
  echo "  ⚠️ 未检测到 ALICLOUD_ACCESS_KEY,跳过;请先 export 再重跑,或手动填 zone"
fi

# 收尾:剩下需要你手填的 ------------------------------------------------------
say "还需你手动填进 terraform.tfvars(vi $ENVDIR/terraform.tfvars):"
cat <<'EOF'
  postgres_multi_az_zone_id  = "<上面④选的 MAZ 值,如 MAZ1(b,c)>"
  kms_key_id                 = "<阶段1 bootstrap 输出的 KMS key id>"
  postgres_admin_password    = "<强密码>"
  redis_admin_password       = "<强密码>"
  clickhouse_admin_password  = "<强密码>"
  sendgrid_api_key           = "<SendGrid key>"
  ops_admin_group            = "<RBAC group,如 he-api-ops-admins>"
EOF
say "填完后,依次跑(⚠️ apply 会花钱,先看 plan):"
cat <<EOF
  cd $ENVDIR
  terraform init -reconfigure
  terraform plan          # 核对:worker_count=1 / region=cn-hongkong / RDS HighAvailability
  terraform apply         # 你亲自确认 yes
EOF
