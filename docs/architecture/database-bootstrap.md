# Database Bootstrap — Story 1.6

> Operator guide for the staging database foundation: PostgreSQL 16, Redis 7.2 (Tair), ClickHouse 24+. Six sections track topology, migration workflow, credentials, capacity, runbook, and decision lineage.

---

## Section 1 — Topology + Endpoints

The staging environment runs three managed Aliyun database instances inside the 1.3 VPC (`vpc-he-api-staging`), all addressable only via VPC-private network.

| DB | Resource | Module | Port |
|----|----------|--------|------|
| PostgreSQL 16 | `alicloud_db_instance` (HighAvailability) | `infra/terraform/modules/rds-postgres/` | 5432 |
| Redis 7.2 (Tair) | `alicloud_kvstore_instance` (cluster edition) | `infra/terraform/modules/redis-tair/` | 6379 |
| ClickHouse 24 | `alicloud_click_house_db_cluster` | `infra/terraform/modules/clickhouse/` | 8443 (HTTPS) |

### VPC-private DNS (m-5 ruling)

The three managed services expose **VPC-private DNS hostnames** automatically:

- `*.pg.rds.aliyuncs.com` — PostgreSQL (e.g. `rm-uf6xxx.pg.rds.aliyuncs.com`).
- `*.redis.rds.aliyuncs.com` — Tair (e.g. `r-uf6xxx.redis.rds.aliyuncs.com`).
- `*.clickhouseserver.aliyuncs.com` — ClickHouse (e.g. `cc-uf6xxx.clickhouseserver.aliyuncs.com`).

Pods inside the ACK cluster resolve these FQDNs automatically via the VPC recursor — no `/etc/hosts` overrides or external DNS configuration are needed. The Terraform `terraform output` command exposes the **FQDNs (NOT IPs)** because the underlying IPs may rotate during managed-DB maintenance windows; consumers MUST connect via FQDN, never via a cached IP.

### Connection wiring

The K8s Secret `he-api-db-creds` (namespace `he-api-staging`) carries three composite URIs (BR-2.5):

```
HE_API_DB_POSTGRES_URI    = postgres://he_api:<pass>@<pg-fqdn>:5432/he_api?sslmode=require
HE_API_DB_REDIS_URI       = rediss://he-api:<pass>@<redis-fqdn>:6379/0
HE_API_DB_CLICKHOUSE_URI  = https://he_api:<pass>@<ch-fqdn>:8443/he_api?secure=true
```

Application pods mount these via `envFrom: secretRef` (see `infra/helm/db-doctor/templates/cronjob.yaml` for the canonical example).

---

## Section 2 — Migration Workflow

Two tools live side-by-side; both are invoked via the unified entrypoint `scripts/db-migrate.sh`.

### PostgreSQL — Atlas (Q1 ruling, versioned mode only)

Atlas operates in **versioned-migrations mode**. Every change is a numbered file (`migrations/postgres/{4-digit}_{snake_case}.sql`) accompanied by an `atlas.sum` integrity entry. The production path is exclusively:

```bash
atlas migrate apply --dir file://migrations/postgres --url "$HE_API_DB_POSTGRES_URI"
```

The declarative `atlas schema apply` path is **prohibited** in production paths (m-1 ruling) because it produces no audit trail and emits opaque diffs.

Drift gate (BR-3.5, MIG-DB-003) runs on every PR via `.github/workflows/db-migrate-check.yml`:

```bash
atlas migrate diff --dir file://migrations/postgres --url "$HE_API_DB_POSTGRES_URI" --dev-url "$HE_API_DB_POSTGRES_URI"
```

Exit code propagates — non-zero blocks the PR.

### ClickHouse — golang-migrate (Q2 ruling, paired up/down)

golang-migrate requires paired up/down files. The Story 1.6 baseline is one-way per BR-3.3, so `001_baseline.down.sql` is an empty stub carrying the literal explanatory comment:

```
-- intentionally empty; baseline migration is one-way per BR-3.3 (rollback via destroy-and-re-provision, not migration)
```

This satisfies the paired-up/down convention while making rollback semantics explicit. Operator invocations:

```bash
migrate -path migrations/clickhouse -database "$HE_API_DB_CLICKHOUSE_URI" up
migrate -path migrations/clickhouse -database "$HE_API_DB_CLICKHOUSE_URI" down 1   # no-op against baseline stub (m-2 + m-3)
```

### Tool-specific semantics summary (m-3 ruling)

| Tool | DB | Down behavior |
|------|----|---------------|
| Atlas (versioned) | PostgreSQL | `atlas migrate down` computes rollback dynamically; no per-version `down.sql` files. |
| golang-migrate | ClickHouse | Paired up/down files mandatory; baseline `001_baseline.down.sql` is the empty stub described above (m-2). `migrate down 1` on the baseline is a documented no-op. |

