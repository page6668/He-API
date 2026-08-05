# He-API 裸机部署指南

> 适用于物理服务器或虚拟机（VM）的全量部署方案。通过 systemd 管理所有服务进程，支持生产级运行。

---

## 📁 目录结构

```
deploy/bare-metal/
├── README.md                        # 本文档
├── install.sh                       # 一键部署脚本（需 sudo 运行）
├── build-console.sh                 # Console 构建脚本（node20+pnpm，CI/本地共用）
├── nginx/
│   └── he-api.conf                  # Nginx 反向代理配置
├── systemd/                         # systemd unit 文件（15个服务）
│   ├── he-api-gateway.service
│   ├── he-adapter-deepseek.service
│   ├── he-adapter-qwen.service
│   ├── he-adapter-doubao.service
│   ├── he-adapter-glm.service
│   ├── he-adapter-ernie.service
│   ├── he-adapter-kimi.service
│   ├── he-auth-svc.service
│   ├── he-billing-svc.service
│   ├── he-payment-svc.service
│   ├── he-notification-svc.service
│   ├── he-routing-svc.service
│   ├── he-analytics-svc.service
│   ├── he-sample-grpc-app.service
│   ├── he-sample-otel-app.service
│   └── he-api-console.service       # Console 前端（Next.js next start）
└── env/                             # 环境变量模板（15个服务）
    ├── gateway.env
    ├── auth.env
    ├── billing.env
    ├── payment.env
    ├── notification.env
    ├── routing.env
    ├── analytics.env
    ├── adapter-deepseek.env
    ├── adapter-qwen.env
    ├── adapter-doubao.env
    ├── adapter-glm.env
    ├── adapter-ernie.env
    ├── adapter-kimi.env
    ├── sample-grpc.env
    ├── sample-otel.env
    └── console.env                  # Console 前端（含构建期/运行期变量说明）
```

---

## 🔧 部署前准备

### 硬件与系统要求

| 项目 | 最低配置 | 推荐配置 |
|------|----------|----------|
| CPU | 2 核 | 4 核+ |
| 内存 | 4 GB | 8 GB+ |
| 磁盘 | 20 GB SSD | 50 GB+ SSD |
| OS | Ubuntu 20.04+ / CentOS 8+ / Debian 11+ | 同左 |

### 基础设施准备

#### 1. 域名与 DNS

- 注册域名 `he-api.example.com`（替换为自己的域名）
- 在 DNS 控制台添加**一条** A 记录（Console 与网关同源部署）：
  - `he-api.example.com` → 服务器公网 IP
- 建议提前 24 小时生效

> 注：裸机采用**同源单域**部署（nginx `/`→console:3000，`/v1`→gateway:8080），
> 因此 auth cookie（`Path=/v1/auth/refresh`，`SameSite=Strict`）为第一方，无需双子域与跨域配置。

#### 2. 安全组 / 防火墙

开放以下端口：

| 端口 | 用途 | 来源 |
|------|------|------|
| 22 | SSH | 运维 IP |
| 80 | HTTP（Let's Encrypt 验证） | 0.0.0.0/0 |
| 443 | HTTPS | 0.0.0.0/0 |
| 2379-2380 | etcd（可选，如用嵌入式etcd可跳过） | 内网 |

#### 3. 基础设施服务

> 以下服务需在部署 He-API **之前** 启动并配置完成。

| 服务 | 版本 | 端口 | 说明 |
|------|------|------|------|
| PostgreSQL | 14+ | 5432 | 主数据库 |
| Redis | 6+ | 6379 | 缓存/会话 |
| Kafka | 3+ | 9092 | 事件总线 |
| ClickHouse | 22+ | 9000 | 分析数据存储（可选） |
| OpenTelemetry Collector | latest | 4317/4318 | 链路追踪收集（可选） |

> **Kafka + ClickHouse 部署说明**：参见 `deploy/bare-metal/kafka/` 目录（独立部署方案）。

#### 4. SSL 证书

```bash
# 使用 Let's Encrypt（推荐）
sudo apt install -y certbot
sudo certbot certonly --standalone -d he-api.example.com \
    --agree-tos -m admin@example.com --keep

# 证书将生成在：
# /etc/letsencrypt/live/he-api.example.com/fullchain.pem
# /etc/letsencrypt/live/he-api.example.com/privkey.pem
```

