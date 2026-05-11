# He-API 产品需求文档（PRD）

> **China LLMs for the World** — 中国大模型出海合规桥梁
>
> 文档版本: v1.0 | 状态: Draft | 输入依据: `docs/project-brief.md`
>
> 关键决策（Q1-Q4）:
> - **Q1 定价**: 混合模式（透明 markup 5-15% + 订阅档 Free / Pro / Team / Enterprise）
> - **Q2 时间线**: 8 周激进 Beta 模式（备案与商业化解耦：4-6 周 Beta 上线 + 6-12 周备案并行；备案完成前以 Sandbox/限额免费或 USDC/Alipay+ 小额结算运行）
> - **Q3 收单主体**: 境内法人公司直连 Stripe / PayPal + USDC + 支付宝 Alipay+ + 微信支付 WeChat Pay HK / Cross-border（全通道并行铺，不设港新子公司）
> - **Q4 模型范围**: MVP 一次性接入 6 家中国主流大模型（Qwen、DeepSeek、Kimi、GLM、Doubao、文心；接入顺序: DeepSeek → Qwen → Kimi → GLM → Doubao → 文心；混元为 V1.1 补齐）

---

## 1. 目标与背景上下文（Goals & Background Context）

### 1.1 业务目标

**北极星指标**: MVP 上线后 60 天内累计注册 ≥ 1,000 个海外开发者（非中国 IP 验证），累计 USD 等价结算 ≥ $10,000。

**MVP 阶段（8 周）目标**:

| ID | 目标 | 衡量 |
|----|------|------|
| G1 | Beta 上线，对外开放注册 | 公网可访问 `api.he-api.com` 与 `console.he-api.com` |
| G2 | 接入 6 家中国主流大模型 | OpenAI 协议入口可调通 6 家 |
| G3 | 完成首笔国际支付结算 | 任一通道（USDC / Stripe / Alipay+ / WeChat Pay）成功收款 ≥ $1 |
| G4 | 多语言控制台 + 文档上线 | 10+ 语言运行时切换可用；Quickstart / API Reference / Cookbook 全语言齐 |
| G5 | 平台 SLA 达标 | Beta 期网关可用性 ≥ 99.5%；P95 网关层叠加延迟 ≤ 150ms |
| G6 | 备案三件套并行启动 | ICP / 算法 / 生成式 AI 备案至少完成材料提交 |

**MVP 后 6 个月目标**: MAU ≥ 5,000 海外付费开发者；月 GMV ≥ $200K；备案三件套全部完成；与 ≥1 家中国 LLM 厂商达成战略合作。

### 1.2 背景上下文（What & Why）

中国大模型（DeepSeek-V3、Qwen2.5、Kimi K1.5、GLM-4 等）在多项 benchmark 上已逼近或超越 GPT-4 / Claude，推理成本低 5-10 倍。但海外开发者面临 5 道墙：注册墙（手机号/银行卡/实名）、语言墙（仅中文）、计费墙（仅 RMB）、合规认知墙、生态墙（OpenRouter/LiteLLM 中国模型支持薄弱）。

He-API 的产品定位是**位于中国境内、面向海外的合规网关层**。海外开发者通过统一 OpenAI 兼容端点 `/v1/chat/completions` 调用，请求由 He-API 路由到背后已完成所有中国合规接入工作的 6 家国产大模型。我们不做模型，只做网关——轻资产、快迭代、可与任何模型厂商建立非排他合作。

### 1.3 变更日志（Change Log）

| 日期 | 版本 | 变更 | 作者 |
|------|------|------|------|
| 2026-05-09 | v1.0 | 初稿创建（基于 project-brief.md + 4 项 Q&A 决策） | Yuri (代笔，原 PM 智能体因 API socket 异常中断两次) |

---

## 2. 需求（Requirements）

### 2.1 功能需求（Functional Requirements, FR）

#### FR-1 账户与身份（Account & Identity）

