# Kafka KRaft 裸机部署指南

> Kafka 3.7.0 | KRaft 模式（无需 Zookeeper）| 单节点

---

## 什么是 KRaft？

KRaft 是 Kafka 内置的 Raft 共识协议实现，自 Kafka 3.3 起生产可用，**不再需要 Zookeeper**。

### vs Zookeeper 模式

| 维度 | KRaft（推荐） | Zookeeper |
|------|--------------|-----------|
| 依赖组件 | 无（自包含） | 需部署 Zookeeper 集群 |
| 部署复杂度 | 低 | 高（至少 3 节点 ZK） |
| 配置项 | ~10 项 | ~30 项 |
| 启动速度 | 快 | 需等 ZK 就绪 |
| Kafka 版本要求 | ≥ 3.3 | 任意 |
| 元数据管理 | Kafka 自己管理 | ZK 管理 |
| 扩缩容 | 简单 | 需要 ZK ACL 配置 |

---

## 硬件需求

| 配置 | 最低 | 推荐 | 说明 |
|------|------|------|------|
| CPU | 2 核 | 2-4 核 | KRaft 共识有一定 CPU 开销 |
| 内存 | 4 GB | 4-8 GB | JVM Heap 建议 2-4 GB |
| 磁盘 | 50 GB | 100 GB+ | SSD 优先，日志写入密集 |
| 网络 | 千兆 | 千兆+ | Producer/Consumer 吞吐瓶颈 |

> **注意**：内存 4 GB VM 建议 JVM Heap 设为 2 GB，留 2 GB 给 OS Page Cache。

---

## 安装步骤

### 完整安装流程

```bash
# 1. 将部署文件复制到目标机器后，以 root 身份运行安装脚本
sudo cp -r deploy/bare-metal/kafka /opt/
cd /opt/kafka

# 2. 运行一键安装脚本（自动下载、创建用户、部署配置、初始化）
sudo ./install-kafka.sh

# 3. 启动 Kafka
sudo systemctl start kafka

# 4. 确认进程在线
sudo systemctl status kafka

# 5. 初始化 Topics（首次启动后运行）
sudo ./create-topics.sh

# 6. 验证 Topics 创建成功
/opt/kafka/bin/kafka-topics.sh --list --bootstrap-server localhost:9092
```

> **重要**：首次运行 `install-kafka.sh` 会自动调用 `init-kafka.sh` 初始化 KRaft cluster ID，重复运行会跳过（防止误初始化）。

### 目录结构

```
/opt/kafka/
├── bin/
│   ├── kafka-server-start.sh
│   ├── kafka-server-stop.sh
│   └── kafka-topics.sh
└── config/
    └── kraft/
        └── server.properties    ← KRaft 配置文件

/etc/systemd/system/
└── kafka.service                ← systemd unit

/opt/kafka/data/                 ← Kafka 数据目录（partition logs）
/opt/kafka/logs/                 ← Kafka 自身日志（server.log 等）
```

---

## Topics 说明

| Topic 名称 | 用途 | 分区数 | 保留期 | 说明 |
|-----------|------|--------|--------|------|
| `billing.usage` | 计费用量事件 | 6 | 7 天 | 高频写入，ClickHouse 消费后作缓冲 |
| `audit.event` | 审计日志事件 | 3 | 30 天 | 合规要求长期保留，ClickHouse 归档 |
| `gdpr.export.requested` | GDPR 导出请求 | 1 | 7 天 | 低频，触发数据导出流程 |
| `notification.events` | 系统通知事件 | 3 | 7 天 | 邮件/推送通知的触发源 |

> **数据流**：Kafka → Flink/Consumer → ClickHouse
> 审计日志由 ClickHouse 存储，Kafka 仅作缓冲和分发。

---

## 健康检查

```bash
# 检查进程是否运行
sudo systemctl status kafka

# 检查端口是否监听
ss -tlnp | grep -E '9092|9093'

# 检查所有 Topics
/opt/kafka/bin/kafka-topics.sh --list --bootstrap-server localhost:9092

# 查看某个 Topic 详情
/opt/kafka/bin/kafka-topics.sh \
  --describe --topic billing.usage \
  --bootstrap-server localhost:9092

# 实时查看 Kafka 日志
sudo journalctl -u kafka -f

# 查看 Kafka 自身日志目录
ls /opt/kafka/logs/
```

---

## 日志位置

| 类型 | 路径 |
|------|------|
| systemd journal | `journalctl -u kafka` |
| Kafka 自身日志 | `/opt/kafka/logs/server.log` |
| Topic 数据 | `/opt/kafka/data/` |

---

## 扩容说明（单节点 → 多节点 KRaft）

KRaft 多节点配置步骤：

1. **每台机器安装 Kafka**（运行 `install-kafka.sh`，跳过 init）
2. **修改 `server.properties`**（每节点不同）：

```properties
# 节点 ID（每节点唯一）
node.id=0          # 节点1
node.id=1          # 节点2
node.id=2          # 节点3

# 所有节点的 controller.quorum.voters
controller.quorum.voters=0@node1:9093,1@node2:9093,2@node3:9093

# 每节点修改 advertised.listeners
advertised.listeners=PLAINTEXT://node1:9092   # 节点1
advertised.listeners=PLAINTEXT://node2:9092   # 节点2
```

3. **同步 cluster ID**（从已有节点获取）：

```bash
# 在已有节点获取 cluster ID
cat /opt/kafka/data/meta.properties

# 在新节点使用相同 cluster ID 初始化
/opt/kafka/bin/kafka-storage.sh format \
  -t <CLUSTER_ID> \
  -c /opt/kafka/config/kraft/server.properties \
  --ignore-formatted
```

4. **重启所有节点**：`sudo systemctl restart kafka`

---

## 与 He-API 集成

He-API 通过环境变量连接 Kafka：

```bash
# .env 或环境变量
HE_API_KAFKA_BROKERS=localhost:9092
```

确保 He-API 服务启动前 Kafka 已就绪（可配置 `After=network.target kafka.service` 依赖）。

---

## 备份

Kafka 数据目录可通过文件系统快照备份：

```bash
# LVM 快照（生产环境推荐）
sudo lvcreate --size 10G --snapshot --name kafka-snap /dev/vg00/lv_kafka

# 或直接 rsync（停机备份）
sudo systemctl stop kafka
rsync -av /opt/kafka/data/ /backup/kafka-data-$(date +%Y%m%d)/
sudo systemctl start kafka
```

> 备份时建议停止 Kafka 写入，确保数据一致性。

---

## 常见问题

**Q: 重启后无法启动，报 `meta.properties` 错误？**
> 可能是数据目录权限问题：`sudo chown -R kafka:kafka /opt/kafka/data`

**Q: `controller.quorum.voters` 配置错误？**
> KRaft 模式下必须正确配置 `controller.quorum.voters`，否则启动失败。单节点为 `0@localhost:9093`。

**Q: 内存不足导致 OOM？**
> 降低 JVM Heap：在 `server.properties` 中改为 `KAFKA_HEAP_OPTS=-Xmx1g -Xms1g`

**Q: 如何彻底重装？**
```bash
sudo systemctl stop kafka
sudo rm -rf /opt/kafka/data/* /opt/kafka/logs/*
# 重新运行 init-kafka.sh
```
