# 香港「单节点常驻最小档」部署命令清单(Option A · 早期真实用户 · 手把手版)

> 目标:用最小常驻资源把 He-API 全栈部署到阿里云**香港**,承接早期真实用户、观察数据/反馈。
> 档位:1×ecs.c7.xlarge(4c8g)单 worker · RDS 保 HA · **跳观测工作负载(只装 CRD)** · Kafka 复用你已买的。
> 形态:Terraform(建云资源)+ CI 构建镜像 + ArgoCD(把服务部署进 K8s)。
> 读法:每步都是【你在干嘛 → 命令 → ✅ 成功应看到 → ⚠️ 坑】。命令里 `<...>` 换成你的真实值,其余原样复制。
> ⚠️ **所有命令都在你的跳板机(那台 2c4g ECS)上跑**;凭据只在跳板机,别外传。

---

## 阶段 −1 — 把代码放到跳板机

**你在干嘛:** 部署要读这个仓库(terraform/helm/脚本)。先让跳板机拿到代码。

- 若代码已在你的 GitHub(如 `github.com/he-api/he-api`):
  ```bash
  # 在跳板机上
  git clone https://github.com/he-api/he-api.git
  cd he-api
  ```
- 若代码只在本地开发机(当前这台,还没远端):先在开发机 push 到你的 Git 远端,跳板机再 clone。
  ```bash
  # 在开发机上(当前这台)
  git remote add origin <你的仓库URL>
  git push -u origin main
  ```

✅ 成功:跳板机上 `ls` 能看到 `infra/ docs/ scripts/ apps/` 等目录。
⚠️ 坑:私有仓库 clone 需要 GitHub token(用户名 + PAT 当密码)或配好 SSH key。

---

## 阶段 0 — 跳板机准备(装工具 + 拿 AccessKey)

### 0.1 SSH 登录跳板机
```bash
ssh root@<跳板机公网IP>          # 密码/密钥用你买 ECS 时设的
```

### 0.2 建 RAM 子账号 + AccessKey(在阿里云控制台网页操作)
1. 登录阿里云控制台 → 搜索进入 **RAM 访问控制** → **用户** → **创建用户**。
2. 勾选 **「OpenAPI 调用访问」**(生成 AccessKey)→ 创建。
3. **立刻记下 AccessKey ID 和 AccessKey Secret**(Secret 只显示这一次,关掉就看不到了)。
4. 给这个用户授权(**用户 → 添加权限**),测试期可直接给 **`AdministratorAccess`**(最省事,风险自担);
   想最小权限就加这些:`AliyunVPCFullAccess` `AliyunCSFullAccess`(ACK)`AliyunContainerRegistryFullAccess`(ACR)`AliyunRDSFullAccess` `AliyunKvstoreFullAccess`(Redis/Tair)`AliyunClickHouseFullAccess` `AliyunKMSFullAccess` `AliyunOSSFullAccess` `AliyunOTSFullAccess`(TableStore)。

### 0.3 装 4 个命令行工具(以下按 x86_64 Linux;命令逐条粘)
```bash
# 前置:解压工具(Alibaba Cloud Linux/CentOS 用 dnf;Ubuntu 用 apt)
sudo dnf install -y unzip curl tar   # Ubuntu: sudo apt-get update && sudo apt-get install -y unzip curl tar

# terraform 1.9.5
curl -fsSLO https://releases.hashicorp.com/terraform/1.9.5/terraform_1.9.5_linux_amd64.zip
unzip terraform_1.9.5_linux_amd64.zip && sudo mv terraform /usr/local/bin/

# kubectl 1.29.1(和集群 k8s 版本对齐)
curl -fsSLO https://dl.k8s.io/release/v1.29.1/bin/linux/amd64/kubectl
sudo install -m 0755 kubectl /usr/local/bin/kubectl

# helm 3(官方脚本)
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

# aliyun CLI
curl -fsSLO https://aliyuncli.alicdn.com/aliyun-cli-linux-latest-amd64.tgz
tar xzf aliyun-cli-linux-latest-amd64.tgz && sudo mv aliyun /usr/local/bin/
```
✅ 成功:下面 4 条都各打印一个版本号
```bash
terraform version && kubectl version --client && helm version && aliyun version
```
⚠️ 坑:装完 `command not found` → 确认 `/usr/local/bin` 在 `PATH`(`echo $PATH`)。

