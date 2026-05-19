# Kubernetes Cluster Setup Guide

This document describes how the dreamgames cluster was set up and how to recreate it.

## Cluster summary

| Node       | IP           | Role             |
|------------|--------------|------------------|
| k8s-master | 10.50.0.10   | Control Plane    |
| node-1     | 10.50.0.11   | Worker           |
| node-2     | 10.50.0.12   | Worker (Jenkins) |

| Component         | Choice                           |
|-------------------|----------------------------------|
| Kubernetes        | v1.29.4                          |
| OS                | Ubuntu 20.04 (ubuntu/focal64)    |
| Container runtime | containerd                       |
| CNI               | Flannel (VXLAN)                  |
| Load Balancer     | MetalLB L2, pool 10.50.0.20–10.50.0.30 |
| Ingress           | nginx-ingress-controller         |
| Storage           | local-path-provisioner (Rancher) |
| DNS               | ExternalDNS + CoreDNS + etcd     |
| Pod CIDR          | 10.244.0.0/16 (Flannel default)  |
| Service CIDR      | 10.20.0.0/16                     |

---

## Step 1 — Prerequisites (all nodes)

Run on **k8s-master**, **node-1**, and **node-2**:

```bash
# Disable swap (required by kubelet)
sudo swapoff -a
sudo sed -i '/swap/d' /etc/fstab

# Load required kernel modules
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF
sudo modprobe overlay
sudo modprobe br_netfilter

# Enable IP forwarding
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
sudo sysctl --system

# Install containerd
sudo apt-get update && sudo apt-get install -y containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
sudo systemctl restart containerd

# Install kubeadm, kubelet, kubectl (v1.29)
sudo apt-get install -y apt-transport-https ca-certificates curl gpg
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.29/deb/Release.key \
  | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] \
  https://pkgs.k8s.io/core:/stable:/v1.29/deb/ /' \
  | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo apt-get update
sudo apt-get install -y kubelet=1.29.4-1.1 kubeadm=1.29.4-1.1 kubectl=1.29.4-1.1
sudo apt-mark hold kubelet kubeadm kubectl
```

---

## Step 2 — Initialise the control plane (k8s-master only)

```bash
sudo kubeadm init \
  --apiserver-advertise-address=10.50.0.10 \
  --pod-network-cidr=10.244.0.0/16 \
  --service-cidr=10.20.0.0/16

# Configure kubectl for the local user
mkdir -p $HOME/.kube
sudo cp /etc/kubernetes/admin.conf $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config
```

---

## Step 3 — Install Flannel CNI (k8s-master)

```bash
kubectl apply -f https://raw.githubusercontent.com/flannel-io/flannel/master/Documentation/kube-flannel.yml
```

**VirtualBox-specific fix (required):**
VirtualBox gives every VM the same `10.0.2.15` IP on `eth0` (NAT interface). Flannel picks this as its public IP, which breaks VXLAN tunnels between nodes. The fix makes Flannel auto-detect the correct interface by specifying an IP that is reachable only via the private network interface (`eth1`/`enp0s8`):

```bash
kubectl patch daemonset kube-flannel-ds -n kube-flannel \
  --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--iface-can-reach=10.50.0.10"}]'

# Verify: all nodes should now show their correct private IP
kubectl logs -n kube-flannel -l app=flannel | grep "public IP"
```

Waited for all nodes to be Ready.


---

## Step 4 — Join worker nodes

After `kubeadm init` completes, it prints a `kubeadm join` command. Run it on **node-1** and **node-2**:

```bash
# Example (tokens will differ in your run):
sudo kubeadm join 10.50.0.10:6443 \
  --token <token> \
  --discovery-token-ca-cert-hash sha256:<hash>
```

---

## Step 5 — Install MetalLB

```bash
helm repo add metallb https://metallb.github.io/metallb && helm repo update
helm upgrade --install metallb metallb/metallb \
  -n metallb-system --create-namespace \
  -f kubernetes/metallb/values.yaml

# After pods are Ready, apply the IP pool
kubectl apply -f kubernetes/metallb/ipaddresspool.yaml
```

---

## Step 6 — Install nginx Ingress Controller

