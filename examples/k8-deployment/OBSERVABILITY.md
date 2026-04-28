# Truva-G3 Observability Stack

This directory contains a complete observability stack for Truva-G3 framework applications using modern cloud-native patterns.

## 🏗️ Architecture Overview

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   Truva-G3 Apps   │───▶│  OTEL Collector  │───▶│   Prometheus    │
│   (Agents/Tools)│    │   (central, OTLP │    │   (Port 9090)   │
│   stdout + OTLP │    │    4317/4318)    │    └─────────────────┘
└─────────────────┘    │                  │
        │              │                  │    ┌─────────────────┐
        │              │                  │───▶│     Jaeger      │
        │ pod logs     │                  │    │   (Port 16686)  │
        ▼              │                  │    └─────────────────┘
┌─────────────────┐    │                  │    ┌─────────────────┐
│ OTEL Collector  │───▶│                  │───▶│      Loki       │
│  Logs (DS,      │    │                  │    │   (Port 3100)   │
│  per-node)      │    │                  │    └─────────────────┘
└─────────────────┘    └──────────────────┘
```

Three signals reach the central OTEL Collector:

- **Metrics** and **traces** are exported by apps via OTLP (SDK-emitted).
- **Logs** are read from pod stdout by a per-node **DaemonSet** (`otel-collector-logs`),
  which tails `/var/log/pods/truvag3-examples_*` and forwards records to the central
  collector. Apps do not export logs via SDK — stdout is the contract.

## 🚀 What Gets Deployed

### Core Infrastructure
- **Redis** - Service discovery backend
- **OTEL Collector** - Central telemetry router (traces + metrics + logs)
- **OTEL Collector Logs** - DaemonSet for pod log collection (one pod per node)
- **Loki** - Log aggregation and query
- **Prometheus** - Metrics storage and alerting
- **Jaeger** - Distributed tracing
- **Grafana** - Visualization and dashboards

### Modern Telemetry Pipeline
1. **Truva-G3 apps** write structured JSON logs to stdout and export metrics/traces
   via **OTLP** (OpenTelemetry Protocol).
2. **OTEL Collector Logs** (DaemonSet) tails pod stdout files, parses the JSON body,
   associates each record to its originating pod via `k8s.pod.uid` extracted from the
   log file path, and enriches with K8s resource attributes (see
   [Log Pipeline Identity](#-log-pipeline-identity-how-service_name-is-set) below).
3. **OTEL Collector** (central) receives OTLP from both SDK exports and the DaemonSet
   and routes each signal:
   - Metrics → Prometheus format on `:8889`
   - Traces → Jaeger (OTLP gRPC)
   - Logs → Loki (OTLP HTTP)
4. **Prometheus** scrapes metrics from the collector.
5. **Grafana** visualizes data from Prometheus, Jaeger, and Loki.

## 📊 Why OTLP-First Design?

### Traditional Approach (Problematic)
```
App → /metrics endpoint → Prometheus (metrics only)
App → Jaeger SDK → Jaeger (traces only)
```
**Issues**: Separate instrumentation, no correlation, vendor lock-in

### Modern OTLP Approach (Implemented)
```
App → OTLP → OTEL Collector → Multiple Backends
```
**Benefits**:
- ✅ Single instrumentation SDK
- ✅ Automatic trace-metric correlation
- ✅ Vendor-agnostic (swap backends without code changes)
- ✅ Better resource efficiency (batched exports)
- ✅ Cloud-native standard (CNCF)

## 🔖 Log Pipeline Identity: How `service_name` Is Set

Every record in Loki carries a `service_name` stream label that identifies which
workload produced the log. Getting that label right — per-record, per-pod — is what
makes `{service_name="hotel-tool"} |= "<request_id>"` work when you're debugging.

### The rule (one sentence)

**`service_name` in Loki is derived from the pod's `metadata.labels.app` value by the
`k8sattributes` processor, not from anything your app writes in its log body.**

Same rule applies to Prometheus/Jaeger — but those pull `service.name` from the SDK's
resource (set by `OTEL_SERVICE_NAME`), not from pod labels. For logs, the pipeline is
the authority.

### The pipeline

```
pod stdout (JSON log line)
  │
  ▼
filelog receiver  (DaemonSet, tails /var/log/pods/truvag3-examples_*)
  │  operators:
  │    - CRI regex parse
  │    - recombine partial lines
  │    - drop non-JSON
  │    - parse JSON body
  │    - extract trace_id / span_id
  │    - regex_parser on log.file.path ───► captures k8s_pod_uid
  │    - move k8s_pod_uid → resource["k8s.pod.uid"]    ◄── per-entry resource
  │
  ▼
