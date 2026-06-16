# Smoke Test Report: Epic 1

| Field            | Value                              |
|------------------|------------------------------------|
| **Epic**         | 1 — 基础设施基座 (Infrastructure Foundation) |
| **Trigger**      | manual (`QA *smoke-test 1`)        |
| **Executed At**  | 2026-06-16T08:40:21Z               |
| **Overall**      | PASS (with concerns)               |
| **Confidence**   | MEDIUM                             |

## 1. Epic Completeness

| Story ID | Title | Status |
|----------|-------|--------|
| 1.1 | 创建 monorepo 骨架（Turborepo + apps/ + packages/） | Done |
| 1.2 | 搭建 CI/CD 流水线（GitHub Actions） | Done |
| 1.3 | 阿里云 VPC + K8s 集群 + 基础网络（Terraform） | Done |
| 1.4 | OpenTelemetry + Prometheus + Loki + Grafana 部署 | Done |
| 1.5 | 内部 gRPC 服务模板（Go） | Done |
| 1.6 | 数据库基础（Postgres + Redis + ClickHouse） | Done |

- **Total Stories**: 6
- **Done**: 6
- **Not Done**: 0
- **Completeness**: 100%

## 2. Regression Suite

> Epic 1 is the **infrastructure foundation**. Its acceptance tests are repo-shape /
> config-snapshot assertions in `packages/shared-types/__tests__/1.*` (vitest), backed
> by the runnable Go surfaces it seeded (`sample-grpc-app`, `go-observability`).
> Live-infra ACs (terraform apply, live K8s, live ClickHouse) are environment-gated
> and **skip** locally (no terraform/atlas/docker — known env limit).

| Metric          | Value            |
|-----------------|------------------|
| **Executed**    | true             |
| **Passed**      | false (4 failures — all confirmed **stale-snapshot drift**, infra verified sound) |
| **Tests Total** | ~729 shared-types (1.x) + Go surfaces |
| **Tests Failed**| 4 (stale acceptance snapshots — see classification) |
| **Skipped**     | 183 (live-infra ACs, environment-gated) |

### Suite Breakdown

| Module | Story | Command | Result |
|--------|-------|---------|--------|
| `shared-types` `1.2` | 1.2 | `vitest run` | 2 fail (stale) / rest pass — CI/CD workflow snapshots |
| `shared-types` `1.3` | 1.3 | `vitest run` | pass — terraform/K8s config presence |
| `shared-types` `1.4` | 1.4 | `vitest run` | pass — OTel/Prometheus/Loki/Grafana config |
| `shared-types` `1.5` | 1.5 | `vitest run` | pass — gRPC service-template shape |
| `shared-types` `1.6` | 1.6 | `vitest run` | 2 fail (stale) / rest pass — DB migration snapshots |
| `apps/sample-grpc-app` | 1.5 | `go test ./...` | ok — gRPC template server |
| `packages/go-observability` | 1.4 | `go test ./...` | ok (verified in Epic 9 smoke) — tracer/meter/logger/requestid |

**Aggregate (Epic-1 shared-types): 542 passed / 183 skipped / 4 failed.**

### Failure Classification — all 4 are STALE SNAPSHOTS, not defects

Each failing test froze an early-project invariant that the 10-epic codebase
**deliberately and correctly outgrew**. The underlying infrastructure was verified
present and sound in every case:

| Test | Asserts | Reality (verified) | Verdict |
|------|---------|--------------------|---------|
| `1.2-UNIT-004` | gofumpt lint scope is `./apps` only, NOT `./packages` | `lint.yml` now lints `./packages` too — correct, since `sdk-go`, `go-observability`, etc. contain Go | STALE (infra more complete) |
| `1.2-UNIT-009` | `apps/api-gateway/cmd/server/main_test.go` contains a `func Test*` | Placeholder removed in **Story 3.1 (T0.2)**; the file documents coverage moved to sibling `coldstart_test.go` / `health_test.go` / `doc_ratification_test.go` (same package) — coverage exists | STALE (test relocated) |
| `1.6-UNIT-128` | `migrations/postgres/*.sql` is exactly 1 file | 19 PG migrations + `atlas.sum` present — additive growth across epics | STALE (expected accretion) |
| `1.6-UNIT-147` | `migrations/clickhouse` is exactly the `001_baseline` up/down pair | 4 CH `.sql` files — additive migrations added later | STALE (expected accretion) |