```bash
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx && helm repo update
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  -n ingress-nginx --create-namespace \
  --set controller.service.type=LoadBalancer
```

MetalLB assigns an IP from `10.50.0.20–10.50.0.30` to the ingress service. Verify:
```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller
```

---

## Step 7 — Install local-path-provisioner

```bash
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.26/deploy/local-path-storage.yaml

kubectl patch storageclass local-path \
  -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
```

---

## Step 8 — Deploy all namespaces

```bash
kubectl apply -f kubernetes/namespaces.yaml
```

---

## Step 9 — Deploy ExternalDNS

ExternalDNS uses the CoreDNS provider backed by a dedicated etcd instance. Both are pinned to the master node.

```bash
kubectl apply -f kubernetes/external-dns/rbac.yaml
kubectl apply -f kubernetes/external-dns/etcd.yaml

# Wait for etcd to be ready
kubectl rollout status deployment/etcd-external-dns -n external-dns

# Get the etcd ClusterIP and patch CoreDNS to add the local. zone
ETCD_IP=$(kubectl get svc etcd-external-dns -n external-dns \
  -o jsonpath='{.spec.clusterIP}')
sed "s/<ETCD_CLUSTER_IP>/$ETCD_IP/" \
  kubernetes/external-dns/coredns-local-zone.yaml | kubectl apply -f -
kubectl rollout restart deployment/coredns -n kube-system

# Deploy ExternalDNS itself
kubectl apply -f kubernetes/external-dns/deployment.yaml
```

---

## Step 10 — Deploy Jenkins

```bash
kubectl apply -f kubernetes/jenkins/rbac.yaml
kubectl apply -f kubernetes/jenkins/pvc.yaml
kubectl apply -f kubernetes/jenkins/secret.yaml
kubectl apply -f kubernetes/jenkins/configmap-jcasc.yaml
kubectl apply -f kubernetes/jenkins/deployment.yaml
kubectl apply -f kubernetes/jenkins/service.yaml

# Plugin installation takes ~3 minutes; follow progress with:
kubectl logs -n jenkins -l app=jenkins -c install-plugins -f
```

---

## Step 11 — Deploy monitoring

```bash
bash scripts/08-deploy-monitoring.sh
```

Deploys: Prometheus, AlertManager, Grafana, kube-state-metrics, node-exporter.
Also applies `alert-rules.yaml` (pod restart/crash alerts) and `grafana-dashboard-cm.yaml`.

---

## Step 12 — Deploy logging

```bash
bash scripts/09-deploy-logging.sh
```

Deploys: Elasticsearch (single-node) and Fluent Bit (DaemonSet on all nodes).

---

## Step 13 — Deploy sample Java app

```bash
kubectl apply -f kubernetes/apps/
```

---

## /etc/hosts (workstation)

Get the ingress IP and add it to your local machine:

```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

```
# Linux/Mac: /etc/hosts
# Windows:   C:\Windows\System32\drivers\etc\hosts
<INGRESS_IP>  monitoring.local jenkins.local app.local
```

---

## Known issues encountered during setup

| Issue | Cause | Fix |
|-------|-------|-----|
| Flannel cross-node VXLAN failure | All VMs share `10.0.2.15` on `eth0` (VirtualBox NAT) | `--iface-can-reach=10.50.0.10` patched onto `kube-flannel-ds` DaemonSet |
| Jenkins pod Pending | node-2 has taint `dedicated=jenkins:NoSchedule` | `tolerations` added to `kubernetes/jenkins/deployment.yaml` |
| Jenkins plugins failed to install | `lts-jdk11` image (v2.462.3) too old for current plugin versions | Switched to `lts-jdk21` |
| CoreDNS crashed after etcd plugin patch | CoreDNS couldn't resolve its own endpoint at startup | Applied etcd plugin only after etcd was confirmed healthy; used ClusterIP (not DNS name) in the endpoint |
| Elasticsearch readiness probe failing | `secret.enabled: false` removed the credential secret; probe script exits if `ELASTIC_PASSWORD` is missing | Removed `secret.enabled: false`; probe works because ES ignores auth headers when security is disabled |
