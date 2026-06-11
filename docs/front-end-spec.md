# He-API 前端规格（Front-End Specification）

> **China LLMs for the World** — UX 设计规范
>
> 文档版本: v1.0 | 状态: Draft | 输入依据: `docs/project-brief.md`、`docs/prd.md`
>
> 起草人: Yuri 代笔（原 UX Expert 智能体计成因 op-He-API 会话第 3 次 API socket 异常中断而无法完成）。基于 PRD 第 3 节 UI 设计目标 + 15 个关键页面 + 7 大差异化卖点起草。

---

## 1. 整体设计原则（Design Principles）

### 1.1 设计哲学

| 原则 | 说明 |
|------|------|
| **Developer-First** | 主要受众是海外开发者，设计语言走"工程师能直接看懂代码"的极简路线，避免营销腔 |
| **Trust by Transparency** | 每个数据点都有来源（"为什么这个模型最便宜？"→ 跳转单价对比） |
| **Zero-Friction Migration** | 任何一处提到 OpenAI 都附上"切换到 He-API 的 1 行代码差异" |
| **Compliance Made Invisible** | 中国合规复杂性对海外用户隐形，但合规白皮书随时可查 |
| **Global by Default** | 所有 UI 文案、错误消息、邮件、PDF 都按 locale 渲染；不假设英文为母语 |

### 1.2 品牌调性（Brand Tone）

- **关键词**: Reliable / Compliant / Global / Cost-Effective / Open
- **语调**: 直白、技术、可信赖；避免感叹号；不夸大；引用数据时给来源
- **避免**: 国旗符号、过度"中国元素"图腾（让产品力说话，不让政治审美干扰决策）

### 1.3 视觉系统

| 维度 | 规范 |
|------|------|
| **配色**（暗色为主） | 主色 `#6366F1`（Indigo 500，蓝紫渐变科技感） / 强调 `#22D3EE`（Cyan 400） / 背景 `#0B0F19` (Slate 950) / 卡片 `#1E293B` (Slate 800) / 边框 `#334155` (Slate 700) / 文本 `#F1F5F9` (Slate 100) / 次文本 `#94A3B8` (Slate 400) |
| **配色**（亮色） | 反转：背景 `#FFFFFF` / 卡片 `#F8FAFC` / 边框 `#E2E8F0` / 文本 `#0F172A` |
| **字体** | 英文 `Inter`（300/400/500/600/700）；CJK `PingFang SC` / `PingFang TC` / `Noto Sans CJK`；阿拉伯文 `Noto Sans Arabic`（RTL 友好）；代码 `JetBrains Mono` |
| **字号阶梯** | 12 / 14（默认正文） / 16 / 18 / 20 / 24 / 30 / 36 / 48 |
| **行高** | 正文 1.6 / 标题 1.3 / 代码 1.5 |
| **圆角** | 4px（small）/ 8px（默认）/ 12px（卡片）/ 16px（弹窗）/ 9999（pill） |
| **阴影** | `shadow-sm` / `shadow` / `shadow-md`（沿用 Tailwind 默认） |
| **间距尺度** | 4 / 8 / 12 / 16 / 20 / 24 / 32 / 48 / 64 / 96px |
| **图标** | Heroicons（outline 默认，solid 强调态）+ 模型厂商品牌 SVG |

### 1.4 核心组件库选型

- **UI 框架**: Tailwind CSS + shadcn-ui（基于 Radix UI 的 headless 组件，可定制）
- **图标**: lucide-react
- **图表**: Recharts（用量大盘）+ Apache ECharts（Benchmark 复杂可视化）
- **代码高亮**: Shiki
- **i18n**: next-intl（Next.js App Router 友好，支持服务端渲染翻译）
- **表单**: React Hook Form + Zod 校验
- **数据请求**: TanStack Query（旧名 React Query）+ orval/openapi-typescript 生成类型
- **状态**: Zustand（轻量全局状态）+ URL state（页签/筛选）

---

## 2. 信息架构（Information Architecture）

### 2.1 站点地图（Sitemap）

```
he-api.com (Marketing)
├── / (Landing)
├── /pricing
├── /models (公开 6 家模型介绍)
├── /benchmark (公开跑分)
├── /compliance (合规白皮书)
├── /blog
├── /about
└── /legal (Terms / Privacy / GDPR / SLA)

console.he-api.com (Authenticated)
├── /signin
├── /signup
├── /onboarding (3 步引导)
├── /dashboard (默认登录页)
├── /keys
├── /models (内部能力矩阵)
├── /routing
├── /logs
├── /billing
│   ├── /billing/balance
│   ├── /billing/recharge
│   ├── /billing/invoices
│   └── /billing/auto
├── /subscription
├── /team
├── /settings
│   ├── /settings/profile
│   ├── /settings/security
│   └── /settings/data (GDPR)
├── /playground
└── /benchmark (内部增强版，含 A/B 测试)

docs.he-api.com (公开文档站)
├── /quickstart
├── /api-reference
├── /cookbook
├── /sdk
│   ├── /sdk/python
│   ├── /sdk/typescript
│   └── /sdk/go
└── /changelog
```

