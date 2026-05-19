# Step 1 — Answers

## Question 7

> This system reads logs through stdout and forwards them to Elasticsearch. However, we've noticed that printing logs to stdout negatively impacts performance. Please explain how you would design and implement the following:
>
> a. Write application logs to a file asynchronously.
> b. Ensure the log file size does not exceed 1 GB.
> c. Rotate log files daily.

---

### a. Asynchronous File Logging

I will implement Log4j2 for asynchronous logging using its **Async Appender** feature. The appender queues log events in memory and writes them on a separate background thread, so the application thread is never blocked by I/O.

```java
import org.apache.logging.log4j.LogManager;
import org.apache.logging.log4j.Logger;

public class TransactionService {

    private static final Logger logger = LogManager.getLogger(TransactionService.class);

    public void processTransaction() {
        try {
            // Application logic runs here...

            // This log event is queued in memory instantly.
            // It does not block the application thread.
            logger.info("Transaction processed successfully.");

        } catch (Exception e) {
            logger.error("Transaction failed", e);
        }
    }
}
```

---

### b. 1 GB File Size Limit

`RollingFileAppender` in `log4j2.xml` is configured with a **`SizeBasedTriggeringPolicy`**. It actively monitors the current log file size; when the file reaches 1 GB it archives the current file (optionally compressed) and opens a new one.

---

### c. Daily Log Rotation

`RollingFileAppender` is also configured with a **`TimeBasedTriggeringPolicy`**. The date pattern (`%d{yyyy-MM-dd}`) in the `filePattern` attribute causes a new file to be created at midnight every day.

---

### b & c — Combined `log4j2.xml` Example

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Configuration status="WARN">
    <Appenders>

        <!-- Handles the 1 GB size limit and daily rotation -->
        <RollingFile name="RollingFile"
                     fileName="logs/app.log"
                     filePattern="logs/app-%d{yyyy-MM-dd}-%i.log.gz">

            <PatternLayout pattern="%d{yyyy-MM-dd HH:mm:ss} [%t] %-5level %logger{36} - %msg%n"/>

            <Policies>
                <!-- Requirement C: rotates the file daily at midnight -->
                <TimeBasedTriggeringPolicy interval="1" modulate="true"/>

                <!-- Requirement B: rotates the file immediately if it reaches 1 GB -->
                <SizeBasedTriggeringPolicy size="1 GB"/>
            </Policies>

        </RollingFile>

        <!-- Requirement A: wraps RollingFile to make all writes fully asynchronous -->
        <Async name="AsyncAppender">
            <AppenderRef ref="RollingFile"/>
        </Async>

    </Appenders>

    <Loggers>
        <Root level="info">
            <AppenderRef ref="AsyncAppender"/>
        </Root>
    </Loggers>
</Configuration>
```

---

# Step 3 — Answers

## Question 1

> Our cluster has a limited number of nodes, and we can't scale them. 2 different apps are running on these nodes.
> App X: A real-time application that requires high availability and performance.
> App Y: An application that manages batch jobs and can be killed or paused without significant issues.
> Given this scenario, how would you ensure that App X can scale effectively under heavy load while considering the limited resources and the presence of App Y?

---

PriorityClasses will resolve this problem.

App X - High Priority

App Y - Low Priority

```yaml
# PriorityClass for App X (Critical / High Availability)
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: realtime-high-priority
value: 1000000
globalDefault: false
description: "This priority class should be used for App X. It will preempt lower priority pods if necessary."
---
# PriorityClass for App Y (Batch / Expendable)
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: batch-low-priority
value: 1000
globalDefault: false
description: "This priority class should be used for App Y. These pods can be killed to make room for App X."
```

**Usage in Application**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-x-realtime
spec:
  replicas: 3 # Managed by HPA dynamically under load
  selector:
    matchLabels:
      app: app-x
  template:
    metadata:
      labels:
        app: app-x
    spec:
      priorityClassName: realtime-high-priority # <--- Attached here
      containers:
      - name: app-x-container
        image: your-registry/app-x:latest
        resources:
          requests:
            cpu: "500m"
            memory: "1Gi"
          limits:
            cpu: "1000m"
            memory: "2Gi"
```

---

## Question 2

> Suppose our application experiences significantly higher traffic during specific periods compared to regular times, and we want to ensure users do not experience increased response times.
> a. How would you ensure the application scale-out and scale-in appropriately before and after these periods?
> b. If you notice there aren't enough nodes in the cluster, how would you increase the number of nodes before these peak times without slowing down the system?

