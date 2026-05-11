# loki — Story 1.4

SingleBinary Loki in emptyDir mode for staging. Source upstream chart: `grafana/loki` 5.x (Loki application 3.x). Paired with the sibling `promtail/` DaemonSet release.

## Staging configuration highlights

- **deploymentMode**: SingleBinary — minimal CPU/mem footprint for the staging 3× ecs.c7.large pool.
- **Storage**: filesystem on emptyDir (Q4). `singleBinary.persistence.enabled: false` + `resources.limits.ephemeral-storage: 2Gi` cap.
- **Retention**: 48h — spans a weekend so on-call has Monday-morning visibility.
- **Replication factor**: 1 (single-binary mode).

Prod PVC / ESSD / OSS object-store strategy is a Story 1.7+ ADR.
