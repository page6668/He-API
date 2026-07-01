# 香港「单节点常驻最小档」部署命令清单(Option A · 早期真实用户)

> 目标:用最小常驻资源把 He-API 全栈部署到阿里云**香港**,承接早期真实用户、观察数据/反馈。
> 档位:1×ecs.c7.xlarge(4c8g)单 worker · RDS 保 HA · **跳观测工作负载(只装 CRD)** · Kafka 复用你已买的。
> 形态:Terraform(基建)+ CI 构建镜像 + ArgoCD GitOps(部署)。
> ⚠️ 所有 `terraform`/`kubectl`/`helm` **必须在你的跳板机上跑**(有工具 + 云凭据);本清单每条命令都填好了香港参数,照着做。
> 前置:`deployment-prerequisites-and-audit.md`(PART A 要准备什么)· `procurement-review-20260622.md §2.5`(单节点常驻档)。

---

## 阶段 0 — 跳板机准备(用你已买的 2c4g ECS)
- [ ] SSH 上跳板机,装:`terraform(>=1.7)` `kubectl` `helm` `aliyun` CLI
- [ ] 记下**跳板机公网出口 IP**(`curl ifconfig.me`)→ 阶段 2 的 CIDR 白名单要用
- [ ] 导出云凭据(香港):
  ```bash
  export ALICLOUD_ACCESS_KEY=<AK>
  export ALICLOUD_SECRET_KEY=<SK>
  export ALICLOUD_REGION=cn-hongkong
  ```

## 阶段 1 — Bootstrap 状态后端(一次性 · 香港)
> 建 OSS bucket + KMS CMK + TableStore 锁表(不归 Terraform 管)。脚本已支持 cn-hongkong(→ bucket 后缀 `-hk`)。
```bash
bash scripts/infra/bootstrap-state-backend.sh staging      # ALICLOUD_REGION=cn-hongkong 已导出
```
- [ ] **记下脚本输出的 KMS key id**(阶段 2 的 `kms_key_id` 要用)
- [ ] 状态后端现在在香港(bucket `he-api-tfstate-staging-hk`)→ **必须改 backend.tf**(见阶段 2 第 0 步)

## 阶段 2 — 配置 + Terraform apply(基建)

**0. 把 backend 指向香港**(两个文件保持一致:`infra/terraform/backend.tf` 与 `infra/terraform/envs/staging/backend.tf`),把 `backend "oss"` 块改成:
```hcl
  backend "oss" {
    bucket              = "he-api-tfstate-staging-hk"
    prefix              = "envs/staging"
    region              = "cn-hongkong"
    tablestore_endpoint = "https://he-api-tfstate-staging.cn-hongkong.ots.aliyuncs.com"
    tablestore_table    = "terraform-lock"
    encrypt             = true
  }
```
> 或者(更省事)让**状态后端留在上海**:阶段 1 改用 `ALICLOUD_REGION=cn-shanghai` 跑 bootstrap,backend.tf 不动,资源仍按 tfvars 建在香港。状态只是元数据,不含用户数据,留哪région都行。

**1. 填 tfvars**(用现成的单节点香港模板):
```bash
cd infra/terraform/envs/staging
cp terraform.tfvars.hongkong-minimal.example terraform.tfvars
```
在 `terraform.tfvars` 里补/改:
- `postgres_multi_az_zone_id` —— `aliyun rds DescribeAvailableZones --RegionId cn-hongkong --Engine PostgreSQL` 查准确值
- `api_server_public_access_allowed_cidrs` —— 把 `203.0.113.42/32` 换成**跳板机真实 /32**
- 末尾敏感值:3 个 DB 密码 + `sendgrid_api_key` + `kms_key_id`(阶段 1 输出)+ `ops_admin_group`

**2. 预建 `ops-cluster-admin` ClusterRole**(env 只建 binding):
```bash
kubectl apply -f <你的 ops-cluster-admin clusterrole.yaml>   # 见 envs/staging/main.tf:198-215 注释
```

**3. Apply**:
```bash
terraform init      # 若改过 backend:terraform init -reconfigure
terraform plan      # ⚠️ 逐项看 diff:确认 worker_count=1、region=cn-hongkong、RDS category=HighAvailability
terraform apply
```
产出:VPC+NAT+EIP · **ACK(1 worker · 4c8g)** · RDS PG16(HA)· Tair · ClickHouse · ACR · K8s namespace/Secret。

**4. 取 kubeconfig**:指向新集群(默认 `~/.kube/he-api-staging.kubeconfig`),`export KUBECONFIG=...`。

