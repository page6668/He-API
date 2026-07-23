-- AD-003 —— 管理员定价后台的最小授权边界(specs/admin-pricing-arch.md)。
--
-- 平台此前无任何管理员概念:users 表无角色列、JWT 无 role claim、无 /v1/admin/*。
-- 写钱的接口(改价)必须有服务端强制的管理员鉴权,否则任何登录用户都能改价。
-- 本迁移只做最小的事:给 users 加一个两值角色列。RBAC/多角色是 YAGNI,不做。
--
-- ADDITIVE:仅 ADD COLUMN(带默认值,不锁表重写)+ 定位单行的 UPDATE。

ALTER TABLE he_api.users
    ADD COLUMN IF NOT EXISTS role VARCHAR(20) NOT NULL DEFAULT 'user';

ALTER TABLE he_api.users
    DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE he_api.users
    ADD CONSTRAINT users_role_check CHECK (role IN ('user', 'admin'));

-- 把运营者本人置为 admin。**执行前把下面的邮箱改成你的登录邮箱。**
-- 定位不到(邮箱不匹配)时这条 UPDATE 影响 0 行、不报错 —— 届时任何人都不是
-- 管理员,/admin/pricing 对所有人 403(fail-closed),需要再补一条 UPDATE。
UPDATE he_api.users
   SET role = 'admin', updated_at = NOW()
 WHERE email = 'CHANGE_ME@example.com'
   AND role <> 'admin';
