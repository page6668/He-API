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

## 7.6 Trace 采样与 Collector 策略（Story 9.4）

全链路 trace 的传播/可视机制见 `docs/architecture/11-可观测性observability.md §11.1`；本节固化**采样比例**与 **otel-collector 管道**策略（Architect Q-SAMPLE / Q-COLLECTOR ratified）。

### 7.6.1 头部采样（head sampling，SDK 侧）

- Sampler 恒为 `ParentBased(TraceIDRatioBased(ratio))`（`packages/go-observability/tracer.go`，两条构造分支均应用）。`ParentBased` 保证子服务**绝不**独立决定丢弃 gateway 已采样的 span（无半截 trace / 无空洞）。**gateway 是采样根**。
- 比例经环境变量驱动，无需重新部署即可调：`OTEL_TRACES_SAMPLER=parentbased_traceidratio`、`OTEL_TRACES_SAMPLER_ARG=<float∈[0,1]>`。不可解析/越界 → fallback `1.0` + 启动 warn slog（绝不 panic）。

| 环境 | `OTEL_TRACES_SAMPLER_ARG` | 说明 |
|------|---------------------------|------|
| 非生产（dev/staging） | `1.0` | 全采样，便于调试 |
| 生产 | `0.1` | 有界，仅在拿到真实流量数据后再调 |

### 7.6.2 尾部采样 + PII 兜底（collector 侧）

`infra/helm/observability/otel-collector/values-staging.yaml` 的 traces 管道为
`otlp → redaction → tail_sampling → batch → otlp(Jaeger)`：

- **`redaction`（PII keep-list，BR-TR-16）**：`allow_all_keys=false` + `allowed_keys`（§11.5 注册 `he.*` 的超集 + 我们 emit 的 OTel semconv）。任何不在白名单的 span attribute 在落 Jaeger 前被**丢弃** — 这是 BR-TR-7 代码级纪律的运行时兜底。`client.address`/`url.full`/`Authorization`/请求体类 key **故意不列入**，因此一律被剥离。`allowed_keys` 必须保持 ⊇ §11.5 注册集（CI 静态门 `1.4-UNIT-057b` + 集成 `9.4-INT-008`）。
- **`tail_sampling`（Q-COLLECTOR）**：`status_code=[ERROR]` 策略以 **100%** 保留所有错误/非 OK trace（这是 Q-SAMPLE「错误必采」的归属地 —— 头部采样在根 span 时点无法预知错误）；其余按 `probabilistic.sampling_percentage=10` 基线保留。
  - **头/尾交互注记**：头部采样在 SDK 侧先丢弃，被丢弃的 trace（含其错误）不会抵达 collector，故无法被尾部「错误必采」追回。若要让生产「错误 100% 保留」完全生效，可将 gateway 根的头部比例提到 `1.0`、把削量职责完全交给 collector 尾部 —— 这是一个**运维调节旋钮**，不在 9.4 默认值内（9.4 默认遵循上表 prod=0.1）。

### 7.6.3 存储与保留

- Trace 后端 = **Jaeger**（`uid: jaeger`，Grafana 既有 datasource 可视），**不迁 Tempo**（Q-STORE ratified）。
- Jaeger staging 存储为 in-memory（1.4 Q4，~2h 自然驱逐）；生产存储/保留期是已知的**后续工作**，不在 9.4 范围。derived-field/tracesToLogs 链接命中已驱逐 trace 时 Grafana 显示「trace not found」，operator 回退到 logs-only（见 runbook）。

---