### 0.4 记下跳板机公网出口 IP(阶段 2 白名单要用)
```bash
curl ifconfig.me ; echo
```
✅ 成功:打印一个公网 IP(如 `47.x.x.x`)。记下它,阶段 2 填成 `47.x.x.x/32`。

### 0.5 导出云凭据(香港)
```bash
export ALICLOUD_ACCESS_KEY=<你的AK>
export ALICLOUD_SECRET_KEY=<你的SK>
export ALICLOUD_REGION=cn-hongkong
```
⚠️ 坑:`export` 只在当前终端有效;断开重连要重新 export。

---

## 阶段 1 — Bootstrap 状态后端(一次性)

**你在干嘛:** Terraform 需要一个地方存"账本"(state)。这步建 OSS 桶 + KMS 密钥 + TableStore 锁表放账本。它不归 Terraform 管(先有鸡才有蛋)。
```bash
cd ~/he-api            # 仓库根目录
bash scripts/infra/bootstrap-state-backend.sh staging
```
✅ 成功:脚本末尾打印创建好的 bucket(`he-api-tfstate-staging-hk`)+ **一个 KMS key id**。
- [ ] **把这个 KMS key id 复制下来**,阶段 2 的 `kms_key_id` 要用。
⚠️ 坑:报权限错 → RAM 用户少了 OSS/KMS/OTS 权限,回 0.2 补。

---

## 阶段 2 — 建云资源(Terraform)

### 2.1 把状态后端指向香港
编辑**两个文件**(内容要一致):`infra/terraform/backend.tf` 和 `infra/terraform/envs/staging/backend.tf`,把 `backend "oss"` 块改成:
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
> 更省事的替代:阶段 1 改用 `ALICLOUD_REGION=cn-shanghai` 跑 bootstrap,这两个文件就不用改(账本留上海,资源仍建香港)。

### 2.2 填变量文件
```bash
cd infra/terraform/envs/staging
cp terraform.tfvars.hongkong-minimal.example terraform.tfvars
vi terraform.tfvars      # 或 nano
```
在 `terraform.tfvars` 里改这几处:
- `api_server_public_access_allowed_cidrs` 里的 `203.0.113.42/32` → 换成 **0.4 记下的跳板机 IP/32**
- `postgres_multi_az_zone_id` → 用命令查真实值:
  ```bash
  aliyun rds DescribeAvailableZones --RegionId cn-hongkong --Engine PostgreSQL
  ```
- 末尾敏感值:3 个 DB 密码(自己设强密码)+ `sendgrid_api_key` + `kms_key_id`(阶段 1 输出)+ `ops_admin_group`

### 2.3 Apply
```bash
terraform init          # 若改过 backend:terraform init -reconfigure
terraform plan          # ⚠️ 先看一眼:worker_count=1、region=cn-hongkong、RDS category=HighAvailability
terraform apply         # 输入 yes 确认
```
✅ 成功:`Apply complete!`,并输出 `cluster_id`、`acr_endpoint` 等。约 10-20 分钟(建集群+数据库慢)。
⚠️ 坑:`plan` 报 CIDR precondition 失败 → 2.2 的白名单是空或没换真实 IP。

### 2.4 拿 kubeconfig(让 kubectl 连上新集群)
```bash
export KUBECONFIG=~/.kube/he-api-staging.kubeconfig    # 路径见 tfvars kubeconfig_path
kubectl get nodes
```
✅ 成功:列出 **1 个 Ready 节点**(你的单 worker)。

---

## 阶段 3 — 跳观测:只装 CRD(单节点必做)

**你在干嘛:** 我们不装吃内存的 Prometheus/Grafana。但服务会创建 ServiceMonitor/PrometheusRule 这类资源,得先让集群"认识"它们(装 2 个 CRD 定义,近乎零资源)。
```bash
kubectl create -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.72.0/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml
kubectl create -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.72.0/example/prometheus-operator-crd/monitoring.coreos.com_prometheusrules.yaml
```
✅ 成功:各打印 `created`。
⚠️ 坑:**不要**跑 `scripts/observability/install.sh`(那是全套观测栈,单节点内存扛不住)。