- **FR-1.1**: 系统必须支持邮箱注册（email + password），仅需邮箱验证码即可激活账号；**禁用国内手机号/身份证/银行卡作为注册必要项**。
- **FR-1.2**: 系统必须支持 OAuth 第三方登录：Google、GitHub、Microsoft（V1.1）。
- **FR-1.3**: 系统必须提供基础双因素认证（TOTP，Google Authenticator 兼容），可选启用。
- **FR-1.4**: 系统必须提供个人资料管理（昵称、头像、所在地区/locale、时区、默认 UI 语言）。
- **FR-1.5**: 系统必须提供 GDPR / CCPA 合规接口：用户可在控制台**导出其全部数据**（JSON 格式），可发起**账号注销与数据删除**请求（30 天宽限期内可撤销）。
- **FR-1.6**: 系统必须支持基础 Team 协作（一个 Owner + 多个 Member，共享配额与 Key），单 Team 上限 5 人（V1）。

#### FR-2 OpenAI 兼容 API 网关（OpenAI-Compatible Gateway）

- **FR-2.1**: 系统必须暴露符合 OpenAI API 协议的端点 `/v1/chat/completions`，支持非流式与流式（SSE）响应；客户改 `base_url` 即可零代码迁移。
- **FR-2.2**: 系统必须支持 `/v1/models` 端点返回可用模型列表（包含 He-API 别名 + 上游模型 ID + 能力标签）。
- **FR-2.3**: 系统必须支持 `/v1/embeddings` 端点（如上游模型支持 embedding，否则返回 405）。
- **FR-2.4**: 系统必须透传 OpenAI Function Calling 基础参数（`tools`、`tool_choice`、`response_format` 中的 JSON mode）；上游不支持的特性返回标准化错误码。
- **FR-2.5**: 系统必须支持 OpenAI 标准 Bearer Token 鉴权（`Authorization: Bearer he-xxxxx`）。
- **FR-2.6**: 系统必须返回符合 OpenAI 规范的错误响应（含 `error.type`、`error.code`、`error.message`、`error.param` 字段），并附加 He-API 自定义字段 `error.he_request_id` 用于客服追溯。

#### FR-3 模型适配器（Model Adapters）

- **FR-3.1**: MVP 阶段必须支持 6 家中国主流大模型：通义千问（Qwen）、DeepSeek、Kimi（Moonshot）、GLM（智谱）、Doubao（字节豆包）、文心（百度）。
- **FR-3.2**: 模型适配器必须以 plugin 形式独立部署，新增模型仅需新增 adapter 而无需修改网关核心。
- **FR-3.3**: 每个适配器必须支持流式与非流式两种调用模式，并完整透传 token 用量（`prompt_tokens` / `completion_tokens` / `total_tokens`）。
- **FR-3.4**: 适配器必须将上游模型差异（参数命名、错误码、流格式）规范化为 OpenAI 标准。
- **FR-3.5**: 系统必须公布"能力矩阵"：列出每家模型在文本对话、长上下文、Function Calling、JSON Mode、视觉、语音等维度的支持状态。

#### FR-4 智能路由（Smart Routing）

- **FR-4.1**: 系统必须提供 3 种路由策略：`quality`（优选最高质量）、`cost`（优选最低单位成本）、`latency`（优选最低延迟）。
- **FR-4.2**: 客户在请求 header（`X-He-Routing-Strategy`）或 model 别名（如 `he-router-cost`）中指定策略。
- **FR-4.3**: 系统必须在所选策略下，**自动选择适配模型并返回**（在响应 header 添加 `X-He-Selected-Model` 透明告知客户）。
- **FR-4.4**: 任一上游模型连续失败 3 次或超时 > 30s，系统必须自动 failover 到同档次下一个模型。
- **FR-4.5**: 系统必须提供 A/B 模式：客户可同时调用 2 个模型并对比结果（`X-He-AB-Models: qwen-max,deepseek-v3`）。

#### FR-5 API Key 与配额限流（API Key, Quota & Rate Limit）

- **FR-5.1**: 用户可创建多个 API Key（单用户 ≤ 20 个），每个 Key 可独立设置：名称、调用范围（哪些模型可用）、IP 白名单、月度消费上限（USD）。
- **FR-5.2**: API Key 哈希存储；创建后明文仅展示一次；用户可吊销但不可恢复。
- **FR-5.3**: 系统必须支持按 Key / 用户 / Team 维度的限流：QPS、RPM（每分钟请求）、TPM（每分钟 tokens）。
- **FR-5.4**: 单个 Key 命中月度消费上限后必须自动熔断，并发邮件通知。
- **FR-5.5**: 系统必须返回标准 `429 Too Many Requests` 与 `Retry-After` header。

