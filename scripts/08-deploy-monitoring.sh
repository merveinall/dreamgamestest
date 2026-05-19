#!/usr/bin/env bash
# Deploy kube-prometheus-stack (Prometheus + AlertManager + Grafana)
# Run on k8s-master after the cluster and nginx-ingress are up.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

echo "==> [1/4] Adding Helm repo..."
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

echo "==> [2/4] Installing kube-prometheus-stack (this takes ~5 min)..."
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --values "$REPO_ROOT/kubernetes/monitoring/values-kube-prometheus-stack.yaml" \
  --timeout 10m \
  --wait

echo "==> [3/4] Applying PrometheusRule (pod restart / crash-loop alerts)..."
kubectl apply -f "$REPO_ROOT/kubernetes/monitoring/alert-rules.yaml"

echo "==> [4/4] Applying Grafana dashboard ConfigMap..."
kubectl apply -f "$REPO_ROOT/kubernetes/monitoring/grafana-dashboard-cm.yaml"

echo ""
echo "Monitoring stack is up."
echo "  Prometheus  : http://monitoring.local/prometheus"
echo "  AlertManager: http://monitoring.local/alertmanager"
echo "  Grafana     : http://monitoring.local/grafana  (admin / admin123)"
