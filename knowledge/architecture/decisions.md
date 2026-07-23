# architecture/decisions

ADR-style entries that `draft-story`, `design-architecture`, and `implement`
read. One entry per decision, terse, with provenance. Append-only; supersede,
do not rewrite.

## AD-001: Capability-first orchestration

- decision: Organize by capability (skills) wired by an orchestrator, not by role agents with handoffs.
- context: Role-handoff pipelines cost 30–50x in cold-start re-reads; strong models make them unnecessary.
- alternatives: BMAD-style role agents (rejected — handoff tax); single monolithic agent (rejected — no isolation/verification).
- status: accepted
- source: human
- added: 2026-06-27
- approved_by: dorayo
- ref: cc-plans/Skill编排器与最小契约设计-v0.1.md

## AD-002: 模型目录以数据库为权威,上游同步只做"发现"

- decision: `he_api.models` 是模型目录唯一权威源;网关每 5 分钟从 PG 刷新内存快照供 `/v1/models` + `/public/models`;CronJob 拉上游 `/v1/models` 仅发现新 id 并落 `status='pending'`;只有 `status='active'` 且有生效 `model_pricing` 的模型才对外可见与可计费;Go `DefaultRegistry` 降级为 DB 不可用时的只读兜底。
- context: 模型清单编译在 Go 里,厂商发版节奏(qwen3.5→3.7 半年三代)远快于本项目发版;2026-07 实测内置 id 已全部被上游下线,线上调用一律 403 且**静默无告警**,同时 DB 里早建好的 models/model_pricing 两表从未被读取,形成双份漂移(Go 11 条 / DB 8 条),价格也因此展示不出来。
- alternatives: 继续硬编码+人工发版(拒绝——已被证伪,半年腐烂一次且静默失败);上游清单直通对外(拒绝——上游不返回价格与能力,且会把上百个内部/图像/语音模型全量抛出,还让上游抖动打穿公开 API)。
- status: accepted
- source: design-architecture
- added: 2026-07-21
- approved_by: pending
- ref: specs/model-catalogue-arch.md

## AD-003: 管理员定价后台的最小授权边界
- decision: 给 users 加 role 列(user|admin),网关在 RequireJWT 之后读库校验 role;据此开放受保护的 POST /v1/admin/models/pricing,前端为 Console /admin/pricing 页面。定价输入「元/百万」在网关侧以 NUMERIC 换算成 USD 后追加写入 model_pricing。
- context: AD-002 使目录数据库驱动后,价格仍只能人工跑 SQL;用户要求自助后台。而平台此前无任何管理员概念(users 无 role、JWT 无 role claim、无 /v1/admin/*),写钱接口必须先立服务端强制的 admin 边界。
- alternatives: env 白名单(管 UUID+重启才能加人,弃);role 写进 JWT(改 auth-svc 令牌铸造,爆炸半径大,弃)。
- status: accepted
- source: design-architecture
- added: 2026-07-23
- approved_by: pending
- ref: specs/admin-pricing-arch.md

<!--
Entry shape (copy for new decisions):

## AD-NNN: <title>
- decision: <one line>
- context: <why it came up, one line>
- alternatives: <what was rejected, one line>
- status: accepted | superseded by AD-MMM
- source: design-architecture | human
- added: <date>
- approved_by: <who>

Metabolism: design-architecture appends new ADRs here; a superseding decision
sets the old one's status to "superseded by AD-MMM" rather than deleting it.
-->
