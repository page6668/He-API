# 2. 需求（Requirements）

## 2.1 功能需求（Functional Requirements, FR）

### FR-1 账户与身份（Account & Identity）

- **FR-1.1**: 系统必须支持邮箱注册（email + password），仅需邮箱验证码即可激活账号；**禁用国内手机号/身份证/银行卡作为注册必要项**。
- **FR-1.2**: 系统必须支持 OAuth 第三方登录：Google、GitHub、Microsoft（V1.1）。
- **FR-1.3**: 系统必须提供基础双因素认证（TOTP，Google Authenticator 兼容），可选启用。
- **FR-1.4**: 系统必须提供个人资料管理（昵称、头像、所在地区/locale、时区、默认 UI 语言）。
- **FR-1.5**: 系统必须提供 GDPR / CCPA 合规接口：用户可在控制台**导出其全部数据**（JSON 格式），可发起**账号注销与数据删除**请求（30 天宽限期内可撤销）。
- **FR-1.6**: 系统必须支持基础 Team 协作（一个 Owner + 多个 Member，共享配额与 Key），单 Team 上限 5 人（V1）。

### FR-2 OpenAI 兼容 API 网关（OpenAI-Compatible Gateway）

- **FR-2.1**: 系统必须暴露符合 OpenAI API 协议的端点 `/v1/chat/completions`，支持非流式与流式（SSE）响应；客户改 `base_url` 即可零代码迁移。
- **FR-2.2**: 系统必须支持 `/v1/models` 端点返回可用模型列表（包含 He-API 别名 + 上游模型 ID + 能力标签）。
- **FR-2.3**: 系统必须支持 `/v1/embeddings` 端点（如上游模型支持 embedding，否则返回 405）。
- **FR-2.4**: 系统必须透传 OpenAI Function Calling 基础参数（`tools`、`tool_choice`、`response_format` 中的 JSON mode）；上游不支持的特性返回标准化错误码。
- **FR-2.5**: 系统必须支持 OpenAI 标准 Bearer Token 鉴权（`Authorization: Bearer he-xxxxx`）。
- **FR-2.6**: 系统必须返回符合 OpenAI 规范的错误响应（含 `error.type`、`error.code`、`error.message`、`error.param` 字段），并附加 He-API 自定义字段 `error.he_request_id` 用于客服追溯。

### FR-3 模型适配器（Model Adapters）

- **FR-3.1**: MVP 阶段必须支持 6 家中国主流大模型：通义千问（Qwen）、DeepSeek、Kimi（Moonshot）、GLM（智谱）、Doubao（字节豆包）、文心（百度）。
- **FR-3.2**: 模型适配器必须以 plugin 形式独立部署，新增模型仅需新增 adapter 而无需修改网关核心。
- **FR-3.3**: 每个适配器必须支持流式与非流式两种调用模式，并完整透传 token 用量（`prompt_tokens` / `completion_tokens` / `total_tokens`）。
- **FR-3.4**: 适配器必须将上游模型差异（参数命名、错误码、流格式）规范化为 OpenAI 标准。
- **FR-3.5**: 系统必须公布"能力矩阵"：列出每家模型在文本对话、长上下文、Function Calling、JSON Mode、视觉、语音等维度的支持状态。

### FR-4 智能路由（Smart Routing）

- **FR-4.1**: 系统必须提供 3 种路由策略：`quality`（优选最高质量）、`cost`（优选最低单位成本）、`latency`（优选最低延迟）。
- **FR-4.2**: 客户在请求 header（`X-He-Routing-Strategy`）或 model 别名（如 `he-router-cost`）中指定策略。
- **FR-4.3**: 系统必须在所选策略下，**自动选择适配模型并返回**（在响应 header 添加 `X-He-Selected-Model` 透明告知客户）。
- **FR-4.4**: 任一上游模型连续失败 3 次或超时 > 30s，系统必须自动 failover 到同档次下一个模型。
- **FR-4.5**: 系统必须提供 A/B 模式：客户可同时调用 2 个模型并对比结果（`X-He-AB-Models: qwen-max,deepseek-v3`）。

### FR-5 API Key 与配额限流（API Key, Quota & Rate Limit）

