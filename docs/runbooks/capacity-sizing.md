# He-API 容量规格 / 服务器清单(采购预算用)

> 部署形态 = **Kubernetes(阿里云 ACK)+ 托管中间件(PaaS)**。所以"服务器台数" = K8s 工作节点数 + 托管数据实例。
> **Staging 列为权威值**(来自仓库 IaC,标注 BR-1.4 baseline);**生产列为估算**(架构文档 §7.2 只给了副本数,未给节点规格;prod 环境 B8 尚未接线,最终规格需上线压测后定稿)。
> 数据源:`infra/terraform/modules/{ack,rds-postgres,redis-tair,clickhouse}`、`infra/helm/*/values.yaml`、`docs/architecture/infrastructure-deployment.md`。

---

## 1. Staging(权威,IaC 默认值)

### K8s 工作节点
| 项 | 值 |
|---|---|
| 节点数 | **3 台** (`worker_count=3`) |
| 机型 | **ecs.c7.large = 2 vCPU / 4 GiB** (`worker_instance_type`) |
| 磁盘 | 80GB 系统 + 100GB 数据(cloud_essd)/ 台 |
| 合计 worker 容量 | **6 vCPU / 12 GiB** |
| ACK 控制面 | 阿里云托管(不占节点)|

### 托管中间件(PaaS 实例)
| 服务 | 规格 | 存储 |
|---|---|---|
| RDS PostgreSQL | **pg.n2.medium.2c = 2c4g**,多 AZ 高可用 | 100GB ESSD |
| Redis(Tair)| **redis.shard.small.ce**(小分片)| — |
| ClickHouse | **S8 = 2c8g** | 100GB ESSD |
| Kafka | 阿里云托管 Kafka | — |

### 负载足迹(验证 3 节点够用)
14 个业务服务,staging 副本基本为 1(routing-svc / adapter-qwen = 2),资源 requests 普遍 `100m CPU / 128Mi`(notification 更小)。约 15-18 个业务 pod,requests 合计 ≈ **1.5-2 vCPU / 2-3 GiB**。
⚠️ **观测栈(Prometheus/Loki/Jaeger/Grafana/otel-collector/promtail)跑在同集群**,是资源大头(约 +2-3 vCPU / +6-8 GiB)。**3×c7.large 跑功能验证够,全量观测栈拉起会偏紧** → 建议加到 **4 节点** 或升 **c7.xlarge(4c8g)**。

**Staging 一句话:3 台 K8s 节点(2c4g)+ 4 个托管实例(PG 2c4g / Redis 小分片 / ClickHouse 2c8g / Kafka)。**

---

## 2. 生产(估算 — 架构文档 §7.2 副本规格 + 容量推算)

### 副本规格(架构文档权威)
| 服务 | 生产副本 | HPA |
|---|---|---|
| api-gateway | **10** | 5–50 |
| auth/billing/notification/routing/analytics/payment | 各 **3** | 有 |
| 6 × adapter-* | 各 **3** | 按模型流量 |
| 5 × CronJob | 1(定时)| — |

稳态业务 pod ≈ **10 + 6×3 + 6×3 = 46+**,HPA 峰值可冲到上百。

### 推算节点(估算,需压测定稿)
| | 稳态 requests | +50% 余量 + HPA 突发 + 观测栈 |
|---|---|---|
| 业务 46 pod | ~7 vCPU / ~7.5 GiB | |
| 观测栈 | ~3-4 vCPU / ~8-10 GiB | |
| **合计** | **~11 vCPU / ~17 GiB** | **→ 目标 ~24 vCPU / ~36 GiB** |

**生产 K8s 建议起点(二选一)**:
- **6-8 台 ecs.c7.xlarge(4c8g)** + 节点自动伸缩(给 gateway HPA 5-50 留突发),或
- **3-4 台 ecs.c7.2xlarge(8c16g)** + 节点自动伸缩。
> 业务服务本身很轻(Go,requests 100m/128Mi),节点数主要被 **gateway 高副本 + HPA 突发 + 观测栈** 拉动。务必上线前做一轮压测再定稿。