### CI drift gate

`atlas migrate diff` runs against an ephemeral `postgres:16` container in CI (`db-migrate-check.yml`) — exit non-zero blocks the PR (BR-3.5). The same script also enforces the BR-3.6 boundary: any non-baseline `.sql` file under `migrations/postgres/` or unpaired file under `migrations/clickhouse/` triggers `MIG-DB-001`.

---

## Section 3 — Credentials & Vault Migration Path

> M-1 ruling: Story 1.6 uses **K8s Secret only**; Vault is deferred to a dedicated future Story. Both layers of credentials are ACK-KMS envelope-encrypted at rest; separation is enforced by **namespace + RBAC**, NOT by tool.

### Current layout

| Layer | Secret | Namespace | RBAC | Contents |
|-------|--------|-----------|------|----------|
| Admin | `he-api-db-admin-creds` | `he-api-ops` | `ops-cluster-admin` ClusterRole binding | `postgres_admin_password`, `redis_admin_password`, `clickhouse_admin_password` |
| Application | `he-api-db-creds` | `he-api-staging` | App ServiceAccount only | `HE_API_DB_POSTGRES_URI`, `HE_API_DB_REDIS_URI`, `HE_API_DB_CLICKHOUSE_URI` |

The application Secret is rendered by the plain Helm template `infra/helm/db-doctor/templates/secret.yaml` with values supplied by Terraform-injected variables. ExternalSecret / SealedSecret operators are intentionally NOT used in 1.6.

### Redis ACL setup

```bash
APP_PWD=$(kubectl get secret he-api-db-creds -n he-api-staging -o jsonpath='{.data.app_password}' | base64 -d)
redis-cli -h "$REDIS_HOST" --tls -a "$ADMIN_PWD" \
  ACL SETUSER he-api on ">$APP_PWD" "~he-api:*" "&*" "+@read" "+@write" "+@stream" "-@dangerous"
```

The ACL scope is `~he-api:*` (key prefix isolation per BR-2.2) and `-@dangerous` (no `FLUSHALL`, `DEBUG`, etc).

### Rotation flow (BR-2.4)

RTO ≤ 2h end-to-end (manual). Steps:

1. Ops engineer generates new credentials (`pwgen -s 32 1`) and updates the K8s Secret via `kubectl edit secret he-api-db-admin-creds -n he-api-ops` (or via the Terraform variable + `terraform apply -target=kubernetes_secret.he_api_db_admin_creds`).
2. The same engineer rotates the DB-side credential (`ALTER ROLE he_api WITH PASSWORD '<new>'` for PG; equivalent for Redis ACL SETUSER and CH ALTER USER).
3. `helm upgrade db-doctor infra/helm/db-doctor -n he-api-staging --set postgres.uri=...` regenerates `he-api-db-creds` with the new value.
4. Application pods auto-reload via Kubernetes Secret remount (or a follow-up rolling restart, depending on the controller).
5. Verify with `scripts/db-doctor.sh` — full PASS expected within the 5-minute SLO.

Failure rollback: re-apply the previous Terraform variable values and re-run steps 1-4; revert path is symmetric.

### Vault Migration Path (M-1 ruling — future-Story narrative)

A future dedicated Story will replace the K8s Secret data sources with `ExternalSecret` CRs pulling from Vault paths:

```
secret/he-api/staging/db/postgres/admin
secret/he-api/staging/db/postgres/application
secret/he-api/staging/db/redis/admin
secret/he-api/staging/db/redis/application
secret/he-api/staging/db/clickhouse/admin
secret/he-api/staging/db/clickhouse/application
```

The application contract (env var names `HE_API_DB_*_URI`, Secret name `he-api-db-creds`) stays identical — **zero-touch migration** for consumers. Only the rendered Secret's `data` field changes from "static-from-Terraform" to "external-from-Vault".

### Open Followups

- [ ] Future Story: Vault provisioning + ExternalSecretsOperator install + admin-cred migration (target: pre-Epic-2 Story 2.1 auth-svc, which needs JWT signing keys in Vault per security.md §8.2).

---

## Section 4 — Capacity & Performance defaults

| DB | Class | Notes |
|----|-------|-------|
| PostgreSQL | `pg.n2.medium.2c` (2 vCPU, 4 GB RAM) | Staging baseline only. Production sizing in a future Story (Epic 9 capacity-planning). |
| Redis Tair | `redis.shard.small.ce` (1 GB shard) | Cluster edition with 1 shard for staging. |
| ClickHouse | `S8` (~2 vCPU, ~8 GB RAM) | Single-node staging baseline; production will scale-out across shards. |

Backups: PostgreSQL 7-day automatic snapshots (`backup_retention_period = 7`). Tair + CH default vendor backup policies (24-hour granularity).

