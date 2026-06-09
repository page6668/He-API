# 7. 基础设施与部署（Infrastructure & Deployment）

## 7.1 云架构

```
主云：阿里云
  Region: cn-shanghai (主) + cn-shenzhen (灾备)
  - VPC + 多 AZ 子网
  - SLB (Server Load Balancer) 7 层
  - ACK (K8s) 集群
  - RDS PostgreSQL 16 (主从)
  - Redis (Tair 集群版)
  - 阿里云 ClickHouse 或自建
  - 阿里云 Kafka (or 自建)
  - OSS (对象存储)
  - SLS (日志服务，备选)
  - KMS (密钥管理)

容灾云：腾讯云
  Region: ap-shanghai
  - 镜像复制（OSS → COS 同步）
  - 主云灾难时手动切换 DNS

边缘：Cloudflare
  - 全球 anycast TLS 终端
  - DDoS 防护
  - WAF
  - DNS
  - 配置：proxied 但 Cache-Control: no-store, all routes
```

## 7.2 K8s 资源拓扑

```
namespace: he-api-prod
  - api-gateway (Deployment, replicas=10, HPA 5-50)
  - auth-svc, billing-svc, ... (各 3 replicas, HPA)
  - adapter-* (各 3 replicas, HPA per model 流量)
  - kafka, postgres, redis, clickhouse 用云托管

namespace: he-api-staging
  - 同 prod 但 replicas=2

namespace: monitoring
  - prometheus, grafana, alertmanager, loki

namespace: cert-manager
namespace: ingress-nginx
namespace: argocd
```

## 7.3 部署流程（GitOps）

```
开发者 push to main
  ↓
GitHub Actions:
  1. lint + unit test (per-service 并行)
  2. integration test
  3. build Docker images, push to registry (阿里云 ACR)
  4. update Helm values (image tag)
  5. commit to infra repo (separate repo or same)
  ↓
ArgoCD detect change
  ↓
ArgoCD sync to staging cluster
  ↓
自动 e2e test on staging
  ↓ (passing)
ArgoCD prompts manual promote to prod
  ↓
ArgoCD sync to prod (Blue-Green or Canary)
```

## 7.4 灾备策略

| 故障级别 | RTO | RPO | 处理 |
|---------|-----|-----|------|
| 单 pod 故障 | < 1min | 0 | K8s 自动重启 |
| 单 AZ 故障 | < 5min | 0 | 多 AZ 部署，流量切到健康 AZ |
| 主 region 故障 | < 30min | < 5min | DNS 切换至腾讯云灾备 |
| 数据库主节点故障 | < 2min | 0 | RDS 自动主从切换 |
| 上游模型 API 故障 | 0（用户透明） | 0 | 智能路由 failover 到其他模型 |

---

## 7.5 计划任务（CronJobs）

| CronJob | Story | Schedule (UTC) | Image | Posture | Notes |
|---------|-------|----------------|-------|---------|-------|
| `db-doctor` | 1.6 | `*/30 * * * *` | `db-doctor` | dev-tooling | 连接性/迁移健康巡检（非数据面）。 |
| `monthly-cost-reset` | 5.4 | `0 0 1 * *` | `monthly-cost-reset` (auth-svc chart) | **生产数据面** | 月初将 `he_api.api_keys.current_month_cost_usd` 重置为 0（非吊销行）+ SCAN+DEL 三类 Redis cap-state key（`usage:apikey:*:month_cost_usd`、`keystate:apikey:cap_tripped:*`、`keystate:apikey:cap_*_notified:*`，cluster-mode 经 `ForEachMaster` 全分片枚举）。`concurrencyPolicy: Forbid` + `startingDeadlineSeconds: 200` + `backoffLimit: 0`（一次执行、失败人工介入）+ `successfulJobsHistoryLimit/failedJobsHistoryLimit: 3` + `timeZone: Etc/UTC`（K8s 1.27+）。复用 auth-svc ServiceAccount + 凭据 Secret；可选 `--purge-redis-only` 运维模式。审计事件 `monthly_cost_reset.completed`（3 次退避重试，best-effort）。CI 金标门 `scripts/ci/verify-cron-schedule.sh` 锁定 schedule 字面量（BR-3.1）。Schedule 漂移会中途重置熔断器 → 成本失控，故纳入门禁。可选 `externalsecret.yaml`（Vault → `auth-svc-vault-secret`，Deployment + CronJob 共享，H-3）默认关闭。 |
| `fx-refresh` | 7.2 | `0 0 * * *` | `fx-refresh` (billing-svc chart) | **生产数据面** | 每日 UTC 00:00 从 FX provider 拉 USD→CNY，向 `he_api.fx_rates` APPEND 一行（最新行 = `ORDER BY fetched_at DESC LIMIT 1`）。专用二进制 `apps/billing-svc/cmd/fx-refresh`（own `main` + Dockerfile + Helm image `he-api/fx-refresh`，Architect H-1 — 非 server 子命令；5.4 同构）。`concurrencyPolicy: Forbid` + `startingDeadlineSeconds: 200` + `backoffLimit: 0`（一次执行）+ `successful/failedJobsHistoryLimit: 3` + `timeZone: Etc/UTC`。**STALE-SERVE**（BR-C-3）：provider timeout/非2xx/malformed/rate≤0 → 不写、保留上一行、`fx_refresh.failed` metric + alert、**exit 0**（次日重试）；零/空汇率绝不持久化或服务。infra 错误（PG connect/insert）→ exit 1（人工介入）。FX provider base-URL/key 为新 secret（`HE_API_FX_PROVIDER_URL`，env 注入，绝不打日志 — BR-C-7；见 `docs/dev/secrets/fxrate-provider.md`）；dev/CI 可用 `FX_MANUAL_USD_CNY` override。CI 金标门 `scripts/ci/verify-cron-schedule.sh` 锁定 `0 0 * * *` 字面量（H-2）。换算仅 read-side display（Q-SOT）：不改 USD 记账 SoT。 |

---
