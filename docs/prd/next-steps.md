# 8. 下一步（Next Steps）

## 8.1 立即行动项

1. **业务方 PRD 审阅**: 检查 FR / NFR / Epic 列表是否符合预期；标注需要修订之处
2. **UX Expert 接力**: 基于 PRD 第 3 节（UI 设计目标）+ Epic 列表中的页面（P-1 到 P-15），生成 `docs/front-end-spec.md`
3. **Architect 接力**: 基于 PRD 第 4 节（技术假设）+ FR / NFR，生成 `docs/architecture.md`（含详细服务拓扑、数据库 schema、API 详细规范、部署方案）
4. **PO 接力**: 执行 `*execute-checklist po-master-validation` 验证 PRD 一致性，再 `*shard` 拆分给 SM 用于故事创建
5. **并行启动法律意见书**: 数据出境合规边界 + 国际支付主体结构（前置阻塞项）
6. **并行启动备案准备**: ICP / 算法 / 生成式 AI 备案三件套材料收集

## 8.2 PM 交接说明

> 本 PRD 由 Yuri（协调者）代笔起草。原 PM 智能体（范蠡）因 op-He-API 会话内的 Claude Code Anthropic API socket 连接两次异常中断而无法完成。Yuri 基于 `docs/project-brief.md` 的 345 行完整上下文 + 业务方对 4 项关键问题的明确回答（Q1 混合定价 / Q2 8 周激进 Beta / Q3 全通道并行 / Q4 6 模型），按 PRD 标准结构产出本文档。如需 PM 智能体重新审阅或润色，可在网络稳定后通过 `/o pm` 加载 PM，让其执行 `*revise-prd` 命令对本文档做修订。

## 8.3 UX Expert 提示词模板（供参考）

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

## 8.4 Architect 提示词模板（供参考）

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
