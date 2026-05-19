# Dream Games - DevOps Engineering Case Study
- Production-oriented infrastructure-as-code for a kubeadm Kubernetes cluster provisioned by the included Vagrantfile.
- Answers to Step 1 - Question 7, Step 3 and 4 are in [answers documentation](answers.md).
- [Cluster Setup Documentation](docs/cluster-setup.md)
- [Step 2 - Application](docs/step2.md)
---

## Cluster topology

| VM name    | Hostname   | IP          | vCPU | RAM  | Role                  |
|------------|------------|-------------|------|------|-----------------------|
| k8s-master | k8s-master | 10.50.0.10  | 2    | 4 GB | Control Plane         |
| node-1     | node-1     | 10.50.0.11  | 2    | 2 GB | Worker                |
| node-2     | node-2     | 10.50.0.12  | 2    | 2 GB | Worker (Jenkins node) |

**"Node 3"** in requirements = `node-2`, the 3rd VM overall.

---

## Assumptions

| # | Assumption | Reason |
|---|-----------|--------|
| 1 | Jenkins pinned to `node-2` | "Node 3" = 3rd VM (`node-2`, IP 10.50.0.12) |
| 2 | CNI: Flannel | Installed during cluster setup; `--iface-can-reach=10.50.0.10` flag applied to fix VXLAN on VirtualBox NAT interfaces |
| 3 | Ingress: nginx-ingress-controller | Standard, works with MetalLB out of the box |
| 4 | StorageClass: `local-path` | No cloud storage in Vagrant; Rancher local-path-provisioner is the simplest fit |
| 5 | ExternalDNS provider: CoreDNS + dedicated etcd | RFC2136 requires an external BIND server unavailable in this setup. A lightweight etcd is deployed on master; CoreDNS is patched with the etcd plugin to serve `*.local` records written by ExternalDNS |
| 6 | MetalLB IP pool: `10.50.0.20–10.50.0.30` | Within the Vagrant private network; free range above the node IPs |
| 7 | Domain suffix: `.local` | Standard convention for dev clusters |
| 8 | Elasticsearch single-node | Workers have only 2 GB RAM; single-node ES with 512 MB JVM heap fits |

---

## Repository structure

```
dreamgames/
├── Vagrantfile                                    # 3-VM cluster definition (existing)
├── Dockerfile                                     # Multi-stage build for SampleJavaApp
├── .dockerignore
│
├── answers/
│   └── README.md                                  # Step 1 Q7 and Step 3 Q1–Q4 answers
│
├── cluster/
│   └── kubeadm-config.yaml                        # kubeadm ClusterConfiguration + InitConfiguration
│
├── docs/
│   ├── cluster-setup.md                           # Step-by-step cluster installation walkthrough
│   └── dockerfile-design.md                       # Layer-cache strategy, size and security decisions
│
├── scripts/
│   ├── 08-deploy-monitoring.sh                    # Deploys kube-prometheus-stack + alert rules + dashboard
│   └── 09-deploy-logging.sh                       # Deploys Elasticsearch + Fluent Bit
│
├── kubernetes/
│   ├── namespaces.yaml                            # All namespaces: jenkins, monitoring, logging, apps, metallb-system, external-dns
│   │
│   ├── metallb/
│   │   ├── values.yaml                            # MetalLB Helm values
│   │   └── ipaddresspool.yaml                     # L2 IP pool 10.50.0.20–10.50.0.30 + L2Advertisement
│   │
│   ├── external-dns/
│   │   ├── rbac.yaml                              # ServiceAccount + ClusterRole + ClusterRoleBinding
│   │   ├── etcd.yaml                              # Dedicated etcd backend: Service + Deployment (pinned to master)
│   │   ├── deployment.yaml                        # ExternalDNS v0.14.2 with CoreDNS provider (pinned to master)
│   │   ├── coredns-local-zone.yaml                # CoreDNS ConfigMap patch: adds local. zone with etcd plugin
│   │   └── secret.yaml                            # RFC2136 TSIG key (kept for reference; not used with CoreDNS)
│   │
│   ├── jenkins/
│   │   ├── rbac.yaml                              # ServiceAccount + ClusterRole + ClusterRoleBinding
│   │   ├── secret.yaml                            # Admin credentials (injected into JCasC via env vars)
│   │   ├── pvc.yaml                               # 20 Gi ReadWriteOnce PVC on local-path
│   │   ├── configmap-jcasc.yaml                   # Jenkins Configuration as Code: security realm, Kubernetes cloud
│   │   ├── deployment.yaml                        # jenkins/jenkins:lts-jdk21, pinned to node-2, plugin init containers
│   │   └── service.yaml                           # LoadBalancer HTTP (jenkins.local) + headless ClusterIP for agents
│   │
│   ├── monitoring/
│   │   ├── values-kube-prometheus-stack.yaml      # Prometheus + AlertManager + Grafana Helm values
│   │   ├── alert-rules.yaml                       # PrometheusRule: PodRestartingFrequently + PodCrashLooping
│   │   └── grafana-dashboard-cm.yaml              # 6-panel K8s overview dashboard (auto-loaded via sidecar)
│   │
│   ├── logging/
│   │   ├── values-elasticsearch.yaml              # Single-node Elasticsearch 8.x Helm values
│   │   └── values-fluent-bit.yaml                 # Fluent Bit DaemonSet Helm values
│   │
│   └── apps/
│       ├── namespace.yaml                         # apps namespace
│       ├── deployment.yaml                        # sample-java-app: 2 replicas, image merveinal1/sample-java-app:v1.0
│       ├── service.yaml                           # ClusterIP, port 9001
│       └── ingress.yaml                           # host app.local
```