## 阶段 3 — 跳观测:只装 CRD(关键 · 单节点必做)
> 服务 chart 的 `serviceMonitor.enabled=true` + 网关 PrometheusRule 会创建 `monitoring.coreos.com` 资源。
> **不装 Prometheus/Loki/Jaeger/Grafana 工作负载(省内存),但必须装它们的 CRD**,否则 ArgoCD 同步 admission 失败。
```bash
# 只装两个 CRD(ServiceMonitor + PrometheusRule),不装任何工作负载:
kubectl create -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.72.0/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml
kubectl create -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.72.0/example/prometheus-operator-crd/monitoring.coreos.com_prometheusrules.yaml
```
- [ ] ⛔ **不要**跑 `scripts/observability/install.sh`(那会装全套观测栈,单节点内存扛不住)
- [ ] 后续要验 Epic 9 观测:加 1-2 个 worker 节点,再跑 `install.sh`

## 阶段 4 — 创建运行时密钥(手建 K8s Secret)
> DB 连接串 Secret 由 Terraform 渲染;**业务外部凭据需手建**。
> ⭐ **完整可复制命令见 [`staging-secrets-hongkong-minimal.md`](./staging-secrets-hongkong-minimal.md)** —— Secret 名/key/namespace 都对准了真实 helm 引用(6 模型 + OAuth + SendGrid + 支付 18 key + 汇率),含顶部必做的 ExternalSecret 开关处理。
- [ ] 按该清单建齐所有 Secret + 自检
- [ ] ⚠️ Doubao ASR/TTS token 尚未接线(已知 GAP,不挡 chat/vision;语音后补)

## 阶段 5 — 构建并推送镜像(CI)
- [ ] GitHub repo 配 CI secrets:`ACR_REGISTRY`(= `he-api-registry.cn-hongkong.cr.aliyuncs.com`)· `ACR_USERNAME` · `ACR_PASSWORD`;var `ARGOCD_ENDPOINT`
- [ ] push 到 main(或手动触发 `build-images.yml`)→ 等绿,镜像 `:${sha}` 进香港 ACR

## 阶段 6 — 部署(ArgoCD GitOps)
```bash
# 1. 集群内装 ArgoCD
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
# 2. 注册 repo 凭据(private repo 用 token),再 apply 项目 + 13 应用:
kubectl apply -f infra/argocd/projects/he-api.yaml
kubectl apply -f infra/argocd/applications/
```
- [ ] 确认 13 个 app 全部 `Synced + Healthy`(api-gateway/auth/billing/notification/analytics/payment/routing + 6 adapter)
- [ ] ⚠️ 单节点资源紧:若有 Pod `Pending`(不够调度),先确认没误装观测栈;仍不够则临时把 worker 提到 2

## 阶段 7 — 数据库迁移
```bash
# 用 Terraform 渲染的 Secret,或导出 HE_API_DB_{POSTGRES,REDIS,CLICKHOUSE}_URI 后:
bash scripts/db-migrate.sh up        # PG 走 atlas,ClickHouse 走 golang-migrate
bash scripts/db-migrate.sh status    # 核对 schema 落地
```
- [ ] 确认 CronJob 已调度(monthly-cost-reset / fx-refresh / account-deletion-sweeper / safety-log-retention / db-doctor)

## 阶段 8 — GA 验证(跑 `../qa/ga-readiness-checklist.md` 的 🔴)
- [ ] 基建连通(集群/DB)· 6 模型真实上游 token 误差 <1% · 5 支付 sandbox 往返入账 · console 核心 e2e · Python SDK clean-room 装包
- [ ] ⚠️ 观测相关 🔴(全链路 trace / 仪表盘)本档**跳过**,记录为"观测验证待补(加节点后做)"

## 阶段 9 — 常驻 / 收尾
- [ ] Option A 是**常驻**:验证全绿后保持运行,承接早期真实用户
- [ ] 想临时省钱(如夜间无流量)可 `terraform destroy` —— ⚠️ **状态后端 OSS/KMS/TableStore 受 destroy 排除保护,不会被删**;但销毁 = 用户不可用,常驻场景一般不销毁
- [ ] 月费监控:留意 ClickHouse/Kafka/RDS 三项大头(§2.5 单节点常驻档 ≈¥5-7k,Kafka 已买则有效 ≈¥4-6k)

---

## 顺序硬依赖
0(跳板机)→ 1(bootstrap)→ 2(基建 · 含 backend 改香港)→ **3(装 CRD 跳观测)** → 4(密钥)→ 5(镜像)→ 6(ArgoCD)→ 7(迁移)→ 8(验证)→ 9(常驻)。

## 与本档相关的仓库改动(已由 Yuri 落地)
- `terraform.tfvars.hongkong-minimal.example` —— 单节点香港 tfvars 模板(新增)
- `bootstrap-state-backend.sh` —— 补 `cn-hongkong` region 支持(否则阶段 1 报错退出)
- `procurement-review-20260622.md §2.5` —— 新增「🟢 单节点常驻」档位行
