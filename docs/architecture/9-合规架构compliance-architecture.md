# 9. 合规架构（Compliance Architecture）

## 9.1 数据不出境的架构强制

| 合规边界 | 强制手段 |
|---------|---------|
| 数据 100% 境内存储 | PostgreSQL / Redis / ClickHouse / OSS 全部部署在阿里云境内 region |
| Edge 不缓存内容 | Cloudflare 配置 `Cache-Control: no-store` 全路由 + Workers 强制移除任何缓存指令 |
| 海外 SaaS 不接触请求/响应内容 | Sentry 自托管在境内（不用 sentry.io）；PostHog 自托管；不发任何用户内容到 SaaS |
| 备份不出境 | OSS 跨 region 复制限定境内（cn-shanghai → cn-shenzhen） |
| 监控告警平台 | Grafana 自托管；告警通过飞书机器人（境内） + PagerDuty（仅传 metric 标识，不传内容） |
| 客服平台 (Intercom，海外 SaaS) | **ZERO-PII** (Story 10.7 OQ-10.7-2 裁定)：仅发送非 PII 的 `he_request_id` 句柄，绝不发 email/name/user_id/IP/内容；Identity-Verification HMAC seam BUILD-but-DORMANT（PO 批准个人信息出境合规路径前不启用）；**PRC region-gate** — 境内用户不 boot Intercom，走飞书/邮件分离通道；运行时由 §11.3 `DataExportSuspect`（境外 egress>0 → critical）哨兵兜底。PII-bearing 扩展为 PO-gated。 |

## 9.2 三件套备案

| 备案 | 申请主体 | 时间线 | 阻塞行为 |
|------|---------|-------|---------|
| ICP 备案 | 中国境内法人公司 | 2-4 周 | 无 ICP 备案不能在境内提供网站服务 |
| 算法备案（互联网算法） | 同上 | 4-8 周 | 提供算法推荐服务必须 |
| 生成式 AI 服务备案 | 同上 | 6-12 周 | 提供 LLM 服务必须（核心阻塞） |

**Beta 模式应对**: 生成式 AI 备案完成前，平台以 Sandbox / 限额免费 / 对外标识 "Beta" 运行，规避正式商业化运营。`feature_flags.beta_mode = true` 全局开关在网关层强制：
- 余额扣减跳过（Free credit only）
- Stripe / PayPal 通道隐藏，仅 USDC / Alipay+ / WeChat 小额开放
- 文档与控制台明显标注 "Beta"

> **Story 10.8 落地说明（一致性注记）**：Beta 期「限额免费试用 $5」**就是**既有 Free 档月度权益（`MonthlyIncludedCreditUSD:"5.00"` + `MonthlyQuotaUSD:"5.00"`，Story 7.8），对新验证（`VerifyEmail` 已验真人）且无 subscription 行的用户经 **default-to-free** 自动解析 —— **无新增 money SoT / 无 balances top-up / 无 credit grant**（与「余额扣减跳过(Free credit only)」一致：Beta 期用量本免费，$5 是月度配额/权益上限，无 balances 扣减发生）。「Beta」对外标识由 console `BetaBadge`（authed 壳，env-driven）+ 文档站 announcementBar 落地；GA 翻 `beta_mode=false` 后须 rebuild/redeploy 摘除（env-driven 不自动隐，见 `docs/runbooks/go-live-checklist.md` drop-badge 步）。Beta 开关翻动仍由 Unleash 操作（He-API 只读，Q-ADMIN-BETA）。

## 9.3 内容安全合规

```
入参 Filter (safety-svc):
  1. Bloom filter 快速排除非命中（98%+ 流量）
  2. 词典匹配（基础关键词列表）
  3. 模型分类器（轻量 BERT 微调，TopP 95% 准确）
  4. 命中 → 拒绝 + 写入 content_safety_logs

出参 Filter:
  - 流式 chunk 缓冲：最近 200 tokens 滑窗检测
  - 命中后立即终止流，返回脱敏内容
  - 已发出的 chunk 客户端处理（无法收回，但日志记录）

治理日志 (Story 8.5 IMPLEMENTED):
  - 命中 → 拒绝/脱敏 → 写入 content_safety_logs（fire-and-forget 异步持久化）
  - 保留 6 个月（合规要求）— cmd/safety-log-retention CronJob 月度清理 created_at < 6mo
  - 备案审计可调取 — cmd/safety-filing-report 按时间段生成聚合 PDF（仅 canonical id + 计数，不含原文/敏感词）
  - 用户 GDPR 删除时不删除（合规优先于个人删除请求）— content_safety_logs.user_id 无 users FK，账户删除不级联
```

> **§9.3 治理日志 pipeline 已闭环（Story 8.5，Epic-8 FINALE）**：`命中 → 拒绝/脱敏 → 写入
> content_safety_logs` 端到端打通；持久化 Recorder 位于独立包
> `apps/api-gateway/internal/safetylog`（保持 `contentsafety` 仅词库、无 import-cycle），
> fire-and-forget（DB 故障/缓冲满不阻塞拦截，graceful-drain on shutdown），`excerpt_redacted`
> 仅存掩码片段（■×rune-count，永不存原文/敏感词原形）。第三层 BERT 分类器（行 32）仍为后续
> epic。**Epic-8 DoD CLOSED**：入参/出参过滤（8.2/8.3）· 严格度 per-Key（8.4）· 治理日志 6 个月
> 保留 + 备案 PDF（8.5）。

### 严格度 → 严重度 gating（per Key，Story 8.4）

每个 API-Key 携带一个 `content_safety_strictness` 严格度（`api_keys.content_safety_strictness VARCHAR(10) NOT NULL DEFAULT 'strict'`），作为**后检测**的严重度阈值门控**双向**过滤（入参拒绝 + 出参脱敏/流式终止）。词典每个命中词带 `severity ∈ {high, medium, low}`（Story 8.1），严格度按 8.1 OQ-8.1-5 ratified 映射到最低拦截严重度：

| 严格度 level | minSeverity | 拦截 | 说明 |
|--------------|-------------|------|------|
| `loose`   | high   | 仅 high            | 实验/内部 Key，仅拦最高危（政治/暴恐等） |
| `default` | medium | high + medium      | 标准 |
| `strict`  | low    | high + medium + low | 备案-保守；== 8.2/8.3 上线的 block-all 行为 |

规则：`Blocks(sev, level) := rank(sev) ≥ rank(minSeverity(level))`，rank high(3) > medium(2) > low(1)，阈值**含等**（at-threshold 拦截）。

**Fail-closed（硬性合规不变量）**：缺失 / 空 / 未知 严格度值一律解析为 `strict`（block-all）——迁移列 DEFAULT、Redis 缓存 `omitempty` rollout 窗口、未知 token 三个面都 fail-closed。放宽（default/loose）只能由显式、已校验、已持久化的 Key 配置产生（`PATCH /v1/me/keys/{id}`）。检测（无漏报）从不被门控削弱——子阈值命中仍被**检测**，只是不被**执行**（first-qualifying-hit：子阈值命中不会短路并掩盖后面的合格命中）。

> 备注：层 (3) 模型分类器（轻量 BERT）仍为后续 Epic；Story 8.4 只新增此后检测严重度门，不改动 8.2/8.3 的检测/无漏报机制。

---