#### FR-6 计费与支付（Billing & Payments）

- **FR-6.1（混合定价）**: 系统支持 **markup 模式 + 订阅档** 双层定价。
  - **Markup 层**: 在每家模型上游单价基础上加价 5-15%（具体百分比每模型独立配置），按实际 token 消耗计费。
  - **订阅档**: Free（限额免费试用）、Pro（$29/月，含 $30 额度 + 优先支持）、Team（$99/月，含 $120 额度 + Team 协作）、Enterprise（自定义，含 SLA + 专属客户经理）。
- **FR-6.2（多币种）**: 计费默认 USD；亦支持 RMB（针对支付宝/微信支付通道）；汇率每日 UTC 00:00 自动刷新。
- **FR-6.3（多通道支付）**: MVP 必须集成以下 5 个支付通道：
  - **Stripe**（信用卡 / Apple Pay / Google Pay）
  - **PayPal**
  - **USDC（加密货币）**: 通过 Coinbase Commerce 或 BitPay
  - **支付宝 Alipay+ 国际版**
  - **微信支付 WeChat Pay HK / Cross-border**
- **FR-6.4（自动充值）**: 用户可设置"余额低于 $X 时自动从默认通道充值 $Y"。
- **FR-6.5（余额预警）**: 余额低于阈值（默认 $5）时邮件通知；账户透支时立即熔断。
- **FR-6.6（账单与发票）**: 月度自动生成 PDF 账单（多语言）；商业用户可申请增值税发票/海外 invoice。
- **FR-6.7（Beta 模式开关）**: 备案完成前，系统支持"Beta 模式"全局开关：
  - 开启时，所有用户进入 Sandbox（限额免费试用 $5 起 + USDC/Alipay+ 小额结算）
  - 关闭时切换为正式商业化模式（全通道开放）
- **FR-6.8（退款）**: 客户可在控制台发起退款申请；自动批准条件: 余额未消耗 + 申请于充值后 7 天内。

#### FR-7 内容安全（Content Safety）

- **FR-7.1**: 系统必须对**入参**（用户 prompt）执行敏感词过滤，命中后返回标准错误码 `400 content_filter` 并记录日志。
- **FR-7.2**: 系统必须对**出参**（模型回包）执行同等过滤；流式响应在终止前替换为脱敏内容。
- **FR-7.3**: 用户可在 API Key 维度配置过滤严格度：`strict` / `default` / `loose`（仅适用于敏感程度低的非中文场景）。
- **FR-7.4**: 误杀率（false positive）必须 < 1%；漏检率（false negative）< 0.1%。
- **FR-7.5**: 系统必须维护"敏感词治理日志"用于备案审计：每次拦截的请求 ID、用户 ID、命中词、动作（拦截/告警）。

#### FR-8 多模态（Multi-Modal）

- **FR-8.1**: 系统必须支持图像理解 API（兼容 OpenAI vision 协议）：底层接入 Qwen-VL、GLM-4V。
- **FR-8.2**: 系统必须支持语音转文字（ASR）API：底层接入 Doubao ASR；兼容 OpenAI Whisper API 协议。
- **FR-8.3**: 系统必须支持文字转语音（TTS）API：底层接入 Doubao TTS；兼容 OpenAI TTS API 协议。
- **FR-8.4**: 视频理解作为 V1.1 目标，MVP 阶段提供占位接口返回 501 Not Implemented。

#### FR-9 监控与日志（Observability）

- **FR-9.1**: 用户可在控制台查看**实时调用日志**（最近 1000 条），按时间倒序排列；支持按 Key / 模型 / 状态码过滤。
- **FR-9.2**: 历史日志保存 90 天，可下载 JSON / CSV。
- **FR-9.3**: 控制台必须提供"用量大盘"：今日 / 本月 / 季度的请求数、成功率、Token 消耗、消费金额；按模型与时间维度聚合。
- **FR-9.4**: 系统必须为每个请求分配唯一 `he_request_id`，覆盖整个生命周期（网关 → 适配器 → 上游 → 响应）。
- **FR-9.5**: 服务端必须暴露 OpenTelemetry trace + Prometheus metrics 给运维，但不向用户公开。