---

## 🚀 安装步骤

### 方式一：从 GitHub Release 安装（推荐）

```bash
# 1. 下载并执行安装脚本
curl -fsSL https://raw.githubusercontent.com/he-api/he-api/main/deploy/bare-metal/install.sh \
    -o /tmp/install.sh
chmod +x /tmp/install.sh
sudo /tmp/install.sh --release 1.0.0
```

### 方式二：从本地构建安装

```bash
# 1. 在本地构建（需在项目根目录执行 Makefile 或构建脚本）
# make build 或 ./scripts/build.sh

# 2. 将构建产物 _output/bin/ 复制到服务器，或直接在服务器上构建
scp -r _output/bin/ ubuntu@your-server:/tmp/he-api-bin/

# 3. 运行安装脚本，指定本地目录
sudo /path/to/install.sh --local /tmp/he-api-bin/
```

> 安装脚本会自动生成 JWT RSA 密钥对（`/opt/he-api/keys/jwt_private.pem` +
> `jwt_public.pem`），auth-svc 用私钥签名、网关用公钥验签，两者自动配对，无需手动配置。

### 方式三：手动部署

```bash
# 1. 创建系统用户
sudo useradd --system --no-create-home --shell /usr/sbin/nologin he-api

# 2. 创建目录
sudo mkdir -p /opt/he-api/{bin,env,logs,data}
sudo chown -R he-api:he-api /opt/he-api

# 3. 放置二进制到 /opt/he-api/bin/

# 4. 复制 env 模板
sudo cp -r env/* /opt/he-api/env/

# 5. 复制 systemd unit
sudo cp systemd/*.service /etc/systemd/system/
sudo systemctl daemon-reload

# 6. 设置开机自启（不启动）
sudo systemctl enable he-api-gateway he-auth-svc ...
```

---

## ⚙️ 环境变量配置

所有 env 文件已部署到 `/opt/he-api/env/`。**请逐个编辑，填写真实值。**

### 必填变量说明

#### `gateway.env` — API 网关（核心）

| 变量 | 示例值 | 说明 |
|------|--------|------|
| `HE_API_DB_POSTGRES_URI` | `postgres://he_api:pass@127.0.0.1:5432/he_api?sslmode=disable` | PostgreSQL 连接串 |
| `HE_API_REDIS_URL` | `redis://127.0.0.1:6379/0` | Redis 连接串（gateway/billing 用） |
| `HE_API_JWT_PUBLIC_KEY_PATH` | `/opt/he-api/keys/jwt_public.pem` | JWT 验签公钥（install.sh 自动生成） |
| `HE_API_KAFKA_BROKERS` | `localhost:9092` | Kafka Broker 地址 |
| `HE_API_OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4317` | OTEL Collector 地址 |

#### `auth.env` — 认证服务

> JWT 采用 **RSA 公私钥对**：auth-svc 用 `HE_API_JWT_PRIVATE_KEY_PATH`（私钥）签名，
> 网关用 `HE_API_JWT_PUBLIC_KEY_PATH`（公钥）验签。两者由 install.sh 自动生成并配对，
> 无需手动填写密钥字符串。auth-svc 同时读取 `HE_API_DB_POSTGRES_URI` 与 `HE_API_DATABASE_URL`。

#### `payment.env` — 支付服务

需填写的密钥对应各支付渠道的控制台申请：

| 变量 | 平台 |
|------|------|
| `STRIPE_SECRET_KEY` | Stripe |
| `PAYPAL_CLIENT_ID` / `PAYPAL_CLIENT_SECRET` | PayPal |
| `ALIPAY_PLUS_CLIENT_ID` / `ALIPAY_PLUS_MERCHANT_PRIVATE_KEY` / `ALIPAY_PLUS_ALIPAY_PUBLIC_KEY` | 支付宝 Alipay+ |
| `WECHAT_PAY_MCH_ID` / `WECHAT_PAY_APIV3_KEY` / `WECHAT_PAY_MERCHANT_PRIVATE_KEY` | 微信支付 ApiV3 |
| `COINBASE_COMMERCE_API_KEY` | Coinbase Commerce |