### 2.2 全局导航（Global Navigation）

#### 顶部 Top Bar（已登录态）

| 区域 | 组件 |
|------|------|
| 左侧 | He-API logo（点击返回 /dashboard） + 当前 Team 切换器（V1） |
| 中央 | 主导航：Dashboard / API Keys / Models / Logs / Playground / Docs |
| 右侧 | Locale 切换器（10+ 语言）/ 主题切换（暗/亮）/ 余额徽章（点击跳 Billing）/ 用户头像菜单 |

#### Sidebar（部分页面用）

- Dashboard → 隐藏 sidebar
- Settings 系列 → 左侧 sidebar 列出 Profile / Security / Data
- Billing 系列 → 左侧 sidebar 列出 Balance / Recharge / Invoices / Auto Recharge

#### 移动端

- 顶部 Top Bar 折叠为汉堡菜单
- 主功能列表纵向排列
- **MVP 阶段移动端仅可读不可编辑**（编辑操作引导回桌面）

---

## 3. 用户流程（User Flows）

### 3.1 注册到首次成功调用流程（北极星流程）

> **目标**：5 分钟内完成（FR-1.1 / 用户成功指标）

```
[Landing] → [Sign Up]
              ├── 邮箱 + 密码（验证码邮件）
              └── Google / GitHub OAuth
                    ↓
[Onboarding 三步]
  Step 1/3: 选择 Locale + 主用例（聊天/代码/翻译/RAG）
  Step 2/3: 创建第一个 API Key（自动命名"My First Key"）
  Step 3/3: 复制 Quickstart 代码（含其 Key），一键 cURL 测试
                    ↓
[Demo 调用成功页]
  - 显示真实 token 用量
  - 提示："你刚刚消费了 $0.0023，免费额度还有 $4.9977"
  - CTA: 阅读完整文档 / 进入控制台 / 加入 Discord
```

**关键设计**：
- Step 3 默认提供 cURL，但有 tab 切换 Python / TypeScript / Go SDK 代码
- API Key 在 Step 2 创建时**仅在前端展示一次明文**（FR-5.2），同时给"复制"按钮 + 警告"妥善保存"
- 用户跳过 Onboarding 不阻塞进入 Dashboard，但下次登录后未完成的步骤会用 hint 提示

### 3.2 OpenAI 用户零代码迁移流程

```
[Dashboard 首屏]
  顶部条幅: "Migrating from OpenAI? Change one line."
  代码示例并排对比:
    OpenAI:                    He-API:
    base_url=                  base_url=
    "https://api.openai.com"   "https://api.he-api.com"
    api_key="sk-..."           api_key="he-..."
                    ↓
[Models 页]
  显示"OpenAI 协议兼容矩阵"，告诉用户哪些 OpenAI 模型可以无缝替换为哪个 He 模型
  例如：gpt-4o ↔ qwen-max / deepseek-v3（按 quality/cost/latency 评分）
                    ↓
[Playground]
  左侧贴 OpenAI 代码 → 右侧实时显示替换后的运行结果
  支持 export 为 SDK 代码
```

### 3.3 充值与多通道支付流程

```
[Billing → Recharge]
  选择金额: [$10] [$50] [$100] [$500] [Custom]
  选择币种: [USD ▼] (默认) / RMB
                    ↓
[选择支付通道]
  网格布局，5 个选项，每个含品牌 logo + 简短优劣提示
    ┌─────────┬─────────┬─────────┐
    │ Stripe  │ PayPal  │ USDC    │
    │ 信用卡  │ 国际钱包│ 加密货币│
    │ 即时    │ 即时    │ 5-10min │
    ├─────────┼─────────┼─────────┤
    │ Alipay+ │ WeChat  │         │
    │ 支付宝  │ 微信支付│         │
    │ 即时    │ 即时    │         │
    └─────────┴─────────┴─────────┘
                    ↓
[支付通道适配]
  Stripe → Stripe Checkout 重定向
  PayPal → PayPal Smart Buttons
  USDC → 显示 QR Code + 钱包地址 + 待确认状态
  Alipay+ → 显示二维码（手机扫）+ 待确认状态
  WeChat Pay → 显示二维码（手机扫）+ 待确认状态
                    ↓
[支付成功]
  - 余额立即更新（含动画过渡）
  - 邮件发送收据 + PDF 发票链接
  - CTA: 配置自动充值 / 返回 Dashboard
```

**关键设计**：
- 通道顺序按用户 locale 智能排序（中文 locale 优先 Alipay/WeChat，英文 locale 优先 Stripe）
- 任一通道失败时，UI 显示 "Try another payment method" 下拉切换，**不让用户重新输金额**
- USDC / Alipay+ / WeChat 的二维码扫描状态用 SSE 实时推送，避免轮询

