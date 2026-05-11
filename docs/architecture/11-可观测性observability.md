# 11. 可观测性（Observability）

## 11.1 三大支柱

| 类型 | 工具 | 内容 |
|------|------|------|
| **Metrics** | Prometheus + Grafana | RED + USE 指标 + 业务指标 |
| **Logs** | Loki + Promtail | 结构化 JSON 日志 |
| **Traces** | OpenTelemetry + Jaeger（后端） | 全链路 trace |

## 11.2 关键 Dashboard

1. **Gateway 大盘**: QPS / 错误率 / 延迟 P50/P95/P99 / Top error codes
2. **业务大盘**: 注册/天 / DAU / 充值金额 / Token 消费 / 月 GMV
3. **模型大盘**: 各模型 QPS / 延迟 / 错误率 / 成本 / 路由命中率
4. **支付大盘**: 各通道成功率 / 平均时间 / 退款率 / 异常告警
5. **合规大盘**: 内容过滤命中率 / 误杀率（需人工标注） / 备案进度 / 数据出境检测（应永远 0）

## 11.3 告警规则（核心）

```yaml
- alert: GatewayP95LatencyHigh
  expr: histogram_quantile(0.95, rate(http_request_duration_seconds_bucket[5m])) > 0.5
  for: 5m
  severity: warning
  
- alert: GatewayErrorRateHigh
  expr: rate(http_requests_total{status=~"5.."}[5m]) / rate(http_requests_total[5m]) > 0.01
  for: 5m
  severity: critical
  
- alert: UpstreamModelDown
  expr: rate(adapter_upstream_errors_total[5m]) > 0.1
  for: 3m
  severity: warning
  labels:
    routing_action: failover
    
- alert: DataExportSuspect
  expr: increase(network_egress_to_overseas_bytes[10m]) > 0
  for: 1m
  severity: critical
  description: "WARNING: 检测到向境外网络的数据传输（不应发生）"
```

## 11.4 通知通道

- **P0 (critical)**: PagerDuty → 值班手机（含技术 + 合规人员）
- **P1 (high)**: 飞书机器人 + 邮件
- **P2 (medium)**: 飞书 + Slack
- **P3 (info)**: Slack

---