### 生产托管中间件(建议升档)
| 服务 | 建议 |
|---|---|
| RDS PG | 升到 4c8g+ HA(主从自动切换 <2min,RTO 见 §DR)+ 只读副本(按读负载)+ 存储按量 |
| Redis | **集群模式多分片**(staging 是单小分片;月度 cap-reset cron 做全分片 `ForEachMaster` 枚举,prod 多分片)|
| ClickHouse | 升节点规格 + 副本(staging S8 单节点 Basic;prod 用量日志量大,需扩 + 定保留期 —— 见架构文档遗留项)|
| Kafka | 多 broker 托管实例 |
| OSS / KMS / ACR / NAT-EIP | 按量,无固定台数 |

---

## 2.5 月度成本估算(⚠️ 约估 list price · cn-shanghai · 包年包月 · 截止 2026-01 · 以阿里云官方计算器为准)

> 这是**基础设施固定成本**,**不含**两项最大变量:① 上游模型 API token 消费(DeepSeek/Qwen/… 的真实 token 费,按量、随业务量线性增长,但与营收挂钩有 markup)② 公网出口带宽。

### Staging(约估)
| 项 | 规格 | 约 ¥/月 |
|---|---|---|
| K8s 节点 | 3-4 × c7.large(2c4g)| 750–1,000 |
| 节点磁盘 | ESSD 180GB × 3-4 | 270–360 |
| RDS PG | 2c4g HA / 100GB | 700–1,000 |
| Redis Tair | 小分片 | 200–350 |
| ClickHouse | S8 2c8g / 100GB | 1,000–1,400 |
| Kafka | 托管(小)| 800–1,200 |
| ACK Pro 控制面 | 托管 | ~460 |
| SLB + NAT + EIP | | 400–700 |
| ACR EE(企业版镜像仓库)| | 600–1,000 |
| OSS / KMS | 按量 | 50–150 |
| **小计(不含出口带宽)** | | **≈ ¥5,200–7,600 / 月(约 $730–1,070)** |

### 生产(约估,稳态,不含 HPA 峰值突发)
| 项 | 规格 | 约 ¥/月 |
|---|---|---|
| K8s 节点 | 6-8 × c7.xlarge(4c8g)| 2,700–3,600 |
| 节点磁盘 | ESSD | 500–700 |
| RDS PG | 4c8g HA + 只读副本 | 2,500–4,000 |
| Redis | 集群多分片 | 1,000–2,500 |
| ClickHouse | 升档 + 副本(用量日志量大)| 3,000–6,000 |
| Kafka | 多 broker | 2,000–4,000 |
| ACK Pro + SLB + NAT + EIP | | 1,300–2,000 |
| ACR EE | | 600–1,000 |
| OSS / KMS / 观测存储 | | 500–1,500 |
| **小计(不含出口带宽 + 模型 API)** | | **≈ ¥14,000–25,000 / 月(约 $2,000–3,500)** |

### 两个压倒性变量(必须单独算)
1. **上游模型 API token 费** —— 本质是 LLM 网关,生产真实成本大头是转发给 6 家模型的 token 消费,**随调用量线性增长**;但这是**过手成本**(有 5–15% markup,见 Epic 7 计费),应与营收一起建模,不是固定基建支出。
2. **公网出口带宽** —— 按流量计费,SSE 流式响应 + 多模态(图/音频)出流量可观;高流量下可能与基建同量级甚至更高。**强烈建议用 CDN/按量带宽包 + 压测后定档。**

### 省钱杠杆
- **包年包月** 比按量付费省约 30–40%;稳态资源(节点/数据库)用包月,突发用按量/抢占式。
- **业务服务极省**(Go,100m/128Mi)—— 别在节点上过度配置,钱花在数据库 + 观测 + 带宽。
- ACR 企业版较贵,小团队可先用共享/个人版镜像仓库过渡。

---

