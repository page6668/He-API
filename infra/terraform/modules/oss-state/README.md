# `oss-state` — Terraform State Backend (meta module)

This module is **documentation-only**. It does NOT create any cloud resources.

## Why a meta module?

The OSS bucket + KMS key + TableStore lock-table that store Terraform state
themselves cannot be managed by the same Terraform run that uses them — the
chicken-and-egg problem. They must exist before `terraform init` can succeed.

Therefore the resources documented here are bootstrapped exactly once by
`scripts/infra/bootstrap-state-backend.sh` (operator-only, admin credentials)
and afterward declared **only** in `infra/terraform/backend.tf`
(`terraform { backend "oss" { ... } }`) as the state location, NOT as
managed `resource` blocks.

## Why OSS + TableStore + KMS (not local / git / S3-compat)?

| Option | Verdict | Reason |
|--------|---------|--------|
| Local `terraform.tfstate` | ❌ | No multi-operator collaboration; loss = disaster. |
| Git-committed state | ❌❌ | State contains sensitive output (kubeconfig); commit = leak. |
| Plain OSS (no lock) | ❌ | Concurrent `apply` corrupts state. |
| OSS + S3-compat lockfile plugin | ⚠️ | Community plugin; Aliyun support quality variable. |
| **OSS + TableStore (Aliyun-native)** | ✅ | Architect Round 1 Q2 ruling — native, integrated lock semantics. |

## Resources created by the bootstrap script

| Resource | Name pattern | Notes |
|----------|--------------|-------|
| OSS bucket | `he-api-tfstate-${env}-${region_short}` | versioning + SSE-KMS + RAM-restricted bucket policy |
| KMS CMK | alias = `alias/he-api-tfstate-${env}` | KeyUsage = ENCRYPT_DECRYPT, rotation = 365d |
| TableStore instance | `he-api-tfstate-${env}` | SSD cluster type |
| TableStore table | `terraform-lock` | PK = `LockID` (string), 1 version, TTL = -1 |

## CRITICAL — destroy exclusion

These 4 resources are Epic-level shared. They are explicitly **excluded** from
every `terraform destroy` run defined in `README.md` § Rollback predicate.
Deleting any of them destroys all stored env state files → Epic-wide rebuild.

Per Architect Round 1 H-3 + Q4 ruling.

## Where do downstream envs reference these?

* `infra/terraform/backend.tf` — staging backend block (`bucket` /
  `tablestore_endpoint` / `tablestore_table` literal values).
* `infra/terraform/envs/staging/backend.tf` — keep in sync with the file above
  (Terraform reads it from each env's root module).
* `infra/terraform/envs/prod/backend.tf` — same shape, distinct `prefix` and
  bucket name (`he-api-tfstate-prod-sh`).
