# Observability Helm chart wrappers (Story 1.4)

Hybrid Helm distribution per **Architect Round 1 Q1 ruling** — five (5) observability releases per cluster:

| Release | Upstream chart | Pinned version | Why |
|---------|----------------|----------------|-----|
| `kube-prometheus-stack` | `prometheus-community/kube-prometheus-stack` | `56.x` | Umbrella: Prometheus 2.50+ + Grafana 10+ + Alertmanager + kube-state-metrics + Prometheus Operator (`ServiceMonitor` / `PrometheusRule` CRDs + sidecar dashboards/datasources). |
| `loki` | `grafana/loki` | `5.x` (app Loki 3.x) | SingleBinary deploymentMode for staging. |
| `promtail` | `grafana/promtail` | Promtail chart 6.x (app version 3.x) | DaemonSet node-level log collection (Round 1 **M-2** — explicit in chart inventory; Promtail in maintenance mode upstream, Alloy migration deferred to Epic 9 ADR). |
| `otel-collector` | `open-telemetry/opentelemetry-collector` | `0.x` (Collector 0.x) | `mode: deployment` gateway-form Collector; receives OTLP 4317/4318, exports to Jaeger + Prometheus. |
| `jaeger` | `jaegertracing/jaeger` | `1.55+` (app Jaeger 1.55+) | All-in-one image, in-memory storage; per architecture §11.1 and Round 1 **Q2 ruling** (Tempo branch fully removed — M-5). |

## Architect Round 1 ruling traceability

- **Q1 (Hybrid distribution)** — `kube-prometheus-stack` umbrella + 4 independent charts. Piecemeal would re-implement Operator coupling for `ServiceMonitor`/`PrometheusRule` CRDs.
- **Q2 (Trace backend = Jaeger)** — preserved per architecture §11.1; Tempo branch removed in M-5.
- **Q3 (Sample service)** — `apps/sample-otel-app/` is a separate chart; api-gateway stays single-responsibility.
- **Q4 (Persistence)** — staging full emptyDir; Prom 24h / Loki 48h / Jaeger ~2h (memory natural eviction); prod PVC = Story 1.7+ ADR.
- **Q5 (Grafana exposure)** — port-forward only; ingress-nginx / cert-manager install deferred to Story 1.7+.
- **Q6 (Grafana auth)** — chart auto-generates admin password into K8s Secret `kube-prometheus-stack-grafana`; OIDC = Epic 9.

## Major fixes (Round 1)

- **M-1** — All dashboard PromQL uses OpenTelemetry Semantic Conventions 1.x: `http_server_request_duration_seconds_*` metric + `service_name` label + `http_response_status_code` label.
- **M-2** — Promtail explicitly listed as its own chart and its own lint matrix entry. Default DaemonSet kind preserved (no Deployment override).
- **M-3** — `nodeExporter.enabled: false` in kube-prometheus-stack values; node-exporter needs hostNetwork/hostPID/hostPath which violates the `monitoring` namespace baseline PSA. Node-level metrics via kube-state-metrics + cAdvisor are sufficient for staging; prod evaluation = Story 1.7+.
- **M-4** — Story 1.2 `deploy-staging.yml` left untouched (hard-codes api-gateway single chart). `sample-otel-app` image tag is injected by `scripts/observability/install.sh` via `--set image.tag=$(git rev-parse --short HEAD)`. Multi-chart automatic rollout = Story 1.5.
- **M-5** — Tempo references removed across all observability values, datasources, scripts.

## Each chart sub-directory layout

```
infra/helm/observability/<chart>/
├── Chart.yaml            # Wrapper chart (apiVersion v2) with the upstream as a dependency
├── values.yaml           # Default values (passed to upstream via chart name key)
├── values-staging.yaml   # Staging overrides (retention, emptyDir, ports, etc.)
├── README.md             # Per-chart notes
└── templates/            # Optional: wrapper-chart-local manifests (e.g., grafana-datasources ConfigMap, ServiceMonitor)
```

**GitOps invariant**: no direct `helm install <repo>/<chart>` without persisted values. The wrapper chart approach lets `helm lint` + `helm template | kubeconform` + `helm dependency update` operate uniformly across charts in CI.

## Install / upgrade flow

See [`scripts/observability/install.sh`](../../../scripts/observability/install.sh) — `# BOOTSTRAP MODE` header marks this script as the staging-only bootstrap; ArgoCD GitOps migration is **Story 1.7+** (m-2 ruling).