## 2.6 ⭐ 上线测试阶段推荐配置(香港区 cn-hongkong)

> 用途:部署到 staging + 跑 GA 验证(`docs/qa/ga-readiness-checklist.md`)。**不是生产承流量**,是把全栈跑起来 + 端到端验证。GA 验证测试本身不重(契约/支付sandbox/Playwright e2e 都是轻负载),集群只需**稳定跑全栈 + 观测栈**。

**推荐(舒适,一次跑通不为资源打架):**
| 类别 | 规格(香港)| 说明 |
|---|---|---|
| **K8s 节点** | **3 × ecs.c7.xlarge(4c8g)= 12c/24G** | 14 服务 + 5 cron + 全套观测栈(Prometheus/Loki/Jaeger/Grafana)留足余量 |
| RDS PG | pg.n2.medium.2c(2c4g)HA / 100GB | staging 档够测 |
| Redis Tair | 小分片 | 够测 |
| ClickHouse | S8(2c8g)/ 100GB | 够测 |
| Kafka | 托管(小)| 够测 |
| 其它 | ACK Pro + SLB×1 + NAT/EIP + OSS/KMS;ACR 可先用共享/个人版省钱 | |

**预算下限(够用但偏紧):** 维持 IaC 默认 **3 × c7.large(2c4g)** —— 能跑,但观测栈全开内存会紧,适合极省/观测最小化。

**计费方式:按量付费(Pay-As-You-Go)** —— 测试是临时的,验证完可销毁;**别买包年包月**(那是上生产再转)。空闲时段销毁集群可进一步省。

**费用(香港 · 按量 · 月度等价):** 约 **¥6,500–9,500 /月**(= staging 基线 + 香港 ~10-30% 溢价 + xlarge 升档)。若集中 2–4 周验证、空闲销毁,**整个测试阶段实际花费约 ¥4,000–7,000**。

> 切香港记得在 tfvars 设:`region=cn-hongkong` + `availability_zones=[cn-hongkong-b/c/d]` + `postgres_multi_az_zone_id`(用 `aliyun rds DescribeAvailableZones` 查)+ 确认机型在港有货。

---

## 3. 采购清单速览(staging vs 生产;上线测试推荐见 §2.6)
| 类别 | Staging(IaC 默认)| 生产(估算)|
|---|---|---|
| K8s 工作节点 | 3 台 c7.large(2c4g)[测试建议 3×c7.xlarge,见 §2.6] | 6-8 台 c7.xlarge(4c8g)或 3-4 台 c7.2xlarge(8c16g)+ 自动伸缩 |
| RDS PostgreSQL | 1 × 2c4g HA / 100GB | 1 × 4c8g+ HA + 读副本 |
| Redis Tair | 1 × 小分片 | 集群多分片 |
| ClickHouse | 1 × 2c8g / 100GB | 升档 + 副本 |
| Kafka | 托管(staging 档)| 托管多 broker |
| ACK 控制面 / OSS / KMS / ACR / NAT-EIP | 托管,按量 | 同 |

---

## 4. 注意事项
1. **生产规格是估算,非定稿** —— prod 环境(B8)尚未接线;架构文档只规定副本数,未给节点机型。**正式采购前必须:① 接通 B8 prod 环境 ② 上线压测得到真实 QPS/资源曲线 ③ 据此定稿节点数与数据库规格。**
2. **观测栈是隐藏成本** —— Prometheus/Loki/Jaeger 同集群跑,资源占比可观;生产可考虑独立节点池或托管可观测服务。
3. **业务服务很省** —— 都是 Go,单 pod requests 仅 100m/128Mi;成本大头在数据库 + gateway 高副本 + 观测,而非业务计算。
4. **Region** = `cn-shanghai`(IaC 默认),多 AZ。

> 相关:`docs/runbooks/deployment-prerequisites-and-audit.md`(部署前置)· `docs/architecture/infrastructure-deployment.md`(架构)· `docs/qa/ga-readiness-checklist.md`(GA 验证)。
