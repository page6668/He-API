# 3. 用户界面设计目标（User Interface Design Goals）

## 3.1 整体视觉与品牌调性

- **设计语言**: 现代、简洁、技术感、国际化（避免过度"中国元素"，主要服务海外用户）
- **配色**: 深色为主（开发者偏好），亮色可切换；强调色使用蓝紫渐变（科技感 + 信任感）
- **字体**: 英文 Inter / 中文 PingFang / 阿拉伯文 Noto Sans Arabic；代码 JetBrains Mono
- **品牌定位关键词**: Reliable / Compliant / Global / Cost-Effective

## 3.2 关键交互范式

- **配置即文档**: 用户在控制台所做的每一项配置（Key / 限流 / 路由策略）都自动生成对应的 cURL / SDK 代码示例，方便复制
- **价格透明**: 任何调用前用户都能看到预估单价；调用后看到实际消耗
- **零障碍迁移**: 注册第一步即引导"如何把现有 OpenAI 代码迁移到 He-API"（修改 base_url + key 即可）

## 3.3 关键页面（MVP 范围）

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

## 3.4 响应式与可访问性

- **响应式**: 桌面优先（≥ 1280px），平板兼容（≥ 768px），手机仅可读不可编辑
- **可访问性**: WCAG 2.1 AA 级别；键盘可达；屏幕阅读器友好
- **暗黑模式**: 默认暗色，用户可切换亮色

---
