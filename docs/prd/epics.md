# 6. Epic 详细定义（Epic Details）

## Epic 1: 项目地基与云基础设施

**目标**: 建立 monorepo、CI/CD、云资源、可观测性基础，为后续所有 Epic 提供可落地的工程地基。

**完成定义（Definition of Done）**:
- 开发者可在 5 分钟内通过 `make dev` 在本地启动完整栈
- PR 自动跑 lint + unit + 部署到 staging
- staging 环境可访问，含 OTel + Prometheus + Grafana

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E1-S1 | 创建 monorepo 骨架（Turborepo + apps/ + packages/） | 仓库可 clone；`pnpm i && pnpm build` 全绿 |
| E1-S2 | 搭建 CI/CD 流水线（GitHub Actions） | PR 自动跑 lint/unit/integration；merge 自动部署 staging |
| E1-S3 | 阿里云 VPC + K8s 集群 + 基础网络（Terraform） | K8s 集群可访问；Helm chart 可部署 |
| E1-S4 | OpenTelemetry + Prometheus + Loki + Grafana 部署 | dashboard 可看到示例服务的 trace 与 metrics |
| E1-S5 | 内部 gRPC 服务模板（Go） | 新服务可基于模板 5 分钟内 scaffold |
| E1-S6 | 数据库基础（Postgres + Redis + ClickHouse） | 三个 DB 可达；migration 工具就绪（atlas 或 goose） |

---

## Epic 2: 账户、身份与多语言 UI 框架

**目标**: 完整的用户身份系统（注册/登录/OAuth/2FA）+ 多语言 UI 基础框架 + GDPR 数据导出/删除。

**完成定义**:
- 海外用户可通过邮箱或 Google/GitHub OAuth 完成注册
- UI 默认英文，可运行时切换到其他 9 种语言
- 用户可在控制台导出/删除自己的数据

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E2-S1 | Next.js 控制台骨架 + i18n 框架（next-intl） | 默认英文；可切换到 zh-CN 看到全套翻译 |
| E2-S2 | 邮箱注册 + 密码登录 + 邮件验证 | 收到验证码邮件后激活账号；密码 bcrypt 哈希 |
| E2-S3 | OAuth: Google / GitHub | 一键登录后自动创建 / 关联账户 |
| E2-S4 | TOTP 2FA | 用户启用后登录需输入 6 位动态码 |
| E2-S5 | 个人资料管理（昵称/locale/时区） | 资料修改后 UI 立即按新 locale 渲染 |
| E2-S6 | GDPR 数据导出（JSON 包） | 用户点击导出后异步生成 zip 包并通过邮件发送下载链接 |
| E2-S7 | 账号注销与数据删除（30 天宽限期） | 注销后账号锁定；30 天内可恢复；30 天后物理删除 |

---

## Epic 3: OpenAI 兼容 API 网关核心

**目标**: 暴露符合 OpenAI 协议的 `/v1/*` 端点，支持流式与非流式，统一鉴权与错误码。

**完成定义**:
- 客户改 `base_url` 即可零代码迁移（验证：OpenAI Python SDK 可调通）
- 流式 SSE 与非流式响应均可用
- 错误响应符合 OpenAI 规范

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E3-S1 | 网关 HTTP 框架（Go net/http + connectrpc, ratify）+ /health + 冷启动基准 | `/health` 返回 200；冷启动 ≤ 1s P95 |
| E3-S2 | Bearer Token 鉴权 + Key 校验 | 无效 Key 返回 401；有效 Key 通过 |
| E3-S3 | `/v1/chat/completions` 非流式实现（占位上游） | OpenAI Python SDK 可调通，返回 mock 数据 |
| E3-S4 | `/v1/chat/completions` 流式（SSE） | 客户端可逐块接收 token；TTFB ≤ 300ms |
| E3-S5 | `/v1/models` + `/v1/embeddings` 端点 | 返回模型列表 + embedding 端点占位 |
| E3-S6 | 标准化错误响应 + `he_request_id` | 错误格式与 OpenAI 一致；带 trace ID |

---

## Epic 4: 6 家中国大模型适配器

**目标**: 实现 6 家中国主流 LLM 的 plugin 适配器，规范化为 OpenAI 协议；并验证流式/非流式/token 用量准确透传。

**完成定义**:
- 6 家模型均可通过 `/v1/chat/completions` 以 OpenAI 协议调通
- Token 用量准确（与上游官方计量误差 < 1%）
- 能力矩阵公布

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E4-S1 | DeepSeek 适配器（流式 + 非流式） | DeepSeek-V3 可调通；token 用量准确 |
| E4-S2 | Qwen（通义千问）适配器 | Qwen-Max / Qwen-Plus 可调通 |
| E4-S3 | Kimi（Moonshot）适配器 | Moonshot-v1-8k / 32k / 128k 可调通 |
| E4-S4 | GLM（智谱）适配器 | GLM-4 可调通 |
| E4-S5 | Doubao（豆包）适配器 | Doubao-pro / lite 可调通 |
| E4-S6 | 文心（百度）适配器 | ERNIE-4.0 可调通 |
| E4-S7 | 能力矩阵接口 + 公开页面 | `/v1/models` 返回完整能力标签；前端能力矩阵页可视化 |
| E4-S8 | 适配器契约测试（防回归） | CI 跑契约测试，OpenAI 协议字段完整性验证 |

---

## Epic 5: API Key 管理、配额与限流

