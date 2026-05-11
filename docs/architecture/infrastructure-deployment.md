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
