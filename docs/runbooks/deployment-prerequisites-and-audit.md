# He-API 部署前置审计 + 操作准备清单(2026-06-16)

> 来源:部署前静态审计(Terraform / 密钥凭据 / Helm-K8s-ArgoCD-CI 三块,子代理逐文件读)。
> **核心结论:代码完整、测试全绿、Phase C 通过 ≠ 可部署。** 基建是架构良好、安全卫生强的**骨架**,但**运行时部署管线端到端是 stub**,需先补工程接线,再做实际部署。

---

## 0. 一句话现状
- **staging 是唯一可部署环境**;**prod 是 code-only**(`envs/prod/main.tf` 只接了 vpc/ack/acr,**缺 RDS/Redis/ClickHouse/Secret 整个数据层**)。
- 部署管线:`build-images`(只构建 17 个里的 3 个)→ `deploy-staging`(只 bump api-gateway 一个 tag)→ **ArgoCD 同步 = 占位 echo**(`deploy-staging.yml:96` 写着 "real CLI wired in Story 1.4",从未完成)。

---

# PART A — 你(运维)需要提前准备的内容 ★照着这个操作★

## A1. 云账号 & 一次性 bootstrap(最先做)
| 准备项 | 怎么做 | 文档路径 |
|---|---|---|
| **Aliyun 账号 + RAM 子账号**(VPC/ACK/ACR/RDS/Tair/ClickHouse/KMS/OSS/TableStore 权限)| 开通阿里云,建 RAM 用户拿 AccessKey | `docs/architecture/infrastructure-deployment.md`(§7.1)|
| **导出云凭据**(代码里**无** provider 凭据块)| `export ALICLOUD_ACCESS_KEY=… ALICLOUD_SECRET_KEY=…` | `scripts/infra/bootstrap-state-backend.sh`(头部注释)|
| **Bootstrap Terraform 状态后端**(鸡生蛋:OSS bucket + KMS CMK + TableStore 锁表,**不归 Terraform 管**)| 跑 `scripts/infra/bootstrap-state-backend.sh staging` | `infra/terraform/modules/oss-state/README.md` |
| **预建 `ops-cluster-admin` ClusterRole**(env 只建 binding 不建 role)| 手动 kubectl apply | `infra/terraform/envs/staging/main.tf:198-215` 注释 |

## A2. Terraform staging 必填变量(5 个,无默认值,不填 apply 直接失败)
写进 `infra/terraform/envs/staging/terraform.tfvars`(已 gitignore):
| 变量 | 含义 | 文档路径 |
|---|---|---|
| `kms_key_id` | RDS/Tair/ClickHouse 静态加密的 KMS key(需先启用)| `docs/architecture/database-bootstrap.md:184` |
| `postgres_admin_password` | RDS 管理员密码(敏感)| `docs/architecture/database-bootstrap.md:99` |
| `redis_admin_password` | Tair 管理员密码(敏感)| 同上 |
| `clickhouse_admin_password` | ClickHouse 管理员密码(敏感)| 同上 |
| `sendgrid_api_key` | notification-svc 发信(需 SendGrid 已验证发件人)| `docs/stories/2.2-…md` · `docs/dev/logs/2.2-dev-log.md` |
| ⚠️ `api_server_public_access_allowed_cidrs` | **必须替换占位** `203.0.113.42/32`(示例 IP)为真实运维出口 /32 + 刷新 GitHub Actions CIDR,否则 ACK precondition 在 plan 阶段报错 | `infra/terraform/envs/staging/terraform.tfvars.example:27-29` |
| ⚠️ `kubeconfig_path` | 指向可达的 kubeconfig | `envs/staging/variables.tf:65-68` |

## A3. 外部服务凭据(部署 + GA 验证都要)— 多数已有文档
> 形态:目前 **Vault/ESO 延后**(Epic 9),实际是手建 K8s Secret 或 Terraform 渲染。完整清单见各文档。

**6 家模型上游**(每家一个 Bearer key)— 文档在 `docs/dev/secrets/{deepseek,qwen,kimi,glm,doubao,ernie}-upstream.md`
- ⚠️ **缺口**:Doubao 的 **ASR/TTS token**(`DOUBAO_ASR_UPSTREAM_API_TOKEN` / `DOUBAO_TTS_UPSTREAM_API_TOKEN`,代码 `apps/adapters/doubao/cmd/server/main.go:177,208`)**无文档**,需补。

