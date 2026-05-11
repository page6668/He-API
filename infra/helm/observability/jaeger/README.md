# jaeger — Story 1.4

All-in-one Jaeger (collector + query + UI + in-memory store) for staging trace inspection. Source upstream chart: `jaegertracing/jaeger` 3.1.x (Jaeger app 1.55+).

## Staging configuration highlights

- **Q2 ruling**: Jaeger preserved per architecture §11.1 — Tempo branch fully removed (M-5).
- **Q4 ruling**: `storage.type: memory` — traces evict naturally as `query.maxTraces` upper bound is reached (~2h of typical staging load).
- **Q5 ruling**: `query.service.type: ClusterIP` — no public exposure; operator uses `kubectl port-forward svc/jaeger-query 16686:16686 -n monitoring`.
- **Agent disabled**: OTel Collector replaces the legacy `jaeger-agent` sidecar pattern; the Collector ships traces directly to `jaeger-collector.monitoring.svc.cluster.local:4317`.
