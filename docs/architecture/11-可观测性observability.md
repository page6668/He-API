# 11. 可观测性（Observability）

## 11.1 三大支柱

| 类型 | 工具 | 内容 |
|------|------|------|
| **Metrics** | Prometheus + Grafana | RED + USE 指标 + 业务指标 |
| **Logs** | Loki + Promtail | 结构化 JSON 日志 |
| **Traces** | OpenTelemetry + Jaeger（后端，Grafana Jaeger datasource 可视） | 全链路 trace |

> **Story 9.4 — 全链路 trace 落地 (Q-STORE ratified: KEEP Jaeger, NO Tempo).** Story 1.4
> shipped the OTel SDK + collector + Jaeger + Grafana datasource; Story 3.6 stamped
> `he.request_id` on every server span. 9.4 closes the CONTINUITY gap so one request
> is ONE trace end-to-end and viewable in Grafana via the existing **Jaeger** datasource
> (`uid: jaeger`) — the epic-9 AC「Grafana 可看到完整请求链路」is satisfied without a
> Tempo migration. Mechanism: a GLOBAL W3C `tracecontext`+`baggage` propagator
> (`obs.SetupPropagation()`, every service main) + outbound-client instrumentation
> (`obs.NewHTTPClient` → `otelhttp.NewTransport`, zero bare `&http.Client{}` on any
> RPC path) for the sync hops, and manual W3C header inject/extract over
> `kafka.Message.Headers` (`obs.Inject/ExtractKafkaHeaders`) for the async hops — every
> Kafka consumer joins the originating trace via a **span LINK** (Q-KAFKA: link, not
> child, for all 4 topics). trace↔log correlation is bidirectional: the Loki→Jaeger
> `derivedField` on `trace_id` (1.4/3.6) **plus** the new Jaeger→Loki `tracesToLogsV2`
> reverse link (9.4 BR-TR-14). Operator runbook: `docs/architecture/runbook-trace-a-request.md`.

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
    # Story 6.3 REALISED — the gateway emits, on every failover hop, a non-PII
    # slog line {event:routing_failover, routing_action:failover, from_model,
    # to_model, reason∈{upstream_unavailable,upstream_timeout}, attempt,
    # strategy, he_request_id} + Prometheus:
    #   he_routing_failover_total{from_model,to_model,reason}  (counter)
    #   he_routing_failover_attempts                            (histogram, buckets [1,2,3])
    # Cardinality bounded (~8×8 catalogue for from×to). A high
    # he_routing_failover_total rate is the canonical "upstream degraded but
    # users transparent" signal (RTO 0 target, infrastructure-deployment §灾备).
    #
    # Story 6.4 A/B REALISED — an A/B request (X-He-AB-Models: a,b) emits a
    # non-PII slog line {event:chat_completions_ab, ab_models, served_models,
    # failed_models, he_request_id} (NEVER user_id / message content) +
    # Prometheus:
    #   he_routing_ab_total{outcome∈{both_ok,partial,both_failed}}  (counter)
    # Cardinality bounded (3 outcomes). A rising `partial`/`both_failed` share is
    # the A/B upstream-health signal; dual-billing means TPM (not QPS) reflects
    # both legs (one A/B request = one QPS/RPM tick).
    
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

> **Story 10.7 (AC1) — wiring LANDED**: this matrix is now the source of truth for
> the inline Alertmanager route tree in `infra/helm/observability/kube-prometheus-stack/values-staging.yaml`.
> P0 is **dual-channel** (`severity=critical → PagerDuty + 飞书 mirror`, `continue:true`)
> so a single-channel delivery failure can't silence a P0; an explicit non-blackhole
> catch-all is mandatory. Receiver secrets (PagerDuty routing key / 飞书 / Slack webhooks /
> SMTP password) are mounted via ExternalSecret→Vault `kv/data/he-api/alerting/*` and
> referenced by `*_file` (never inline). The mapping is locked by the CI gold gate
> `scripts/ci/verify-alertmanager-routes.sh` (route drift = silent P0 loss).

## 11.5 Span Attribute Naming

> Landed by Story 3.6 per Architect Round 1 High-Issue ruling (BR-2.8). Source of truth for all `he.*` OTel span attributes the gateway + downstream services emit.

**Namespace reservation**: the `he.` prefix is RESERVED as the project's OTel span-attribute namespace. OpenTelemetry semantic conventions OWN the un-prefixed namespace (`http.*`, `db.*`, `messaging.*`, `rpc.*`, etc.). Project-specific attributes MUST use the `he.` prefix to avoid colliding with future OTel-semconv additions.

**Registered `he.*` attributes**:

| Attribute | Type | Format | Stamped by | Story | Notes |
|-----------|------|--------|-----------|-------|-------|
| `he.request_id` | string | `^req_[a-f0-9]{12}$` | `apps/api-gateway/internal/middleware/requestid.RequestID` (on every /v1/* + /v1/auth/* + /v1/me* + /v1/account/* span) | 3.6 | Customer-support correlation handle. Searchable in Jaeger UI as `Tags: he.request_id=req_a1b2c3d4e5f6`. |

**Forward-looking note (informational, NOT a future commitment)**: Epic 4 (`he.selected_model` — Story 4.7 routing) and Epic 7 (`he.cost_usd` — billing) will extend this namespace. Subsequent Stories MUST register new `he.*` attributes in §11.5 in the SAME PR that adds the attribute to code; an attribute that lands in code without a §11.5 row is a Story-level breach of the BR-2.8 convention.

### 11.5 Change Log

| Date | Story | Change |
|------|-------|--------|
| 2026-05-19 | Story 3.6 (Dev) | §11.5 created; `he.request_id` registered as the first reserved `he.*` span attribute (per BR-2.8 + T8.9). |
| 2026-06-11 | Story 9.4 (Dev) | NO new `he.*` attribute added — 9.4 reuses `he.request_id` (now continuous across the whole trace once the global propagator is installed). Reaffirms BR-TR-7: span attributes (and Kafka headers) are an ALLOW-LIST of registered `he.*` + OTel `http.*`/`rpc.*`/`db.system` semconv ONLY — NEVER prompt/response bodies, `Authorization`/`X-He-Api-Key`, api-key plaintext/hash, email, or client IP. Runtime backstop = otel-collector `redaction` keep-list (`infra/helm/observability/otel-collector/values-staging.yaml`), whose `allowed_keys` MUST stay ⊇ this registry. |

---