k8sattributes processor
  │  - reads pod by UID from the K8s API
  │  - sets resource.service.name from pod label `app`
  │  - sets k8s.pod.name, k8s.namespace.name, k8s.deployment.name, k8s.node.name
  │    (k8s.deployment.name is derived by walking pod → replicaset → deployment
  │     owner references, hence the replicasets RBAC rule)
  │
  ▼
OTLP to central collector
  │
  ▼
Loki OTLP ingester
  │  - promotes service.name → service_name (indexed label)
  │  - promotes k8s.namespace.name, k8s.pod.name, k8s.deployment.name,
  │    deployment.environment → indexed labels
  │  - k8s.node.name (and other non-default resource attrs) are attached as
  │    structured metadata on each record — not indexed, but still queryable
  │    via `| k8s_node_name="…"` pipe syntax
  │
  ▼
Loki storage
  indexed labels: service_name, k8s_deployment_name, k8s_namespace_name,
                  k8s_pod_name, deployment_environment
  structured metadata: k8s_node_name, log_file_path, plus JSON-parsed fields
                       (component, operation, request_id, level, method, …)
```

The step that matters is the `k8sattributes` processor. It associates records to pods
by UID (extracted from the log file path) and writes `service.name` to the resource
attributes **per record**. That guarantees records from different pods carry different
resources, so they land in distinct Loki streams — no cross-pod contamination.

### Developer requirements

The pipeline assumes three things about your deployment manifest. If all three hold,
everything works; if any one breaks, you lose the clean identity contract.

| Requirement | What happens if violated |
|-------------|--------------------------|
| Pod template has `metadata.labels.app: <service-name>` | Without an `app:` label, `k8sattributes` leaves `service.name` unset → Loki labels the record `unknown_service` |
| `app:` label matches the identity you want to query by | Records land under the label-value you chose, not the identity your app emits in the log body |
| App writes structured JSON logs to stdout | Non-JSON lines (panics, init-container output) are dropped by the filelog pipeline — they never reach Loki |

Keep the pod `app:` label aligned with `OTEL_SERVICE_NAME` (SDK traces/metrics) and
the logger's `service` field (log body). All three should equal your service name.
See [LOGGING_IMPLEMENTATION_GUIDE.md §10 — Service Identity Contract](../../docs/LOGGING_IMPLEMENTATION_GUIDE.md#10-structured-logging-field-naming-standards).

### Querying Loki

```logql
# All records for one service (anchored on the pod's app: label)
{service_name="hotel-tool"}

# One service's ERROR-level records for a specific request
{service_name="hotel-tool"} |= "orch-1776904450804389754" | json | level="ERROR"

# All services in the namespace — useful for request-wide trace reconstruction
{k8s_namespace_name="truvag3-examples"} |= "orch-1776904450804389754"

# One pod specifically (e.g., when multiple replicas)
{k8s_pod_name="hotel-tool-7f6fc48496-vhfhh"}
```

The indexed labels are: `service_name`, `k8s_namespace_name`, `k8s_pod_name`,
`k8s_deployment_name`, `deployment_environment`. Non-indexed, queryable data
includes `k8s_node_name` and `log_file_path` (structured metadata, filter with
`| key="value"`) plus every JSON body field — component, operation, request_id,
level, method, path — reachable via `| json` then `| field="value"`. Non-indexed
filters are fast enough for interactive debugging but scan more than indexed
filters at scale.

### RBAC

The DaemonSet's ServiceAccount needs read access to pods, namespaces, and replicasets
(the last one is required for `k8s.deployment.name` owner-reference walking). The
shipped [otel-collector-logs.yaml](otel-collector-logs.yaml) includes the ClusterRole
with those verbs already.

## 🛠️ Deployment

### Quick Start
```bash
# Deploy the complete observability stack (idempotent; skips healthy resources)
cd examples/k8-deployment
./setup-infrastructure.sh

# Verify deployment
kubectl get pods -n truvag3-examples

# Access services
kubectl port-forward svc/grafana 3000:80 -n truvag3-examples      # Grafana UI
kubectl port-forward svc/prometheus 9090:9090 -n truvag3-examples # Prometheus UI
kubectl port-forward svc/jaeger-query 16686:80 -n truvag3-examples # Jaeger UI
```

> `kubectl apply -k examples/k8-deployment/` currently fails due to a broken
> replacement rule in [kustomization.yaml](kustomization.yaml) (unrelated
> to this fix — pre-existing kustomize regex issue). Use `setup-infrastructure.sh`
> instead until that is resolved.

### Environment Variables for Truva-G3 Apps

The stack automatically configures these environment variables for your applications:

```yaml
env:
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: "http://otel-collector.truvag3-examples:4318"    # OTLP export endpoint
- name: REDIS_URL
  value: "redis://redis:6379"           # Service discovery
