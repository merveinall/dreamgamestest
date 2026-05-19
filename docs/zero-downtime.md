# Zero-Downtime Deployment Approach

## Strategy: RollingUpdate with maxUnavailable: 0

The Deployment is configured with:

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0   # never remove a pod until a new one is Ready
    maxSurge: 1         # spin up one extra pod at a time
```

Kubernetes brings up a new pod, waits for its readiness probe to pass, and only then
terminates an old pod. At all times during the rollout the cluster serves at least
`replicas` healthy pods.

## Readiness Probe Gates Traffic

```yaml
readinessProbe:
  httpGet:
    path: /api/foos?val=health
    port: 9001
  initialDelaySeconds: 10
  periodSeconds: 5
  failureThreshold: 3
```

kube-proxy and the Nginx Ingress controller only add a pod to Service endpoints after
the readiness probe succeeds. New pods absorb production traffic only when the Spring
Boot context has fully started.

## Automatic Rollback in Jenkins

The deploy pipeline calls `kubectl rollout undo` in the `post { failure { ... } }` block.
If the smoke test or the rollout-status wait fails, the previous ReplicaSet is immediately
restored.

## Connection Draining

When a pod is terminated, Kubernetes removes it from Service endpoints (stops new requests)
before sending `SIGTERM`. Spring Boot finishes in-flight requests during the graceful-shutdown
window. Add to `application.properties`:

```
server.shutdown=graceful
spring.lifecycle.timeout-per-shutdown-phase=20s
```

## Topology Spread — Node Failure Resilience

With `maxSkew: 1` across 2 worker nodes, a single node failure leaves 2 pods on the
surviving node — the application remains available without manual intervention.

## Pod Disruption Budget (recommended)

Prevents voluntary disruptions (node drain, cluster upgrades) from removing too many pods:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: sample-java-app
  namespace: apps
spec:
  minAvailable: 3
  selector:
    matchLabels:
      app: sample-java-app
```
