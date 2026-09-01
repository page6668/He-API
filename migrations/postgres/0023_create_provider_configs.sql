-- AD-004 —— LLM Provider API Key 运行时配置表(specs/admin-providers-arch.md)。
--
-- 平台此前 adapter 的 key 只能通过环境变量注入,管理员要改 key 必须 SSH 上 ECS
-- 编辑 env + systemctl restart。本迁移引入运行时配置表,使 admin 角色可在
-- Console 改 key 并通过 gateway → adapter 内部端点 → 30s 热加载生效。
--
-- 设计要点:
--   * PK = provider_name (与适配器 Process 内枚举一致:deepseek/doubao/ernie/glm/kimi/qwen)
--   * api_key_encrypted BYTEA:AES-256-GCM(nonce 12B + ciphertext + tag 16B,共 N+28 字节)
--     密钥来自 /opt/he-api/secrets/provider-encryption.key,env 不暴露密钥
--   * base_url TEXT NOT NULL:adapter 默认会兜底用 env *_UPSTREAM_BASE_URL,
--     但表里有值时优先用表的(允许 Console 改端点,如切换中转)
--   * enabled BOOL:管理员可临时下线某 provider(请求直接 503 而非尝试上游)
--   * last_verified_at / last_error:测试连通性结果回写,UI 可显示健康度
--   * updated_by_user_id FK users(id):审计来源
--   * 不存 model 列表/价格(那些在 model_pricing 和 models 表,本表只管密钥)

CREATE TABLE IF NOT EXISTS he_api.provider_configs (
    provider_name        VARCHAR(50)  PRIMARY KEY
        CHECK (provider_name IN ('deepseek', 'doubao', 'ernie', 'glm', 'kimi', 'qwen')),
    api_key_encrypted    BYTEA        NOT NULL,
    base_url             TEXT         NOT NULL,
    enabled              BOOLEAN      NOT NULL DEFAULT true,
    last_verified_at     TIMESTAMPTZ,
    last_error           TEXT,
    updated_by_user_id   UUID         REFERENCES he_api.users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_provider_configs_updated_at
    ON he_api.provider_configs (updated_at DESC);

-- 触发器:自动维护 updated_at
CREATE OR REPLACE FUNCTION he_api.touch_provider_configs_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_provider_configs_updated_at ON he_api.provider_configs;
CREATE TRIGGER trg_provider_configs_updated_at
    BEFORE UPDATE ON he_api.provider_configs
    FOR EACH ROW EXECUTE FUNCTION he_api.touch_provider_configs_updated_at();

-- 权限:he_api_user 应能读写(adapter 与 gateway 共享此账号)
GRANT SELECT, INSERT, UPDATE, DELETE ON he_api.provider_configs TO he_api_user;