### 3.4 智能路由配置流程

```
[Routing 页]
  当前默认策略: Quality (一目了然的卡片选择)
    ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
    │ Quality      │ │ Cost ✓       │ │ Latency      │
    │ 优选最高质量 │ │ 优选最低成本 │ │ 优选最低延迟 │
    │ 推荐生产级AI │ │ 推荐内部测试 │ │ 推荐流式聊天 │
    │              │ │              │ │              │
    │ 月省 $X      │ │ 月省 $Y ⭐   │ │ P95 ↓ Zms    │
    └──────────────┘ └──────────────┘ └──────────────┘

  下方说明: "你也可以在请求 header 中临时覆盖：X-He-Routing-Strategy: latency"

  高级配置（折叠）:
    - 模型黑白名单
    - failover 阈值（默认 3 次失败）
    - A/B 模式开启（指定两个对比模型）
                    ↓
[保存生效] 即时生效，下次调用按新策略
```

### 3.5 GDPR 数据导出流程

```
[Settings → Data]
  说明: "你可以下载或删除你在 He-API 的所有数据。"

  [Export My Data] 按钮
                    ↓
[Confirm Export]
  弹窗确认: "我们将打包以下内容：账户信息 / API Keys 元数据 / 调用日志（90 天）/ 账单"
                    ↓
[处理中]
  显示"正在生成数据包，处理完成后会发送邮件下载链接（约 5-30 分钟）"
                    ↓
[邮件] 用户邮箱收到带签名 URL 的下载链接（24h 有效）

[Delete My Account]
  弹窗确认 + 二次密码验证
                    ↓
[宽限期] 30 天内可登录撤销；30 天后物理删除
```

### 3.6 内容安全错误反馈流程

```
[用户调用 API 含敏感词]
                    ↓
[网关返回 400 + content_filter]
                    ↓
[SDK 抛 ContentFilterException]
                    ↓
[用户在 Logs 页查看]
  - 红色高亮显示该请求
  - 命中规则: "violence" (示例)
  - 提供"申诉"按钮 → 跳转至工单系统
                    ↓
[Settings → API Key 配置严格度]
  默认 / Strict / Loose 切换；带说明 "Strict 推荐用于面向终端用户的应用"
```

---

## 4. 关键页面线框图（Wireframes）

> 以下为页面结构与关键组件描述（文字 wireframe）。最终视觉设计由 Designer 在 Figma 完成。

### P-1 Landing（营销首页）

```
┌──────────────────────────────────────────────────────────────┐
│ [He-API logo]  Models  Pricing  Benchmark  Docs   [Sign In] │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│   Stripe for Chinese LLMs                                    │
│   Use DeepSeek, Qwen, Kimi, GLM through                      │
│   one OpenAI-compatible endpoint.                            │
│                                                              │
│   [Get Started — Free $5 credit] [Read Docs]                 │
│                                                              │
│   ┌───────────────────── code preview ────────────────────┐ │
│   │  # Just change base_url. That's it.                    │ │
│   │  client = OpenAI(                                      │ │
│   │    base_url="https://api.he-api.com/v1",               │ │
│   │    api_key="he-..."                                    │ │
│   │  )                                                     │ │
│   └────────────────────────────────────────────────────────┘ │
│                                                              │
│   Trusted by [logos: 10 海外 SaaS / 研究机构]                │
│                                                              │
├──────────────────────────────────────────────────────────────┤
│  6 Top Chinese LLMs in one API (model logos)                 │
│  Multi-currency, multi-channel payments                      │
│  Built for global developers (10+ languages)                 │
│  Smart routing: Quality, Cost, Latency                       │
│  Compliance baked in (CN regulatory framework)               │
├──────────────────────────────────────────────────────────────┤
│  Live benchmark widget (今日 benchmark 摘要 + 跳转)          │
│  Customer testimonial 1-3 个                                 │
│  Pricing 区块（含 4 个订阅档对比）                           │
│  CTA: Get started in under 5 minutes                         │
└──────────────────────────────────────────────────────────────┘
```

**关键 CTA**: "Get Started — Free $5 credit" 双倍出现（首屏 + 底部）。
**社会证明**: Logos + Testimonials + Live benchmark。
**SEO**: 单独 `/pricing` `/benchmark` `/models` 子页面，便于搜索引擎索引。

### P-3 Onboarding 三步引导

