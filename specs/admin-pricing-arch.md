# 管理员定价后台 — 架构决策

## Decision（一句话)

新增一个**最小管理员授权边界**:给 `he_api.users` 加 `role` 列(默认 `user`),
网关在既有 `RequireJWT` 之后**读库校验 role**(不改 JWT、不改 auth-svc);据此
提供一个受管理员保护的写接口,由 Console 的 `/admin/pricing` 页面调用,把「元/百万
tokens」的输入在**网关侧以 NUMERIC 换算成 USD** 后追加写入 `model_pricing`。

## Context（为什么现在需要)

模型目录已数据库驱动(AD-002),但价格目前只能靠人工跑 SQL 改。用户要求「给我个
后台自己填」。调查(specs 见 §事实)确认:平台**没有任何管理员概念**——users 表无
role 列、JWT 无 role claim、无 `/v1/admin/*`、`model_pricing` 零运行时写入。7.8 story
已明说「admin 角色是一条新的横切授权边界,值得独立 story」。写钱的接口**必须**有
服务端强制的管理员鉴权,否则任何登录用户都能改价——这正是本决策要先立的边界。

## 事实依据(调查,均带 file:line 核实)

- `users` 建表 `0002_create_users.sql:12-28`,后续 ALTER 无一新增角色列;唯一等级维度是
  `status` 枚举。
- Console 会话 = `he_access` cookie(网关签发 RS256 JWT);`(console)/layout.tsx:37-40` 仅查
  cookie 存在性,真鉴权在网关。
- BFF 写操作范式 `settings/profile/_actions/update-my-routing-strategy.ts`:Server Action 读
  `he_access` cookie → `fetch(PUT ${gateway}/v1/me/...)` 带 `Cookie` 头;**user_id 由网关从
  JWT 派生,绝不入 body**。
- 网关 `middleware/jwt_verify.go`:`RequireJWT` 解 `sub`→`WithUserID`;claims 仅
  sub/aud/purpose/aal,**无 role**。与 bearer API key 是两套独立 context key。
- `model_pricing` 零运行时写入(routing-svc/billing-svc 皆只读);PK=`(model_id,
  effective_at)`,列为 USD/1K tokens + `markup_percent`。

## Alternatives（各自的取舍)

1. **env 白名单(admin user_id 列表注入网关)** — 拒绝。零 DB 变更看似更小,但要管理
   UUID 列表、加一个管理员就得改 Secret + 重启,且与「用户自助」诉求相悖。用户 UUID 放
   env 也难核对。
2. **把 role 写进 JWT claim** — 拒绝。JWT 由 auth-svc 签发,加 claim 要改跨服务的令牌铸造
   + 刷新逻辑,爆炸半径远大于本功能;且 role 变更要等令牌过期才生效。
3. **(选定)role 列 + 网关读库校验** — 采纳。网关**刚接上 DB 连接池**(commit 85d89f8),
   `SELECT role FROM users WHERE id=$1` 是一次廉价读;加管理员=一条 UPDATE 无需重启、无需
   改 auth-svc;正是 7.8 设想的「role enforced at the gateway」。管理操作低频,每请求一次
   读库开销可忽略。

## 关键不变量

- **写钱必须服务端强制鉴权**:`RequireAdmin` 在网关强制,前端隐藏菜单只是体验、不是安全。
  fail-closed:role 读取失败或非 `admin` 一律 403。
- **换算不经浮点(沿用 M-1)**:管理员输入「元/百万」,网关以 `ROUND(cny/1000/fx, 6)` 在
  参数化 SQL 内用 NUMERIC 完成;Go/TS 只搬字符串。
- **调价是追加不是覆盖**:插入新 `effective_at` 行,旧行保留 → 历史账单永远可还原
  (PK 已含 effective_at)。
- **只有 active 且已定价才对外**(AD-002 不变量)沿用不变。

## Impact（影响面)

- **迁移** `0022`:`ALTER TABLE he_api.users ADD COLUMN role VARCHAR(20) NOT NULL DEFAULT 'user'`
  + CHECK `role IN ('user','admin')`;把运营者本人那行 `UPDATE ... SET role='admin'`(按 email 定位)。
- **网关**:①`middleware` 新增 `RequireAdmin`(在 `RequireJWT` 之后,用 `UserIDFromContext`
  读库查 role);②新 handler `POST /v1/admin/models/pricing`(body:model_id、input_cny_per_million、
  output_cny_per_million、fx_usd_cny,全为十进制字符串;校验>0;NUMERIC 换算后 INSERT);
  ③`catalogue` 包加写侧仓储函数。路由 `main.go` 挂 `RequireJWT`+`RequireAdmin`。
- **Console**:`app/[locale]/(console)/admin/pricing` 页面(server component,layout 层用
  role 隐藏入口)+ 一个 Server Action(BFF,转发 he_access cookie 到上述接口);沿用
  `lib/api/money.ts` 的字符串-十进制约定,不引浮点库。fx 默认值可读 `he_api.fx_rates` 最新
  USD→CNY,允许管理员在页面覆盖。
- **鉴权**:复用 `RequireJWT`,不改 auth-svc、不改 JWT。

## 不做(YAGNI)

- 不做通用 RBAC/多角色/权限矩阵——只 `user`|`admin` 两值。
- 不做 model_pricing 的 CNY/fx 溯源列——换算入参记入结构化日志即可,不为此加列。
- 不做能力/上下架的后台编辑——本决策只覆盖**定价**;上下架仍走数据操作(AD-002 已支持)。
