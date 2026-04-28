#!/bin/bash
#
# setup-signoz.sh - SigNoz Observability Stack for Truva-G3 Examples
#
# DESCRIPTION:
#   This script manages SigNoz deployment as an optional, feature-rich alternative
#   to Jaeger for distributed tracing. SigNoz provides trace-log correlation,
#   unified observability, and a ClickHouse-powered backend.
#
# USAGE:
#   ./setup-signoz.sh [COMMAND]
#
# COMMANDS:
#   setup   Install SigNoz stack (default if no command given)
#           - Installs SigNoz via Helm (ClickHouse, Query Service, Frontend)
#           - Updates OTEL Collector to export traces/metrics to SigNoz
#           - Deploys log collector DaemonSet with trace-log correlation
#           - Configures ClickHouse TTL for log retention
#           - Removes Jaeger (SigNoz replaces it)
#
#   revert  Switch back to Jaeger (lightweight tracing)
#           - Restores original OTEL Collector configuration
#           - Redeploys Jaeger
#           - Removes log collector DaemonSet
#           - Leaves SigNoz installed but idle
#
#   help    Show this help message
#
# ENVIRONMENT VARIABLES:
#   LOG_RETENTION_DAYS  Number of days to retain logs in ClickHouse (default: 1)
#
# EXAMPLES:
#   ./setup-signoz.sh                      # Install SigNoz with default settings
#   LOG_RETENTION_DAYS=7 ./setup-signoz.sh # Install with 7-day log retention
#   ./setup-signoz.sh revert               # Switch back to Jaeger
#   ./setup-signoz.sh help                 # Show help
#
# MEMORY REQUIREMENTS:
#   Jaeger:  ~200MB (lightweight, suitable for dev machines)
#   SigNoz:  ~2GB  (feature-rich, requires more RAM)
#
# PREREQUISITES:
#   - kubectl configured with cluster access
#   - Helm 3.x installed
#   - Truva-G3 infrastructure deployed (./setup-infrastructure.sh)
#
# ACCESS AFTER INSTALLATION:
#   SigNoz UI: kubectl port-forward -n signoz svc/signoz-frontend 3301:3301
#              Open http://localhost:3301 (admin@admin.com / admin)
#
#   Jaeger UI: kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80
#              Open http://localhost:16686
#

set -e

NAMESPACE_SIGNOZ="signoz"
NAMESPACE_TRUVAG3="truvag3-examples"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    command -v kubectl >/dev/null 2>&1 || { log_error "kubectl is required but not installed."; exit 1; }
    command -v helm >/dev/null 2>&1 || { log_error "helm is required but not installed."; exit 1; }

    # Check cluster connection
    if ! kubectl cluster-info >/dev/null 2>&1; then
        log_error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
        exit 1
    fi

    log_info "Prerequisites check passed."
}

# Remove Jaeger deployment
remove_jaeger() {
    log_info "Removing Jaeger deployment..."

    # Delete Jaeger resources if they exist
    kubectl delete deployment jaeger -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true
    kubectl delete service jaeger -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true
    kubectl delete service jaeger-query -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true

    log_info "Jaeger resources removed."
}

# Create values file for Kind
create_values_file() {
    log_info "Creating Kind-optimized values file..."

    cat > "${SCRIPT_DIR}/signoz-values-kind.yaml" << 'EOF'
# SigNoz Helm values optimized for Kind cluster
# Total resources: ~1 CPU, ~2GB RAM

global:
  storageClass: "standard"

clickhouse:
  enabled: true
  replicaCount: 1
  shards: 1
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 500m
      memory: 1Gi
  persistence:
    enabled: true
    size: 5Gi
    storageClass: "standard"
  keeper:
    enabled: false

queryService:
  replicaCount: 1
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 200m
      memory: 512Mi

frontend:
  replicaCount: 1
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 100m
      memory: 128Mi
  service:
    type: NodePort
    nodePort: 30301

otelCollector:
  replicaCount: 1
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 200m
      memory: 256Mi

alertmanager:
  enabled: false

schemaMigrator:
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 100m
      memory: 128Mi
EOF

    log_info "Values file created at ${SCRIPT_DIR}/signoz-values-kind.yaml"
}

