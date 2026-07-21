# 模型目录数据库驱动 + 上游自动同步 — Architecture Decision

## Decision

**`he_api.models` 表成为模型目录的唯一权威源**;网关每 5 分钟从 PG 刷新一次内存快照对外服务 `/v1/models` 与 `/public/models`;一个 K8s CronJob 定时拉取上游 `/v1/models` **只负责发现**新模型并以 `status='pending'` 落库;**未经人工补全定价与能力的模型永不对外暴露、永不可计费**。Go 的 `DefaultRegistry` 降级为"DB 不可用时的只读兜底",不再是权威。

## Context

模型目录此前硬编码在 `packages/models-catalogue/registry.go`,进程启动时构建一次快照。2026-07 实测:表中每一个 id(`qwen-max`/`deepseek-v3`/`glm-4`…)都已被上游厂商下线,真实调用一律 403 `Model.AccessDenied` —— **整个模型广场处于静默损坏状态,且无人察觉**。厂商发版节奏(qwen3.5→3.6→3.7 半年三代)远快于本项目的发版节奏,编译期清单必然腐烂。

同时 `he_api.models` / `he_api.model_pricing` 两张表在 migration 0007 就已建好却从未被读取,形成 Go(11 条)与 DB(8 条)双份漂移;价格数据在 DB,而模型广场读 Go —— 这正是"模型广场想显示价格却显示不出来"的根因。

## Alternatives

**方案一:继续硬编码,靠人工发版跟进(现状 + 环境变量止血)**
- 拒绝。已被证伪:半年腐烂一次且**静默失败**(线上全量 403 无告警)。`HE_API_EXTRA_MODEL_ROUTES` 只是把"改代码"降级为"改配置",运营仍依赖工程发布,且两份清单继续漂移。

**方案二:上游清单直通对外(网关实时代理 `/v1/models`)**
- 拒绝。三个硬伤:①上游只返回 `id/created/owned_by`,**没有价格与能力**,而价格是网关产品的核心决策信息,直通等于永远显示不出价格;②上游把上百个模型(含图像、语音、内部测试模型如 `test-sre-gpu-auto-handle`)全量抛出,直接对外即产品失控;③上游抖动会直接打穿我们的公开 API。

**方案三(采纳):DB 为权威 + CronJob 只做发现 + 人工补全后上架**
- 上游只用于**发现**(它唯一可靠的信息就是"哪些 id 存在"),价格与能力由人补全 —— 尊重了"上游不提供这些数据"这一事实约束。
- 自动化收敛在"发现"这一步,把不可自动化的部分(定价、商业决策)显式交给人,而不是假装能自动。

## 状态机(新模型生命周期)

```
上游出现新 id
  → CronJob upsert 为 status='pending'   (不对外可见、不可调用)
  → 运营在控制台补齐 display_name / capabilities / model_pricing
  → 置 status='active'                    (对外可见、可调用、可计费)

上游不再返回某 id
  → CronJob 置 status='deprecated'       (对外隐藏,但保留行与历史用量外键)
  → 永不物理删除(usage_ledger 有外键引用,且需保留历史账单可追溯)
```

**关键不变量:只有 `status='active'` 且存在有效 `model_pricing` 行的模型才对外可见与可计费。** 定价缺失即视为未上架 —— 宁可少上一个模型,不可产生无法计价的调用。

## 数据流与权威源

| 关注点 | 权威源 | 消费方 |
|---|---|---|
| 模型存在性/能力/状态 | `he_api.models` | 网关快照 → `/v1/models`、`/public/models` |
| 价格 | `he_api.model_pricing`(按 `effective_at` 取当前生效行) | 模型广场展示、计费 |
| 上游真实模型名 | `he_api.models.upstream_model_id`(此前建了从未填) | 适配器请求体的 `model` 字段 |
| 模型 → 厂商适配器路由 | `he_api.models.vendor` | 网关 adapterclient |

## Impact

**受影响模块**
- `apps/api-gateway`:新增目录仓储(复用既有 `buildBillingPool`,**不新建连接**)+ 5 分钟 TTL 内存快照;`BuildPublicModelsSnapshot` 改为从快照取数。
- `apps/api-gateway/internal/adapterclient`:路由表由 DB 驱动;`HE_API_EXTRA_MODEL_ROUTES` 保留为紧急覆盖(止血通道不拆)。
- 新增 `apps/api-gateway/cmd/models-sync`(CronJob 镜像,复用 build-images 矩阵模式)。
- `packages/models-catalogue`:`DefaultRegistry` 降级为兜底常量,注释标明"非权威"。
- 适配器 `*_BOUND_MODEL_IDS`:**本次不改**,继续由 values 维护(见下"刻意不做")。

**数据**
- 复用既有两表,不新增表;补充索引 `idx_models_status`。
- 新增迁移:`upstream_model_id` 回填 + `status` 增加 `'pending'` 取值(应用层校验,列本就是 VARCHAR)。

**接口**
- `/v1/models`、`/public/models` 响应结构**不变**(向后兼容),仅数据来源与内容变化。
- 模型广场新增价格字段属**加法变更**,老客户端不受影响。

## 失败降级(无状态多副本前提)

1. **DB 不可用** → 继续用最后一次成功的内存快照对外服务(陈旧但可用),并记 `catalogue_stale` 指标;**不返回 5xx**,与既有"nil pool → 降级但服务不挂"的姿态一致。
2. **进程冷启动即遇 DB 不可用** → 回落到 Go `DefaultRegistry` 兜底,并 `logger.Warn`。宁可给一份陈旧清单,也不让公开 API 挂掉(SEO 与客户端集成都依赖它可用)。
3. **CronJob 失败** → 目录保持原样(纯加法同步,不做破坏性写入);连续失败告警。**同步任务永不删除行、永不改 `active` 为其它状态之外的破坏性变更**。
4. **上游返回空列表** → 视为异常,跳过本轮(防止误把全量模型标记为 deprecated)。

## 刻意不做(YAGNI,留给未来 story)

- **不做自动定价**:价格涉及商业决策与毛利率,必须人工。
- **不改适配器放行列表为 DB 驱动**:本次只统一"对外目录"这一件事;适配器 env 已能免发版调整,收益不足以扩大爆炸半径。
- **不做多上游价格比价/自动选路**:属 Epic 6 路由域,不在此决策范围。