> **Root cause:** these are frozen-shape assertions ("exactly N files", "scope ==
> apps only") that capture the repo at Epic-1 authoring time. They are **false
> negatives** today — the infra is correct and more complete, but the tests were never
> updated as later epics added migrations, Go packages, and relocated the gateway
> smoke test. They generate CI noise without signal.

## 3. Core User Journeys

> Epic 1 has no end-user journeys; its "journeys" are platform-capability checks.
> Each is verified by config presence + the runnable Go surfaces. Live provisioning
> (terraform apply / k8s deploy) is environment-gated (SMOKE-1-002).

### Journey 1: Monorepo skeleton — Turborepo + apps/ + packages/ (1.1)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Workspace resolves apps + packages | turbo.json, pnpm-workspace, go.work present | present (22 Go modules in go.work; apps/ + packages/ populated) | PASS |

### Journey 2: CI/CD pipeline — GitHub Actions (1.2)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Lint / unit / integration workflows exist | lint.yml, test.yml + release/deploy/db-migrate-check | 10 workflows present: lint, test, build-images, deploy-staging, db-migrate-check, infra-lint, contract-tests-live, release-sdk-{go,python,typescript} | PASS |
| 2 | gofumpt + golangci scope | Covers all Go (apps + packages) | `lint.yml` lints both — note `1.2-UNIT-004` snapshot is stale, not the config | PASS |

### Journey 3: Aliyun VPC + K8s + Terraform (1.3)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Terraform + K8s manifests present | infra/terraform, k8s-base, helm, argocd | `infra/{terraform,k8s-base,helm,argocd,grafana-dashboards}` present; `1.3` snapshot suite passes | PASS (config) |
| 2 | `terraform validate` / live apply | Plan validates | **NOT RUN LOCALLY** — no terraform binary / cloud creds | CONCERN (SMOKE-1-002) |

### Journey 4: OTel + Prometheus + Loki + Grafana (1.4)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Observability libs + dashboards | go-observability functional; grafana dashboards present | `go-observability` go test ok; `infra/grafana-dashboards` present; `1.4` snapshot suite passes | PASS |

### Journey 5: Internal gRPC service template — Go (1.5)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | gRPC template builds + serves | sample-grpc-app tests pass | `apps/sample-grpc-app` go test ok; `1.5` snapshot suite passes | PASS |

### Journey 6: Database foundation — Postgres + Redis + ClickHouse (1.6)

| Step | Action | Expected | Actual | Result |
|------|--------|----------|--------|--------|
| 1 | Migration tooling + baselines | PG (Atlas) + CH (golang-migrate) migrations + db-migrate.sh | 19 PG migrations + atlas.sum, 4 CH files, db-migrate.sh present — note `1.6-UNIT-128/147` snapshots are stale, not the migrations | PASS |
| 2 | Live migrate / DB connectivity | Migrations apply to live DBs | **NOT RUN LOCALLY** — no docker/atlas (env limit) | CONCERN (SMOKE-1-002) |

**Summary**: 6 / 6 capabilities present and sound; 2 carry a live-provisioning verification gap (J3, J6)

## 4. Cross-Cutting Concerns

| Concern              | Result | Details          |
|----------------------|--------|------------------|
| Console Errors       | N_A    | No UI in Epic 1 |
| Network Failures     | N_A    | No live services run |
| Visual Consistency   | N_A    | No UI |
| Performance          | N_A    | Infra epic; no runtime to measure |
| Auth Flow            | N_A    | No auth surface in Epic 1 |

## 5. Issues Found

| ID | Severity | Finding | Journey | Suggested Action |
|----|----------|---------|---------|------------------|
| SMOKE-1-001 | MEDIUM | 4 Epic-1 acceptance tests fail as **stale snapshots** (`1.2-UNIT-004`, `1.2-UNIT-009`, `1.6-UNIT-128`, `1.6-UNIT-147`). The underlying infra is verified sound and *more* complete; the tests froze early-project invariants (exact file counts, lint scope, placeholder location) that the codebase correctly outgrew. They are false negatives producing CI red without signal. | J2, J6 | Refresh the 4 assertions to current repo shape (lint scope incl. ./packages; migration counts as ≥1 / present; relocate the gateway-smoke assertion to `coldstart_test.go`). Route to SM/Dev as an Epic-1 housekeeping story. See cross-reference in QA memory. |
| SMOKE-1-002 | MEDIUM | Live infrastructure provisioning was not exercised — `terraform validate`/apply (1.3), live K8s deploy, and live DB migrate (1.6) cannot run locally (no terraform/atlas/docker; known env limit). 183 live-infra ACs skipped accordingly. | J3, J6 | Run `terraform validate` + a staging `terraform plan`, a kind/staging K8s smoke, and `scripts/db-migrate.sh` against staging DBs in CI before GA (workflows `infra-lint.yml`, `db-migrate-check.yml` already exist for this). |

## 6. Evidence Files

| Type | Path | Description |
|------|------|-------------|
| log | (inline, this report §2) | `vitest run` (shared-types 1.x) + `go test` (sample-grpc-app, go-observability) |
| artifact | `infra/{terraform,k8s-base,helm,argocd,grafana-dashboards}` | IaC + deployment manifests (1.3/1.4) |
| artifact | `migrations/postgres/` (19 + atlas.sum), `migrations/clickhouse/` (4) | DB baselines + accreted migrations (1.6) |
| artifact | `.github/workflows/` (10 files) | CI/CD pipelines (1.2) |

No screenshots (infra epic, no UI).

## 7. Recommendation

**Result**: PASS (with concerns)

Epic 1's deliverables are all present and sound: the Turborepo monorepo (22 Go modules),
10 CI/CD workflows, the full IaC tree (terraform/k8s/helm/argocd), the observability
stack config + functional `go-observability`, the gRPC service template
(`sample-grpc-app` green), and the database foundation (19 PG migrations + atlas.sum, CH
migrations, db-migrate.sh).

The verdict is **PASS with concerns** — not a clean PASS — because Epic 1's own
acceptance suite is **4 tests red**. Critically, all four were investigated and
confirmed to be **stale-snapshot drift, not defects**: the infrastructure is correct
and more complete than the frozen assertions assume. They must still be fixed because
they make CI red without signal (SMOKE-1-001).

Confidence is **MEDIUM**: config and code-level surfaces are verified, but live
provisioning (terraform/K8s/DB migrate) could not be exercised locally (SMOKE-1-002).
Two pre-GA actions: (1) refresh the 4 stale acceptance tests; (2) run the live-infra
validation via the existing `infra-lint.yml` / `db-migrate-check.yml` workflows against
staging.