```
Step 1/3: Personalize ──────●─────○─────○
┌──────────────────────────────────────────┐
│  Welcome to He-API                       │
│                                          │
│  Preferred language:                     │
│  [English ▼] (10+ languages available)   │
│                                          │
│  Primary use case (helps us recommend):  │
│  ○ Chat / Conversation                   │
│  ○ Code generation                       │
│  ○ Translation                           │
│  ○ RAG / Search                          │
│  ○ Other                                 │
│                                          │
│  [Continue →]              Skip for now  │
└──────────────────────────────────────────┘

Step 2/3: Create your first API Key ──○─●──○
┌──────────────────────────────────────────┐
│  Name: [My First Key______________]      │
│  Scope: [All models ▼]                   │
│                                          │
│  ⚡ Generated:                           │
│  he-xxxxxxxxxxxxxxxxxxxxxxxxxxxx        │
│  [📋 Copy]  ⚠ This is shown only once.   │
│                                          │
│  [Continue →]                            │
└──────────────────────────────────────────┘

Step 3/3: Try it now ──○──○──●
┌──────────────────────────────────────────┐
│  Run this in your terminal:              │
│  ┌─────────[ cURL | Python | TS | Go ]──┐│
│  │ curl https://api.he-api.com/v1/...   ││
│  │   -H "Authorization: Bearer he-..."  ││
│  │   -d '{"model":"deepseek-v3", ...}' ││
│  │ [📋 Copy]                            ││
│  └──────────────────────────────────────┘│
│                                          │
│  Or click [Run in Playground →]          │
│  to test in your browser.                │
│                                          │
│  [Finish →]                              │
└──────────────────────────────────────────┘

Done!
┌──────────────────────────────────────────┐
│  ✅ You made your first call!            │
│  Cost: $0.0023 (free credit: $4.9977)    │
│                                          │
│  [Open Dashboard]  [Read full docs]      │
└──────────────────────────────────────────┘
```

### P-4 Dashboard

```
┌──────────────────────────────────────────────────────────────┐
│ Welcome back, {name}                          [+ New Key]    │
├──────────────────────────────────────────────────────────────┤
│ ┌─Balance─┐ ┌─Today──┐ ┌─Month──┐ ┌─Avg Latency─┐           │
│ │ $42.18  │ │ 1,234  │ │ 24,890 │ │ 380ms (P95) │           │
│ │ [+ Add] │ │ calls  │ │ calls  │ │             │           │
│ └─────────┘ └────────┘ └────────┘ └─────────────┘           │
├──────────────────────────────────────────────────────────────┤
│ Usage trend (last 30 days, line chart)                       │
│  - Toggle by Model / By Status / By Day                      │
├──────────────────────────────────────────────────────────────┤
│ Recent Logs (top 10) ─────────────  [View all logs →]       │
│ Time      Model          Status   Tokens   Latency           │
│ 12:01    qwen-max         200    1,234     420ms             │
│ 11:58    deepseek-v3      200    980       310ms             │
│ ...                                                          │
├──────────────────────────────────────────────────────────────┤
│ Migrating from OpenAI? Change one line. [Show me how →]     │
└──────────────────────────────────────────────────────────────┘
```

### P-13 Playground

```
┌──────────────────────────────────────────────────────────────┐
│ Playground                                                   │
│                                                              │
│ [Model: qwen-max ▼]  [Temperature: 0.7]  [Max tokens: 2048] │
│ [+ Compare A/B]                                              │
│                                                              │
│ ┌─System──────────────┐                                      │
│ │ You are a helpful   │                                      │
│ │ assistant.          │                                      │
│ └─────────────────────┘                                      │
│                                                              │
│ ┌─User────────────────┐ ┌─Output──────────────┐              │
│ │ Hello! Tell me a    │ │ Sure! Here's a fun  │              │
│ │ fun fact about      │ │ fact about pandas...│              │
│ │ pandas.             │ │                     │              │
│ │                     │ │ [Streaming...]      │              │
│ └─────────────────────┘ └─────────────────────┘              │
│                                                              │
│ Tokens: 87 in / 130 out  Cost: $0.0012  Latency: 480ms       │
│                                                              │
│ [Export as: cURL | Python | TypeScript | Go]                 │
└──────────────────────────────────────────────────────────────┘
```

**A/B 模式开启时**: 输出区域分为两栏，并排显示两个模型的回包，底部对比 cost / latency / quality 评分。

### P-14 Benchmark

```
┌──────────────────────────────────────────────────────────────┐
│ Benchmark: Chinese LLMs vs Western                           │
│                                                              │
│ Filter: [All tasks ▼] [Languages: en + zh ▼]  [Last 7 days ▼]│
│                                                              │
│ ┌──────────────────────────────────────────────────────────┐ │
│ │ Quality Score (Higher is better)                         │ │
│ │                                                          │ │
│ │ deepseek-v3   ████████████████ 87.3                      │ │
│ │ qwen-max      ███████████████  85.1                      │ │
│ │ gpt-4o        ███████████████  85.0                      │ │
│ │ claude-sonnet ██████████████   84.2                      │ │
│ │ glm-4         █████████████    81.6                      │ │
│ │ kimi          █████████████    80.9                      │ │
│ └──────────────────────────────────────────────────────────┘ │
│                                                              │
│ Cost per 1M tokens (Lower is better) - bar chart             │
│ Latency P95 (Lower is better) - bar chart                    │
│                                                              │
│ Methodology: [Click to expand]                               │
│ Last updated: 2026-05-09 03:00 UTC                           │
│ Run your own benchmark: [Open A/B in Playground]             │
└──────────────────────────────────────────────────────────────┘
```

