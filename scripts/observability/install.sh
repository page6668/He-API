#!/usr/bin/env bash
# BOOTSTRAP MODE — superseded by ArgoCD Application once ArgoCD is installed (Story 1.7+)
#
# Story 1.4 observability stack bootstrap. Five observability releases install
# into the `monitoring` namespace in the strict ordering required by Architect
# Round 1 Q1; the reference-impl release installs into `he-api-staging` with
# `--set image.tag=$(git rev-parse --short HEAD)` per Round 1 M-4.
#
# The ServiceMonitor CRD `kubectl wait` between step 1 (umbrella) and steps
# 2-5 is mandatory per Round 1 m-3 — it prevents downstream chart admission
# failures when dependents reference the CRD before the API server has finished
# registering it.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OBS_DIR="$ROOT/infra/helm/observability"
SAMPLE_DIR="$ROOT/infra/helm/sample-otel-app"

MONITORING_NS="monitoring"
SAMPLE_NS="he-api-staging"

# Round 1 M-4: git SHA used for sample-otel-app image tag (NOT the placeholder
# from values-staging.yaml).
GIT_SHA="$(git -C "$ROOT" rev-parse --short HEAD)"

echo ">>> Story 1.4 observability bootstrap (BOOTSTRAP MODE)"
echo ">>> Repo root: $ROOT"
echo ">>> Sample-otel-app image tag (M-4 SHA injection): $GIT_SHA"

# ----------------------------------------------------------------------------
# Step 1 — kube-prometheus-stack umbrella (Q1 ruling). Ships ServiceMonitor +
# PrometheusRule CRDs and the Prometheus Operator that watches them.
# ----------------------------------------------------------------------------
echo ""
echo ">>> [1/7] kube-prometheus-stack umbrella"
helm dependency update "$OBS_DIR/kube-prometheus-stack/" >/dev/null
helm upgrade --install kube-prometheus-stack "$OBS_DIR/kube-prometheus-stack/" \
  --namespace monitoring \
  --create-namespace \
  -f "$OBS_DIR/kube-prometheus-stack/values-staging.yaml" \
  --wait \
  --timeout 10m
kubectl wait --for=condition=ready pod \
  --namespace monitoring \
  --selector "release=kube-prometheus-stack" \
  --timeout=300s

# ----------------------------------------------------------------------------
# Step 1.5 — wait for ServiceMonitor CRD Established (Round 1 m-3). All
# downstream charts in steps 2-5 may emit ServiceMonitor objects; admission
# fails if the CRD has not finished registering.
# ----------------------------------------------------------------------------
echo ""
echo ">>> [1.5] kubectl wait for ServiceMonitor CRD Established (Round 1 m-3)"
kubectl wait --for condition=Established crd/servicemonitors.monitoring.coreos.com --timeout=60s

# ----------------------------------------------------------------------------
# Step 2 — loki (SingleBinary, filesystem in emptyDir, 48h retention).
# ----------------------------------------------------------------------------
echo ""
echo ">>> [2/7] loki"
helm dependency update "$OBS_DIR/loki/" >/dev/null
helm upgrade --install loki "$OBS_DIR/loki/" \
  --namespace monitoring \
  --create-namespace \
  -f "$OBS_DIR/loki/values-staging.yaml" \
  --wait \
  --timeout 10m
kubectl wait --for=condition=ready pod \
  --namespace monitoring \
  --selector "app.kubernetes.io/name=loki" \
  --timeout=300s

# ----------------------------------------------------------------------------
# Step 3 — promtail (DaemonSet, Round 1 M-2 explicit release).
# ----------------------------------------------------------------------------
echo ""
echo ">>> [3/7] promtail"
helm dependency update "$OBS_DIR/promtail/" >/dev/null
helm upgrade --install promtail "$OBS_DIR/promtail/" \
  --namespace monitoring \
  --create-namespace \
  -f "$OBS_DIR/promtail/values-staging.yaml" \
  --wait \
  --timeout 10m
kubectl wait --for=condition=ready pod \
  --namespace monitoring \
  --selector "app.kubernetes.io/name=promtail" \
  --timeout=300s

# ----------------------------------------------------------------------------
# Step 4 — jaeger (all-in-one, in-memory; Q2 + M-5: trace backend preserved).
# ----------------------------------------------------------------------------
echo ""
echo ">>> [4/7] jaeger"
helm dependency update "$OBS_DIR/jaeger/" >/dev/null
helm upgrade --install jaeger "$OBS_DIR/jaeger/" \
  --namespace monitoring \
  --create-namespace \
  -f "$OBS_DIR/jaeger/values-staging.yaml" \
  --wait \
  --timeout 10m
kubectl wait --for=condition=ready pod \
  --namespace monitoring \
  --selector "app.kubernetes.io/name=jaeger" \
  --timeout=300s

# ----------------------------------------------------------------------------
# Step 5 — otel-collector (deployment mode; exports OTLP→jaeger:4317 and
# Prometheus :8889 scraped by the umbrella's Prometheus).
# ----------------------------------------------------------------------------
echo ""
echo ">>> [5/7] otel-collector"
helm dependency update "$OBS_DIR/otel-collector/" >/dev/null
helm upgrade --install otel-collector "$OBS_DIR/otel-collector/" \
  --namespace monitoring \
  --create-namespace \
  -f "$OBS_DIR/otel-collector/values-staging.yaml" \
  --wait \
  --timeout 10m
kubectl wait --for=condition=ready pod \
  --namespace monitoring \
  --selector "app.kubernetes.io/name=opentelemetry-collector" \
  --timeout=300s

# ----------------------------------------------------------------------------
# Step 6 — sample-otel-app (he-api-staging namespace; M-4 SHA injection).
# ----------------------------------------------------------------------------
echo ""
echo ">>> [6/7] sample-otel-app (image.tag=$GIT_SHA — Round 1 M-4)"
helm upgrade --install sample-otel-app "$SAMPLE_DIR/" \
  --namespace he-api-staging \
  --create-namespace \
  -f "$SAMPLE_DIR/values-staging.yaml" \
  --set "image.tag=$(git rev-parse --short HEAD)" \
  --wait \
  --timeout 5m
kubectl wait --for=condition=ready pod \
  --namespace he-api-staging \
  --selector "app=sample-otel-app" \
  --timeout=180s

echo ""
echo ">>> [7/7] DONE — 5 observability releases + sample-otel-app installed"
echo ""
echo ">>> Next steps:"
echo "    helm list -n $MONITORING_NS"
echo "    kubectl get pod -n $MONITORING_NS"
echo "    kubectl port-forward svc/kube-prometheus-stack-grafana 3000:80 -n $MONITORING_NS"
echo ""
echo ">>> Grafana admin password:"
echo "    kubectl get secret -n $MONITORING_NS kube-prometheus-stack-grafana -o jsonpath='{.data.admin-password}' | base64 -d"
