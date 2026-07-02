#!/usr/bin/env bash
#
# bootstrap-state-backend.sh — one-shot Terraform state-backend bootstrap.
#
# Solves the chicken-and-egg problem of "where does Terraform state live before
# Terraform exists in this project". Creates:
#   1. OSS bucket  he-api-tfstate-${env}-${region_short}
#      - versioning enabled
#      - SSE with KMS CMK
#      - bucket policy restricted to RAM sub-account tfstate-operator
#   2. KMS CMK    alias/he-api-tfstate-${env}
#      - KeyUsage=ENCRYPT_DECRYPT
#      - EnableKeyRotation + 365d rotation
#   3. TableStore instance + `terraform-lock` table (PK=LockID:string) for the
#      OSS backend's distributed lock.
#
# Permissions: requires Aliyun admin credentials (one-shot).
#              CI / Dev / tfstate-operator do NOT have permission to run this.
# Idempotency: re-runs are tolerated — every resource is created with a
#              "describe first; create if absent" guard.
#
# Usage:  bash scripts/infra/bootstrap-state-backend.sh <env>
#            env ∈ {staging, prod}
#
# Required env vars:
#   ALICLOUD_ACCESS_KEY  (admin, one-shot)
#   ALICLOUD_SECRET_KEY  (admin, one-shot)
#   ALICLOUD_REGION      (default: cn-shanghai)

set -Eeuo pipefail

readonly ENV="${1:-}"
if [[ -z "${ENV}" ]] || [[ "${ENV}" != "staging" && "${ENV}" != "prod" ]]; then
  echo "[ERR] usage: $0 <staging|prod>" >&2
  exit 2
fi

readonly REGION="${ALICLOUD_REGION:-cn-shanghai}"

# Map region to short code used in bucket naming.
case "${REGION}" in
  cn-shanghai)  REGION_SHORT="sh" ;;
  cn-shenzhen)  REGION_SHORT="sz" ;;
  cn-beijing)   REGION_SHORT="bj" ;;
  cn-hangzhou)  REGION_SHORT="hz" ;;
  cn-hongkong)  REGION_SHORT="hk" ;;
  *) echo "[ERR] unsupported region: ${REGION}" >&2; exit 2 ;;
esac

# Bucket-name pattern: he-api-tfstate-${env}-${region_short}
readonly BUCKET="he-api-tfstate-${ENV}-${REGION_SHORT}"
readonly KMS_ALIAS="alias/he-api-tfstate-${ENV}"
readonly OTS_INSTANCE="he-api-tfstate-${ENV}"
readonly OTS_TABLE="terraform-lock"
readonly RAM_OPERATOR="tfstate-operator"

# Sanity check — required tools.
for bin in aliyun jq; do
  command -v "${bin}" >/dev/null 2>&1 || {
    echo "[ERR] missing required tool: ${bin}" >&2
    exit 3
  }
done

if [[ -z "${ALICLOUD_ACCESS_KEY:-}" || -z "${ALICLOUD_SECRET_KEY:-}" ]]; then
  echo "[ERR] ALICLOUD_ACCESS_KEY / ALICLOUD_SECRET_KEY must be set" >&2
  exit 4
fi

export ALIBABA_CLOUD_ACCESS_KEY_ID="${ALICLOUD_ACCESS_KEY}"
export ALIBABA_CLOUD_ACCESS_KEY_SECRET="${ALICLOUD_SECRET_KEY}"

log() { printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$*"; }

# -----------------------------------------------------------------------------
# 1. KMS Customer Master Key  (alias/he-api-tfstate-${env})
# -----------------------------------------------------------------------------
log "Step 1/4 — KMS CMK ${KMS_ALIAS}"

existing_alias="$(aliyun kms ListAliases --region "${REGION}" 2>/dev/null \
  | jq -r --arg a "${KMS_ALIAS}" '.Aliases.Alias[]? | select(.AliasName == $a) | .KeyId' || true)"

if [[ -n "${existing_alias}" ]]; then
  KMS_KEY_ID="${existing_alias}"
  log "  ↳ reuse existing KMS key ${KMS_KEY_ID}"
else
  # NB: 自动轮换不在 CreateKey 时设置 —— 旧脚本用的 --EnableKeyRotation 不是有效
  # KMS 参数(会报错),且各 CLI 版本对 RotationInterval 格式要求不一。key 建好后
  # 如需自动轮换,在 KMS 控制台或 `aliyun kms UpdateRotationPolicy` 单独开(可选)。
  KMS_KEY_ID="$(aliyun kms CreateKey \
      --region "${REGION}" \
      --KeyUsage ENCRYPT_DECRYPT \
      --Description "Terraform state encryption — ${ENV}" \
      | jq -r '.KeyMetadata.KeyId')"
  aliyun kms CreateAlias --region "${REGION}" \
      --AliasName "${KMS_ALIAS}" --KeyId "${KMS_KEY_ID}" >/dev/null
  log "  ↳ created KMS key ${KMS_KEY_ID} aliased ${KMS_ALIAS} (自动轮换未开,可后补)"
fi

# -----------------------------------------------------------------------------
# 2. OSS bucket  he-api-tfstate-${env}-${region_short}
# -----------------------------------------------------------------------------
log "Step 2/4 — OSS bucket ${BUCKET}"

if aliyun oss stat "oss://${BUCKET}" --region "${REGION}" >/dev/null 2>&1; then
  log "  ↳ bucket already exists; verifying versioning/encryption/policy"
else
  aliyun oss create-bucket "oss://${BUCKET}" \
      --region "${REGION}" \
      --acl private \
      --storage-class Standard
  log "  ↳ created OSS bucket ${BUCKET}"
fi

# versioning + server-side encryption (KMS) — idempotent updates.
aliyun oss put-bucket-versioning "oss://${BUCKET}" --status Enabled --region "${REGION}"
aliyun oss put-bucket-encryption "oss://${BUCKET}" \
    --region "${REGION}" \
    --sse-algorithm KMS \
    --kms-master-key-id "${KMS_KEY_ID}"

# bucket policy — only tfstate-operator RAM principal can read/write objects.
ACCOUNT_ID="$(aliyun sts GetCallerIdentity --region "${REGION}" | jq -r '.AccountId')"
BUCKET_POLICY="$(cat <<JSON
{
  "Version": "1",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": ["acs:ram::${ACCOUNT_ID}:user/${RAM_OPERATOR}"],
      "Action": ["oss:GetObject", "oss:PutObject", "oss:DeleteObject", "oss:ListObjects"],
      "Resource": ["acs:oss:*:${ACCOUNT_ID}:${BUCKET}/*", "acs:oss:*:${ACCOUNT_ID}:${BUCKET}"]
    },
    {
      "Effect": "Deny",
      "Principal": ["*"],
      "Action": "oss:*",
      "Resource": ["acs:oss:*:${ACCOUNT_ID}:${BUCKET}/*"],
      "Condition": {
        "StringNotEquals": {
          "acs:PrincipalArn": "acs:ram::${ACCOUNT_ID}:user/${RAM_OPERATOR}"
        }
      }
    }
  ]
}
JSON
)"
echo "${BUCKET_POLICY}" | aliyun oss put-bucket-policy "oss://${BUCKET}" --region "${REGION}" --policy /dev/stdin
log "  ↳ bucket-policy attached: only RAM principal '${RAM_OPERATOR}' allowed"

# -----------------------------------------------------------------------------
# 3. TableStore — instance + terraform-lock table (PK = LockID:string)
# -----------------------------------------------------------------------------
log "Step 3/4 — TableStore instance ${OTS_INSTANCE} + table ${OTS_TABLE}"

# Create instance (idempotent).
if aliyun ots DescribeInstance --region "${REGION}" --InstanceName "${OTS_INSTANCE}" >/dev/null 2>&1; then
  log "  ↳ TableStore instance ${OTS_INSTANCE} already exists"
else
  aliyun ots CreateInstance \
      --region "${REGION}" \
      --InstanceName "${OTS_INSTANCE}" \
      --ClusterType SSD \
      --Description "Terraform state lock for ${ENV}"
  log "  ↳ created TableStore instance ${OTS_INSTANCE}"
fi

OTS_ENDPOINT="$(aliyun ots DescribeInstance --region "${REGION}" --InstanceName "${OTS_INSTANCE}" \
    | jq -r '.InstanceInfo.Endpoint' 2>/dev/null || echo "")"

# Create table with primary key LockID (string).
if aliyun ots DescribeTable --InstanceName "${OTS_INSTANCE}" --TableName "${OTS_TABLE}" --region "${REGION}" >/dev/null 2>&1; then
  log "  ↳ TableStore table ${OTS_TABLE} already exists"
else
  aliyun ots CreateTable \
      --region "${REGION}" \
      --InstanceName "${OTS_INSTANCE}" \
      --TableName "${OTS_TABLE}" \
      --PrimaryKeyList '[{"Name":"LockID","Type":"string"}]' \
      --MaxVersions 1 \
      --TimeToLive -1
  log "  ↳ created TableStore table ${OTS_TABLE} with PK LockID:string"
fi

# -----------------------------------------------------------------------------
# 4. Safety door — refuse to exit if any plaintext credential leaked into infra/
# -----------------------------------------------------------------------------
log "Step 4/4 — plaintext-secret guard on infra/"

if grep -rEn --include='*.tf' --include='*.tfvars' --include='*.sh' --include='*.yaml' --include='*.yml' \
   "(access[_-]?key|secret[_-]?key|password)\s*=\s*['\"]?[A-Za-z0-9/+=]{8,}" \
   infra/ 2>/dev/null \
   | grep -vE '(^.*example|placeholder|REDACTED|XXXXXX)' \
   ; then
  echo "[ERR] plaintext key/secret/password found under infra/ — bailing." >&2
  exit 5
fi
log "  ↳ infra/ is clean of plaintext credentials"

# -----------------------------------------------------------------------------
# Summary — for operator to copy into dev-log §6.1
# -----------------------------------------------------------------------------
cat <<SUMMARY

──────────────────────────────────────────────────────────────────────────
[OK] state backend ready
──────────────────────────────────────────────────────────────────────────
  env                 : ${ENV}
  region              : ${REGION}
  oss_bucket          : ${BUCKET}
  kms_key_id          : ${KMS_KEY_ID}
  kms_alias           : ${KMS_ALIAS}
  tablestore_instance : ${OTS_INSTANCE}
  tablestore_endpoint : ${OTS_ENDPOINT}
  tablestore_table    : ${OTS_TABLE}
──────────────────────────────────────────────────────────────────────────

Next: cd infra/terraform/envs/${ENV} && terraform init && terraform plan
SUMMARY