### P-9 Billing

```
┌──────────────────────────────────────────────────────────────┐
│ Billing                                                      │
├──────────────────────────────────────────────────────────────┤
│ Current Balance: $42.18 USD                                  │
│ [Add credit]   Auto-recharge: Off [Configure →]              │
├──────────────────────────────────────────────────────────────┤
│ Subscription: Pro ($29/mo)   [Manage →]                      │
│ Next billing: 2026-06-09                                     │
├──────────────────────────────────────────────────────────────┤
│ This month                                                   │
│ Tokens consumed: 24.8M    Cost: $87.40                       │
│                                                              │
│ Breakdown by model (table, sortable)                         │
│ Model         Tokens     Cost    % of total                  │
│ qwen-max      8.2M      $30.10   34%                         │
│ deepseek-v3   6.1M      $19.50   22%                         │
│ ...                                                          │
├──────────────────────────────────────────────────────────────┤
│ Recent invoices                                              │
│ Date       Amount   Method        Status   [Download PDF]    │
│ 2026-04-09 $50.00   Stripe        Paid     [⬇]               │
│ 2026-03-09 $30.00   Alipay+       Paid     [⬇]               │
└──────────────────────────────────────────────────────────────┘
```

### P-7 Routing

参见 3.4 用户流程章节。

### P-5 API Keys

```
┌──────────────────────────────────────────────────────────────┐
│ API Keys                                          [+ New Key]│
├──────────────────────────────────────────────────────────────┤
│ Name           Scope        Created    Last used    Actions  │
│ My First Key   All models   2026-05-08 5min ago    [⚙][⊗]   │
│ Production     deepseek-v3  2026-04-12 just now    [⚙][⊗]   │
│ Sandbox Test   qwen-max     2026-05-01 3 days ago  [⚙][⊗]   │
└──────────────────────────────────────────────────────────────┘

[New Key Modal]
┌──────────────────────────────────────────┐
│ Create API Key                           │
│                                          │
│ Name: [_____________________]            │
│ Scope: [All models ▼] / Custom          │
│ IP whitelist (optional):                 │
│   [_______________] [+ Add another]      │
│ Monthly cost cap: [$________] USD        │
│                                          │
│ [Create]               [Cancel]          │
└──────────────────────────────────────────┘
```

### P-8 Logs

```
┌──────────────────────────────────────────────────────────────┐
│ Logs                                            [Download ⬇] │
├──────────────────────────────────────────────────────────────┤
│ Filters:                                                     │
│ [Last 24h ▼] [All keys ▼] [All models ▼] [All status ▼]      │
├──────────────────────────────────────────────────────────────┤
│ Time      Key       Model        Status  Tokens   Latency   │
│ 12:01:23  prod      qwen-max     200     1,234    420ms     │
│ 12:01:18  prod      deepseek-v3  200     980      310ms     │
│ 11:58:45  test      kimi-32k     400     0        12ms ⚠    │
│   └─ [Expand: full request/response, error trace]            │
│ ...                                                          │
└──────────────────────────────────────────────────────────────┘
```

每行点击展开 detail panel：full request / response / token breakdown / cost / he_request_id（用于客服）。

> **Story 9.2 实现说明（MVP 子集）**：`/{locale}/logs` 页（Server Component + `getUsageLogs` Server Action → 网关 `GET /v1/me/usage/logs`）落地本线框的**过滤 + 分页表格**部分。列 = Time / Model / Status / Streaming / Tokens / Latency / API Key / Request ID（8 列，`<th scope>` 语义表格）。过滤栏 = Model / Status / Streaming / 时间范围 / 每页（filter+pagination 反映在 URL query，可分享/back-safe，BR-UI-3），分页走 ≤1000 条最近窗口（Prev/Next，has_more）。状态以 **文本+图标**呈现（非仅颜色，WCAG 1.4.1）。**故意省略**：① **cost / 消费 列**（per-request 成本 non-authoritative，H-1-R / BR-UI-4，余额消费见 /dashboard 与 usage_ledger）；② 行展开 detail panel + Download（Download 属 Story 9.3 历史下载范围）。i18n `logs` namespace ×10 + RTL（时间戳/ID/数字 cell 保持 LTR）。

> **Story 9.3 实现说明（历史日志下载）**：`/{locale}/logs` 页顶部新增**导出控件**（`LogExportDialog`，section 而非 modal）：格式单选（JSON / CSV，radiogroup，格式码恒 LTR）+ 导出按钮（aria-label + disabled 态）。Server Action `request-log-export.ts` POST 网关 `POST /v1/me/usage/logs/export`（cookie 透传，`cache:'no-store'`，判别联合 ok|unauthorized|rate_limited|error）；`get-current-log-export.ts` 读 `GET .../export/current` 驱动 CTA-disable（pending|processing 时禁用 + "完成后邮件发送链接"状态行，BR-UI-3）。完成态 → "链接已邮件发送，{expiry} 过期"横幅（**控制台从不渲染原始签名 URL** — 链接仅邮件投递，BR-UI-3 / TS-CONS-008）；失败态 → 重新启用 + 重试。状态以**文本+图标**呈现（非仅颜色，WCAG 2.1 AA）。i18n `logs.export.*` ×10（en+zh-CN 真实，8 `[en-pending]`）；`/ar` RTL 镜像，格式码/时间戳保持 LTR。

