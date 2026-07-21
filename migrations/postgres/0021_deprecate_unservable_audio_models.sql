-- AD-002 收尾 —— 下架两个宣称了却调不通的音频模型。
--
-- 0016 / 0017 分别插入了 doubao-asr 与 doubao-tts,status 取列默认值 'active'
-- 且带定价行。0020 只把 0007 那批种子置为 deprecated,漏了这两条 —— 目录改由
-- 数据库驱动之后,漏掉的行不再是死代码,而是直接摆上货架的商品。
--
-- 事实依据(2026-07-21 实测):adapter-doubao-upstream-api-key 里是 18 字节的
-- 占位值(真实火山引擎密钥远长于此),网关 extraModelRoutes 也只覆盖 7 个走
-- qwen 适配器的路由。即:没有可用的豆包凭据,这两个模型必然调不通。宣称一个
-- 调不通的模型,正是 AD-002 要消灭的那件事。
--
-- 为什么这值得一次迁移,而不是一次数据操作:上下架本身是数据操作(AD-002 的
-- 核心价值就是它不需要发版)。但这里修的是 0016/0017 种子数据的遗留后果 ——
-- 任何全新环境跑完整套迁移后都会再次得到 active 的豆包模型。把修正固化进迁移,
-- 才能让所有环境收敛到同一状态。此后的常规上下架仍走数据操作,不必写迁移。
--
-- 可逆:拿到真实豆包密钥后,一条 UPDATE 置回 'active' 即可,5 分钟内自动生效。
-- 行永不删除(usage_ledger 外键 + 历史账单可追溯)。
--
-- 权限:纯 UPDATE,应用账号 he_api 即可执行(0001 的 ALTER DEFAULT PRIVILEGES
-- 已授予 ALL)。不需要属主 he_admin —— 那是 ALTER TABLE 才有的要求。

UPDATE he_api.models
   SET status = 'deprecated', updated_at = NOW()
 WHERE id IN ('doubao-asr', 'doubao-tts')
   AND status <> 'deprecated';