**目标**: 完整的 API Key 生命周期 + 多维限流 + 月度消费上限熔断。

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E5-S1 | API Key 创建 / 列表 / 吊销 | 创建后明文仅展示一次；吊销立即生效（Redis 缓存失效） |
| E5-S2 | Key 配置（范围/IP 白名单/月度上限） | 不在白名单 IP 调用返回 403；超月度上限返回 402 |
| E5-S3 | 限流（QPS / RPM / TPM） | 超限返回 429 + Retry-After |
| E5-S4 | 月度消费上限熔断 + 邮件预警 | 命中熔断后 Key 暂停；用户邮件通知 |
| E5-S5 | 控制台 Keys 页面（CRUD + 配置 UI） | UI 可完成所有 Key 操作 |

---

## Epic 6: 智能路由与故障转移

**目标**: 实现 3 种路由策略 + 自动 failover + A/B 模式。

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E6-S1 | 路由器核心（策略接口 + 决策引擎） | 单测覆盖 3 种策略边界 |
| E6-S2 | quality / cost / latency 策略实现 | 决策结果可观测（X-He-Selected-Model header） |
| E6-S3 | 自动 failover（3 次失败 / 30s 超时） | 模拟上游故障，自动切换到下一个模型 |
| E6-S4 | A/B 模式（X-He-AB-Models） | 同时调用 2 个模型，响应包含两侧结果 |
| E6-S5 | 路由策略配置 UI（控制台） | 用户可在 UI 选择默认策略 |

---

## Epic 7: 计费、定价与多通道支付

**目标**: 实现混合定价 + 5 通道支付 + Beta 模式开关。

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E7-S1 | 计费引擎（按 token / 按调用 + markup 5-15%） | 每次调用消费扣减余额；账单准确 |
| E7-S2 | 多币种 + 汇率刷新 | USD 默认；RMB 可切换；汇率每天 UTC 00:00 刷新 |
| E7-S3 | Stripe + PayPal 集成 | 充值 + 订阅扣款均可用 |
| E7-S4 | USDC（Coinbase Commerce）集成 | 用户可生成充值地址，确认后自动到账 |
| E7-S5 | 支付宝 Alipay+ 国际版集成 | 海外用户可用 Alipay+ 充值（USD/RMB） |
| E7-S6 | 微信支付 WeChat Pay HK / Cross-border | 港澳用户与海外华人可用微信充值 |
| E7-S7 | 自动充值 + 余额预警 + 月度账单 PDF | 余额低于阈值自动充值；月底邮件发送 PDF 账单 |
| E7-S8 | 订阅档（Free/Pro/Team/Enterprise）+ Beta 模式开关 | 用户可订阅升降级；Beta 模式开启时进入 Sandbox 限额 |

---

## Epic 8: 内容安全与合规准备

**目标**: 双向敏感词过滤 + 治理日志 + 备案材料归档。

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E8-S1 | 敏感词词库（中英基础库 + 可扩展） | 内置 1000+ 中文敏感词 + 500+ 英文 |
| E8-S2 | 入参过滤（命中拦截 + 错误码） | 含敏感词请求返回 400 content_filter |
| E8-S3 | 出参过滤（含流式响应替换） | 流式中检测到敏感词立即终止流并返回脱敏内容 |
| E8-S4 | 严格度配置（per Key） | 用户可在 Key 配置严格度 |
| E8-S5 | 治理日志（拦截记录） + 备案材料模板 | 日志保留 6 个月；备案材料 PDF 模板生成 |

---

## Epic 9: 监控、日志与多模态

**目标**: 用户级用量大盘 + 历史日志 + Vision/ASR/TTS 多模态接入。

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E9-S1 | 用量大盘（实时 + 历史聚合） | 控制台显示今日/本月/季度的请求数/成功率/Token/消费 |
| E9-S2 | 实时调用日志（最近 1000 条） | 控制台分页查询 + 多维过滤 |
| E9-S3 | 历史日志下载（90 天 + JSON/CSV） | 用户发起异步下载任务，邮件接收链接 |
| E9-S4 | 全链路 trace（OpenTelemetry） | Grafana 可看到完整请求链路 |
| E9-S5 | Vision API（兼容 OpenAI） + Qwen-VL/GLM-4V | 图像理解请求可通 |
| E9-S6 | ASR API（兼容 Whisper） + Doubao | 语音转文字可用 |
| E9-S7 | TTS API + Doubao | 文字转语音可用 |

---

## Epic 10: 多语言控制台、文档、SDK 与 Beta 上线

**目标**: 完整多语言体验 + 3 个 SDK + 文档站 + 上线检查表 + Beta 切换。

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E10-S1 | 控制台 10 种语言翻译落地 | 全 UI 可切换；阿拉伯文 RTL 正确 |
| E10-S2 | Python SDK（drop-in OpenAI 替代） | `pip install he-api`；与 OpenAI Python SDK 接口一致 |
| E10-S3 | TypeScript SDK | `npm install @he-api/sdk` |
| E10-S4 | Go SDK | `go get github.com/he-api/sdk-go` |
| E10-S5 | 文档站（Quickstart + API Reference + Cookbook，10 种语言） | docs.he-api.com 可访问 |
| E10-S6 | 在线 Playground + Benchmark 页 | 用户可在浏览器调试 + 看跑分 |
| E10-S7 | 上线检查表 + 监控告警 + 客服接入 | PagerDuty 集成；Intercom 上线 |
| E10-S8 | Beta 公测开关 + 限额免费试用配置 | Beta 模式开启时新注册自动获得 $5 试用额度 |

---