#### FR-10 多语言 i18n

- **FR-10.1**: 控制台 UI 必须支持运行时切换以下 10 种语言：英语（en，默认）、简体中文（zh-CN）、繁体中文（zh-TW，V1.1）、日语（ja）、韩语（ko）、西班牙语（es）、法语（fr）、德语（de）、葡萄牙语（pt）、俄语（ru）、阿拉伯语（ar，含 RTL）。
- **FR-10.2**: 错误消息、邮件通知、PDF 账单必须按用户 locale 输出对应语言。
- **FR-10.3**: API 文档（Quickstart / API Reference / Cookbook）必须 10+ 语言齐全；翻译可借助机器翻译 + 人工校对。
- **FR-10.4**: 货币与时间格式按用户 locale 显示。

#### FR-11 文档与 SDK（Developer Experience）

- **FR-11.1**: 必须提供至少 3 个官方 SDK 客户端：Python、Node.js / TypeScript、Go；每个 SDK 与 OpenAI 官方 SDK 接口一致（drop-in replacement）。
- **FR-11.2**: 必须提供交互式 Playground：用户在浏览器选模型、改参数、看响应、复制 cURL/SDK 代码。
- **FR-11.3**: 必须提供 6 家模型 vs GPT-4 / Claude / Gemini 的内置 benchmark 页面，公开质量、延迟、单价数据。

### 2.2 非功能需求（Non-Functional Requirements, NFR）

- **NFR-1 性能**:
  - 网关层 P95 叠加延迟 ≤ 100ms（不含模型推理）
  - 流式响应 TTFB（首字符）网关层叠加 ≤ 300ms
  - 单网关实例并发 ≥ 5,000 RPS
  - 横向无状态扩展，理论容量上限取决于上游模型 quota

- **NFR-2 可用性**:
  - Beta 期 ≥ 99.5%；GA 后 ≥ 99.95%
  - 任一上游模型故障时整体可用性不受影响（智能路由 + failover 兜底）

- **NFR-3 安全**:
  - 全链路 TLS 1.3
  - API Key 哈希存储（bcrypt）；敏感字段（用户密码、支付凭据）KMS 加密
  - 所有用户输入须做 OWASP Top 10 防护（SQL 注入、XSS、SSRF、命令注入）
  - Stripe / PayPal / 支付宝 / 微信 PCI-DSS 合规由通道方负责，He-API 不存储信用卡明文

- **NFR-4 合规**:
  - 必须以中国境内法人公司主体运营
  - 完成 ICP 备案 + 算法备案 + 生成式 AI 备案三件套（Beta 期可并行办理，正式商业化前必须完成）
  - 数据 100% 境内存储，不向海外服务器复制/缓存任何持久化数据
  - 海外用户数据处理符合 GDPR / CCPA（导出/删除接口）
  - 内容安全敏感词治理日志保留 ≥ 6 个月

- **NFR-5 可观测性**:
  - 全链路 OpenTelemetry trace
  - 结构化 JSON 日志（含 he_request_id）
  - Prometheus metrics + Grafana dashboard
  - 关键告警：P95 延迟 / 错误率 / 上游可用性 → PagerDuty 或飞书机器人

- **NFR-6 可扩展性**:
  - 网关层无状态，K8s HPA 自动扩缩容
  - 模型适配器以独立服务部署，可单独扩缩
  - 单 region 容灾（同 region 多 AZ），跨 region 容灾为 V1.2 目标

- **NFR-7 国际化**:
  - 所有用户可见文案外置为 i18n 资源文件，禁止硬编码
  - 前端 react-intl / next-intl；后端 ICU MessageFormat
  - 邮件模板 / PDF 账单 / 错误消息 / 错误页面均按 locale 渲染

- **NFR-8 成本**:
  - 早期低成本基础设施起步：按量付费优先于预购
  - 单位调用成本（基础设施开销 / 调用数）目标 ≤ $0.0001 / 调用

