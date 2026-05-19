#!/usr/bin/env bash
# Deploy Elasticsearch + Fluent Bit (logging namespace)
# Run on k8s-master after 08-deploy-monitoring.sh completes.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

echo "==> [1/3] Adding Helm repos..."
helm repo add elastic https://helm.elastic.co
helm repo add fluent  https://fluent.github.io/helm-charts
helm repo update

echo "==> [2/3] Installing Elasticsearch (single-node, no auth)..."
helm upgrade --install elasticsearch elastic/elasticsearch \
  --namespace logging \
  --values "$REPO_ROOT/kubernetes/logging/values-elasticsearch.yaml" \
  --timeout 10m \
  --wait

echo "==> Waiting for Elasticsearch StatefulSet to roll out..."
kubectl rollout status statefulset/elasticsearch-master -n logging --timeout=300s

echo "==> Checking Elasticsearch health..."
kubectl exec -n logging statefulset/elasticsearch-master -- \
  curl -s http://localhost:9200/_cluster/health?pretty | grep '"status"'

echo "==> [3/3] Installing Fluent Bit..."
helm upgrade --install fluent-bit fluent/fluent-bit \
  --namespace logging \
  --values "$REPO_ROOT/kubernetes/logging/values-fluent-bit.yaml" \
  --timeout 5m \
  --wait

echo ""
echo "Logging stack is up."
echo "  Elasticsearch UI  : http://monitoring.local/elasticsearch"
echo "  Fluent Bit ships all container logs → index kubernetes-YYYY.MM.DD"
echo ""
echo "Verify log ingestion (run from master):"
echo "  kubectl exec -n logging statefulset/elasticsearch-master -- \\"
echo "    curl -s 'http://localhost:9200/_cat/indices?v'"
