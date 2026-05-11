# otel-collector — Story 1.4

Gateway-form OpenTelemetry Collector. Source upstream chart: `open-telemetry/opentelemetry-collector` 0.x.

## Staging configuration highlights

- **Mode**: `deployment` / `replicaCount: 1` (staging — gateway pattern, non-DaemonSet to spare per-node CPU).
- **Receivers**: OTLP/gRPC `0.0.0.0:4317`, OTLP/HTTP `0.0.0.0:4318`.
- **Trace exporter**: OTLP → `jaeger-collector.monitoring.svc.cluster.local:4317` (Q2 + M-5 — Jaeger only; Tempo branch removed).
- **Metric exporter**: Prometheus on `0.0.0.0:8889`; scraped by the kube-prometheus-stack Prometheus via this chart's `templates/servicemonitor.yaml`.
- **Processors**: `batch` (5s / 1024) + `k8sattributes` (auto-fills `k8s.*` resource attrs from pod metadata so app code does not hardcode them — see Round 1 Q3 / sample-otel-app contract 1.4-UNIT-208).