```

#### Automatic Telemetry Activation

**Important**: Setting `OTEL_EXPORTER_OTLP_ENDPOINT` automatically enables telemetry in the framework.

**How it works** (from [core/config.go:579-581](../../core/config.go#L579-L581)):
```go
if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
    c.Telemetry.Endpoint = v
    c.Telemetry.Enabled = true  // Auto-enabled
}
```

**Default telemetry settings** (from [core/config.go:297-304](../../core/config.go#L297-L304)):
```go
TelemetryConfig{
    Enabled:        false,  // Changed to true when endpoint is set
    Provider:       "otel",
    MetricsEnabled: true,   // Metrics collection enabled by default
    TracingEnabled: true,   // Distributed tracing enabled by default
    SamplingRate:   1.0,    // 100% trace sampling
    Insecure:       true,   // No TLS (for dev/local environments)
}
```

**What gets collected automatically:**
- ✅ **Metrics**: Request counts, latencies, error rates, resource usage
- ✅ **Traces**: Distributed request traces with full span context
- ✅ **Context Propagation**: W3C Trace Context headers for correlation

**You do NOT need to set:**
- ❌ `TRUVAG3_TELEMETRY_ENABLED=true` (auto-enabled by endpoint)
- ❌ `TRUVAG3_TELEMETRY_METRICS=true` (enabled by default)
- ❌ `TRUVAG3_TELEMETRY_TRACING=true` (enabled by default)

## 📈 What You Get

### Prometheus Metrics (localhost:9090)
- **Truva-G3 Framework Metrics**: Request counts, latencies, errors
- **AI/LLM Metrics**: Token usage, costs, rate limits
- **Circuit Breaker Metrics**: Success/failure rates, state changes
- **Discovery Metrics**: Service registrations, health checks
- **Infrastructure Metrics**: Redis, OTEL Collector performance

### Jaeger Tracing (localhost:16686)
- **Distributed request traces** across agents and tools
- **AI request tracing** with token usage and latencies
- **Service discovery traces** showing component interactions
- **Automatic correlation** with metrics via trace IDs

### Grafana Dashboards (localhost:3000)
- **Truva-G3 Overview**: System health and performance
- **AI Usage Dashboard**: Token consumption, costs, provider performance
- **Service Discovery**: Component topology and health
- **Infrastructure**: Redis, Kubernetes, OTEL Collector metrics

## 🔍 Monitoring Your Applications

### Application Integration
Your Truva-G3 applications automatically export telemetry when the framework is configured:

```go
// Framework automatically handles this
import "github.com/truvaagents/truva-g3/telemetry"

// Initialize telemetry (usually done by framework)
telemetry.Initialize(telemetry.ProfileProduction)

// Metrics are automatically emitted for:
// - HTTP requests (latency, errors, throughput)
// - AI API calls (tokens, costs, latency)
// - Service discovery (registrations, lookups)
// - Circuit breaker events (trips, recoveries)
```

### Custom Metrics
```go
// Add custom business metrics
telemetry.Counter("orders.processed", "status", "success")
telemetry.Histogram("payment.amount", 99.99, "method", "stripe")
telemetry.Gauge("queue.size", 42, "queue", "orders")
```

## 🚨 Alerting Rules

Included Prometheus alerts:
- **TruvaG3ComponentDown**: Component unavailable > 1 minute
- **TruvaG3HighErrorRate**: Error rate > 10% for 2 minutes
- **TruvaG3HighLatency**: 95th percentile latency > 1 second for 5 minutes

## 🔧 Configuration

### OTEL Collector Config
The collector is configured to:
- Receive OTLP on ports 4317 (gRPC) and 4318 (HTTP)
- Export Prometheus metrics on port 8888
- Forward traces to Jaeger
- Include resource attribution and batching for efficiency

### Prometheus Discovery
Uses Kubernetes service discovery to automatically find:
- Truva-G3 agents with label `truvag3.framework/type: agent`
- Truva-G3 tools with label `truvag3.framework/type: tool`
- OTEL Collector with proper annotations

### Resource Requirements
- **OTEL Collector**: 128Mi RAM, 100m CPU
- **Prometheus**: 512Mi RAM, 200m CPU
- **Jaeger**: 512Mi RAM, 200m CPU
- **Grafana**: 256Mi RAM, 100m CPU
- **Redis**: 256Mi RAM, 100m CPU

Total: ~1.6GB RAM, ~800m CPU for complete observability stack

## 📚 Additional Resources

- [OpenTelemetry Documentation](https://opentelemetry.io/)
- [Prometheus Best Practices](https://prometheus.io/docs/practices/)
- [Jaeger Documentation](https://www.jaegertracing.io/)
- [Truva-G3 Telemetry Module](../../telemetry/README.md)