---

## How each requirement is satisfied

### 1. Application Dockerfile
- `Dockerfile` — two-stage build: `eclipse-temurin:11-jdk-alpine` builds the JAR, `eclipse-temurin:11-jre-alpine` runs it as non-root `appuser`, port 9001
- `.dockerignore` — excludes `target/`, `.git/`, all infra directories
- `docs/dockerfile-design.md` — documents layer-cache ordering, JDK→JRE size reduction, and non-root security decision
- Image published to DockerHub: `merveinal1/sample-java-app:v1.0`

### 2. Kubernetes cluster as code
- `cluster/kubeadm-config.yaml` — Kubernetes v1.29.4, controlPlaneEndpoint `10.50.0.10:6443`, pod CIDR `10.10.0.0/16`, service CIDR `10.20.0.0/16`
- `docs/cluster-setup.md` — full manual walkthrough: prerequisites, `kubeadm init`, CNI, worker join, MetalLB, nginx-ingress, storage, ExternalDNS

### 3. ExternalDNS

**Architecture:**
```
Service / Ingress annotation (external-dns.alpha.kubernetes.io/hostname)
  ↓  ExternalDNS writes DNS record
etcd-external-dns  (port 2379, Deployment on master)
  ↓  CoreDNS reads via etcd plugin
CoreDNS — serves *.local queries cluster-wide
```

- `kubernetes/external-dns/rbac.yaml` — least-privilege RBAC (list/watch services, endpoints, nodes, ingresses)
- `kubernetes/external-dns/etcd.yaml` — single-node etcd Service + Deployment, pinned to master with `registry.k8s.io/etcd:3.5.10-0` (image already present from kubeadm, no pull needed)
- `kubernetes/external-dns/deployment.yaml` — ExternalDNS `--provider=coredns`, `--domain-filter=local.`, co-located with etcd on master; `wait-for-etcd` init container prevents start before etcd is healthy
- `kubernetes/external-dns/coredns-local-zone.yaml` — patches the `coredns` ConfigMap to add a `local.:53` block with the etcd plugin; must be applied after etcd ClusterIP is known

**DNS annotation used across the cluster:**
```yaml
annotations:
  external-dns.alpha.kubernetes.io/hostname: jenkins.local
```

### 4. Jenkins on Kubernetes
Plain `kubectl apply` manifests — no Helm:
- `rbac.yaml` — ServiceAccount with pod/exec/log/secret permissions for the Kubernetes cloud agent plugin
- `secret.yaml` — admin credentials, injected into JCasC via `JENKINS_ADMIN_USER` / `JENKINS_ADMIN_PASSWORD` env vars
- `pvc.yaml` — 20 Gi ReadWriteOnce on `local-path` StorageClass
- `configmap-jcasc.yaml` — Jenkins Configuration as Code: local security realm, `loggedInUsersCanDoAnything`, Kubernetes cloud pointing to `jenkins.jenkins.svc.cluster.local`
- `deployment.yaml` — `jenkins/jenkins:lts-jdk21`, pinned to `node-2` via `nodeSelector` + `dedicated=jenkins:NoSchedule` toleration, two init containers: `init-permissions` (chown `/var/jenkins_home`) and `install-plugins` (pre-installs kubernetes, workflow-aggregator, git, configuration-as-code, credentials-binding, github-branch-source, docker-workflow), `strategy: Recreate` to avoid PVC conflicts
- `service.yaml` — LoadBalancer port 80→8080 with ExternalDNS annotation (`jenkins.local`); headless ClusterIP for JNLP agent port 50000