Connection pool sizing: out of scope for 1.6; tracked as an Epic 9 followup. Pod-side pgx/redis/clickhouse-go pool defaults are documented in the Epic-2 service template once auth-svc lands.

---

## Section 5 — Operator Runbook

### "连不上" — cannot connect to a DB

1. From any pod in `he-api-staging`, run `scripts/db-doctor.sh` to confirm which DB is unreachable.
2. If FQDN resolution fails: check `kubectl get nodes -o wide` and `kubectl exec ... -- nslookup <fqdn>`. VPC DNS recursor handles the wildcard suffixes automatically; failures usually mean a VPC peering / route table change.
3. If TLS handshake fails: confirm Tair `tls_enabled = true` and PG `force_ssl = true` parameter group is applied (`alicloud_db_instance.this.parameters`).
4. If 5432/6379/8443 is open but auth fails: re-fetch `he-api-db-creds` and verify `HE_API_DB_*_URI` matches.

### Drift detected by `db-migrate.sh diff`

1. Inspect CI log diff output (atlas emits unified diff format).
2. If an out-of-band actor altered the DB: revert via `atlas migrate apply --to <baseline-version>` or, for non-trivial drift, escalate to Architect.
3. If `migrations/postgres/` was modified without re-running `atlas migrate hash`: regenerate via `atlas migrate hash --dir migrations/postgres` and commit the updated `atlas.sum`.

### 凭据丢失 — credential loss / Secret missing

1. `kubectl get secret he-api-db-creds -n he-api-staging` returns `NotFound` → re-run `helm upgrade db-doctor infra/helm/db-doctor -n he-api-staging -f values-staging.yaml`.
2. If the admin Secret in `he-api-ops` is also missing: re-run `terraform apply -target=kubernetes_secret.he_api_db_admin_creds`. The Terraform state always carries the source-of-truth value (OSS backend, KMS-encrypted).
3. Cross-namespace get failures are expected for non-ops ServiceAccounts (M-1 RBAC isolation).

### KMS unwrap failed

1. Verify the ACK KMS key referenced in `var.kms_key_id` is **enabled** (`aliyun kms DescribeKey --KeyId ...`).
2. Check ACK encryption-config CRD: `kubectl get encryptionconfiguration -n kube-system -o yaml`.
3. If the key was rotated: re-encrypt existing Secrets via `kubectl get secret he-api-db-creds -n he-api-staging -o yaml | kubectl replace -f -`.
4. Last resort: `terraform taint kubernetes_secret.he_api_db_admin_creds && terraform apply` recreates the Secret with the current key version.

### GDPR data export — orphan-pending row alert + manual re-run (Story 2.6 T8.6)

**Orphan `pending` rows** (Kafka publish failed after PG commit per AC2 step 3 edge):

1. Grafana panel query: `data_export_requests.status='pending' AND created_at < NOW() - INTERVAL '15 minutes'`. Alert routes to Feishu (default) — bump to PagerDuty when count > 5.
2. Recovery path: `psql -c "UPDATE he_api.data_export_requests SET status='failed', failure_reason='kafka outbox lost' WHERE id IN (<ids>)"` then notify the affected users via the support channel. The future BR-4.7 cron will pick up these rows; until then the manual flip preserves the operator-readable status invariant.

**Manual re-run for a failed export**:

1. `psql -c "UPDATE he_api.data_export_requests SET status='pending', failure_reason=NULL, started_at=NULL, completed_at=NULL WHERE id='<id>'"`.
2. Produce a fresh `gdpr.export.requested` Kafka message via `kafka-console-producer --topic gdpr.export.requested --property "parse.key=true" --property "key.separator=:" <<< "<user_id>:<protobuf-payload>"`. (Operator builds the protobuf via `protoc --encode=DataExportRequestedEvent ...`.)
3. analytics-svc consumer picks it up; row transitions to `processing`.

**Extend the signed URL** (user reports the 24h link expired but still wants the zip):