### P-11 Settings

子页面通过左侧 sidebar 切换：
- **Profile**: 昵称 / 头像 / locale / 时区 / 默认 UI 语言
- **Security**: 修改密码 / 启用 2FA / Active sessions（可踢出）
- **Data (GDPR)**: Export my data / Delete my account（参见 3.5 流程）

### P-15 Docs（独立站 docs.he-api.com）

```
┌──────────────────────────────────────────────────────────────┐
│ He-API Docs                          [Search ⌘K] [Sign in]   │
├────────────┬─────────────────────────────────────────────────┤
│ Sidebar    │  Quickstart                                     │
│ - Quickstart│                                                 │
│ - Migration│  Get started in under 5 minutes...              │
│   from OpenAI│                                              │
│ - API Reference│  1. Sign up at console.he-api.com         │
│ - Cookbook  │  2. Create your first API Key                  │
│ - SDKs      │  3. Run this code...                           │
│   - Python  │                                                │
│   - TS      │  ```python                                     │
│   - Go      │  from openai import OpenAI                     │
│ - Models    │  client = OpenAI(                              │
│ - Errors    │    base_url="...",                             │
│ - Changelog │    api_key="he-..."                            │
│             │  )                                              │
│             │  ```                                            │
│             │  [Run in Playground →]                          │
└────────────┴─────────────────────────────────────────────────┘
```

**关键**: 每个代码块右上角有 "Run in Playground" 按钮，把代码自动塞进 Playground。

---

## 5. 组件库（Component Library）

### 5.1 基础组件（基于 shadcn-ui 扩展）

| 组件 | 说明 |
|------|------|
| Button | primary / secondary / ghost / destructive；含 loading 态 |
| Input | text / email / number / password；含 prefix/suffix slot |
| Select | 单选/多选；可搜索 |
| Modal / Dialog | 默认 medium；支持 small / large；ESC 关闭 |
| Toast | success / error / warn / info；自动 4s 消失 |
| Tabs | 横向 / 纵向；URL state 同步 |
| Card | 默认带 1px 边框 + 8px 圆角 |
| Badge | counter / status；多色（success/warn/error/info/neutral） |
| Avatar | 用户头像；可堆叠（Team 成员） |
| Skeleton | 加载占位；按页面布局 |
| EmptyState | 通用空数据；含图标 + 文案 + CTA |

### 5.2 业务组件（He-API 专属）

| 组件 | 用途 |
|------|------|
| `<ModelChip>` | 显示模型品牌图标 + 名称 + 价格 tag |
| `<RoutingStrategyCard>` | Routing 页 3 张策略卡 |
| `<PaymentMethodGrid>` | Recharge 页 5 通道网格 |
| `<UsageChart>` | Dashboard 用量趋势图（基于 Recharts） |
| `<CodeSnippet>` | 多语言代码块 + 一键复制 + Run in Playground |
| `<ApiKeyDisplay>` | 一次性明文展示 + 自动 mask + 复制 — **REALIZED by Story 5.5** (`apps/console/components/business/ApiKeyDisplay.tsx`; mask/reveal/copy/confirm + one-shot aria-live + plaintext discipline BR-PD-1..7) |
| `<BenchmarkBar>` | Benchmark 页评分柱状图 |
| `<BalancePill>` | 顶栏余额徽章；点击跳 Billing |
| `<LocaleSwitch>` | 顶栏语言切换；下拉显示原生语言名（English / 中文 / 日本語 / 한국어 / Español / Français / Deutsch / Português / Русский / العربية） |
| `<ThemeToggle>` | 顶栏主题切换 |
| `<DiffViewer>` | A/B 模式对比两个模型回包 |
| `<RTLProvider>` | 阿拉伯文 RTL 容器；自动反转 flex 方向 |

### 5.3 表单设计规范

- **校验时机**: blur 时校验单字段；submit 时校验全表单
- **错误提示**: 字段下方 12px，红色文本；`role="alert"` 用于屏幕阅读器
- **必填标记**: 字段名后 `*` 红色
- **禁用态**: 0.5 透明度 + 不响应；hover 时显示 tooltip 解释
- **加载态**: 按钮显示 spinner；其他字段保持可见但禁用

---

## 6. 国际化设计（i18n）

### 6.1 多语言支持

