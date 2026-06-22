# Staging 部署分步 runbook(香港区 · 上线测试用)

> 目标:把 He-API 全栈部署到阿里云香港 staging 环境,跑通 GA 验证。
> 形态:Terraform(基建)+ CI 构建镜像 + ArgoCD GitOps(部署)。
> 前置阅读:`deployment-prerequisites-and-audit.md`(要准备什么)· `capacity-sizing.md §2.6`(买什么规格)。
> ⚠️ 命令引自仓库真实脚本;但本地无 terraform/helm,**`terraform plan/apply` 必须在装了工具 + 有云凭据的运维机/CI 上执行**。

---

## 阶段 0 — 准备(对照 PART A)
- [ ] 阿里云账号 + RAM 子账号 AccessKey(VPC/ACK/ACR/RDS/Tair/ClickHouse/KMS/OSS/TableStore 权限)
- [ ] 运维机装:`terraform` `kubectl` `helm` `aliyun` CLI(可选 `atlas`)
- [ ] 备齐外部凭据:6 模型 key · 5 支付 key · OAuth · SendGrid · ACR 用户名密码(见各 `docs/dev/secrets/*.md`)
- [ ] 导出云凭据:
  ```bash
  export ALICLOUD_ACCESS_KEY=<AK>  ALICLOUD_SECRET_KEY=<SK>
  export ALICLOUD_REGION=cn-hongkong
  ```

## 阶段 1 — Bootstrap 状态后端(一次性)
> OSS bucket + KMS CMK + TableStore 锁表,不归 Terraform 管(鸡生蛋)。
```bash
bash scripts/infra/bootstrap-state-backend.sh staging
```
- [ ] 记下脚本输出的 KMS key id(阶段 2 的 `kms_key_id` 要用)
- [ ] 若 backend.tf 写死 cn-shanghai,改成香港的 OSS/TableStore endpoint(或新建香港后端)

## 阶段 2 — 配置 + Terraform apply(基建)
1. 复制并填写 tfvars(gitignored):
   ```bash
   cd infra/terraform/envs/staging
   cp terraform.tfvars.example terraform.tfvars
   ```
2. 在 `terraform.tfvars` 填(香港切换 + 5 个敏感变量):
   ```hcl
   region                    = "cn-hongkong"
   availability_zones        = ["cn-hongkong-b","cn-hongkong-c","cn-hongkong-d"]
   postgres_multi_az_zone_id = "MAZ1(b,c)"        # aliyun rds DescribeAvailableZones 查准确值
   worker_instance_type      = "ecs.c7.xlarge"    # 测试推荐 4c8g
   worker_count              = 3
   api_server_public_access_allowed_cidrs = ["<你的运维出口/32>", "<GitHub Actions CIDR>"]
   kms_key_id                = "<阶段1 输出>"
   postgres_admin_password   = "<强密码>"
   redis_admin_password      = "<强密码>"
   clickhouse_admin_password = "<强密码>"
   sendgrid_api_key          = "<SendGrid key>"
   ```
3. 预建 `ops-cluster-admin` ClusterRole(env 只建 binding):`kubectl apply -f <你的 clusterrole.yaml>`
4. Apply:
   ```bash
   terraform init
   terraform plan      # 仔细看 diff
   terraform apply
   ```
   产出:VPC + ACK 集群 + RDS PG(HA)+ Tair + ClickHouse + 基础 K8s Secret/namespace。
5. 取 kubeconfig 指向新集群(`kubeconfig_path` 默认 `~/.kube/he-api-staging.kubeconfig`)。

## 阶段 3 — 创建运行时密钥(Vault/ESO 延后,手建 K8s Secret)
> 数据库连接串 Secret(`he-api-db-creds`)由 Terraform 渲染;**业务外部凭据需手建**。
```bash
# 示例:模型上游 key(逐个 adapter),支付,OAuth,Intercom 等
kubectl create secret generic he-api-upstream-deepseek -n he-api-staging \
  --from-literal=api_key=<DEEPSEEK_KEY>
# … 其余 5 模型 / 5 支付 / OAuth(he-api-oauth-credentials)/ SendGrid(he-api-notification-creds)…
```
- [ ] 逐项对照各 `docs/dev/secrets/*.md` 的 Secret 名 + key 名建齐
- [ ] ⚠️ 别忘 Doubao ASR/TTS token(文档待补,见审计 GAP)

## 阶段 4 — 构建并推送镜像
> CI `build-images.yml`:push 到 main 时按全服务 matrix 构建 + 推 ACR(需 CI secret `ACR_REGISTRY/ACR_USERNAME/ACR_PASSWORD`)。
- [ ] 在 GitHub repo 配 CI secrets:`ACR_REGISTRY` `ACR_USERNAME` `ACR_PASSWORD`(+ var `ARGOCD_ENDPOINT`)
- [ ] push 到 main(或手动触发)→ 等 build-images 绿,镜像 `:${sha}` 进 ACR

## 阶段 5 — 部署(ArgoCD GitOps)
> `deploy-staging.yml`(由 build-images 完成触发):SHA-bump values-staging 的 tag → `${ACR_REGISTRY}` 替换(fail-closed 守卫)→ `trigger-argocd-sync` 同步全部 app。
- [ ] 在集群装 ArgoCD,注册 repo(`https://github.com/he-api/he-api`)+ in-cluster 凭据
- [ ] apply AppProject(`infra/argocd/projects/`)+ 13 个 Application(`infra/argocd/applications/`)
- [ ] 确认 ArgoCD 各 app `Synced + Healthy`(api-gateway/auth/billing/notification/analytics/payment/routing/6 adapter)

## 阶段 6 — 数据库迁移
```bash
bash scripts/db-migrate.sh up      # Atlas 应用 baseline(migrations/postgres/atlas.hcl + clickhouse)
```
- [ ] 确认 PG/ClickHouse schema 落地;CronJob(monthly-cost-reset / fx-refresh / account-deletion-sweeper / safety-log-retention / db-doctor)已调度

## 阶段 7 — GA 验证(跑 `ga-readiness-checklist.md` 的 🔴)
- [ ] **基建**:`terraform validate` + 集群/DB 连通(SMOKE-1-002)
- [ ] **6 模型真实上游** token 误差 <1%:夜跑 `contract-tests-live.yml`(SMOKE-4-03)
- [ ] **5 支付 sandbox** 真实 webhook 往返 + 入账(SMOKE-7-001)
- [ ] **console 核心 e2e**(注册/登录/OAuth/2FA/注销 + 安全攻击):staging 跑 Playwright(SMOKE-2-001)
- [ ] **Python SDK clean-room 装包**:CI 干净环境(SMOKE-10-001)
- [ ] 其余 🟡 项按需

## 阶段 8 — 收尾
- [ ] 验证全绿 → 记录证据 → 进入 GA 切换(`go-live-checklist.md`:Beta 开关 + 10.8 GA badge)
- [ ] 测试若暂停:**销毁集群省钱**(按量付费)`terraform destroy`(状态后端 OSS/KMS/TableStore 受 destroy 排除保护,不会删)

---

## 关键提醒
- **真正可部署的是 staging;prod(B8)还没接线** —— 别在 prod env 跑 apply(缺数据层)。
- **`terraform validate` 我本地没法跑** —— 阶段 2 apply 前务必 `terraform plan` 人工过一遍,或先让 CI `infra-lint.yml` 绿。
- **凭据永不进 git** —— tfvars 已 gitignore,K8s Secret 手建,生产再上 Vault/ESO。
- 顺序硬依赖:0→1→2(基建)→3(密钥)→4(镜像)→5(部署)→6(迁移)→7(验证)。