- **NFR-9 文档**:
  - 文档站独立部署（docs.he-api.com）
  - 文档代码示例必须可运行（CI 验证）
  - 主要文档变更需要 PR review

---

## 3. 用户界面设计目标（User Interface Design Goals）

### 3.1 整体视觉与品牌调性

- **设计语言**: 现代、简洁、技术感、国际化（避免过度"中国元素"，主要服务海外用户）
- **配色**: 深色为主（开发者偏好），亮色可切换；强调色使用蓝紫渐变（科技感 + 信任感）
- **字体**: 英文 Inter / 中文 PingFang / 阿拉伯文 Noto Sans Arabic；代码 JetBrains Mono
- **品牌定位关键词**: Reliable / Compliant / Global / Cost-Effective

### 3.2 关键交互范式

- **配置即文档**: 用户在控制台所做的每一项配置（Key / 限流 / 路由策略）都自动生成对应的 cURL / SDK 代码示例，方便复制
- **价格透明**: 任何调用前用户都能看到预估单价；调用后看到实际消耗
- **零障碍迁移**: 注册第一步即引导"如何把现有 OpenAI 代码迁移到 He-API"（修改 base_url + key 即可）

### 3.3 关键页面（MVP 范围）

| ID | 页面 | 说明 |
|----|------|------|
| P-1 | Landing | 营销首页，"Stripe for Chinese LLMs" |
| P-2 | Sign Up / Sign In | 邮箱 + Google/GitHub OAuth |
| P-3 | Onboarding | 3 步引导：选语言 → 创建 Key → 第一次调用 Demo |
| P-4 | Dashboard | 用量大盘 + 余额 + 快捷入口 |
| P-5 | API Keys | Key 列表 / 创建 / 吊销 / 配置 |
| P-6 | Models | 6 家模型列表 + 能力矩阵 + 价格 |
| P-7 | Routing | 智能路由策略配置（quality / cost / latency） |
| P-8 | Logs | 实时调用日志 + 历史日志下载 |
| P-9 | Billing | 余额 / 充值 / 自动充值配置 / 账单历史 / 发票 |
| P-10 | Subscription | 订阅档对比 / 升降级 |
| P-11 | Settings | 个人资料 + 安全（2FA、密码） + GDPR（导出/删除） |
| P-12 | Team | 团队成员管理（V1） |
| P-13 | Playground | 在线调试 + 模型对比 |
| P-14 | Benchmark | 跑分页面（中国模型 vs GPT-4 / Claude / Gemini） |
| P-15 | Docs | Quickstart / API Reference / Cookbook（独立站） |

### 3.4 响应式与可访问性

- **响应式**: 桌面优先（≥ 1280px），平板兼容（≥ 768px），手机仅可读不可编辑
- **可访问性**: WCAG 2.1 AA 级别；键盘可达；屏幕阅读器友好
- **暗黑模式**: 默认暗色，用户可切换亮色

---

## 4. 技术假设（Technical Assumptions）

> Architect 阶段会做最终选型；以下为业务方建议方向。

### 4.1 仓库结构（Repository Structure）

- **Monorepo**（推荐 Turborepo / Nx）
  - `apps/gateway` — 网关核心（Go）
  - `apps/console` — Web 控制台（Next.js）
  - `apps/docs` — 文档站（Docusaurus / Mintlify）
  - `apps/admin` — 内部管理后台（Next.js）
  - `packages/sdk-python` — Python SDK
  - `packages/sdk-typescript` — TypeScript SDK
  - `packages/sdk-go` — Go SDK
  - `packages/adapters/*` — 各模型适配器
  - `packages/proto` — 共享类型与协议
  - `infra/` — Terraform / Helm / K8s manifests

### 4.2 服务架构（Service Architecture）

- **网关层**: 无状态 Go 服务，K8s 横向扩展
- **账户/计费/监控/Key 管理**: 独立服务，gRPC 内部通信
- **模型适配器**: 每家一个独立 deployment，便于独立发版与扩缩
- **数据流**: API 请求 → API Gateway (Go) → Adapter Service → 上游 LLM；账单/日志通过 Kafka 异步消费 → ClickHouse / Postgres 持久化

### 4.3 测试策略（Testing Strategy）

