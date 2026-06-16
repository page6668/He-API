# He-API 交付与流程复盘(2026-06-16)

> 范围:迭代 2(Epic 6.4 起)→ 全 10 Epic 收官 + 最终体检 + Phase C 冒烟。
> 数据源:全部 story 文档、QA gate、`docs/qa/evidence/smoke-test-epic-*.md`、git 历史。
> 结论性、数据驱动;判断只基于实际记录。

## 1. 交付总览
- **10 Epic / 31 故事全部交付**(29 迭代2 + 缺口补齐 2.7、6.5),QA gate 全 PASS。
- 最终体检:Go 22 模块 build+test、TS SDK、console typecheck、console 单测(411)、i18n-keys 全绿;修复 1 个 i18n codegen 漂移(`9479b4a`)。
- **Phase C 冒烟 10/10 Epic PASS**;唯一遗留为环境门控的 staging 验证项(见 `ga-readiness-checklist.md`)。

---

## 2. 架构师 Review 的价值(数据)
49/57 故事做了架构师 review,8 个按 Decision-8A 主动跳过(纯增量/已验证 seam)。**8 个被打回 RequiresRevision(真返工)**:3.3 · 3.4 · 3.6 · 5.2 · 5.3 · 6.1 · 9.1 · 9.5,另 ~24 个"批准带强制修改项"。

**它实际拦下的、QA/Dev 下游才会撞的硬伤(可对照代码验证)**:
- 3.4 `streaming`↔`handlers` 包循环导入(Go 拒绝编译);5.2 SQL 写不存在的列;5.3 依赖不存在的字段 `scope.rate_limits`;7.1 float64 计费精度陷阱;8.1 安全 CI 门"永不运行";8.4/9.5 proto 字段号线协议冲突;9.3 增量列致 GDPR 导出回归;6.1/9.5 Tasks/Dev Notes 整段空。

**结论:实质审查,非盖章。** 评分校准真实(5.0–10.0,低分都含 Critical),会推翻自己(9.7/9.6 二轮自我纠正),验证故事断言 vs 真实代码库。**核心洞见:强模型照样高频"幻觉代码库现状"(errgroup 已在 go.mod / fpdf 是 workspace 依赖 / scope 有 rate_limits —— 全是假的),架构师 review = 第二个模型在写代码前拿计划对真实代码对账。** 水分:单作者(Wright)、部分 Low/Medium 是行号漂移。
**建议:保留但分级**——火力留给 schema/proto/钱/安全/跨服务/首创;纯增量复制可跳过(Decision-8A 已在做)。

---

## 3. Test-design 的价值(数据)
**56/57 故事都做了 test-design**(仅 1.1 脚手架没做)—— 本项目**没有"不做 test-design"的对照组**,无法 A/B。但有强力间接信号:

需多轮 QA 的 13 个故事中,**几乎每一个第 1 轮失败都精确命中一个 test-design 早已设计的 P0 场景,是 Dev 没实现它**:
- 2.4 2FA:`DisableTOTP` 非原子 + JTI TOCTOU 竞态(2FA 可绕过)= P0 `BLIND-CONCURRENCY-001`
- 9.4 trace:服务间 `http.DefaultClient` 致 trace 断链 = P0 `UNIT-009`/`INT-002`
- 4.1 适配器:request-id / token 用量跨服务一致性 = P0 `BLIND-DATA-001/002`
- 2.6 GDPR:生产 worker NoOp 空实现 + PII 守卫没测 = P0 `E2E-001`/`UNIT-028`
- 5.3/5.4:整个 chaos/集成层缺失 + 流式 TPM 半实现 = `CHAOS-001..005` 强制场景

**结论:test-design 的价值不是让首轮过,而是定义了那条会抓住 Dev 盲区的及格线**(安全竞态/跨服务不变量/混沌)。没有它,这些 P0 很可能根本不会被测 → 带病上线。test-design(立标)+ QA review(强制执行)是互补闭环。
**反向洞见:多轮成本一大块不是 test-design 的锅,是流程卫生**——unticked 复选框在 2.4/3.2/4.5/4.6/5.3/5.4/7.8/9.7 反复触发返工(4.5/4.6 二轮纯勾框零代码)。真正"QA 现场新发现、test-design 没预料"的极少(9.7 spec 自相矛盾、9.4/9.6 架构裁决)。

---

## 4. Phase C 的价值:抓住逐故事 QA 抓不到的"整故事缺失"
Phase C epic 级冒烟发现 **2 个真实范围缺口**,均在 epic 边界/"提前宣告完成"时漏掉:
- **2.7 账号注销/数据删除(GDPR Art.17 被遗忘权,合规级)** —— epic-2 yaml 声明但从未起草
- **6.5 路由策略配置 UI** —— epic-6 yaml 声明(estimated_stories:5)但只做了 6.1–6.4

**为何逐故事 QA 抓不到:没起草的故事没有 QA gate。** 只有 epic 级冒烟把"PRD 声明的故事数 vs 实际交付"对齐才能发现。两个均已补齐(`5a90523`、`b5a51af`)、QA 过、Epic 2/6 冒烟复测 PASS。
**根因:迭代 2 启动时把 Epic 6 当成"6.4 即完结"**(iteration-scope 从 Epic 7 起),6.5 / 2.7 在边界丢失。
**建议:每个 epic 收尾时机械核对 `estimated_stories`/声明 id vs 实际 story 文件数**,作为 SM/PO 的 epic-complete 签收门(本复盘的核对脚本可固化进 CI)。

---

## 5. 自动化编排的工程教训(本次会话实测)
代码质量高、跑得快;**瓶颈是基础设施而非模型**:
1. **HANDOFF 丢失**:自指 handoff 被同窗口保护拦掉 / `SKIP: Window locked` 竞态 / 反馈弹窗吞命令 —— 链路干等。
2. **session 限额**:每天约停 12h 等重置(内联式 + 模态两种形态)。
3. **监控延迟**:最惨一次 handoff 丢失干等 ~3 天才被补。
4. **孤儿提交**:9.1 故事标 Done 但提交在 churn 中丢失,7.6k 行裸在工作区(已抢救 `1730dc5`)。
5. **并行甩尾**:多故事并行时低号故事(9.1b/10.2/10.4)在共享 Dev 窗口被反复晾。

**应对(已沉淀):** 自愈 watcher 覆盖 5 类故障(自指掉链 / 通用断链 / 限额识别 / 按故事状态推导下一步 / 分级重试);churn 后例行查"Done 但未提交"孤儿;回归串行避免甩尾。

---

## 6. 行动项
- [ ] GA 前完成 `docs/qa/ga-readiness-checklist.md` 的 🔴 阻塞项(staging 环境验证)。
- [ ] 测试债 housekeeping:刷新 4 个 Epic-1 过期快照(SMOKE-1-001);消除 3.4 流式断连 flaky(SMOKE-3-001)。
- [ ] 流程:epic-complete 签收加"声明故事数 vs 实际"机械核对门。
- [ ] 流程:架构师 review 分级(高价值场景必做、纯增量可跳)。
