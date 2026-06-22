# He-API 部署与上线 — Runbook 索引

> 项目状态(2026-06-22):**开发完成** —— 10 Epic / 全部故事交付,最终体检全绿,Phase C 10/10 冒烟 PASS,staging 部署管线(B1–B7)已接通。
> 当前阶段:**等运维准备凭据 → 部署到 staging → GA 验证**。本页是这条线的导航。

---

## 🧭 按顺序读(部署上线全流程)

| # | 阶段 | 文档 | 谁 |
|---|---|---|---|
| 1 | **看懂前置 + 准备清单** | [`deployment-prerequisites-and-audit.md`](./deployment-prerequisites-and-audit.md) | 你 + Dev |
| 2 | **定服务器规格 + 报预算** | [`capacity-sizing.md`](./capacity-sizing.md) | 你 |
| 3 | **部署到 staging** | [`../architecture/infrastructure-deployment.md`](../architecture/infrastructure-deployment.md) | 运维 |
| 4 | **GA 前 staging 验证** | [`../qa/ga-readiness-checklist.md`](../qa/ga-readiness-checklist.md) | 运维 |
| 5 | **上线检查 + GA 切换** | [`go-live-checklist.md`](./go-live-checklist.md) | PO + 运维 |

---

## 📄 各文档一句话

- **[deployment-prerequisites-and-audit.md](./deployment-prerequisites-and-audit.md)** — 部署前静态审计结论 + 你要准备的(PART A:云账号/bootstrap/5 个 Terraform 必填变量/外部凭据,均带文档路径)+ 工程接线状态(PART B:B1–B7 ✅ 已由 Story 1.7 闭合,B8 prod 待做)。**先读这个。**
- **[capacity-sizing.md](./capacity-sizing.md)** — 服务器/中间件规格(staging 权威:3×c7.large + 4 托管实例;生产估算:6-8×c7.xlarge)+ **月度成本估算**(staging ~¥5-7.6k / 生产 ~¥14-25k,不含模型 token 与出口带宽)+ 采购清单速览。
- **[go-live-checklist.md](./go-live-checklist.md)** — Story 10.7 的上线检查表(cron 就位、告警/看板、支付、合规、DNS 切换、SDK 发布),机器校验 `scripts/ci/verify-go-live-checklist.sh`。
- **[../qa/ga-readiness-checklist.md](../qa/ga-readiness-checklist.md)** — GA 前必须在 staging 跑的环境类验证(5 个 🔴 阻塞项:基建部署 / 真实上游 token 误差 / 支付 sandbox / console e2e / SDK 装包)+ 测试债。

---

## 🔑 凭据准备(PART A 速查 → 详见各 secrets 文档)
- 6 家模型上游:`../dev/secrets/{deepseek,qwen,kimi,glm,doubao,ernie}-upstream.md`(⚠️ Doubao ASR/TTS token 待补文档)
- 5 家支付:`../dev/secrets/payment-provider.md`
- 汇率:`../dev/secrets/fxrate-provider.md`
- OAuth / SendGrid / ACR / DB 密码 / KMS:见 `deployment-prerequisites-and-audit.md` PART A2/A3

---

## ⏭️ 还需开发的(部署线唯一剩余 dev 工作)
- **B8 — prod 环境接线**(`envs/prod` 数据层 + values-prod + prod ArgoCD app + AppProject)。**staging 端到端验证通过后再做。**

---

## 📚 相关
- 交付/流程复盘:[`../retrospectives/2026-06-16-process-and-delivery-retrospective.md`](../retrospectives/2026-06-16-process-and-delivery-retrospective.md)
- Phase C 冒烟证据:`../qa/evidence/smoke-test-epic-*.md`
- 部署架构 / GitOps:[`../architecture/infrastructure-deployment.md`](../architecture/infrastructure-deployment.md)
- 数据库 bootstrap:[`../architecture/database-bootstrap.md`](../architecture/database-bootstrap.md)