| Locale | 显示名 | 状态 |
|--------|--------|------|
| en | English | MVP 默认，主要文档语言 |
| zh-CN | 简体中文 | MVP 必备 |
| ja | 日本語 | MVP 必备（日本市场重要） |
| ko | 한국어 | MVP 必备 |
| es | Español | MVP 必备（拉美 + 西班牙） |
| fr | Français | MVP 必备 |
| de | Deutsch | MVP 必备 |
| pt | Português | MVP 必备（巴西） |
| ru | Русский | MVP 必备 |
| ar | العربية | MVP 必备（**RTL**） |
| zh-TW | 繁體中文 | V1.1 |
| hi | हिन्दी | V1.1 |
| id | Bahasa Indonesia | V1.1 |

### 6.2 i18n 实现要点

- **运行时切换**: 改 locale 后页面立即刷新内容（不需要重载）
- **服务端渲染**: 关键 SEO 页（Landing / Docs）按 locale 走 SSG
- **资源文件**: JSON 格式，按 namespace 拆分（auth.json / billing.json / dashboard.json …）
- **复数与变量**: 使用 ICU MessageFormat（`{count, plural, one {1 key} other {# keys}}`）
- **日期/时间/数字/货币**: 用 `Intl.DateTimeFormat` / `Intl.NumberFormat`，按 locale 自动格式化
- **缺失翻译回退**: 顺序为 user locale → 同语言变体（zh-TW → zh-CN）→ en
- **RTL 支持**: 阿拉伯文激活时整个文档 `dir="rtl"`；图标方向自动镜像（如返回箭头）
- **测试**: 长文本 locale（德语单词偏长）必须不破坏布局；RTL 必须人工检查

---

## 7. 可访问性（Accessibility）

### 7.1 WCAG 2.1 AA 目标

| 维度 | 要求 |
|------|------|
| **对比度** | 文本 4.5:1，大文本 3:1（暗色与亮色主题均验证） |
| **键盘导航** | 所有交互元素 `Tab` 可达；视觉 focus ring 明显（2px solid + 颜色） |
| **屏幕阅读器** | 语义化 HTML；表单 `<label>`；`aria-label` / `aria-describedby` 补充 |
| **Live region** | Toast / 余额变化 / 流式响应使用 `aria-live="polite"` |
| **跳过导航** | 顶部首屏有 "Skip to main content" 跳转链 |
| **图片替代** | 所有 `<img>` 含 `alt`；装饰性图片 `alt=""` |
| **色彩独立性** | 状态不仅靠颜色（成功/失败/警告均带图标 + 文字） |
| **可缩放** | 200% 字体放大不破坏布局 |

### 7.2 关键场景

- **API Key 复制**: 复制成功有 aria-live 通知"Copied to clipboard"
- **流式响应**: 屏幕阅读器只读完整段落，不读每个 token
- **支付二维码**: 提供 alt 文本 + URL 文本备选
- **错误提示**: 表单错误自动 focus 到第一个错误字段

---

## 8. 响应式设计（Responsive Design）

### 8.1 断点

| Name | Min Width | 用途 |
|------|-----------|------|
| `sm` | 640px | 大手机 / 小平板（可读） |
| `md` | 768px | 平板（基础编辑） |
| `lg` | 1024px | 桌面（默认设计基线） |
| `xl` | 1280px | 大桌面 |
| `2xl` | 1536px | 超宽屏（最大宽度限制） |

### 8.2 移动端策略（MVP）

- **可读不可编辑**：移动端可查看 Dashboard / Logs / Balance / Subscription，但创建 Key / 修改配置 / 充值等编辑动作引导回桌面（含 deeplink 推送邮件）
- **手机看 docs**: 文档站完全响应式，移动端友好
- **手机看 Landing**: 营销首页移动端首屏 CTA 显眼

### 8.3 V1.1 移动端目标

V1.1 阶段会补齐 PWA：充值 / Key 管理 / 个人资料修改 移动端可用。

---

## 9. 动画与性能（Animation & Performance）

### 9.1 动画原则

- **目的**: 反馈用户操作（按钮 hover）/ 引导注意力（toast 出现）/ 平滑过渡（页面切换）
- **避免**: 装饰性动画分散注意力；不必要的 hover 弹跳
- **持续时间**: 100-300ms（短）/ 400-500ms（页面过渡）；> 500ms 必须提供加载指示
- **缓动**: `cubic-bezier(0.4, 0, 0.2, 1)`（Material 标准）
- **可关闭**: `prefers-reduced-motion: reduce` 用户禁用所有非必要动画

### 9.2 性能预算

| 指标 | 目标 |
|------|------|
| LCP (Largest Contentful Paint) | < 2.5s（3G 网络） |
| CLS (Cumulative Layout Shift) | < 0.1 |
| INP (Interaction to Next Paint) | < 200ms |
| Bundle (gzip) | 控制台 < 250KB / 首屏 |
| 图片 | 按 viewport 自动 srcset；WebP 优先 |
| 代码分割 | 路由级 + 重型组件懒加载（如 Recharts） |
| Service Worker | 暂不上（V1.1 PWA 时启用） |