#### `analytics.env` — 分析服务

| 变量 | 说明 |
|------|------|
| `HE_API_CLICKHOUSE_DSN` | ClickHouse 连接串（注意变量名是 `_DSN` 而非 `_URI`） |
| `HE_API_OSS_GDPR_EXPORTS_BUCKET` | OSS bucket（GDPR 数据导出存放） |

#### `adapter-*.env` — 各 LLM 适配器

每个适配器仅需两个变量：

| 文件 | 变量 | 说明 |
|------|------|------|
| `adapter-deepseek.env` | `DEEPSEEK_API_KEY` | DeepSeek API Key |
| `adapter-qwen.env` | `QWEN_API_KEY` | 通义千问 API Key |
| `adapter-doubao.env` | `DOUBAO_API_KEY` | 豆包 API Key |
| `adapter-glm.env` | `GLM_API_KEY` | 智谱 GLM API Key |
| `adapter-ernie.env` | `ERNIE_API_KEY` | 百度文心 API Key |
| `adapter-kimi.env` | `KIMI_API_KEY` | Moonshot Kimi API Key |

---

## 🖥️ Console 前端部署

Console 是 Next.js 14（App Router）单页应用。裸机以 **Node 直接 `next start`** 运行，
**不使用** Docker 的 standalone 产物——pnpm 符号链接布局下，Next 的 standalone 文件追踪器
会漏打 `styled-jsx` 等传递依赖的顶层符号链接，导致 `next start`/`server.js` 启动即报
`Cannot find module 'styled-jsx'`。改用 `pnpm deploy --prod` 生成扁平 `node_modules` + `next start` 规避。

### 架构

```
用户 ──HTTPS 443──> nginx (he-api.example.com)
                        ├── /v1/*  ──> gateway:8080   （API + SSE 流式）
                        └── /*     ──> console:3000  （Next.js）
console server action ──HTTP──> gateway:8080 (HE_API_GATEWAY_URL=http://127.0.0.1:8080)
```

同源部署使 auth cookie（`Path=/v1/auth/refresh`，`SameSite=Strict`）为第一方，零跨域问题。

### 从 GitHub Release 安装（--release 自动包含）

`install.sh --release <tag>` 会下载 `he-api-console-<tag>-linux-amd64.tar.gz` 解压到
`/opt/he-api/console`，并将 `node` 真实路径写入 `he-api-console.service` 的 `ExecStart`，
最后 `systemctl enable he-api-console`。**前置：服务器已装 Node 20+**（`command -v node`）。

### 本地 / 手动构建

```bash
# 在 monorepo 根执行（需 node20 + pnpm9）
bash deploy/bare-metal/build-console.sh <repo-root> /tmp/console-out

# 打包并部署到服务器
tar -czf console.tgz -C /tmp/console-out console
scp console.tgz server:/tmp/
ssh server 'sudo tar -xzf /tmp/console.tgz -C /opt/he-api/ && sudo systemctl daemon-reload'
```

CI 中由 `.github/workflows/release-binaries.yml` 的 `build-console` job 调用同一个脚本。

### ⚠️ 构建期变量（NEXT_PUBLIC_*）

`env/console.env` 中 `NEXT_PUBLIC_*` 在 `next build` 时**烘焙进前端 JS bundle**。
修改这些变量后**必须重新构建**（`build-console.sh`），仅改 env 并 `restart` 不生效。
非 `NEXT_PUBLIC_*` 为运行期变量，改完 `restart` 即生效。

### 关键变量（env/console.env）

| 变量 | 说明 |
|------|------|
| `HE_API_GATEWAY_URL` | 控制台服务端调用网关的内部地址，裸机必设 `http://127.0.0.1:8080` |
| `PORT` | `next start` 监听端口（默认 3000，nginx 反代目标） |
| `BARE_METAL_BUILD` | 必须 `=1`，使运行期配置与裸机构建产物一致（关闭 standalone），否则 `next start` 报警告 |
| `NEXT_PUBLIC_HE_API_BASE` | 同源部署留空（playground 用相对 `/v1/me/playground/chat`） |
| `NEXT_PUBLIC_API_GATEWAY_URL` / `NEXT_PUBLIC_CONSOLE_ORIGIN` | 对外域名（OAuth 回跳 / CORS Origin），改域名需重新构建 |

