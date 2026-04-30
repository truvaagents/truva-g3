# TruvaG3 Kubernetes Infrastructure

Welcome to your production-ready TruvaG3 infrastructure! This guide will walk you through deploying a complete Kubernetes setup that supports all TruvaG3 examples and applications. Think of this as the foundation that makes everything else work seamlessly.

## Table of Contents

- [What Is This and Why Should You Care?](#what-is-this-and-why-should-you-care)
  - [The City Infrastructure Analogy](#the-city-infrastructure-analogy)
  - [What This Infrastructure Provides](#what-this-infrastructure-provides)
- [Infrastructure Components](#infrastructure-components)
- [Quick Start](#quick-start)
  - [Prerequisites](#prerequisites)
  - [Installation](#installation)
  - [Verify Installation](#verify-installation)
- [Local Development with Kind](#local-development-with-kind)
  - [Setting Up Kind Cluster](#setting-up-kind-cluster)
  - [Deploy Infrastructure to Kind](#deploy-infrastructure-to-kind)
  - [Local Access URLs](#local-access-urls)
- [Production Server Deployment](#production-server-deployment)
  - [Storage Requirements](#storage-requirements)
  - [Ingress Configuration](#ingress-configuration-recommended)
  - [Production Scaling](#production-scaling)
- [Component Configuration Deep Dive](#component-configuration-deep-dive)
  - [Redis Configuration](#redis-configuration)
  - [OTEL Collector Configuration](#otel-collector-configuration)
  - [Loki Configuration](#loki-configuration)
  - [Prometheus Configuration](#prometheus-configuration)
  - [Jaeger Configuration](#jaeger-configuration)
  - [Grafana Configuration](#grafana-configuration)
- [Observability: Trace-Log Correlation](#observability-trace-log-correlation)
  - [How It Works](#how-it-works)
  - [Using Grafana for Trace-Log Correlation](#using-grafana-for-trace-log-correlation)
  - [Finding trace_id from request_id](#finding-trace_id-from-request_id)
- [Customization and Advanced Configuration](#customization-and-advanced-configuration)
- [Troubleshooting Common Issues](#troubleshooting-common-issues)
- [Monitoring Your Infrastructure](#monitoring-your-infrastructure)
- [Production Best Practices](#production-best-practices)
- [Updates and Maintenance](#updates-and-maintenance)
- [Summary](#summary)

## What Is This and Why Should You Care?

### The City Infrastructure Analogy

Imagine you're building a smart city with different services:
- **Electric power grid** (Redis for service discovery)
- **Communication network** (OTEL Collector for telemetry)
- **Monitoring stations** (Prometheus for metrics)
- **Security cameras** (Jaeger for tracing)
- **Control center dashboard** (Grafana for visualization)

That's exactly what this k8-deployment setup provides for your TruvaG3 applications! It creates the essential infrastructure that every TruvaG3 component needs to work together effectively.

### What This Infrastructure Provides

1. **Shared Namespace** - A dedicated space (`truvag3-examples`) for all your components
2. **Service Discovery** - Redis registry so components can find each other
3. **Observability** - Complete monitoring stack with metrics, logs, and traces
4. **Debugging** - Visual dashboards to understand what's happening
5. **Production-Ready** - Persistent storage, security, and scaling configurations

## Infrastructure Components

### Core Infrastructure (setup-infrastructure.sh)

| Component | Image | Purpose | Access | Storage |
|-----------|-------|---------|--------|---------|
| **Namespace** | N/A | Isolated environment for TruvaG3 apps | N/A | N/A |
| **Redis** | `redis:7-alpine` | Service discovery registry | `redis:6379` | 1Gi PVC |
| **Loki** | `grafana/loki:3.6.7` | Log aggregation with retention | `loki:3100` | 5Gi PVC |
| **OTEL Collector** | `otel/opentelemetry-collector-contrib:latest` | Telemetry routing (traces, metrics, logs) | `otel-collector:4318` | None |
| **OTEL Collector Logs** | `otel/opentelemetry-collector-contrib:latest` | Pod log collection (DaemonSet) | N/A (node-level) | None |
| **Prometheus** | `prom/prometheus:latest` | Metrics storage and querying | `prometheus:9090` | 5Gi PVC |
| **Jaeger** | `jaegertracing/jaeger:2.15.1` | Distributed tracing (v2, OTLP-native) | `jaeger-query:16686` | In-memory (50k traces) |
| **Grafana** | `grafana/grafana:latest` | Visualization (Prometheus + Jaeger + Loki) | `grafana:80` | 2Gi PVC |

### Pod Label Requirement for Observability

The OTEL Log Collector DaemonSet resolves each log record's `service_name` (the
indexed Loki label you filter on) from the pod's `metadata.labels.app` value. Every
agent/tool deployment manifest in this repo must set this label, and it should equal
the service's canonical name.

```yaml
spec:
  template:
    metadata:
      labels:
        app: hotel-tool   # ← This becomes service_name in Loki
```

**Keep the pod `app:` label, `OTEL_SERVICE_NAME` env var, and your application's
logger service name aligned** — all three should be the same string. If they drift,
Loki, Jaeger, and Prometheus report different identities for the same workload.

See [OBSERVABILITY.md — Log Pipeline Identity](OBSERVABILITY.md#-log-pipeline-identity-how-service_name-is-set)
for the full pipeline walkthrough, and
[../../docs/TOOL_DEVELOPMENT_GUIDE.md §8](../../docs/TOOL_DEVELOPMENT_GUIDE.md#8-step-6-add-deployment-files)
for the developer-facing contract.

## Quick Start

### Prerequisites

**For Local Development (Kind):**
- Docker Desktop or Docker Engine
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/) installed
- kubectl configured
- Basic Kubernetes knowledge

**For Server/Cloud Kubernetes:**
- Running Kubernetes cluster (v1.20+)
- kubectl configured with cluster access
- StorageClass available for persistent volumes
- Ingress controller (recommended)

### Installation

#### Method 1: Intelligent Setup Script (Recommended!)

The setup script checks for existing infrastructure and only deploys what's needed. It **never deletes** existing resources.

```bash
cd examples/k8-deployment

# Deploy with automatic checks
./setup-infrastructure.sh

# Check status anytime
./setup-infrastructure.sh status

# See all options
./setup-infrastructure.sh help
```

**Safety Features:**
- Checks if services already exist before deploying
- Skips deployment if service is healthy and running
- Never deletes existing resources
- Shows clear status of what's new vs existing
- Waits for each component to be ready before proceeding

#### Pushing a Config/RBAC Change — `rebuild`

The default `setup` is idempotent: it skips components that are already healthy. That
is exactly the wrong behavior when you change a ConfigMap, RBAC rule, or other
non-spec field — the YAML has changed but the running pods still use the old config.
`kubectl apply` on a ConfigMap does not itself restart the pods that mount it.

Use `rebuild` to force a re-apply **and** roll the workload so the new config takes
effect:

```bash
# Rebuild a single component (recommended for targeted config changes)
./setup-infrastructure.sh rebuild otel-collector-logs
./setup-infrastructure.sh rebuild grafana

# Rebuild every infra workload in dependency order
./setup-infrastructure.sh rebuild
```

**Valid targets:**
`otel-collector`, `otel-collector-logs`, `loki`, `prometheus`, `jaeger`, `grafana`,
`redis`, `swagger-ui`, `qdrant`.

What `rebuild` does for each target:
1. `kubectl apply -f <yaml>` — updates ConfigMap, ClusterRole, ClusterRoleBinding,
   ServiceAccount, Service, and Deployment/DaemonSet as a single unit.
2. `kubectl rollout restart` — forces pods to re-mount the ConfigMap on startup.
3. `kubectl rollout status --timeout=120s` — waits for the new pods to be ready.

**When to use `rebuild`:**
- You edited a ConfigMap (OTel collector config, Prometheus scrape config, Grafana
  dashboards, etc.)
- You changed a ClusterRole's verbs/resources
- You bumped an env var or secret ref in the workload spec
- You want to force pods onto a freshly pulled `:latest` image

**When NOT to use `rebuild`:**
- First-time deploy on a clean cluster — use `setup` instead.
- You only added a pod-manifest spec change (image tag, replica count, env); plain
  `kubectl apply -f <yaml>` triggers a rollout automatically. `rebuild` still works
  but is slightly redundant.

Rebuild skips `ingress-nginx` (lives in `ingress-nginx` namespace) and `metrics-server`
(lives in `kube-system`) — both rarely reconfigured and handled by separate deploy
paths in `setup`.

**Order matters for cross-workload config changes.** When a single change updates both
the central `otel-collector` and the `otel-collector-logs` DaemonSet (or the log DS
and Loki, etc.), rebuild the **consumer** first and the **producer** second. The
canonical example is the logs-pipeline identity fix: rebuild `otel-collector` first
so its old OTTL transform is gone, then rebuild `otel-collector-logs` so the new
`k8sattributes`-enriched records land on a central collector that won't overwrite
their `service.name`. Running the DaemonSet first with the old central still in
place produces a transient window where the old transform silently reverts the new
resource attribute. For this specific change, the
[LOGS_PIPELINE_FIX_PLAN.md — Rollout order](LOGS_PIPELINE_FIX_PLAN.md#rollout-order-important)
section spells out the exact sequence.

For most config changes that touch a single workload, order is irrelevant.

#### Method 2: One-Command Kustomize (currently broken)

> **Do not use this method until the kustomize file is fixed.** The
> [kustomization.yaml](kustomization.yaml) has a pre-existing broken replacement rule
> (`[name=*]` predicate evaluated as regex, causing `missing argument to repetition
> operator`). `kubectl apply -k examples/k8-deployment` fails on server-side dry-run
> and on real apply:
>
> ```
> error: unable to find field
> "spec.template.spec.containers.[name=*].env.[name=REDIS_URL].value"
> in replacement target
> ```
>
> Use [Method 1 (setup-infrastructure.sh)](#method-1-intelligent-setup-script-recommended)
> for new deploys and `./setup-infrastructure.sh rebuild` for config changes. The
> kustomize fix is tracked as a separate concern from the logs-pipeline identity PR.

For reference, the intended Kustomize path (once the replacement rule is fixed):

```bash
cd examples/k8-deployment
kubectl apply -k .
```

#### Method 3: Step-by-Step Deployment (For Understanding)

```bash
# 1. Create the namespace first
kubectl apply -f namespace.yaml

# 2. Deploy Redis (service discovery)
kubectl apply -f redis.yaml

# 3. Deploy Loki (log aggregation — must be up before OTEL Collector)
kubectl apply -f loki.yaml

# 4. Deploy OTEL Collector (telemetry routing)
kubectl apply -f otel-collector.yaml

# 4b. Deploy OTEL Collector Logs DaemonSet (pod log collection)
kubectl apply -f otel-collector-logs.yaml

# 5. Deploy Prometheus (metrics)
kubectl apply -f prometheus.yaml

# 6. Deploy Jaeger v2 (tracing)
kubectl apply -f jaeger.yaml

# 7. Deploy Grafana (dashboards — connects to Prometheus, Jaeger, Loki)
kubectl apply -f grafana.yaml
```

#### Verify Installation

```bash
# Check all pods are running
kubectl get pods -n truvag3-examples

# Expected output:
# NAME                              READY   STATUS    RESTARTS
# redis-xxx                         1/1     Running   0
# loki-xxx                          1/1     Running   0
# otel-collector-xxx                1/1     Running   0
# otel-collector-logs-xxx           1/1     Running   0
# prometheus-xxx                    1/1     Running   0
# jaeger-xxx                        1/1     Running   0
# grafana-xxx                       1/1     Running   0
```

## Local Development with Kind

Kind (Kubernetes in Docker) is perfect for local development and testing.

### Setting Up Kind Cluster

```bash
# Create a kind cluster with specific configuration
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: truvag3-dev
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  # Expose common ports for local access
  - containerPort: 30080  # HTTP services
    hostPort: 30080
    protocol: TCP
  - containerPort: 30090  # Prometheus
    hostPort: 30090
    protocol: TCP
  - containerPort: 30030  # Grafana
    hostPort: 30030
    protocol: TCP
  - containerPort: 30160  # Jaeger
    hostPort: 30160
    protocol: TCP
EOF
```

### Deploy Infrastructure to Kind

```bash
# Set kubectl context to kind cluster
kubectl cluster-info --context kind-truvag3-dev

# Deploy the infrastructure
cd examples/k8-deployment
kubectl apply -k .

# Wait for all pods to be ready
kubectl wait --for=condition=ready pod --all -n truvag3-examples --timeout=300s
```

### Local Access URLs

With Kind setup above, access services locally:

```bash
# Get service URLs (using port-forward)
kubectl port-forward -n truvag3-examples svc/prometheus 9090:9090 &
kubectl port-forward -n truvag3-examples svc/grafana 3000:80 &
kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80 &
kubectl port-forward -n truvag3-examples svc/loki 3100:3100 &

# Access in browser:
# - Prometheus: http://localhost:9090
# - Grafana:    http://localhost:3000 (admin/admin)
# - Jaeger:     http://localhost:16686
# - Loki API:   http://localhost:3100 (no UI — query via Grafana)
```

### Kind-Specific Configuration

For Kind clusters, the infrastructure automatically:
- Uses `emptyDir` volumes (data is ephemeral)
- Configures smaller resource limits
- Skips ingress setup (use port-forward instead)

## Production Server Deployment

For production Kubernetes clusters (AWS EKS, Google GKE, Azure AKS, on-premises):

### Storage Requirements

Ensure your cluster has a default StorageClass:

```bash
# Check available StorageClasses
kubectl get storageclass

# If none exists, create one (example for AWS EBS)
cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: gp2-retain
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: kubernetes.io/aws-ebs
parameters:
  type: gp2
  fsType: ext4
reclaimPolicy: Retain
allowVolumeExpansion: true
EOF
```

### Ingress Configuration (Recommended)

For production access, set up ingress:

```bash
# Example Ingress for NGINX Ingress Controller
cat <<EOF | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: truvag3-infrastructure
  namespace: truvag3-examples
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  rules:
  - host: prometheus.truvag3.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: prometheus
            port:
              number: 9090
  - host: grafana.truvag3.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: grafana
            port:
              number: 3000
  - host: jaeger.truvag3.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: jaeger-query
            port:
              number: 16686
EOF
```

### Production Scaling

Scale components based on your needs:

```bash
# Scale Redis for high availability
kubectl patch deployment redis -n truvag3-examples -p '{"spec":{"replicas":3}}'

# Scale OTEL Collector for high throughput
kubectl patch deployment otel-collector -n truvag3-examples -p '{"spec":{"replicas":2}}'

# Scale Prometheus for reliability
kubectl patch statefulset prometheus -n truvag3-examples -p '{"spec":{"replicas":2}}'
```

## Component Configuration Deep Dive

### Redis Configuration

Redis serves as the service discovery backend for all TruvaG3 components.

**Key Features:**
- Persistent storage with volume claims
- Memory optimization for large deployments
- Health checks and restart policies
- Configurable maxmemory policies

**Environment Variables:**
```yaml
# In redis.yaml
args:
  - --appendonly yes          # Enable persistence
  - --appendfsync everysec    # Sync every second
  - --maxmemory 1gb          # Memory limit
  - --maxmemory-policy allkeys-lru  # Eviction policy
```

**Accessing Redis:**
```bash
# Connect to Redis from within cluster
kubectl exec -it -n truvag3-examples deployment/redis -- redis-cli

# Check registration keys
redis-cli --scan --pattern "truvag3:*"

# Monitor service registrations
redis-cli MONITOR
```

### OTEL Collector Configuration

The OTEL Collector is the central telemetry hub — all TruvaG3 apps send OTLP data here, and it routes signals to the appropriate backends.

**Architecture:**

There are two collector components:

1. **OTEL Collector (gateway)** — central Deployment that receives OTLP and exports to backends
2. **OTEL Collector Logs (DaemonSet)** — runs on every node, collects pod logs via `filelog` receiver, extracts `trace_id`/`span_id`, and forwards to the gateway

**Pipeline Flow:**
```
TruvaG3 Apps ──OTLP──→ OTEL Collector ──→ Prometheus (metrics)
                                      ──→ Jaeger v2 (traces via OTLP gRPC)
                                      ──→ Loki (logs via OTLP HTTP)

Pod stdout ──filelog──→ OTEL Collector Logs ──OTLP──→ OTEL Collector ──→ Loki
              (DaemonSet)                              (gateway)
```

**Gateway Configuration Highlights:**
```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318  # TruvaG3 apps connect here

processors:
  batch:           # Improves performance
  memory_limiter:  # Prevents OOM (256 MiB limit)

exporters:
  prometheus:                    # Metrics → Prometheus scrape endpoint
    endpoint: "0.0.0.0:8889"
  otlp/jaeger:                   # Traces → Jaeger v2 via OTLP gRPC
    endpoint: jaeger-collector:4317
  otlphttp/loki:                 # Logs → Loki via native OTLP HTTP
    endpoint: http://loki.truvag3-examples:3100/otlp
```

**Log Collector DaemonSet (`otel-collector-logs.yaml`):**

Collects logs from all TruvaG3 pods, parses CRI format, filters non-JSON logs, extracts trace context for correlation:

```yaml
operators:
  # Parse CRI container log format
  - type: regex_parser
    regex: '^(?P<time>[^ ]+) (?P<stream>stdout|stderr) (?P<logtag>[^ ]*) (?P<log>.*)$'
  # Filter: DROP non-JSON logs
  - type: filter
    expr: 'attributes.log == nil or not (attributes.log matches "^\\{")'
  # Parse JSON log body from TruvaG3 apps
  - type: json_parser
    parse_from: attributes.log
  # Extract trace context for log-trace correlation
  - type: trace_parser
    trace_id:
      parse_from: attributes.trace_id
    span_id:
      parse_from: attributes.span_id
```

### Loki Configuration

Loki provides log aggregation with automatic retention, ingestion rate limiting, and trace-log correlation. It runs in **monolithic (single-binary) mode** — one pod handles ingestion, querying, and compaction.

**Image:** `grafana/loki:3.6.7`

**Key Features:**
- Native OTLP HTTP endpoint (`/otlp`) — no special exporter needed
- Automatic retention via compactor (deletes logs older than configured period)
- Ingestion rate limits to prevent burst-driven OOM
- Stream limits to prevent label cardinality explosion
- Filesystem storage with PVC for durability

**Resource Protection (defaults for Kind):**

| Safeguard | Value | Purpose |
|-----------|-------|---------|
| Retention period | 30h | Auto-deletes older logs |
| Compaction interval | 10m | Runs cleanup every 10 minutes |
| Volume cap (PVC) | 5Gi | Hard disk limit |
| Memory limit | 1Gi | OOMKilled before starving laptop |
| CPU limit | 500m | Won't hog CPU from other pods |
| Ingestion rate | 4 MB/s | Caps ingest bandwidth |
| Burst limit | 8 MB | Prevents spike-driven memory exhaustion |
| Max streams | 5,000 | Prevents label cardinality explosion |
| Per-stream rate | 3 MB/s | No single stream can dominate |
| Max query length | 30h | Prevents expensive full-scan queries |
| Max entries per query | 10,000 | Bounds query result size |

**Configuration (`loki.yaml`):**

```yaml
# Key sections from the Loki config
auth_enabled: false  # No multi-tenancy for local dev

schema_config:
  configs:
  - from: "2024-01-01"
    store: tsdb
    object_store: filesystem
    schema: v13
    index:
      prefix: index_
      period: 24h        # Required for retention to work

limits_config:
  retention_period: 30h  # Logs older than 30h are auto-deleted
  ingestion_rate_mb: 4
  ingestion_burst_size_mb: 8
  per_stream_rate_limit: 3MB
  max_global_streams_per_user: 5000

compactor:
  retention_enabled: true
  compaction_interval: 10m
  retention_delete_delay: 1h
```

**Scaling for more tools:**

The default limits support ~80 pods at INFO level. If you add more tools:
- Increase `max_global_streams_per_user` (each pod creates ~5-10 streams based on label combinations)
- Increase `ingestion_rate_mb` if you see rate-limit errors in the otel-collector logs
- Increase PVC size if 5Gi fills up (check with `kubectl get pvc loki-pvc -n truvag3-examples`)

**Verifying Loki is healthy:**

```bash
# Check readiness
kubectl port-forward -n truvag3-examples svc/loki 3100:3100
curl http://localhost:3100/ready    # Should return: ready

# Check available labels
curl http://localhost:3100/loki/api/v1/labels

# Check ingestion metrics
curl http://localhost:3100/metrics | grep loki_distributor_lines_received_total
```

### Prometheus Configuration

Prometheus scrapes metrics from the OTEL Collector and provides querying capabilities.

**Key Features:**
- Persistent storage for historical data
- Automatic service discovery
- Configurable retention policies
- Web UI for queries and debugging

**Scrape Configuration:**
```yaml
scrape_configs:
  - job_name: 'otel-collector'
    static_configs:
      - targets: ['otel-collector:8888']
  - job_name: 'kubernetes-pods'
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: ['truvag3-examples']
```

### Jaeger Configuration

Jaeger v2 collects and visualizes distributed traces. Unlike Jaeger v1 (which reached EOL Dec 31, 2025), v2 is built entirely on the OpenTelemetry Collector and uses a YAML config file instead of environment variables.

**Image:** `jaegertracing/jaeger:2.15.1`

**Key Features:**
- Built on OpenTelemetry Collector — accepts OTLP natively (gRPC + HTTP)
- In-memory storage (50,000 traces max — suitable for local dev/Kind)
- YAML-based configuration via ConfigMap
- Prometheus metrics endpoint for self-monitoring
- Health check v2 endpoint

**Configuration (`jaeger.yaml`):**
```yaml
extensions:
  jaeger_query:
    storage:
      traces: mem_store
    http:
      endpoint: "0.0.0.0:16686"   # Web UI
  jaeger_storage:
    backends:
      mem_store:
        memory:
          max_traces: 50000       # Adjust for your needs
  healthcheckv2:
    use_v2: true
    http:
      endpoint: "0.0.0.0:13133"

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [jaeger_storage_exporter]
```

**Services:**
- **jaeger-query** (port 80 → 16686): Web UI and query API
- **jaeger-collector** (ports 4317, 4318): OTLP receiver for traces from OTEL Collector

**Note:** Jaeger v2 only stores **traces**, not logs. Log storage is handled by Loki.

### Grafana Configuration

Grafana provides unified dashboards with three pre-provisioned datasources: Prometheus, Jaeger, and Loki. It is the single UI for metrics, traces, and logs with bidirectional correlation.

**Image:** `grafana/grafana:latest` (currently 12.4.0)

**Key Features:**
- Three auto-provisioned datasources (Prometheus, Jaeger, Loki)
- Bidirectional trace-log correlation (Jaeger → Loki, Loki → Jaeger)
- Pre-built TruvaG3 dashboards (Agent Telemetry + Resource Usage)
- Default admin/admin credentials
- Persistent storage for dashboards (2Gi PVC)

**Provisioned Datasources (`grafana.yaml`):**
```yaml
datasources:
  - name: Prometheus
    type: prometheus
    url: http://prometheus:9090
    isDefault: true
    uid: prometheus

  - name: Jaeger
    type: jaeger
    url: http://jaeger-query
    uid: jaeger
    jsonData:
      tracesToLogsV2:           # "View Logs" button in trace view
        datasourceUid: loki
        filterByTraceID: true
        spanStartTimeShift: "-5m"
        spanEndTimeShift: "5m"

  - name: Loki
    type: loki
    url: http://loki:3100
    uid: loki
    jsonData:
      derivedFields:            # Clickable trace_id links in log view
      - datasourceUid: jaeger
        matcherRegex: '"trace_id":"(\\w+)"'
        name: TraceID
```

**Pre-built Dashboards:**
- **TruvaG3 Agent Telemetry** — active tools/agents, request counts, error rates, AI request latency, tool call performance, discovery operations, cache operations
- **TruvaG3 Resource Usage** — CPU/memory by pod, agents vs tools comparison, top resource consumers

## Observability: Trace-Log Correlation

One of the most powerful features of this infrastructure is bidirectional trace-log correlation. When debugging distributed requests across multiple TruvaG3 services, you can follow a single request from agent to tools and back, seeing both the trace spans and the associated log entries.

### How It Works

TruvaG3's structured logging automatically includes `trace_id` and `span_id` in all log entries. The OTEL Collector Logs DaemonSet extracts these fields and propagates them to Loki. This means every log line stored in Loki carries its trace context, enabling correlation with Jaeger traces.

```
TruvaG3 App logs JSON with trace_id/span_id
        ↓
OTEL Collector Logs (DaemonSet) extracts trace context via trace_parser
        ↓
OTEL Collector (gateway) receives via OTLP
        ↓
Loki stores logs with trace_id      Jaeger stores trace spans
        ↓                                    ↓
Grafana: Loki ←──── bidirectional link ────→ Jaeger
         (derivedFields)              (tracesToLogsV2)
```

### Using Grafana for Trace-Log Correlation

**From Trace → Logs (Jaeger → Loki):**

1. Open Grafana at `http://localhost:3000` (admin/admin)
2. Go to **Explore** → select **Jaeger** datasource
3. Find your trace (search by service, operation, or time range)
4. Click on a trace to open the detail view
5. Click the **Logs for this span** button — Grafana jumps to Loki with the trace_id filter applied
6. See all log entries from all services that participated in this trace

**From Logs → Trace (Loki → Jaeger):**

1. Go to **Explore** → select **Loki** datasource
2. Query logs: `{service_name=~".+"}` or filter by specific service
3. Expand a log entry
4. Look for the **TraceID** derived field — it's a clickable link
5. Click to jump directly to the full trace view in Jaeger

### Finding trace_id from request_id

TruvaG3 uses `request_id` (format: `orch-{timestamp}`) to identify orchestration requests. Here's how to find the `trace_id` for a specific request:

**Method 1: Search Logs in Grafana**

1. Open Grafana → **Explore** → **Loki**
2. Run a LogQL query:
   ```
   {service_name=~".+"} |= "orch-1769808213849075719"
   ```
3. Expand any matching log entry
4. Find the `trace_id` field and click the **TraceID** link to view the full trace

**Method 2: Loki API (CLI)**

```bash
curl -s --get 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={service_name=~".+"} |= "orch-1769808213849075719"' \
  --data-urlencode 'limit=5'
```

**Example Workflow:**

```
User Request → request_id: orch-1769808213849075719
                    ↓
Search in Grafana Loki: {service_name=~".+"} |= "orch-1769808213849075719"
                    ↓
Find trace_id: b0c5eee719ce74c48206b86b3ad52b3e
                    ↓
Click TraceID link → Grafana shows Jaeger trace:
    See all spans across travel-chat-agent,
    weather-tool, currency-tool, etc.
                    ↓
Click "Logs for this span" → See all log entries
    correlated with this distributed trace
```

**What You Can See in a Correlated View:**

- **Trace Spans**: The full request flow across all services with timing
- **Logs per Span**: INFO/debug logs emitted during each span's execution
- **Cross-Service Context**: How data flows from agent to tools and back
- **Error Attribution**: Which service/span produced errors

## Customization and Advanced Configuration

### Environment-Specific Kustomization

Create environment overlays:

```bash
# Development overlay
mkdir -p overlays/dev
cat <<EOF > overlays/dev/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
- ../../base

patchesStrategicMerge:
- redis-dev.yaml

configMapGenerator:
- name: environment-config
  literals:
  - ENVIRONMENT=development
  - LOG_LEVEL=debug
EOF
```

### Custom Resource Limits

Adjust resources based on your cluster capacity:

```bash
# Create resource patches
cat <<EOF > resource-limits.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  namespace: truvag3-examples
spec:
  template:
    spec:
      containers:
      - name: prometheus
        resources:
          requests:
            memory: "2Gi"
            cpu: "1000m"
          limits:
            memory: "4Gi"
            cpu: "2000m"
EOF

kubectl patch deployment prometheus -n truvag3-examples --patch-file resource-limits.yaml
```

### Adding Custom Dashboards

```bash
# Create ConfigMap with dashboard JSON
kubectl create configmap custom-dashboard \
  --from-file=dashboard.json \
  -n truvag3-examples

# Mount in Grafana deployment
kubectl patch deployment grafana -n truvag3-examples -p '{
  "spec": {
    "template": {
      "spec": {
        "containers": [{
          "name": "grafana",
          "volumeMounts": [{
            "name": "custom-dashboard",
            "mountPath": "/var/lib/grafana/dashboards/custom"
          }]
        }],
        "volumes": [{
          "name": "custom-dashboard",
          "configMap": {"name": "custom-dashboard"}
        }]
      }
    }
  }
}'
```

## Troubleshooting Common Issues

### Pods Not Starting

**Check pod status:**
```bash
kubectl get pods -n truvag3-examples
kubectl describe pod <pod-name> -n truvag3-examples
kubectl logs <pod-name> -n truvag3-examples
```

**Common Issues:**
1. **ImagePullBackOff**: Check internet connectivity and image names
2. **Pending**: Check resource availability and storage classes
3. **CrashLoopBackOff**: Check container logs and configuration

### Storage Issues

**Check PVC status:**
```bash
kubectl get pvc -n truvag3-examples

# If PVC is pending, check StorageClass
kubectl get storageclass
kubectl describe storageclass <default-class>
```

**Fix common storage issues:**
```bash
# For Kind clusters, ensure PVCs use emptyDir
kubectl patch deployment redis -n truvag3-examples -p '{
  "spec": {
    "template": {
      "spec": {
        "volumes": [{
          "name": "redis-data",
          "emptyDir": {}
        }]
      }
    }
  }
}'
```

### Service Discovery Issues

**Check Redis connectivity:**
```bash
# Test Redis from another pod
kubectl run redis-test --image=redis:7-alpine -it --rm --restart=Never \
  -- redis-cli -h redis.truvag3-examples.svc.cluster.local ping

# Should return: PONG
```

**Monitor service registrations:**
```bash
kubectl exec -it -n truvag3-examples deployment/redis -- redis-cli MONITOR
```

### OTEL Collector Issues

**Check OTEL Collector logs:**
```bash
kubectl logs -n truvag3-examples deployment/otel-collector -f
```

**Test OTLP endpoint:**
```bash
# From within cluster
kubectl run test-pod --image=curlimages/curl -it --rm --restart=Never \
  -- curl -X POST http://otel-collector.truvag3-examples.svc.cluster.local:4318/v1/traces
```

### Prometheus Not Scraping

**Check Prometheus targets:**
```bash
# Access Prometheus UI and check Status -> Targets
kubectl port-forward -n truvag3-examples svc/prometheus 9090:9090
# Open http://localhost:9090/targets
```

**Common fixes:**
```bash
# Restart Prometheus to reload config
kubectl rollout restart deployment/prometheus -n truvag3-examples
```

## Monitoring Your Infrastructure

### Health Check Commands

```bash
# Quick health check script
cat <<'EOF' > health-check.sh
#!/bin/bash
echo "TruvaG3 Infrastructure Health Check"
echo "==================================="

NAMESPACE="truvag3-examples"

echo "Pod Status:"
kubectl get pods -n $NAMESPACE

echo -e "\nService Status:"
kubectl get svc -n $NAMESPACE

echo -e "\nStorage Status:"
kubectl get pvc -n $NAMESPACE

echo -e "\nRecent Events:"
kubectl get events -n $NAMESPACE --sort-by='.lastTimestamp' | tail -5

echo -e "\nHealth Check Complete!"
EOF

chmod +x health-check.sh
./health-check.sh
```

### Performance Monitoring

```bash
# Monitor resource usage
kubectl top pods -n truvag3-examples

# Monitor specific component
kubectl top pod -n truvag3-examples -l app=redis

# Get detailed resource usage
kubectl describe node | grep -A5 "Allocated resources"
```

## Production Best Practices

### 1. Security Hardening

```bash
# Create network policies
cat <<EOF | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: truvag3-network-policy
  namespace: truvag3-examples
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: truvag3-examples
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          name: truvag3-examples
EOF
```

### 2. Backup Strategy

```bash
# Backup Redis data
kubectl exec -n truvag3-examples deployment/redis -- redis-cli BGSAVE

# Backup Prometheus data
kubectl exec -n truvag3-examples deployment/prometheus -- \
  tar -czf /backup/prometheus-$(date +%Y%m%d).tar.gz /prometheus/data

# Backup Grafana dashboards
kubectl get configmap -n truvag3-examples -o yaml > grafana-backup.yaml
```

### 3. Monitoring Alerts

```bash
# Add Prometheus alerting rules
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: prometheus-alerts
  namespace: truvag3-examples
data:
  alert.rules: |
    groups:
    - name: truvag3
      rules:
      - alert: TruvaG3ComponentDown
        expr: up{job="truvag3-components"} == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "TruvaG3 component is down"
EOF
```

### 4. Resource Management

```bash
# Set resource quotas
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ResourceQuota
metadata:
  name: truvag3-quota
  namespace: truvag3-examples
spec:
  hard:
    requests.cpu: "4"
    requests.memory: "8Gi"
    limits.cpu: "8"
    limits.memory: "16Gi"
    persistentvolumeclaims: "10"
EOF
```

## Updates and Maintenance

### Updating Infrastructure

```bash
# Update to latest configurations
git pull origin main

# Re-apply updated manifests and restart workloads (handles ConfigMap changes)
./setup-infrastructure.sh rebuild
```

> Prior revisions of this doc recommended `kubectl apply -k examples/k8-deployment`.
> That path is currently broken by a pre-existing kustomize replacement rule
> (unrelated to this PR). Use `setup-infrastructure.sh rebuild` instead — it handles
> `kubectl apply` plus rollout restart for every infra workload. See
> [Pushing a Config/RBAC Change — `rebuild`](#pushing-a-configrbac-change--rebuild)
> for targeted single-component rebuilds.

### Version Management

```bash
# Tag current configuration
kubectl annotate namespace truvag3-examples \
  deployment.version="v$(date +%Y%m%d-%H%M%S)"

# Rollback if needed
kubectl rollout undo deployment/redis -n truvag3-examples
```

## Summary

This infrastructure provides the foundation for running TruvaG3 applications at scale. You now have:

1. **Complete Infrastructure** — Redis, OTEL Collector, Loki, Prometheus, Jaeger v2, Grafana
2. **Flexible Deployment** — Works on local Kind clusters and production Kubernetes
3. **Full Observability** — Metrics (Prometheus), traces (Jaeger), logs (Loki), dashboards (Grafana)
4. **Trace-Log Correlation** — Bidirectional linking between Jaeger traces and Loki logs via `trace_id`
5. **Resource Protection** — Retention limits, ingestion rate caps, PVC size bounds to prevent storage explosion
6. **Easy Maintenance** — Health checks, updates, and troubleshooting guides
7. **Unified Debugging** — Find any request by `request_id` and see the full distributed trace with correlated logs

### What's Next?

1. Deploy TruvaG3 applications using this infrastructure
2. Explore the [Monitoring Guide](../monitoring/README.md) for advanced observability
3. Check out example applications in other folders
4. Set up custom dashboards and alerts

**Congratulations!** Your TruvaG3 infrastructure is ready for action. All your applications can now discover each other, export telemetry, and provide rich observability. Happy building!