---

## 阶段 4 — 建密钥(手建 K8s Secret)

**你在干嘛:** 把 6 家模型 key、支付、OAuth、SendGrid、汇率填进集群。
👉 **完整可复制命令见 [`staging-secrets-hongkong-minimal.md`](./staging-secrets-hongkong-minimal.md)** —— 照那份逐条跑即可(Secret 名/key/namespace 都对好了)。
- [ ] 建完用那份文档末尾的"自检"命令确认齐了
⚠️ 坑:namespace 要先存在;`he-api-staging` 阶段 2 已建,`he-api-adapters` 由阶段 6 ArgoCD 自动建 —— 所以 adapter 的 6 个 Secret **在阶段 6 之后建**,其余现在就能建。

---

## 阶段 5 — 构建并推送镜像(GitHub CI)

**你在干嘛:** 让 CI 把 14 个服务打成镜像,推进你的香港 ACR。
- [ ] GitHub 仓库 → Settings → Secrets:加 `ACR_REGISTRY`(= `he-api-registry.cn-hongkong.cr.aliyuncs.com`)、`ACR_USERNAME`、`ACR_PASSWORD`(ACR 控制台设的访问凭证);变量加 `ARGOCD_ENDPOINT`
- [ ] push 到 main(或在 Actions 手动触发 `build-images.yml`)
✅ 成功:Actions 里 `build-images` 变绿;香港 ACR 里能看到各服务的 `:{git-sha}` 镜像。

---

## 阶段 6 — 部署服务(ArgoCD)

**你在干嘛:** 装 ArgoCD,让它照仓库把 13 个应用同步进集群。
```bash
# 1. 装 ArgoCD
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl -n argocd rollout status deploy/argocd-server        # 等就绪

# 2. 注册仓库凭据(私有仓库需要),再下发项目 + 13 应用
kubectl apply -f infra/argocd/projects/he-api.yaml
kubectl apply -f infra/argocd/applications/
```
- [ ] 回到阶段 4,把 6 个 adapter Secret 建到自动生成的 `he-api-adapters` namespace
```bash
# 3. 看同步状态
kubectl get applications -n argocd
```
✅ 成功:13 个 application 都 `Synced + Healthy`。
⚠️ 坑:Pod 卡 `Pending`(单节点资源紧)→ 先确认没误装观测栈;实在不够,临时把 tfvars `worker_count` 改 2 再 `terraform apply`。

---

## 阶段 7 — 数据库迁移

**你在干嘛:** 建表(PostgreSQL + ClickHouse 的 schema)。
```bash
# 导出连接串(从 Terraform 渲染的 Secret 取,或手填),然后:
bash scripts/db-migrate.sh up
bash scripts/db-migrate.sh status      # 核对
```
✅ 成功:`status` 显示 PG/ClickHouse 迁移都到最新版本。

---

## 阶段 8 — 验证能用(GA 检查)

对照 [`../qa/ga-readiness-checklist.md`](../qa/ga-readiness-checklist.md) 的 🔴 项跑:
- [ ] 集群/DB 连通 · 6 模型真实上游调用 · 5 支付 sandbox 往返 · console 注册登录 e2e · Python SDK 装包
- [ ] 观测相关项本档跳过,记"待补(加节点后做)"

---

## 阶段 9 — 常驻 / 收尾

- [ ] 验证全绿 → 保持运行,承接早期真实用户(Option A 是常驻,不销毁)
- [ ] 月费盯 ClickHouse/Kafka/RDS 三大头(单节点常驻档 ≈¥5-7k;Kafka 已买则有效 ≈¥4-6k)
- [ ] 真要临时省钱可 `terraform destroy`,但 = 服务下线;**状态后端 OSS/KMS/TableStore 受保护不会被删**

---

## 顺序硬依赖
−1(代码上机)→ 0(工具+AccessKey)→ 1(bootstrap)→ 2(建资源·含 backend 改香港)→ **3(装 CRD)** → 4(密钥,adapter 部分在 6 之后)→ 5(镜像)→ 6(ArgoCD)→ 7(迁移)→ 8(验证)→ 9(常驻)。

## 卡住怎么办
把出错那一步的**命令 + 完整报错**贴给 Yuri,我帮你判断 + 给下一步。