### 5. Monitoring and Logging

**Monitoring** — `bash scripts/08-deploy-monitoring.sh`:
- `values-kube-prometheus-stack.yaml` — Prometheus (`/prometheus`), AlertManager (`/alertmanager`), Grafana (`/grafana`) all under `monitoring.local`; Prometheus storage 10 Gi, AlertManager storage 2 Gi; Grafana sidecar auto-loads ConfigMaps with `grafana_dashboard: "1"`
- `alert-rules.yaml` — two PrometheusRules: `PodRestartingFrequently` (>0.5 restarts/min over 15 min, severity warning) and `PodCrashLooping` (CrashLoopBackOff, severity critical)
- `grafana-dashboard-cm.yaml` — 6 panels: running pod count, node count, CPU by node, memory by node, pod restart rate (with thresholds), sample-java-app request rate

**Logging** — `bash scripts/09-deploy-logging.sh`:
- `values-elasticsearch.yaml` — single-node ES 8.x, `discovery.type: single-node`, security disabled, 512 MB JVM heap, ingress at `monitoring.local/elasticsearch` with nginx rewrite-target
- `values-fluent-bit.yaml` — DaemonSet on all nodes (including control-plane), tails `/var/log/containers/*.log`, enriches with Kubernetes metadata, outputs to ES with `Suppress_Type_Name On` (required for ES 8.x), index pattern `kubernetes-YYYY.MM.DD`

### 6. Sample Java App
- `kubernetes/apps/deployment.yaml` — 2 replicas, `merveinal1/sample-java-app:v1.0`, port 9001
- `kubernetes/apps/service.yaml` — ClusterIP
- `kubernetes/apps/ingress.yaml` — host `app.local`, path `/`

---

## Deployment steps (in order, run on k8s-master)

```bash
# 1. All namespaces
kubectl apply -f kubernetes/namespaces.yaml

# 2. MetalLB
helm repo add metallb https://metallb.github.io/metallb && helm repo update
helm upgrade --install metallb metallb/metallb \
  -n metallb-system -f kubernetes/metallb/values.yaml
kubectl apply -f kubernetes/metallb/ipaddresspool.yaml

# 3. nginx Ingress Controller
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx && helm repo update
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  -n ingress-nginx --create-namespace \
  --set controller.service.type=LoadBalancer

# 4. local-path StorageClass
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.26/deploy/local-path-storage.yaml
kubectl patch storageclass local-path \
  -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'

# 5. ExternalDNS
kubectl apply -f kubernetes/external-dns/rbac.yaml
kubectl apply -f kubernetes/external-dns/etcd.yaml

# Wait for etcd, then patch CoreDNS with the etcd ClusterIP
kubectl rollout status deployment/etcd-external-dns -n external-dns
ETCD_IP=$(kubectl get svc etcd-external-dns -n external-dns \
  -o jsonpath='{.spec.clusterIP}')
sed "s/<ETCD_CLUSTER_IP>/$ETCD_IP/" \
  kubernetes/external-dns/coredns-local-zone.yaml | kubectl apply -f -
kubectl rollout restart deployment/coredns -n kube-system
kubectl apply -f kubernetes/external-dns/deployment.yaml

# 6. Jenkins
kubectl apply -f kubernetes/jenkins/rbac.yaml
kubectl apply -f kubernetes/jenkins/pvc.yaml
kubectl apply -f kubernetes/jenkins/secret.yaml
kubectl apply -f kubernetes/jenkins/configmap-jcasc.yaml
kubectl apply -f kubernetes/jenkins/deployment.yaml
kubectl apply -f kubernetes/jenkins/service.yaml

# 7. Monitoring (Prometheus + AlertManager + Grafana)
bash scripts/08-deploy-monitoring.sh

# 8. Logging (Elasticsearch + Fluent Bit)
bash scripts/09-deploy-logging.sh

# 9. Sample Java App
kubectl apply -f kubernetes/apps/
```