- **FR-5.1**: 用户可创建多个 API Key（单用户 ≤ 20 个），每个 Key 可独立设置：名称、调用范围（哪些模型可用）、IP 白名单、月度消费上限（USD）。
- **FR-5.2**: API Key 哈希存储；创建后明文仅展示一次；用户可吊销但不可恢复。
- **FR-5.3**: 系统必须支持按 Key / 用户 / Team 维度的限流：QPS、RPM（每分钟请求）、TPM（每分钟 tokens）。
- **FR-5.4**: 单个 Key 命中月度消费上限后必须自动熔断，并发邮件通知。
- **FR-5.5**: 系统必须返回标准 `429 Too Many Requests` 与 `Retry-After` header。

### FR-6 计费与支付（Billing & Payments）

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

### FR-7 内容安全（Content Safety）

- **FR-7.1**: 系统必须对**入参**（用户 prompt）执行敏感词过滤，命中后返回标准错误码 `400 content_filter` 并记录日志。
- **FR-7.2**: 系统必须对**出参**（模型回包）执行同等过滤；流式响应在终止前替换为脱敏内容。
- **FR-7.3**: 用户可在 API Key 维度配置过滤严格度：`strict` / `default` / `loose`（仅适用于敏感程度低的非中文场景）。
- **FR-7.4**: 误杀率（false positive）必须 < 1%；漏检率（false negative）< 0.1%。
- **FR-7.5**: 系统必须维护"敏感词治理日志"用于备案审计：每次拦截的请求 ID、用户 ID、命中词、动作（拦截/告警）。

### FR-8 多模态（Multi-Modal）

- **FR-8.1**: 系统必须支持图像理解 API（兼容 OpenAI vision 协议）：底层接入 Qwen-VL、GLM-4V。
- **FR-8.2**: 系统必须支持语音转文字（ASR）API：底层接入 Doubao ASR；兼容 OpenAI Whisper API 协议。
- **FR-8.3**: 系统必须支持文字转语音（TTS）API：底层接入 Doubao TTS；兼容 OpenAI TTS API 协议。
- **FR-8.4**: 视频理解作为 V1.1 目标，MVP 阶段提供占位接口返回 501 Not Implemented。

### FR-9 监控与日志（Observability）

- **FR-9.1**: 用户可在控制台查看**实时调用日志**（最近 1000 条），按时间倒序排列；支持按 Key / 模型 / 状态码过滤。
- **FR-9.2**: 历史日志保存 90 天，可下载 JSON / CSV。
- **FR-9.3**: 控制台必须提供"用量大盘"：今日 / 本月 / 季度的请求数、成功率、Token 消耗、消费金额；按模型与时间维度聚合。
- **FR-9.4**: 系统必须为每个请求分配唯一 `he_request_id`，覆盖整个生命周期（网关 → 适配器 → 上游 → 响应）。
- **FR-9.5**: 服务端必须暴露 OpenTelemetry trace + Prometheus metrics 给运维，但不向用户公开。

### FR-10 多语言 i18n

- **FR-10.1**: 控制台 UI 必须支持运行时切换以下 10 种语言：英语（en，默认）、简体中文（zh-CN）、繁体中文（zh-TW，V1.1）、日语（ja）、韩语（ko）、西班牙语（es）、法语（fr）、德语（de）、葡萄牙语（pt）、俄语（ru）、阿拉伯语（ar，含 RTL）。
- **FR-10.2**: 错误消息、邮件通知、PDF 账单必须按用户 locale 输出对应语言。
- **FR-10.3**: API 文档（Quickstart / API Reference / Cookbook）必须 10+ 语言齐全；翻译可借助机器翻译 + 人工校对。
- **FR-10.4**: 货币与时间格式按用户 locale 显示。

### FR-11 文档与 SDK（Developer Experience）

- **FR-11.1**: 必须提供至少 3 个官方 SDK 客户端：Python、Node.js / TypeScript、Go；每个 SDK 与 OpenAI 官方 SDK 接口一致（drop-in replacement）。
- **FR-11.2**: 必须提供交互式 Playground：用户在浏览器选模型、改参数、看响应、复制 cURL/SDK 代码。
- **FR-11.3**: 必须提供 6 家模型 vs GPT-4 / Claude / Gemini 的内置 benchmark 页面，公开质量、延迟、单价数据。

## 2.2 非功能需求（Non-Functional Requirements, NFR）

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
