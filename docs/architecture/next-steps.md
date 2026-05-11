# 16. 下一步（Next Steps）

## 16.1 立即行动

1. **业务方审阅本架构**，标注需要修订之处
2. **Architect 实地评估**：
   - 阿里云 / 腾讯云 region 与 instance 类型选型
   - PostgreSQL / Redis / ClickHouse 容量规划（基于 MVP 1k 用户假设）
   - 月度基础设施成本估算（建议 ≤ ¥30K / $4K 起）
3. **Tech Lead 拆分**：将本文 Epic 1-10 进一步拆分为 Sprint Backlog（每 2 周一个 Sprint）
4. **PO 接力**：执行 `*execute-checklist po-master-validation` 验证 PRD/Spec/Architecture 三方一致性
5. **法务对接**：基于 9.1 节合规架构，请法律意见书针对：
   - Cloudflare TLS 终端是否构成数据出境
   - 5 个支付通道分别的合规边界
   - 三件套备案时间线确认

## 16.2 与 PO 的接口

> PO 在 *execute-checklist 时需特别检查：
> - PRD 的 44 条 FR 是否全部对应到 Architecture 的服务/模块？
> - PRD 的 9 条 NFR 是否在 Architecture 中有具体实现方案？
> - Front-end Spec 的 15 个页面是否在 Architecture 的 console 模块内规划？
> - 4 项 Q&A 决策是否在三份文档中保持一致？

## 16.3 与 SM (Phase B) 的接口

> 进入开发阶段后，SM 基于本架构 + PRD 拆分故事时需要：
> - 每个故事关联到 Architecture 中的具体服务（apps/xxx）
> - 复杂度评分参考本架构的"服务边界" + "测试策略"
> - 验收标准引用本架构定义的 NFR 指标

---

> **架构 v1.0 终版**。等待业务方审阅。审阅通过后转交 PO 做 master validation 与 shard。
