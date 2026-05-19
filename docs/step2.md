# Step 2 — Application Deployment & Pipelines

## 1. Deployment

I deployed the application with 4 replicas so that both worker nodes run pods at all times.

**a. Even distribution across nodes**

I added a `topologySpreadConstraints` block to the Deployment with `maxSkew: 1` and `topologyKey: kubernetes.io/hostname`. This prevents Kubernetes from stacking pods on a single node — each worker node always gets exactly 2 pods. If a placement would violate the skew, Kubernetes holds the pod pending rather than scheduling it unevenly (`whenUnsatisfiable: DoNotSchedule`).

**b. Readiness & liveness probes**

- The `readinessProbe` hits `/api/foos?val=health` before the pod is added to the Service endpoints, so it only receives traffic once Spring Boot is fully started.
- The `livenessProbe` hits the same path on a longer interval; if it fails 3 times in a row, Kubernetes restarts the container automatically.

Load balancing is handled by the `sample-java-app` ClusterIP Service in front of the pods, with Nginx Ingress as the external entry point.

---

## 2. Build Pipeline

I wrote [jenkins/Jenkinsfile.build](../jenkins/Jenkinsfile.build) for the CI pipeline. Steps:

1. Checkout source
2. Run unit tests (`mvn test`)
3. Build the JAR (`mvn clean package -DskipTests`)
4. Build the Docker image tagged with `BUILD_NUMBER`
5. Push to Docker Hub (`merveinal1/sample-java-app:<tag>` and `:latest`)

Credentials are injected via a Jenkins `usernamePassword` credential (`dockerhub-credentials`) — no secrets in the Jenkinsfile.

---

## 3. Deployment Pipeline

I wrote [jenkins/Jenkinsfile.deploy](../jenkins/Jenkinsfile.deploy) for the CD pipeline.

**a. Ansible applies the manifests**

I used an Ansible playbook ([ansible/deploy-playbook.yaml](../ansible/deploy-playbook.yaml)) with the `kubernetes.core.k8s` module. The playbook runs on the control-plane node and applies the namespace, Deployment, Service, and Ingress manifests in order, then waits for the Deployment to reach `Available` status.

**b. Hostname access via Ingress**

The app is reachable at `http://app.local` through the Nginx Ingress defined in `kubernetes/apps/ingress.yaml`. ExternalDNS handles the DNS record automatically via the annotation on the Service.

**c. Zero-downtime deployments**

I configured `RollingUpdate` with `maxUnavailable: 0` and `maxSurge: 1`. Kubernetes always brings up a new pod and waits for its readiness probe to pass before it terminates an old one — there is never a moment with fewer than 4 healthy pods serving traffic. If the rollout or smoke test fails, the pipeline automatically calls `kubectl rollout undo` to restore the previous ReplicaSet. Full details are in [docs/zero-downtime.md](zero-downtime.md).

---

## 4. Validation Webhook

I wrote a custom `ValidatingWebhookConfiguration` in Go ([webhook/main.go](../webhook/main.go)) that rejects any Deployment missing CPU or memory `requests` on any container.

**a. Namespace control via ConfigMap**

Rather than hardcoding namespaces in the webhook configuration, the webhook reads [webhook/kubernetes/configmap.yaml](../webhook/kubernetes/configmap.yaml) at runtime. The `enabled-namespaces` key holds a comma-separated list (e.g. `apps,default`). The list is cached for 30 seconds to avoid hitting the API on every admission request. Updating the ConfigMap takes effect without redeploying the webhook.

**b. Prometheus metrics**

The webhook exposes a plain HTTP `/metrics` endpoint on port 8080, scraped by Prometheus via a `ServiceMonitor`. Exposed metrics:

| Metric | Description |
|---|---|
| `resource_webhook_requests_total` | Total requests, by operation and namespace |
| `resource_webhook_allowed_total` | Allowed requests, by namespace |
| `resource_webhook_denied_total` | Denied requests, by namespace and reason |
| `resource_webhook_duration_seconds` | Request processing latency histogram |