1. Confirm the OSS object still exists: `aliyun oss ls oss://he-api-gdpr-exports/gdpr-exports/<user_id>/<export_id>.zip` (the 25h lifecycle rule means there's a ~1h window after `signed_url_expires_at` where the object is still there).
2. Re-sign: `aliyun oss sign --expires 86400 oss://...`. Send the new URL via the support channel (DO NOT log the URL — TS-CONS-008 applies to operator workflows too).
3. If the lifecycle rule already fired and the object is gone: ask the user to request a fresh export (`POST /v1/account/data-export` — the 24h idempotency window has already passed since they exhausted theirs).

**Delete a user's OSS prefix on Story-2.7 account deletion**:

1. `aliyun oss rm oss://he-api-gdpr-exports/gdpr-exports/<user_id>/ --recursive`. The Story-2.7 deletion handler is responsible for the call; this runbook entry exists for manual recovery if the handler errored.

---

## Section 6 — Decision Lineage

Architect Round 1 + Round 2 rulings, with reasoning summaries and citations back to Story 1.6 anchors.

| ID | Ruling | Reasoning | Story link |
|----|--------|-----------|------------|
| Q1 | PostgreSQL migrations use **Atlas versioned mode** exclusively; declarative `schema apply` prohibited in production. | Atlas's declarative ethos aligns with the project's Terraform-first / GitOps posture; drift detection at CI gate is the critical safety net for Epic 2-10's broad-blast 14-service schema evolution. Versioned mode preserves audit trail. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 Q1]` | AC3 BR-3.1 / T4 |
| Q2 | ClickHouse migrations use **golang-migrate** with paired up/down files. | Battle-tested across multi-DB projects; CH driver maintenance is active. Atlas CH provider remains pre-GA (re-evaluation when GA). `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 Q2]` | AC3 BR-3.2 / T5 |
| Q3 | DB credentials carrier: **K8s Secret ONLY** for Story 1.6; Vault deferred to dedicated future Story. | Vault is not provisioned anywhere in Epic 1; absorbing Vault into 1.6 would expand scope beyond "DB foundation". Namespace + RBAC separation provides the required defense-in-depth in the interim. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 Q3]` | AC2 BR-2.1 / T2 / T3 |
| Q4 | **Three independent Terraform modules** under `infra/terraform/modules/{rds-postgres,redis-tair,clickhouse}/`. | Mirrors the existing 1.3 per-resource pattern (`modules/{vpc,ack,acr,oss-state}/`); independent lifecycle (CH may later migrate to self-hosted); blast-radius isolation. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 Q4]` | AC1 / T1 |
| Q5 | **Bash for local + Helm CronJob for cluster**, with SQL probe single-source-of-truth in `scripts/db-doctor/probes/`. | KISS — for a ~50-line health check, bash beats Go on cost/value. SQL probe SoT avoids drift between local and cluster paths. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 Q5]` | AC4 / T7 / T8 |
| M-1 | Vault phantom prerequisite removed; K8s-Secret-only narrative applied across AC2 / BR-2.1 / T2 / T3 / Database Design / SEC-CRED-003 / Section 3 / Technical Constraints. | Round-1 review caught the cross-cutting Vault dependency and rewrote 8 touch points. Round-2 verification confirmed coherent backfill. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 M-1 + Round 2 Backfill Verification Matrix]` | AC2 / T2 / T3 / Section 3 |
| M-2 | ArgoCD binding removed from `deliverable_bindings`; staging deployment uses direct `helm install` until Story 1.7+. | `infra/argocd/applications/` is a deferred-infra placeholder (Story 1.4 m-2 ruling). Direct `helm install` is the correct contract for 1.6 timing. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 M-2]` | T8 |
| m-1 | Atlas versioned-mode lock-in expressed in T4 + AC3 BR-3.1 + Deliverables. | Locks the production path to `atlas migrate apply`; declarative paths cannot be invoked. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 m-1]` | AC3 BR-3.1 / T4 |
| m-2 | golang-migrate paired up/down convention; literal explanatory comment in `001_baseline.down.sql`. | Convention requires paired files; baseline is one-way per BR-3.3 so the down file is an explicit empty stub with documentation. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 m-2]` | T5 / BR-3.3 |
| m-3 | Atlas-vs-golang-migrate tool-specific rollback semantics documented in BR-3.3 and Section 2 above. | The two tools have fundamentally different down semantics; documenting both prevents operator confusion. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 m-3]` | BR-3.3 |
| m-4 | `database-bootstrap.md` lifted to 6 sections with this Section 6 "Decision Lineage". | Q1-Q5 rulings deserved a permanent operator-discoverable home; the 5-section structure had no slot. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 m-4]` | BR-4.4 / T9 |
| m-5 | VPC-private DNS subsection in Section 1 with three FQDN suffix examples; `terraform output` exposes FQDNs (NOT IPs). | Managed DBs auto-provision VPC DNS hostnames; documenting this prevents the "pods can't reach DB by IP" failure mode. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 1 m-5]` | AC1 / Section 1 |
| m-6 | Five non-normative Vault label echoes (BR-1.6 / T6 / T10 / AC Coverage Matrix / Edge cases) cleaned up in-pass per Architect Round 2 Recommendation #2. | Cosmetic alignment with the M-1 ruling; the normative bodies were already authoritative. `[Source: docs/stories/1.6-database-foundation-postgres-redis-clickhouse.md §Architect Review Round 2 m-6 + Change Log entry dated 2026-05-11 (QA test-design)]` | Story body cleanup |