---

### a. Pre-scaling with KEDA Cron Trigger

We will deploy KEDA and utilize its cron trigger.

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: app-x-cron-scaler
spec:
  scaleTargetRef:
    name: app-x-deployment
  minReplicaCount: 3 # Normal baseline traffic
  maxReplicaCount: 20
  triggers:
  - type: cron
    metadata:
      timezone: Europe/Istanbul
      # Scale out at 08:45, scale in at 17:15
      start: "45 8 * * *"
      end: "15 17 * * *"
      desiredReplicas: "15" # Proactively scales to 15 before the 09:00 peak
```

---

### b. Node Pre-warming with Balloon Pods

We create a deployment of "dummy" pods and assign them a very low PriorityClass. We use KEDA to schedule these balloon pods to scale up 30-45 minutes before the application peak. They consume the cluster's available capacity, forcing the Cluster Autoscaler to boot up new physical nodes in advance. Later, when the actual application scales up, the K8s scheduler will instantly preempt (evict) the low-priority balloon pods, giving App immediate access to the pre-warmed nodes.

```yaml
# 1. Define the low priority class
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: balloon-low-priority
value: -100 # Negative value ensures it gets preempted by App X
globalDefault: false
description: "Used to warm up nodes before peak hours."
---
# 2. Deploy the Balloon (Pause) Pods
apiVersion: apps/v1
kind: Deployment
metadata:
  name: node-warmup-balloon
spec:
  replicas: 0 # Baseline is 0
  selector:
    matchLabels:
      app: node-warmup
  template:
    metadata:
      labels:
        app: node-warmup
    spec:
      priorityClassName: balloon-low-priority
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        resources:
          requests:
            cpu: "2000m" # Requests enough resources to force a new node to boot
            memory: "4Gi"
---
# 3. KEDA Scaler for the Balloon Pods
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: balloon-cron-scaler
spec:
  scaleTargetRef:
    name: node-warmup-balloon
  minReplicaCount: 0
  maxReplicaCount: 10
  triggers:
  - type: cron
    metadata:
      timezone: Europe/Istanbul
      # Start warming nodes at 08:15, release them at 17:30
      start: "15 8 * * *"
      end: "30 17 * * *"
      desiredReplicas: "5"
```

---

## Question 3

> We have an application that is critical, and any issues with the new version could impact users.
> a. What deployment strategy would you use to minimize risk when deploying a new version of this critical application?
> b. Describe how you would implement a traffic shift from the old version to the new version.

---

### a. Canary Deployment Strategy

The most robust approach to minimize risk is a **Canary Deployment** strategy.
In a Canary deployment, the new version of the application is deployed alongside the stable version, but it initially receives only a tiny fraction of the live user traffic (1% or 5%).

**How this mitigates potential issues:**
- **Minimized Blast Radius:** If the new version contains a critical bug, memory leak, or performance degradation, only that small subset of users (5%) is impacted, rather than the entire user base experiencing a global outage.
- **Real-World Validation:** Unlike staging environments, a Canary release tests the new code against real production data, actual user behavior, and peak network conditions, surfacing edge cases that synthetic tests might miss.
- **Rapid Rollback:** Because the old, stable version is still running and serving 95% of the traffic, reverting is instantaneous. You simply route the 5% back to the stable version without waiting for pod termination or redeployments.

---

### b. Progressive Traffic Shift with Argo Rollouts

The traffic shift should be handled dynamically via a Service Mesh (like OpenShift Service Mesh/Istio) or an ingress controller, orchestrated by a progressive delivery controller like **Argo Rollouts**.
The key to a smooth transition here is entirely removing human error by relying on **Zero-Touch Operations**. The shift should be governed by automated metric analysis rather than manual observation.

**Implementation steps:**

1. **Baseline and Canary Deployment:** Deploy the new version (Canary) alongside the old version (Stable). At this stage, the Canary receives 0% of the traffic.
2. **Initial Traffic Shift (The "Bake" Period):** Configure the Service Mesh to route exactly 5% of incoming HTTP/gRPC requests to the Canary pods. Leave the remaining 95% routed to the Stable pods.
3. **Automated Metric Analysis (Zero-Touch Operations):** Instead of having engineers stare at dashboards, configure an automated analysis run (using Prometheus metrics). The system should automatically compare the Canary's metrics against the Stable version's metrics for a defined period (e.g., 10 minutes). *Key metrics to evaluate:* HTTP 5xx error rates, response latency (P95/P99), CPU throttling, and memory spikes.
4. **Progressive Escalation:** If the automated analysis passes the 5% stage, Argo Rollouts automatically shifts the traffic to 20%, then 50%, and finally 100%, pausing for metric evaluation at each step.
5. **Automated Rollback:** If at any point during the traffic shift (e.g., at the 20% mark) the error rate spikes or latency exceeds defined Service Level Objectives (SLOs), the delivery controller must automatically abort the rollout and instantly shift 100% of the traffic back to the Stable version.

**Sample Argo Rollouts Manifest Strategy:**

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: critical-app-rollout
spec:
  replicas: 10
  strategy:
    canary:
      canaryService: app-canary-svc
      stableService: app-stable-svc
      trafficRouting:
        istio:
          virtualService:
            name: app-virtual-service
            routes:
            - primary
      steps:
      - setWeight: 5
      # Pause for 10 minutes to allow automated analysis to run
      - pause: {duration: 10m}
      - setWeight: 20
      - pause: {duration: 10m}
      - setWeight: 50
      - pause: {duration: 10m}
      - setWeight: 100
```