- **单元测试**: 覆盖率 ≥ 70%（业务逻辑模块），关键路径 ≥ 90%
- **集成测试**: 适配器 vs 真实上游（Mock 不替代）；契约测试（OpenAI 协议兼容性）
- **E2E**: Playwright（关键控制台流程）+ k6（API 压测）
- **混沌测试**: 注入上游 timeout / error，验证 failover 行为
- **CI**: GitHub Actions / GitLab CI；PR 强制跑 lint + unit + integration

### 4.4 部署与基础设施

- **云**: 阿里云 / 腾讯云 / 华为云境内 VPC（满足备案要求）
- **容器**: Docker；K8s + Helm
- **CI/CD**: ArgoCD / Flux（GitOps）
- **可观测**: OpenTelemetry + Prometheus + Loki + Grafana
- **CDN**: Cloudflare 仅做 TLS 终端 + 静态资产分发，**不缓存 API 请求/响应内容**（合规边界）
- **DNS**: 国际域 `he-api.com`；地区子域 `us.he-api.com` / `eu.he-api.com`（V1.1 多 region）

### 4.5 数据存储

- **PostgreSQL**: 用户、订单、Key、订阅、配额（OLTP）
- **Redis**: 限流计数、缓存、Session（in-memory）
- **ClickHouse / TimescaleDB**: 请求日志、用量聚合（OLAP）
- **对象存储**（OSS/COS）: 用户数据导出包、PDF 账单

### 4.6 第三方集成

- **OAuth**: Google / GitHub / Microsoft
- **邮件**: SendGrid（海外）/ 阿里云邮推（国内备份）
- **支付**: Stripe / PayPal / Coinbase Commerce / 支付宝开放平台 / 微信支付商户平台
- **客服**: Intercom 或 Crisp（多语言聊天）
- **监控告警**: PagerDuty / 飞书机器人 / Slack
- **分析**: PostHog（产品分析）+ Plausible（隐私友好的网站分析）

---

## 5. Epic 列表（Epic List）

> MVP 共 10 个 Epic。Epic 1-4 为基础设施与协议层，Epic 5-7 为商业化能力，Epic 8-10 为合规、体验与上线准备。
>
> **Epic 1-4 必须串行**（彼此有强依赖）；**Epic 5-7 可并行**（一旦 Epic 4 完成）；**Epic 8 与 Epic 9 可并行**；**Epic 10 是收尾**。
>
> 8 周激进节奏建议: Week 1-2 → E1+E2 / Week 3-4 → E3+E4 / Week 5 → E5+E6 / Week 6 → E7+E8 / Week 7 → E9+E10 / Week 8 → 上线 + 调优。

| Epic | 标题 | 目标 | 故事数（估） |
|------|------|------|------|
| **E1** | 项目地基与云基础设施 | Monorepo + CI/CD + 云资源 + 可观测基础 | 6 |
| **E2** | 账户、身份与多语言 UI 框架 | 注册/登录/OAuth/2FA + i18n shell + GDPR 导出删除 | 7 |
| **E3** | OpenAI 兼容 API 网关核心 | `/v1/chat/completions` 等端点 + 流式 + 鉴权 + 标准错误码 | 6 |
| **E4** | 6 家中国大模型适配器 | DeepSeek / Qwen / Kimi / GLM / Doubao / 文心 6 个 plugin | 8 |
| **E5** | API Key 管理、配额与限流 | 多 Key / 范围限定 / IP 白名单 / 月度上限熔断 / QPS-RPM-TPM 限流 | 5 |
| **E6** | 智能路由与故障转移 | 3 种策略（quality/cost/latency）+ 自动 failover + A/B 模式 | 5 |
| **E7** | 计费、定价与多通道支付 | 混合定价 + USD/RMB + 5 通道支付 + 自动充值 + Beta 模式开关 | 8 |
| **E8** | 内容安全与合规准备 | 双向敏感词过滤 + 治理日志 + 备案材料归档 | 5 |
| **E9** | 监控、日志与多模态 | 全链路 trace + 用量大盘 + 历史日志 + Vision/ASR/TTS 多模态 | 7 |
| **E10** | 多语言控制台、文档、SDK 与 Beta 上线 | 10+ 语言 UI + Quickstart / API Reference / Cookbook + 3 个 SDK + 上线检查表 | 8 |