# Install SigNoz
install_signoz() {
    log_info "Adding SigNoz Helm repository..."
    helm repo add signoz https://charts.signoz.io
    helm repo update

    log_info "Creating namespace ${NAMESPACE_SIGNOZ}..."
    kubectl create namespace ${NAMESPACE_SIGNOZ} --dry-run=client -o yaml | kubectl apply -f -

    log_info "Installing SigNoz (this may take several minutes)..."
    helm upgrade --install signoz signoz/signoz \
        --namespace ${NAMESPACE_SIGNOZ} \
        --values "${SCRIPT_DIR}/signoz-values-kind.yaml" \
        --wait \
        --timeout 15m

    log_info "SigNoz installation complete."
}

# Update OTEL Collector to export traces and metrics to SigNoz
update_otel_collector() {
    log_info "Updating OTEL Collector configuration for traces and metrics..."

    cat > "${SCRIPT_DIR}/otel-collector-signoz.yaml" << 'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector-config
  namespace: truvag3-examples
  labels:
    app: otel-collector
    component: telemetry
data:
  config.yaml: |
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318
      prometheus:
        config:
          scrape_configs:
          - job_name: 'otel-collector'
            scrape_interval: 5s
            static_configs:
            - targets: ['localhost:8888']

    processors:
      batch:
        timeout: 1s
        send_batch_size: 1024
        send_batch_max_size: 2048
      memory_limiter:
        limit_mib: 256
        check_interval: 1s
      resource:
        attributes:
        - key: deployment.environment
          value: "development"
          action: upsert
        - key: service.namespace
          value: "truvag3-examples"
          action: upsert

    exporters:
      otlp/signoz:
        endpoint: signoz-otel-collector.signoz:4317
        tls:
          insecure: true
      prometheus:
        endpoint: "0.0.0.0:8889"
        namespace: "truvag3"
        const_labels:
          cluster: "truvag3-examples"
        resource_to_telemetry_conversion:
          enabled: true

    extensions:
      health_check:
        endpoint: 0.0.0.0:13133

    service:
      extensions: [health_check]
      pipelines:
        # Traces and metrics are sent via OTLP from Truva-G3 apps
        traces:
          receivers: [otlp]
          processors: [memory_limiter, resource, batch]
          exporters: [otlp/signoz]
        metrics:
          receivers: [otlp, prometheus]
          processors: [memory_limiter, resource, batch]
          exporters: [otlp/signoz, prometheus]
        # NOTE: No logs pipeline here - Truva-G3 apps write logs to stdout.
        # Logs are collected separately via the filelog receiver DaemonSet.
      telemetry:
        logs:
          level: "info"
        metrics:
          level: detailed
EOF

    kubectl apply -f "${SCRIPT_DIR}/otel-collector-signoz.yaml"

    # Restart OTEL Collector to pick up new config
    kubectl rollout restart deployment/otel-collector -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true

    log_info "OTEL Collector configuration updated (traces + metrics)."
}