---

## Question 4

> Our application experiences significantly higher traffic during specific periods compared to its regular times, which increases the load on our replica databases. We don't want our users to experience high response times.
> a. How would you ensure the replica instances scale out and in appropriately before and after peak times?
> b. How would you integrate newly created databases with the application?
> c. Describe a method to pre-fill memory on the replica databases before traffic spikes.

---

### a. KEDA Cron Scaler for Database Replicas

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: db-replica-cron-scaler
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: StatefulSet
    name: postgres-replica # Target your database replica StatefulSet
  minReplicaCount: 2
  maxReplicaCount: 10
  triggers:
  - type: cron
    metadata:
      timezone: Europe/Istanbul
      # Scale out to 8 replicas at 08:30 (before 09:00 peak)
      # Scale back to baseline at 17:30
      start: "30 8 * * *"
      end: "30 17 * * *"
      desiredReplicas: "8"
```

---

### b. Read-Only Pool Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: db-read-only-pool
  labels:
    app: database
    role: replica
spec:
  type: ClusterIP
  selector:
    app: database
    role: replica # Routes traffic ONLY to synced read-replicas
  ports:
  - name: postgres
    port: 5432
    targetPort: 5432
```

---

### c. Cache Pre-warming with pg_prewarm

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: db-cache-warming-job
spec:
  # Schedule to run at 08:45, after replicas are created but before 09:00 peak
  schedule: "45 8 * * *"
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: psql-client
            image: postgres:15-alpine
            env:
            - name: PGURI
              value: "postgresql://user:pass@db-read-only-pool:5432/appdb"
            command:
            - /bin/sh
            - -c
            # Uses pg_prewarm to load the 'transactions' and 'users' tables into RAM
            - |
              psql $PGURI -c "CREATE EXTENSION IF NOT EXISTS pg_prewarm;"
              psql $PGURI -c "SELECT pg_prewarm('transactions');"
              psql $PGURI -c "SELECT pg_prewarm('users');"
          restartPolicy: OnFailure