### 服务管理

```bash
sudo systemctl start he-api-console      # 依赖网关，建议网关启动后再起
sudo systemctl restart he-api-console
sudo journalctl -u he-api-console -f
curl -I http://localhost:3000/          # 健康检查（应 200/307 重定向到 /<locale>）
```

---

## 🌐 Nginx 配置

### 步骤 1：复制配置

```bash
sudo cp nginx/he-api.conf /etc/nginx/conf.d/
sudo nginx -t
```

### 步骤 2：修改证书路径

编辑 `/etc/nginx/conf.d/he-api.conf`，将以下两行替换为实际证书路径：

```nginx
ssl_certificate /etc/letsencrypt/live/he-api.example.com/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/he-api.example.com/privkey.pem;
```

### 步骤 3：重载 Nginx

```bash
sudo systemctl reload nginx
```

---

## 🏃 服务管理

### 启动服务

```bash
# 启动 API 网关（核心，先启动）
sudo systemctl start he-api-gateway

# 启动所有适配器
sudo systemctl start he-adapter-deepseek \
    he-adapter-qwen \
    he-adapter-doubao \
    he-adapter-glm \
    he-adapter-ernie \
    he-adapter-kimi

# 启动所有后端服务
sudo systemctl start he-auth-svc \
    he-billing-svc \
    he-payment-svc \
    he-notification-svc \
    he-routing-svc \
    he-analytics-svc

# 启动示例应用
sudo systemctl start he-sample-grpc-app he-sample-otel-app

# 启动 Console 前端（需网关先起）
sudo systemctl start he-api-console
```

### 建议启动顺序

```
1. auth-svc, billing-svc, payment-svc, notification-svc   （基础依赖）
2. routing-svc, analytics-svc                            （事件消费者）
3. he-adapter-*（6个）                                   （LLM 适配器）
4. he-api-gateway                                        （API 网关，最后启动）
5. he-api-console                                        （前端，依赖网关）
```

### 常用命令

```bash
# 查看状态
sudo systemctl status he-api-gateway

# 查看实时日志
sudo journalctl -u he-api-gateway -f
sudo journalctl -u he-api-gateway --since "10 minutes ago"

# 重启服务
sudo systemctl restart he-api-gateway

# 关闭服务
sudo systemctl stop he-api-gateway

# 取消开机自启
sudo systemctl disable he-api-gateway

# 查看所有 he-api 服务状态
sudo systemctl list-units 'he-api-*' --state=running
```

---

## ✅ 健康检查

```bash
# 基础健康检查
curl http://localhost:8080/healthz

# 带认证的健康检查（JWT）
curl -H "Authorization: Bearer <token>" http://localhost:8080/api/v1/status

# 端口连通性检查
for port in 8080 9001 9002 9003 9004 9005 9006 8091 8092 8093 8094 8095 8096 9081 9082; do
    nc -zv 127.0.0.1 "$port" 2>&1 | grep -q succeeded && echo "✓ :$port OK" || echo "✗ :$port FAIL"
done
```

---

## 🗄️ 数据库迁移（启动服务前必做）

He-API 的 PostgreSQL schema 由 22 个版本化 migration 定义（`migrations/postgres/0001..0022`）。
裸机环境不依赖 K8s 的 Atlas，改用随附的 `migrate.sh`（psql 驱动，幂等）。

### 前置条件

- 已安装 `postgresql-client`（提供 `psql`）
- 已创建数据库 `he_api`（建议阿里云 RDS PG 或同机自建）
- 首次运行用**超级用户**（如 `postgres`）连接，脚本会自动创建 `he_api` 角色与 schema

### 执行迁移

```bash
# 设置连接串（密码会用于创建 he_api 角色，使其与连接密码一致）
export HE_API_DB_POSTGRES_URI='postgres://postgres:你的密码@localhost:5432/he_api?sslmode=disable'

# 预览将要执行的 migration（不实际执行）
sudo -E bash deploy/bare-metal/migrate.sh dry-run

# 应用所有未执行的 migration
sudo -E bash deploy/bare-metal/migrate.sh up

# 校验状态
sudo -E bash deploy/bare-metal/migrate.sh status
```