# Deploy log collector DaemonSet to collect stdout logs with trace correlation
deploy_log_collector() {
    log_info "Deploying log collector DaemonSet with trace-log correlation..."

    cat > "${SCRIPT_DIR}/otel-collector-logs.yaml" << 'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector-logs-config
  namespace: truvag3-examples
data:
  config.yaml: |
    receivers:
      filelog:
        include:
          - /var/log/pods/truvag3-examples_*/*/0.log
        exclude:
          # Exclude log collector's own logs to prevent feedback loop
          - /var/log/pods/truvag3-examples_otel-collector-logs-*/*/0.log
        start_at: end
        include_file_path: true
        operators:
          # Parse CRI container log format
          - type: regex_parser
            regex: '^(?P<time>[^ ]+) (?P<stream>stdout|stderr) (?P<logtag>[^ ]*) (?P<log>.*)$'
            timestamp:
              parse_from: attributes.time
              layout: '%Y-%m-%dT%H:%M:%S.%LZ'
          # Filter: DROP non-JSON logs (filter operator drops matching entries)
          - type: filter
            expr: 'attributes.log == nil or not (attributes.log matches "^\\{")'
          # Parse JSON log body from Truva-G3 apps
          - type: json_parser
            parse_from: attributes.log
            on_error: send
            timestamp:
              parse_from: attributes.timestamp
              layout: '%Y-%m-%dT%H:%M:%S.%LZ'
          # Extract trace context from parsed JSON attributes to OTel log record
          - type: trace_parser
            trace_id:
              parse_from: attributes.trace_id
            span_id:
              parse_from: attributes.span_id
            on_error: send

    processors:
      batch:
        timeout: 1s
        send_batch_size: 100
      resource:
        attributes:
          - key: deployment.environment
            value: "development"
            action: upsert
      transform/log_identity:
        log_statements:
          - context: log
            statements:
              - set(resource.attributes["service.name"], attributes["service_name"]) where attributes["service_name"] != nil
              - set(resource.attributes["service.name"], attributes["service"]) where attributes["service_name"] == nil and attributes["service"] != nil
              - delete_key(attributes, "service_name") where attributes["service_name"] != nil

    exporters:
      otlp/signoz:
        endpoint: signoz-otel-collector.signoz:4317
        tls:
          insecure: true

    service:
      pipelines:
        logs:
          receivers: [filelog]
          processors: [resource, transform/log_identity, batch]
          exporters: [otlp/signoz]
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: otel-collector-logs
  namespace: truvag3-examples
spec:
  selector:
    matchLabels:
      app: otel-collector-logs
  template:
    metadata:
      labels:
        app: otel-collector-logs
    spec:
      serviceAccountName: otel-collector-logs
      containers:
      - name: otel-collector
        image: otel/opentelemetry-collector-contrib:latest
        args:
        - --config=/etc/otelcol-contrib/config.yaml
        volumeMounts:
        - name: config
          mountPath: /etc/otelcol-contrib
          readOnly: true
        - name: varlogpods
          mountPath: /var/log/pods
          readOnly: true
        resources:
          requests:
            cpu: 50m
            memory: 64Mi
          limits:
            cpu: 100m
            memory: 128Mi
      volumes:
      - name: config
        configMap:
          name: otel-collector-logs-config
      - name: varlogpods
        hostPath:
          path: /var/log/pods
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: otel-collector-logs
  namespace: truvag3-examples
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: otel-collector-logs
rules:
- apiGroups: [""]
  resources: ["pods", "namespaces"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: otel-collector-logs
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: otel-collector-logs
subjects:
- kind: ServiceAccount
  name: otel-collector-logs
  namespace: truvag3-examples
EOF

    kubectl apply -f "${SCRIPT_DIR}/otel-collector-logs.yaml"

    log_info "Log collector DaemonSet deployed with trace-log correlation."
}

# Wait for pods to be ready
wait_for_pods() {
    log_info "Waiting for SigNoz pods to be ready..."

    kubectl wait --for=condition=ready pod \
        -l app.kubernetes.io/instance=signoz \
        -n ${NAMESPACE_SIGNOZ} \
        --timeout=300s

    log_info "All SigNoz pods are ready."
}

# Configure ClickHouse TTL for log retention (1 day by default)
configure_log_retention() {
    local retention_days="${LOG_RETENTION_DAYS:-1}"
    local retention_seconds=$((retention_days * 86400))

    log_info "Configuring ClickHouse log retention to ${retention_days} day(s)..."

    # Find the ClickHouse pod
    local clickhouse_pod=$(kubectl get pods -n ${NAMESPACE_SIGNOZ} -l app.kubernetes.io/name=clickhouse -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

    if [ -z "$clickhouse_pod" ]; then
        log_warn "ClickHouse pod not found. Skipping TTL configuration."
        log_warn "You can configure TTL manually later with:"
        log_warn "  kubectl exec -n signoz <clickhouse-pod> -- clickhouse-client --query \"ALTER TABLE signoz_logs.distributed_logs MODIFY TTL toDateTime(timestamp / 1000000000) + INTERVAL ${retention_days} DAY\""
        return
    fi

    # Apply TTL to the distributed_logs table
    local ttl_query="ALTER TABLE signoz_logs.distributed_logs MODIFY TTL toDateTime(timestamp / 1000000000) + INTERVAL ${retention_days} DAY"

    if kubectl exec -n ${NAMESPACE_SIGNOZ} "$clickhouse_pod" -- clickhouse-client --query "$ttl_query" 2>/dev/null; then
        log_info "ClickHouse log TTL set to ${retention_days} day(s)."
    else
        log_warn "Failed to set ClickHouse TTL. This may be because the table doesn't exist yet."
        log_warn "After some logs are ingested, you can run:"
        log_warn "  kubectl exec -n signoz $clickhouse_pod -- clickhouse-client --query \"$ttl_query\""
    fi
}

# Print access information
print_access_info() {
    local retention_days="${LOG_RETENTION_DAYS:-1}"

    echo ""
    echo "=============================================="
    echo -e "${GREEN}SigNoz Installation Complete!${NC}"
    echo "=============================================="
    echo ""
    echo "Access SigNoz UI:"
    echo "  Option 1 (NodePort): http://localhost:30301"
    echo "  Option 2 (Port Forward):"
    echo "    kubectl port-forward svc/signoz-frontend -n signoz 3301:3301"
    echo "    Then open: http://localhost:3301"
    echo ""
    echo "Default Credentials (first login):"
    echo "  Email: admin@admin.com"
    echo "  Password: admin"
    echo ""
    echo "What's deployed:"
    echo "  - SigNoz stack (ClickHouse, Query Service, Frontend)"
    echo "  - OTEL Collector updated for traces + metrics → SigNoz"
    echo "  - Log collector DaemonSet for stdout logs → SigNoz"
    echo "  - ClickHouse TTL configured for ${retention_days}-day log retention"
    echo ""
    echo "View pods:"
    echo "  kubectl get pods -n signoz"
    echo "  kubectl get pods -n truvag3-examples -l app=otel-collector-logs"
    echo ""
    echo "Log-Trace Correlation:"
    echo "  Logs from Truva-G3 apps include trace_id and span_id fields."
    echo "  In SigNoz Logs view, click any log's trace_id to jump to its trace."
    echo ""
    echo "Finding trace_id from request_id:"
    echo "  1. Go to Logs tab in SigNoz"
    echo "  2. Search: request_id = 'orch-XXXX'"
    echo "  3. Click on a log entry, find trace_id field"
    echo "  4. Click trace_id to view the full distributed trace"
    echo ""
    echo "Environment Variables:"
    echo "  LOG_RETENTION_DAYS=N  - Set log retention (default: 1 day)"
    echo ""
    echo "=============================================="
}

# Main execution
main() {
    echo ""
    echo "=============================================="
    echo "  SigNoz Setup for Truva-G3 Examples"
    echo "=============================================="
    echo ""

    check_prerequisites
    remove_jaeger
    create_values_file
    install_signoz
    update_otel_collector
    deploy_log_collector
    wait_for_pods
    configure_log_retention
    print_access_info
}

# Revert to Jaeger (undo SigNoz setup)
revert_to_jaeger() {
    echo ""
    echo "=============================================="
    echo "  Reverting to Jaeger (lightweight tracing)"
    echo "=============================================="
    echo ""

    check_prerequisites

    log_info "Restoring original OTEL Collector configuration..."
    kubectl apply -f "${SCRIPT_DIR}/otel-collector.yaml"

    log_info "Redeploying Jaeger..."
    kubectl apply -f "${SCRIPT_DIR}/jaeger.yaml"

    log_info "Removing log collector DaemonSet..."
    kubectl delete daemonset otel-collector-logs -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true
    kubectl delete configmap otel-collector-logs-config -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true
    kubectl delete serviceaccount otel-collector-logs -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true
    kubectl delete clusterrole otel-collector-logs 2>/dev/null || true
    kubectl delete clusterrolebinding otel-collector-logs 2>/dev/null || true

    log_info "Restarting OTEL Collector..."
    kubectl rollout restart deployment/otel-collector -n ${NAMESPACE_TRUVAG3}

    log_info "Waiting for Jaeger to be ready..."
    kubectl wait --for=condition=available --timeout=120s deployment/jaeger -n ${NAMESPACE_TRUVAG3} 2>/dev/null || true

    echo ""
    echo -e "${GREEN}Reverted to Jaeger!${NC}"
    echo ""
    echo "Access Jaeger UI:"
    echo "  kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80"
    echo "  Open: http://localhost:16686"
    echo ""
    echo "Note: SigNoz is still installed but not receiving data."
    echo "To fully remove SigNoz: helm uninstall signoz -n signoz"
    echo ""
}

# Show help
show_help() {
    cat << 'EOF'
SigNoz Setup Script for Truva-G3 Examples
========================================

SigNoz is an optional, feature-rich alternative to Jaeger for distributed
tracing. It provides trace-log correlation, unified observability, and
a ClickHouse-powered backend.

USAGE:
  ./setup-signoz.sh [COMMAND]

COMMANDS:
  setup     Install SigNoz stack (default if no command given)
            - Installs SigNoz via Helm (ClickHouse, Query Service, Frontend)
            - Updates OTEL Collector to export traces/metrics to SigNoz
            - Deploys log collector DaemonSet with trace-log correlation
            - Configures ClickHouse TTL for log retention
            - Removes Jaeger (SigNoz replaces it)

  revert    Switch back to Jaeger (lightweight tracing)
            - Restores original OTEL Collector configuration
            - Redeploys Jaeger
            - Removes log collector DaemonSet
            - Leaves SigNoz installed but idle (helm uninstall to fully remove)

  help      Show this help message

ENVIRONMENT VARIABLES:
  LOG_RETENTION_DAYS    Number of days to retain logs (default: 1)

EXAMPLES:
  ./setup-signoz.sh                        # Install SigNoz with defaults
  LOG_RETENTION_DAYS=7 ./setup-signoz.sh   # Install with 7-day retention
  ./setup-signoz.sh revert                 # Switch back to Jaeger
  ./setup-signoz.sh help                   # Show this help

COMPARISON: Jaeger vs SigNoz
  ┌─────────────────────┬────────────────────┬─────────────────────┐
  │ Feature             │ Jaeger (Default)   │ SigNoz (Optional)   │
  ├─────────────────────┼────────────────────┼─────────────────────┤
  │ Memory              │ ~200MB             │ ~2GB                │
  │ Traces              │ Yes                │ Yes                 │
  │ Logs                │ No                 │ Yes (correlated)    │
  │ Trace-Log Link      │ No                 │ Yes (click to jump) │
  │ Best for            │ Dev machines, CI   │ Full debugging      │
  └─────────────────────┴────────────────────┴─────────────────────┘

ACCESS AFTER INSTALLATION:
  SigNoz:  kubectl port-forward -n signoz svc/signoz-frontend 3301:3301
           Open http://localhost:3301 (admin@admin.com / admin)

  Jaeger:  kubectl port-forward -n truvag3-examples svc/jaeger-query 16686:80
           Open http://localhost:16686

PREREQUISITES:
  - kubectl configured with cluster access
  - Helm 3.x installed
  - Truva-G3 infrastructure deployed (./setup-infrastructure.sh)

TO FULLY REMOVE SIGNOZ:
  helm uninstall signoz -n signoz
  kubectl delete namespace signoz

EOF
}

# Handle commands
case "${1:-}" in
    ""|"setup")
        main
        ;;
    "revert")
        revert_to_jaeger
        ;;
    "help"|"-h"|"--help")
        show_help
        ;;
    *)
        echo -e "${RED}Unknown command: $1${NC}"
        echo ""
        show_help
        exit 1
        ;;
esac