```
---
 # Step 4 — Answers

## Question 1

> We have several macOS build machines on-premises for Unity iOS/Android builds via Jenkins. Too many jobs are queued and slowing the release cycle. Move CI/CD to the cloud, handle artifacts, and handle licensing.

### a. Cloud Migration Plan

The tricky part here is that iOS builds require macOS, so I can't replace everything with Linux containers.

My approach would be three phases. First, I'd move the Jenkins controller to EKS and run it as a pod. With the Kubernetes plugin, Linux agents — Android builds, unit tests, lint — spin up as ephemeral pods on demand. This alone kills most of the queue problem since Linux pods scale freely.

Second, I'd replace the on-prem Macs with **AWS EC2 Mac instances** (`mac2.metal`). These are dedicated Apple Silicon hosts I can register as persistent Jenkins agents. Since EC2 Mac has a 24-hour minimum billing period, I'd keep a small warm pool always running instead of trying to spin them up per job.

Third, I'd route jobs by label. Android and test jobs go to cheap Linux pods. iOS and Unity iOS jobs go to macOS agents only. No macOS capacity wasted on jobs that don't need it.

### b. Artifact Handling

After every successful build, I'd push the `.ipa` or `.apk` to **S3** with a structured path like:

```
s3://builds/{project}/{platform}/{branch}/{build-number}/app.ipa
```

For developer installs, I'd use **Firebase App Distribution**. It's free, works on both platforms, and automatically emails developers a one-tap install link after every build — no provisioning profile setup needed.

For iOS beta, I'd use **TestFlight** via fastlane `pilot`. For production, fastlane `deliver` submits to App Store Connect. For Android, fastlane `supply` pushes to the Play Console internal track.

### c. Licensing Challenges

**Unity:** The standard per-seat license breaks in CI because multiple agents try to activate it simultaneously. I'd switch to **Unity Build Server**, which is a concurrent-seat model built for CI. I'd run the license server on a dedicated EC2 instance — agents check out a seat to build, then release it when done.

**Apple:** I'd store Developer account credentials in **AWS Secrets Manager** and use fastlane `match` to manage certificates and provisioning profiles centrally. More on this in Question 2.

**EC2 Mac cost:** Because of the 24-hour minimum tenancy, idle machines still cost money. I'd buy a 1-year Reserved Instance for the baseline fleet and monitor build durations with CloudWatch to right-size over time.

---

## Question 2

> iOS builds are manual and error-prone. How do you automate the full iOS development lifecycle?

I'd use **fastlane**. It covers every step of the process and is the industry standard for this.

**`match`** handles code signing. It stores all certificates and provisioning profiles encrypted in a private Git repo or S3 and syncs them to every machine at build time. No more expired certificates or per-developer profile conflicts — the most common cause of iOS CI failures.

**`scan`** runs the XCTest suite in a clean simulator and outputs JUnit XML for Jenkins. If tests fail, the pipeline stops and nothing gets shipped.

**`gym`** compiles the app and produces the `.ipa`.

**`pilot`** uploads to TestFlight. **`deliver`** submits to App Store Connect. **`supply`** handles the Android Play Console.

A single `Fastfile` ties it all together:

```ruby
lane :release do
  increment_build_number(build_number: ENV["BUILD_NUMBER"])
  match(type: "appstore")
  scan(scheme: "MyApp")
  gym(scheme: "MyApp")
  pilot(skip_waiting_for_build_processing: true)
end
```

In Jenkins, the whole process runs with one command:

```bash
fastlane release
```

If any step fails, fastlane exits with a non-zero code and Jenkins marks the build as failed. No human involvement at any point.

---

## Question 3

> Describe a disaster recovery plan for a Kubernetes cluster on AWS. Cover recovery steps, data persistence, backups, and configuration management.

### a. Recovery Steps

I'd always go with **EKS** over self-managed Kubernetes. AWS manages the control plane across multiple AZs, so I don't have to deal with etcd backups or control plane failures myself.

For worker nodes, I'd spread them across at least 3 AZs with multi-AZ node groups. If one AZ goes down, pods reschedule to healthy nodes automatically.

For cluster state backups, I'd use **Velero**. It snapshots all Kubernetes objects — Deployments, ConfigMaps, Secrets, PVCs — to S3 on a schedule. Full backup every 24 hours, 30-day retention. Restoring is a single command:

```bash
velero restore create --from-backup daily-backup-2024-01-15
```

I'd manage all infrastructure with **Terraform**, state stored in S3 with DynamoDB locking. If the cluster needs to be fully rebuilt, `terraform apply` gets it back in minutes.

I'd also define RTO and RPO before any incident happens. A solid baseline is RPO 1 hour and RTO 2 hours.

### b. Data Persistence, Backups, and Configuration Management

For persistent volumes, I'd use the **EBS CSI driver** with `reclaimPolicy: Retain` on the StorageClass so volumes aren't deleted when pods are removed.

For databases, I'd use **RDS with automated snapshots** and at least 7-day retention. For any databases running inside the cluster, Velero can snapshot their PVCs via EBS snapshots using the `velero-plugin-for-aws` plugin.

For configuration management, all manifests live in Git and are applied by **ArgoCD**. When the cluster is rebuilt, ArgoCD syncs the full desired state automatically — no manual `kubectl apply`, no config drift. Secrets never go into Git. I'd use the **External Secrets Operator** to pull them from AWS Secrets Manager at runtime.

My rule of thumb: if it's not in Git or S3, it doesn't exist in the DR plan.