**5 家支付**(文档 `docs/dev/secrets/payment-provider.md`)
- Stripe:`STRIPE_SECRET_KEY` + `STRIPE_WEBHOOK_SIGNING_SECRET`(两个不同)
- PayPal:client id/secret + webhook id
- Coinbase Commerce(USDC):api key + webhook secret
- 支付宝+/Antom:client id + 商户私钥 + 支付宝公钥
- 微信支付 HK:mch_id + 商户私钥 + 证书序列号 + 平台公钥/序列号 + APIv3 key + appid

**其它**
| 凭据 | 用途 | 文档/位置 |
|---|---|---|
| Google / GitHub OAuth client id/secret | auth-svc 登录 | `docs/stories/2.3-…md` · `infra/terraform/modules/oauth-credentials/variables.tf` |
| 汇率 provider URL | billing-svc fx-refresh cron | `docs/dev/secrets/fxrate-provider.md` |
| Aliyun ACR(镜像仓库)用户名/密码 | CI 推镜像 | `docs/stories/1.2-…md` · `infra/terraform/modules/acr/` |
| Intercom App ID + 验证密钥 | console 客服(10.7)| ⚠️ 仅代码注释 `apps/console/lib/intercom/`,无 secrets 文档 |
| npm token / Go SDK repo token | CI 发 SDK | `release-sdk-{typescript,go}.yml` |
| JWT RS256 密钥对 | auth-svc/网关 | **自动生成**(Terraform `tls_private_key`),你**不用准备** |
| PG/Redis/ClickHouse 连接串 | 各服务 | **Terraform 自动渲染**进 K8s Secret,你只供 A2 的管理员密码 |

---

# PART B — 部署前必须先补的工程接线(不是运维准备,是 Dev 工作)

> ⚠️ 准备好 PART A 的所有凭据后,**仍无法部署**,因为以下管线缺口:

| # | 缺口 | 证据 | 影响 |
|---|---|---|---|
| B1 | **ArgoCD 同步是占位 echo** | `deploy-staging.yml:96-97` | 没有任何东西自动进集群 |
| B2 | **CI 只构建 3/17 服务**(api-gateway/sample-*)| `build-images.yml:28-32` | auth/billing/notification/routing/6 适配器/cron **都没镜像** |
| B3 | **ArgoCD app 只有 7 个**(6 适配器+routing),且读 `values.yaml` 不读 `values-staging.yaml` | `infra/argocd/applications/` | api-gateway/auth/billing/notification **无 app**;CI bump 的 staging values 被忽略 |
| B4 | **镜像引用是占位符** `${ACR_REGISTRY}/...` 从不替换 + 部分 `tag: latest` | `auth-svc/values-staging.yaml:5-6` | 真渲染会 ImagePullBackOff |
| B5 | **无 Ingress / DNS / TLS** | 所有 chart `ingress.enabled: false`,无 Ingress 模板 | 外部无流量入口 |
| B6 | **3 个 cron + billing-svc 无 Dockerfile/Deployment** | safety-log-retention / account-deletion-sweeper / db-doctor 无 Dockerfile;billing-svc chart 只有 cronjob | 这些 workload 无法构建/部署 |
| B7 | **payment-svc 无 Helm chart** | `infra/helm/` 只有 billing-svc | 5 支付密钥documented 但无 pod 消费 |
| B8 | **prod 环境完全没接** | `envs/prod` 缺数据层;无 values-prod / prod ArgoCD app;`he-api` AppProject 引用但未定义 | prod 不可部署 |

**这些根因是 Story 1.4(GitOps 接线)未完成。** 建议作为一个 **"Epic 1 部署接线收尾"工作包**走 Orchestrix 链补齐(类似 Phase C 补 2.7/6.5)。

---

## 推荐顺序
1. **(Dev)补 PART B 工程接线**(B1–B8)—— 否则 PART A 准备了也部署不了。
2. **(你/运维)准备 PART A**(云账号 + bootstrap + 5 变量 + 凭据)—— 可与 1 并行准备。
3. `terraform apply`(staging)→ 数据层 + 集群起来。
4. CI 构建全 17 镜像 → ArgoCD 同步 → DB 迁移。
5. 跑 `docs/qa/ga-readiness-checklist.md` 的 🔴 验证项。
6. 修复 → 复验 → GA 切换(Beta 开关 + 10.8 GA badge)。

## 关键文档索引
- 部署架构/GitOps:`docs/architecture/infrastructure-deployment.md`
- 上线检查表:`docs/runbooks/go-live-checklist.md`
- 数据库 bootstrap:`docs/architecture/database-bootstrap.md`
- 状态后端:`infra/terraform/modules/oss-state/README.md` + `scripts/infra/bootstrap-state-backend.sh`
- 各上游/支付/汇率密钥:`docs/dev/secrets/*.md`
- GA 验证:`docs/qa/ga-readiness-checklist.md`