---

## 10. 设计交付（Design Handoff）

### 10.1 Figma 文件结构

```
He-API Design System
├── 01 - Foundations (颜色 / 字体 / 间距 / 圆角 / 阴影)
├── 02 - Components (基础组件库)
├── 03 - Business Components (He 专属)
├── 04 - Pages
│   ├── Marketing (Landing / Pricing / Models / Benchmark)
│   ├── Auth (Sign In / Sign Up / Onboarding)
│   ├── Console (Dashboard / Keys / Routing / Logs / Billing / Settings)
│   ├── Playground & Benchmark
│   └── Docs
├── 05 - Mobile (响应式预览)
└── 06 - Dark / Light variants
```

### 10.2 切图与资源

- 所有图标 24x24 lucide-react SVG，颜色继承
- 模型厂商品牌 logo 单独维护（svg 文件夹）
- 国旗图标采用 emoji（统一中性，不引战）

### 10.3 与开发协同

- Figma → Design tokens（JSON）→ Tailwind config 同步
- 设计稿组件命名严格对应 React 组件命名
- 重要交互附 Lottie / 视频原型，避免歧义

---

## 11. 关键决策记录（Design Decisions Log）

| ID | 决策 | 备选方案 | 理由 |
|----|------|---------|------|
| D-1 | 暗色为默认 | 亮色为默认 | 海外开发者强偏好暗色 |
| D-2 | 无国旗图标 | 国旗 + 语言名 | 中性、不引政治情绪；语言切换器只显示原生语言名 |
| D-3 | 5 通道支付平铺 | 折叠为 2 个主推 | "全通道兜底"是核心差异化（Q3 决策），平铺让用户看到选择多 |
| D-4 | OpenAI 协议在首屏强调 | 仅在文档强调 | 零代码迁移是最强卖点，营销首屏即必须出现 |
| D-5 | 移动端 MVP 仅可读 | 响应式全编辑 | 时间紧张，集中桌面体验；移动 PWA 推到 V1.1 |
| D-6 | shadcn-ui + Tailwind | Material UI / Chakra | 灵活定制 + 开发者审美友好 |
| D-7 | 文档站独立部署 | 内嵌控制台 | SEO 独立 + 公开访问无需登录 |
| D-8 | Playground 内置 A/B 模式 | 独立页面 | 与 Routing 决策结合，让用户直观感受路由策略价值 |

---

## 12. 待办（Open Questions for Design）

- [ ] **D-Q1**: He-API logo 设计（外部 design agency / Fiverr）
- [ ] **D-Q2**: 6 家中国模型品牌 logo 是否需要授权使用？（建议法务确认）
- [ ] **D-Q3**: Onboarding Step 3 的 Demo cURL 是否需要提供"无 Key 也可玩"的限速版？（提升转化但增加滥用风险）
- [ ] **D-Q4**: Billing 页是否需要显示"如果你换成另一个支付通道可以省 $X 手续费"的提示？（透明 vs 复杂度）
- [ ] **D-Q5**: 阿拉伯文 RTL 是否需要找母语 reviewer 验证？（外包可行）
- [ ] **D-Q6**: 流式响应中如何展示成本/token 实时增长？（潜在 jank 风险）

---

## 13. 下一步（Next Steps）

### 13.1 立即行动

1. **业务方审阅本前端规格**，标注需要修订之处
2. **设计师启动**：基于本规格在 Figma 制作高保真稿（含所有 P-1 ~ P-15 关键页面 + 暗/亮主题 + 移动端预览）
3. **Architect 接力**：基于 PRD + 本前端规格，生成 `docs/architecture.md`，确保前端技术选型（Next.js + i18n + shadcn-ui）与后端兼容
4. **i18n 团队启动**：开始翻译资源文件（10 种语言初版）
5. **logo 与品牌 asset**：外包 design agency 或 Fiverr，2 周内交付

### 13.2 与 Architect 接口

> Architect 设计架构时需明确以下前端依赖：
> - SSR / SSG / CSR 在哪些页面采用（推荐：Landing/Docs 用 SSG，Console 用 CSR + 服务端 API）
> - 前端如何获取 i18n 资源（推荐：编译时打包静态文件 + CDN 分发）
> - 流式响应（SSE）的客户端实现（推荐：原生 fetch + ReadableStream）
> - 客户端 OAuth callback 的安全处理（推荐：PKCE flow）
> - 二维码扫描状态实时推送（推荐：SSE 或 WebSocket）

### 13.3 PO Validation 提示

> PO 在 *execute-checklist po-master-validation 时需特别检查：
> - 前端规格的 15 个关键页面是否覆盖 PRD 的所有 FR / Epic 故事？
> - i18n 10 种语言是否覆盖目标市场？
> - 移动端 MVP 仅可读策略是否符合"越快越好"目标？

---

> **前端规格 v1.0 终版**。等待业务方审阅。审阅通过后转交 Architect。