### ClickHouse（可选）

仅当部署 `analytics-svc` 时才需要。设置 `HE_API_DB_CLICKHOUSE_URI` 并安装
`clickhouse-client` 后，重新运行 `migrate.sh up` 即会一并应用 ClickHouse migration：

```bash
export HE_API_DB_CLICKHOUSE_URI='clickhouse://default:@localhost:9000/he_api'
sudo -E bash deploy/bare-metal/migrate.sh up
```

> 未设置该变量时，脚本会跳过 ClickHouse 部分并提示，不影响 PostgreSQL 迁移。

### 回滚

```bash
# 从追踪表移除最近 N 条记录（best-effort，多数 migration 为 ADDITIVE 无反向 SQL）
sudo -E bash deploy/bare-metal/migrate.sh down 1
```

---

## 💾 备份与恢复

### 数据库备份

```bash
# PostgreSQL 每日备份 cron
0 3 * * * pg_dump -U heapi -d heapi | gzip > /opt/he-api/backups/heapi_$(date +\%Y\%m\%d).sql.gz

# 恢复
gunzip < /opt/he-api/backups/heapi_20240101.sql.gz | psql -U heapi -d heapi
```

### 配置备份

```bash
# 备份 env 文件（包含密钥，禁止泄漏）
tar -czf /opt/he-api/backups/config-$(date +\%Y\%m\%d).tar.gz \
    /opt/he-api/env/

# 备份 systemd units
tar -czf /opt/he-api/backups/systemd-$(date +\%Y\%m\%d).tar.gz \
    /etc/systemd/system/he-api-*.service
```

---

## 🔒 安全加固建议

1. **禁止 root 运行**：所有服务以 `he-api` 用户运行（已配置）
2. **env 文件权限**：`chmod 600 /opt/he-api/env/*.env`
3. **日志审计**：通过 `journalctl` 集中收集审计日志
4. **网络隔离**：Kafka、PostgreSQL、Redis 仅监听 `127.0.0.1`
5. **定期轮换密钥**：JWT Secret、API Keys 定期更换

---

## 🐛 常见问题

### Q: 服务启动失败，提示 `User=he-api not found`

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin he-api
```

### Q: `Failed to start he-api-gateway.service: Unit not found`

```bash
sudo systemctl daemon-reload
sudo systemctl list-unit-files 'he-api-*'
```

### Q: JWT 验证失败（401 Unauthorized）

检查 `auth.env` 的 `HE_API_JWT_PRIVATE_KEY_PATH` 与 `gateway.env` 的 `HE_API_JWT_PUBLIC_KEY_PATH` 是否指向 install.sh 生成的同一对密钥（`/opt/he-api/keys/jwt_private.pem` + `jwt_public.pem`）。两者必须配对，否则网关验签会 401。

### Q: 适配器连接超时

确认各 `adapter-*.env` 的 `PORT` 与 systemd unit 中的 `ExecStart` 对应的二进制监听端口一致。

### Q: Kafka 消费者报错 `Leader not available`

```bash
# 检查 Kafka 是否正常运行
sudo systemctl status kafka
# 检查 Kafka topic 是否存在
kafka-topics.sh --bootstrap-server localhost:9092 --list
```

### Q: nginx 502 Bad Gateway

检查 upstream 后端是否全部启动：

```bash
sudo ss -tlnp | grep -E ':(8080|3000|9001)'
```

---

## 📞 服务端口一览

| 服务 | 端口 |
|------|------|
| api-gateway | 8080 |
| adapter-deepseek | 8091 |
| adapter-qwen | 8092 |
| adapter-doubao | 8093 |
| adapter-glm | 8094 |
| adapter-ernie | 8095 |
| adapter-kimi | 8096 |
| auth-svc | 9001 |
| billing-svc | 9002 |
| payment-svc | 9003 |
| notification-svc | 9004 |
| routing-svc | 9005 |
| analytics-svc | 9006 |
| sample-grpc-app | 9081 |
| sample-otel-app | 9082 |
| **console (Next.js)** | **3000**（仅内网，经 nginx 443 反代） |