**故事总数估算**: ~65（每故事 0.5-2 人天，全队 6 人 8 周可达成）

---

## 6. Epic 详细定义（Epic Details）

### Epic 1: 项目地基与云基础设施

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

### Epic 2: 账户、身份与多语言 UI 框架

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

### Epic 3: OpenAI 兼容 API 网关核心

**目标**: 暴露符合 OpenAI 协议的 `/v1/*` 端点，支持流式与非流式，统一鉴权与错误码。

**完成定义**:
- 客户改 `base_url` 即可零代码迁移（验证：OpenAI Python SDK 可调通）
- 流式 SSE 与非流式响应均可用
- 错误响应符合 OpenAI 规范

**故事**:

| ID | 故事 | 验收标准 |
|----|------|---------|
| E3-S1 | 网关 HTTP 框架（Go fiber/echo） + 路由 + 健康检查 | `/health` 返回 200；冷启动 < 1s |
| E3-S2 | Bearer Token 鉴权 + Key 校验 | 无效 Key 返回 401；有效 Key 通过 |
| E3-S3 | `/v1/chat/completions` 非流式实现（占位上游） | OpenAI Python SDK 可调通，返回 mock 数据 |
| E3-S4 | `/v1/chat/completions` 流式（SSE） | 客户端可逐块接收 token；TTFB ≤ 300ms |
| E3-S5 | `/v1/models` + `/v1/embeddings` 端点 | 返回模型列表 + embedding 端点占位 |
| E3-S6 | 标准化错误响应 + `he_request_id` | 错误格式与 OpenAI 一致；带 trace ID |

---

### Epic 4: 6 家中国大模型适配器

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

### Epic 5: API Key 管理、配额与限流

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

### Epic 6: 智能路由与故障转移

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

### Epic 7: 计费、定价与多通道支付

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

### Epic 8: 内容安全与合规准备

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

### Epic 9: 监控、日志与多模态

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

### Epic 10: 多语言控制台、文档、SDK 与 Beta 上线

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

## 7. 检查表结果（Checklist Results）

### 7.1 PRD 完整性自检

| 检查项 | 状态 | 备注 |
|--------|------|------|
| 是否覆盖 4 项 Q&A 决策？ | ✅ | Q1 混合定价 (FR-6.1) / Q2 8 周 Beta (G1-G6, FR-6.7) / Q3 全通道 (FR-6.3) / Q4 6 模型 (Epic 4) |
| 是否覆盖简报中的 7 大差异化卖点？ | ✅ | 海外注册（FR-1）/ 多通道（FR-6）/ i18n（FR-10）/ OpenAI 兼容（FR-2）/ 智能路由（FR-4）/ 跑分（FR-11.3）/ 合规（FR-7） |
| 是否覆盖简报中标记的 🔴 高风险？ | ✅ | 合规边界（NFR-4）/ 支付通道（FR-6.3 全通道并行）/ 上游依赖（FR-3 多模型分散） |
| Epic 之间依赖与时间排布是否合理？ | ✅ | E1→E2→E3→E4 串行；E5/E6/E7 可在 E4 后并行 |
| 8 周时间线是否可达？ | ⚠️ | **置信度: 中**。65 故事 × 6 人需高效协作；备案三件套并行不阻塞 Beta 上线 |

### 7.2 风险标注（继承自简报）

| 风险 | PRD 中的应对 |
|------|-------------|
| 🔴 合规边界 | NFR-4（境内主体 + 三件套并行）+ FR-6.7（Beta 模式开关） |
| 🔴 国际支付被冻结 | FR-6.3（5 通道并行，任一通道故障可承接 80%+ 流量） |
| 🔴 上游模型政策变动 | FR-3（6 家分散依赖）+ FR-4.4（自动 failover） |

### 7.3 跨文档一致性

- 与 `docs/project-brief.md` 对齐 ✅
- 4 项 Q&A 决策全部落地为 FR / NFR / Epic 故事 ✅

---

## 8. 下一步（Next Steps）

### 8.1 立即行动项

