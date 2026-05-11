# kube-prometheus-stack (umbrella) — Story 1.4

Umbrella chart bundling Prometheus + Grafana + Alertmanager + kube-state-metrics + Prometheus Operator. Source upstream chart: `prometheus-community/kube-prometheus-stack` 56.x.

## Staging configuration highlights

- **Retention**: Prometheus 24h (Q4 — covers 1 working-day failure window) — emptyDir storage.
- **Sidecars**: dashboards label `grafana_dashboard: "1"` + datasources label `grafana_datasource: "1"` (Grafana auto-loads ConfigMaps with these labels from any namespace).
- **Admin password**: Chart auto-generates and stores in K8s Secret `kube-prometheus-stack-grafana`. Extract:
  ```bash
  kubectl get secret -n monitoring kube-prometheus-stack-grafana \
    -o jsonpath='{.data.admin-password}' | base64 -d
  ```
- **No ingress**: per Q5, only port-forward (`kubectl port-forward svc/kube-prometheus-stack-grafana 3000:80 -n monitoring`).
- **No node-exporter**: per M-3, `nodeExporter.enabled: false` (PSA baseline compatibility).
- **kube-state-metrics**: enabled (PSA baseline OK — only reads K8s API).

## Grafana datasources ConfigMap

The wrapper chart ships `templates/grafana-datasources.yaml` declaring the 3 datasources (Prometheus / Loki / Jaeger) per Round 1 Q2 ruling.