---

## Access URLs

Get the nginx-ingress LoadBalancer IP and add it to `/etc/hosts`:

```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

```
# /etc/hosts  (Linux/Mac)
# C:\Windows\System32\drivers\etc\hosts  (Windows)
<INGRESS_IP>  monitoring.local jenkins.local app.local
```

| Service       | URL                                   | Credentials      |
|---------------|---------------------------------------|------------------|
| Prometheus    | http://monitoring.local/prometheus    | —                |
| AlertManager  | http://monitoring.local/alertmanager  | —                |
| Grafana       | http://monitoring.local/grafana       | admin / admin123 |
| Elasticsearch | http://monitoring.local/elasticsearch | —                |
| Jenkins       | http://jenkins.local                  | admin / adminadmin |
| Java App      | http://app.local/api/foos?val=TEST    | —                |

---

## Application image

```
merveinal1/sample-java-app:v1.0
```

Already built and pushed to DockerHub. The `Dockerfile` in this repo documents how it was built and can reproduce the image. See `docs/dockerfile-design.md` for build details.

---

## Credentials (change after first login)

| Component | File | Default value |
|-----------|------|---------------|
| Jenkins admin | `kubernetes/jenkins/secret.yaml` | `adminadmin` |
| Grafana admin | `kubernetes/monitoring/values-kube-prometheus-stack.yaml` | `admin123` |

---

## Known issues and fixes applied to the live cluster

| Issue | Cause | Fix applied |
|-------|-------|-------------|
| Flannel VXLAN cross-node failure | All VMs share `10.0.2.15` on `eth0` (VirtualBox NAT) | `--iface-can-reach=10.50.0.10` arg patched onto the `kube-flannel-ds` DaemonSet |
| Jenkins pod Pending | Node-2 has `dedicated=jenkins:NoSchedule` taint | `tolerations` added to `deployment.yaml` |
| Jenkins plugins failed to install | `lts-jdk11` image (v2.462.3) too old for current plugin versions | Switched to `lts-jdk21` |
| CoreDNS broke after etcd plugin patch | CoreDNS couldn't start when etcd wasn't ready yet | Applied etcd plugin only after etcd was confirmed healthy; used ClusterIP instead of DNS name in endpoint |
| Elasticsearch readiness probe failing | `secret.enabled: false` removed the credential secret; probe script exits if `ELASTIC_PASSWORD` is unset | Removed `secret.enabled: false`; chart creates the secret and probe works even with security disabled |
| MetalLB / nginx-ingress webhook timeout | Validating webhook unreachable because API server couldn't route to pod IP across nodes | Deleted `ValidatingWebhookConfiguration` (`metallb-webhook-configuration`, `ingress-nginx-admission`); webhooks re-register on next reconcile |
| All nodes reporting `INTERNAL-IP: 10.0.2.15` | kubelet binding to VirtualBox NAT interface instead of private network | Added `KUBELET_EXTRA_ARGS="--node-ip=10.50.0.x"` to `/etc/default/kubelet` on each node and restarted kubelet |
| ExternalDNS RFC2136 i/o timeout | No BIND server running at `10.50.0.1:53` in Vagrant environment | Switched ExternalDNS to `--provider=coredns`; deployed dedicated etcd in `external-dns` namespace; added etcd plugin to CoreDNS ConfigMap |
| Elasticsearch memory limit warning (`fractional byte value`) | Helm values used `"1.2Gi"` which Kubernetes interprets as milli-bytes | Changed to `"1300Mi"` (integer Mi value) |
| Elasticsearch CrashLoopBackOff on node-2 | Chart injects `xpack.security.enabled: true` env vars overriding `esConfig` values | Added `extraEnvs` in values to explicitly set all xpack security flags to `"false"` |
| Elasticsearch scheduled on overloaded node-1 (2 GB RAM) | No node affinity set; scheduler placed pod on node-1 already running 12+ pods | Added `nodeSelector: kubernetes.io/hostname: node-2` and `tolerations` for `dedicated=jenkins:NoSchedule` to Elasticsearch Helm values |
| node-1 NotReady / containerd deadlock | Memory/IO exhaustion caused containerd to hang; kubelet lost contact with API server | `vagrant reload node-1` (force); restarted `containerd` and `kubelet` after VM came back up |
