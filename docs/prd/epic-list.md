# 5. Epic 列表（Epic List）

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