1. **业务方 PRD 审阅**: 检查 FR / NFR / Epic 列表是否符合预期；标注需要修订之处
2. **UX Expert 接力**: 基于 PRD 第 3 节（UI 设计目标）+ Epic 列表中的页面（P-1 到 P-15），生成 `docs/front-end-spec.md`
3. **Architect 接力**: 基于 PRD 第 4 节（技术假设）+ FR / NFR，生成 `docs/architecture.md`（含详细服务拓扑、数据库 schema、API 详细规范、部署方案）
4. **PO 接力**: 执行 `*execute-checklist po-master-validation` 验证 PRD 一致性，再 `*shard` 拆分给 SM 用于故事创建
5. **并行启动法律意见书**: 数据出境合规边界 + 国际支付主体结构（前置阻塞项）
6. **并行启动备案准备**: ICP / 算法 / 生成式 AI 备案三件套材料收集

### 8.2 PM 交接说明

> 本 PRD 由 Yuri（协调者）代笔起草。原 PM 智能体（范蠡）因 op-He-API 会话内的 Claude Code Anthropic API socket 连接两次异常中断而无法完成。Yuri 基于 `docs/project-brief.md` 的 345 行完整上下文 + 业务方对 4 项关键问题的明确回答（Q1 混合定价 / Q2 8 周激进 Beta / Q3 全通道并行 / Q4 6 模型），按 PRD 标准结构产出本文档。如需 PM 智能体重新审阅或润色，可在网络稳定后通过 `/o pm` 加载 PM，让其执行 `*revise-prd` 命令对本文档做修订。

### 8.3 UX Expert 提示词模板（供参考）

```
基于 docs/project-brief.md 与 docs/prd.md，按 PRD 第 3 节（UI 设计目标）和
Epic 列表中的 P-1 到 P-15 关键页面，生成 docs/front-end-spec.md。

特别关注：
1. 多语言 i18n（10+ 语言运行时切换、阿拉伯文 RTL）
2. 海外开发者优先的视觉调性（深色为主，技术感）
3. P-3 Onboarding 三步引导（选语言 → 创建 Key → Demo 调用）的转化漏斗设计
4. P-9 Billing 的多币种 + 5 通道支付的统一交互
5. P-13 Playground 与 P-14 Benchmark 作为差异化体验的核心展示位
```

### 8.4 Architect 提示词模板（供参考）

```
基于 docs/project-brief.md、docs/prd.md，按 PRD 第 4 节（技术假设）展开为完整
全栈架构文档 docs/architecture.md。

特别关注：
1. 海外用户访问中国境内服务的延迟优化方案（边缘 TLS 终端 + 不缓存）
2. 网关层无状态横向扩展设计（Go + K8s HPA）
3. 5 通道支付的统一抽象层
4. 模型适配器 plugin 架构（动态加载 / 热升级）
5. 内容安全过滤的高性能实现（避免成为延迟瓶颈）
6. 数据 100% 境内存储的数据流证明
7. Beta 模式开关在架构层面的实现方式
```

---

## 附录: 关键术语表

| 术语 | 含义 |
|------|------|
| **markup 模式** | 在上游模型单价基础上加价 X% 的定价方式 |
| **Beta 模式** | 备案完成前 He-API 以 Sandbox / 限额免费 / 小额结算运行的过渡形态 |
| **三件套备案** | ICP 备案 + 算法备案 + 生成式 AI 备案 |
| **OpenAI 兼容协议** | 客户改 `base_url` 即可零代码迁移的事实标准 API |
| **failover** | 主上游故障时自动切换到次选模型的机制 |
| **能力矩阵** | 列出每家模型在文本/视觉/语音/Function Calling 等维度支持状态的对照表 |
| **smart routing** | 按 quality/cost/latency 三维度自动选模型的路由能力 |
| **drop-in replacement** | "无缝替代"——客户原代码无需任何修改即可切换到新供应商 |
| **i18n** | Internationalization 国际化（多语言支持） |
| **RTL** | Right-to-Left 阿拉伯文/希伯来文等从右到左书写的排版 |

---

> **PRD 终版结束**。等待业务方审阅，确认后转交 UX Expert / Architect / PO 串行处理。
