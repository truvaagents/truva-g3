#!/bin/bash

# TruvaG3 Infrastructure Setup Script
# Intelligently deploys infrastructure components only if they don't exist
# Never deletes existing resources - always checks services first

set -e

# Colors
COLOR_RED='\033[0;31m'
COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_BLUE='\033[0;34m'
COLOR_PURPLE='\033[0;35m'
COLOR_NC='\033[0m'

NAMESPACE=${NAMESPACE:-truvag3-examples}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo -e "${COLOR_BLUE}"
echo "🏗️  TruvaG3 Infrastructure Setup"
echo "================================"
echo -e "${COLOR_NC}"

# Function to check if a service exists and is healthy
check_service_exists() {
    local service_name=$1
    local namespace=$2

    if kubectl get service "$service_name" -n "$namespace" &>/dev/null; then
        # Service exists, check if it has endpoints (healthy)
        local endpoints=$(kubectl get endpoints "$service_name" -n "$namespace" -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null)
        if [ -n "$endpoints" ]; then
            return 0  # Service exists and is healthy
        else
            return 1  # Service exists but no healthy endpoints
        fi
    else
        return 2  # Service doesn't exist
    fi
}

# Function to check if deployment is ready
check_deployment_ready() {
    local deployment_name=$1
    local namespace=$2

    local ready=$(kubectl get deployment "$deployment_name" -n "$namespace" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)
    if [ "$ready" = "True" ]; then
        return 0
    else
        return 1
    fi
}

# Function to create namespace if it doesn't exist
setup_namespace() {
    echo -e "${COLOR_YELLOW}📁 Checking namespace...${COLOR_NC}"

    if kubectl get namespace "$NAMESPACE" &>/dev/null; then
        echo -e "${COLOR_GREEN}✅ Namespace '$NAMESPACE' already exists${COLOR_NC}"
    else
        echo -e "${COLOR_BLUE}📦 Creating namespace '$NAMESPACE'...${COLOR_NC}"
        kubectl apply -f "$SCRIPT_DIR/namespace.yaml"
        echo -e "${COLOR_GREEN}✅ Namespace created${COLOR_NC}"
    fi
    echo ""
}

# Function to deploy a component with checks
deploy_component() {
    local component_name=$1
    local service_name=$2
    local deployment_name=$3
    local yaml_file=$4

    echo -e "${COLOR_YELLOW}🔍 Checking ${component_name}...${COLOR_NC}"

    # Check if service exists (capture exit status without triggering set -e)
    local service_status=0
    check_service_exists "$service_name" "$NAMESPACE" && service_status=0 || service_status=$?

    if [ $service_status -eq 0 ]; then
        # Service exists and is healthy
        local deployment_ready=0
        check_deployment_ready "$deployment_name" "$NAMESPACE" && deployment_ready=0 || deployment_ready=$?
        if [ $deployment_ready -eq 0 ]; then
            echo -e "${COLOR_GREEN}✅ ${component_name} already running and healthy${COLOR_NC}"
            echo -e "${COLOR_BLUE}   Service: ${service_name}, Deployment: ${deployment_name}${COLOR_NC}"
            return 0
        else
            echo -e "${COLOR_YELLOW}⚠️  ${component_name} service exists but deployment not ready${COLOR_NC}"
            echo -e "${COLOR_BLUE}   Checking if we should redeploy...${COLOR_NC}"
        fi
    elif [ $service_status -eq 1 ]; then
        echo -e "${COLOR_YELLOW}⚠️  ${component_name} service exists but has no healthy endpoints${COLOR_NC}"
        echo -e "${COLOR_BLUE}   Will apply configuration to fix...${COLOR_NC}"
    else
        echo -e "${COLOR_BLUE}📦 ${component_name} not found, deploying...${COLOR_NC}"
    fi

    # Deploy or update the component
    kubectl apply -f "$SCRIPT_DIR/$yaml_file"

    # Wait for deployment to be ready
    echo -e "${COLOR_BLUE}⏳ Waiting for ${component_name} to be ready...${COLOR_NC}"
    if kubectl wait --for=condition=available --timeout=120s deployment/"$deployment_name" -n "$NAMESPACE" 2>/dev/null; then
        echo -e "${COLOR_GREEN}✅ ${component_name} is ready${COLOR_NC}"
    else
        echo -e "${COLOR_YELLOW}⚠️  ${component_name} deployment timeout, but may still be starting${COLOR_NC}"
    fi

    echo ""
}

# Function to deploy a DaemonSet component with checks
deploy_daemonset() {
    local component_name=$1
    local daemonset_name=$2
    local yaml_file=$3

    echo -e "${COLOR_YELLOW}🔍 Checking ${component_name}...${COLOR_NC}"

    # Check if DaemonSet exists and has desired number of pods scheduled
    local desired=$(kubectl get daemonset "$daemonset_name" -n "$NAMESPACE" -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null)
    local ready=$(kubectl get daemonset "$daemonset_name" -n "$NAMESPACE" -o jsonpath='{.status.numberReady}' 2>/dev/null)

    if [ -n "$desired" ] && [ "$desired" -gt 0 ] && [ "$desired" = "$ready" ]; then
        echo -e "${COLOR_GREEN}✅ ${component_name} already running and healthy (${ready}/${desired} pods)${COLOR_NC}"
        return 0
    elif [ -n "$desired" ]; then
        echo -e "${COLOR_YELLOW}⚠️  ${component_name} exists but not fully ready (${ready:-0}/${desired} pods)${COLOR_NC}"
        echo -e "${COLOR_BLUE}   Reapplying configuration...${COLOR_NC}"
    else
        echo -e "${COLOR_BLUE}📦 ${component_name} not found, deploying...${COLOR_NC}"
    fi

    # Deploy or update the DaemonSet
    kubectl apply -f "$SCRIPT_DIR/$yaml_file"

    # Wait for rollout
    echo -e "${COLOR_BLUE}⏳ Waiting for ${component_name} to be ready...${COLOR_NC}"
    if kubectl rollout status daemonset/"$daemonset_name" -n "$NAMESPACE" --timeout=60s 2>/dev/null; then
        echo -e "${COLOR_GREEN}✅ ${component_name} is ready${COLOR_NC}"
    else
        echo -e "${COLOR_YELLOW}⚠️  ${component_name} rollout timeout, but may still be starting${COLOR_NC}"
    fi

    echo ""
}

# Function to rebuild a component: re-applies YAML and restarts the workload
# so ConfigMap/RBAC changes actually take effect. Unlike deploy_*, this does
# NOT skip on health — the whole point of rebuild is to push a config change.
rebuild_component() {
    local component_name=$1
    local yaml_file=$2
    local workload_kind=$3   # "deployment" or "daemonset"
    local workload_name=$4

    echo -e "${COLOR_YELLOW}🔄 Rebuilding ${component_name}...${COLOR_NC}"

    if [ ! -f "$SCRIPT_DIR/$yaml_file" ]; then
        echo -e "${COLOR_RED}❌ Missing file: $yaml_file${COLOR_NC}"
        return 1
    fi

    # Apply the full YAML (ConfigMap, workload, ServiceAccount, RBAC, etc.)
    kubectl apply -f "$SCRIPT_DIR/$yaml_file"

    # If the workload already exists, roll it to pick up ConfigMap changes.
    # A fresh kubectl apply of a ConfigMap does not by itself restart the pods
    # that mount it, so the rollout restart is what actually delivers the fix.
    if kubectl get "${workload_kind}/${workload_name}" -n "$NAMESPACE" &>/dev/null; then
        echo -e "${COLOR_BLUE}⏳ Restarting ${workload_kind}/${workload_name}...${COLOR_NC}"
        kubectl rollout restart "${workload_kind}/${workload_name}" -n "$NAMESPACE"
        if kubectl rollout status "${workload_kind}/${workload_name}" -n "$NAMESPACE" --timeout=120s 2>/dev/null; then
            echo -e "${COLOR_GREEN}✅ ${component_name} rebuilt${COLOR_NC}"
        else
            echo -e "${COLOR_YELLOW}⚠️  ${component_name} rollout timeout, pods may still be starting${COLOR_NC}"
        fi
    else
        echo -e "${COLOR_BLUE}ℹ️  ${workload_kind}/${workload_name} did not pre-exist; newly created by apply${COLOR_NC}"
    fi

    echo ""
}

# Rebuild all truvag3-examples infrastructure workloads in dependency order.
# Does NOT touch ingress-nginx (kube-system) or metrics-server (kube-system) —
# those are rarely reconfigured and have their own deploy paths above.
rebuild_all() {
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo -e "${COLOR_GREEN}🔁 Rebuilding all infrastructure${COLOR_NC}"
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo ""

    rebuild_component "Redis"              "redis.yaml"               "deployment" "redis"
    rebuild_component "Loki"               "loki.yaml"                "deployment" "loki"
    rebuild_component "OTEL Collector"     "otel-collector.yaml"      "deployment" "otel-collector"
    rebuild_component "OTEL Log Collector" "otel-collector-logs.yaml" "daemonset"  "otel-collector-logs"
    rebuild_component "Prometheus"         "prometheus.yaml"          "deployment" "prometheus"
    rebuild_component "Jaeger"             "jaeger.yaml"              "deployment" "jaeger"
    kubectl apply -f "$SCRIPT_DIR/grafana-dashboards-new.yaml" >/dev/null
    rebuild_component "Grafana"            "grafana.yaml"             "deployment" "grafana"
    rebuild_component "Swagger UI"         "swagger-ui.yaml"          "deployment" "swagger-ui"

    if [ -f "$SCRIPT_DIR/qdrant.yaml" ] && kubectl get deployment qdrant -n "$NAMESPACE" &>/dev/null; then
        rebuild_component "Qdrant" "qdrant.yaml" "deployment" "qdrant"
    fi

    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo -e "${COLOR_GREEN}✅ Rebuild complete${COLOR_NC}"
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo ""
}

# Rebuild one named component, or all if no name given.
rebuild_target() {
    local target=$1
    case "$target" in
        "otel-collector")      rebuild_component "OTEL Collector"     "otel-collector.yaml"      "deployment" "otel-collector" ;;
        "otel-collector-logs") rebuild_component "OTEL Log Collector" "otel-collector-logs.yaml" "daemonset"  "otel-collector-logs" ;;
        "loki")                rebuild_component "Loki"               "loki.yaml"                "deployment" "loki" ;;
        "prometheus")          rebuild_component "Prometheus"         "prometheus.yaml"          "deployment" "prometheus" ;;
        "jaeger")              rebuild_component "Jaeger"             "jaeger.yaml"              "deployment" "jaeger" ;;
        "grafana")             kubectl apply -f "$SCRIPT_DIR/grafana-dashboards-new.yaml" >/dev/null; rebuild_component "Grafana"            "grafana.yaml"             "deployment" "grafana" ;;
        "redis")               rebuild_component "Redis"              "redis.yaml"               "deployment" "redis" ;;
        "swagger-ui")          rebuild_component "Swagger UI"         "swagger-ui.yaml"          "deployment" "swagger-ui" ;;
        "qdrant")              rebuild_component "Qdrant"             "qdrant.yaml"              "deployment" "qdrant" ;;
        "")                    rebuild_all ;;
        *)
            echo -e "${COLOR_RED}❌ Unknown component: $target${COLOR_NC}"
            echo -e "${COLOR_BLUE}   Valid targets: otel-collector, otel-collector-logs, loki, prometheus, jaeger, grafana, redis, swagger-ui, qdrant${COLOR_NC}"
            echo -e "${COLOR_BLUE}   Omit the target to rebuild all.${COLOR_NC}"
            exit 1
            ;;
    esac
}

# Function to show infrastructure status
show_status() {
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo -e "${COLOR_GREEN}📊 Infrastructure Status${COLOR_NC}"
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo ""

    echo -e "${COLOR_BLUE}Services:${COLOR_NC}"
    kubectl get svc -n "$NAMESPACE" -o wide 2>/dev/null || echo "No services found"
    echo ""

    echo -e "${COLOR_BLUE}Deployments:${COLOR_NC}"
    kubectl get deployments -n "$NAMESPACE" -o wide 2>/dev/null || echo "No deployments found"
    echo ""

    echo -e "${COLOR_BLUE}Pods:${COLOR_NC}"
    kubectl get pods -n "$NAMESPACE" -o wide 2>/dev/null || echo "No pods found"
    echo ""

    echo -e "${COLOR_BLUE}Persistent Volume Claims:${COLOR_NC}"
    kubectl get pvc -n "$NAMESPACE" 2>/dev/null || echo "No PVCs found"
    echo ""
}

# Function to check prerequisites
check_prerequisites() {
    echo -e "${COLOR_YELLOW}🔍 Checking prerequisites...${COLOR_NC}"

    if ! command -v kubectl &>/dev/null; then
        echo -e "${COLOR_RED}❌ kubectl not found${COLOR_NC}"
        echo "Please install kubectl: https://kubernetes.io/docs/tasks/tools/"
        exit 1
    fi

    # Check if kubectl can connect to cluster
    if ! kubectl cluster-info &>/dev/null; then
        echo -e "${COLOR_RED}❌ Cannot connect to Kubernetes cluster${COLOR_NC}"
        echo "Please ensure your kubeconfig is set up correctly"
        exit 1
    fi

    echo -e "${COLOR_GREEN}✅ Prerequisites OK${COLOR_NC}"
    echo -e "${COLOR_BLUE}   Cluster: $(kubectl config current-context)${COLOR_NC}"
    echo ""
}

# Function to verify files exist
verify_files() {
    echo -e "${COLOR_YELLOW}🔍 Verifying configuration files...${COLOR_NC}"

    local files=(
        "namespace.yaml"
        "ingress-nginx.yaml"
        "ingress-infra.yaml"
        "redis.yaml"
        "otel-collector.yaml"
        "otel-collector-logs.yaml"
        "loki.yaml"
        "prometheus.yaml"
        "jaeger.yaml"
        "grafana-dashboards-new.yaml"
        "grafana.yaml"
        "metrics-server.yaml"
        "swagger-ui.yaml"
        "qdrant.yaml"
    )

    local missing=()
    for file in "${files[@]}"; do
        if [ ! -f "$SCRIPT_DIR/$file" ]; then
            missing+=("$file")
        fi
    done

    if [ ${#missing[@]} -ne 0 ]; then
        echo -e "${COLOR_RED}❌ Missing files: ${missing[*]}${COLOR_NC}"
        exit 1
    fi

    echo -e "${COLOR_GREEN}✅ All configuration files found${COLOR_NC}"
    echo ""
}

# Function to deploy metrics-server (in kube-system namespace)
deploy_metrics_server() {
    echo -e "${COLOR_YELLOW}🔍 Checking Metrics Server...${COLOR_NC}"

    # Check if metrics-server is already running in kube-system
    if kubectl get deployment metrics-server -n kube-system &>/dev/null; then
        local ready=$(kubectl get deployment metrics-server -n kube-system -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)
        if [ "$ready" = "True" ]; then
            echo -e "${COLOR_GREEN}✅ Metrics Server already running and healthy${COLOR_NC}"
            return 0
        else
            echo -e "${COLOR_YELLOW}⚠️  Metrics Server exists but not ready, reapplying...${COLOR_NC}"
        fi
    else
        echo -e "${COLOR_BLUE}📦 Metrics Server not found, deploying...${COLOR_NC}"
    fi

    # Deploy metrics-server
    kubectl apply -f "$SCRIPT_DIR/metrics-server.yaml"

    # Wait for deployment to be ready
    echo -e "${COLOR_BLUE}⏳ Waiting for Metrics Server to be ready...${COLOR_NC}"
    if kubectl wait --for=condition=available --timeout=120s deployment/metrics-server -n kube-system 2>/dev/null; then
        echo -e "${COLOR_GREEN}✅ Metrics Server is ready${COLOR_NC}"
        echo -e "${COLOR_BLUE}   kubectl top pods/nodes will be available shortly${COLOR_NC}"
    else
        echo -e "${COLOR_YELLOW}⚠️  Metrics Server deployment timeout, but may still be starting${COLOR_NC}"
    fi

    echo ""
}

# Function to deploy NGINX Ingress Controller
deploy_ingress_controller() {
    echo -e "${COLOR_YELLOW}🔍 Checking NGINX Ingress Controller...${COLOR_NC}"

    # Check if ingress-nginx namespace and controller exist
    if kubectl get deployment ingress-nginx-controller -n ingress-nginx &>/dev/null; then
        local ready=$(kubectl get deployment ingress-nginx-controller -n ingress-nginx -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)
        if [ "$ready" = "True" ]; then
            echo -e "${COLOR_GREEN}✅ NGINX Ingress Controller already running and healthy${COLOR_NC}"
            return 0
        else
            echo -e "${COLOR_YELLOW}⚠️  Ingress Controller exists but not ready, reapplying...${COLOR_NC}"
        fi
    else
        echo -e "${COLOR_BLUE}📦 NGINX Ingress Controller not found, deploying...${COLOR_NC}"
    fi

    kubectl apply -f "$SCRIPT_DIR/ingress-nginx.yaml"

    echo -e "${COLOR_BLUE}⏳ Waiting for Ingress Controller to be ready...${COLOR_NC}"
    if kubectl wait --for=condition=available --timeout=120s deployment/ingress-nginx-controller -n ingress-nginx 2>/dev/null; then
        echo -e "${COLOR_GREEN}✅ NGINX Ingress Controller is ready${COLOR_NC}"
    else
        echo -e "${COLOR_YELLOW}⚠️  Ingress Controller timeout, but may still be starting${COLOR_NC}"
    fi

    echo ""
}

# Function to deploy infrastructure Ingress resources (after all services are up)
deploy_infra_ingress() {
    echo -e "${COLOR_YELLOW}🔍 Setting up Ingress routes for infrastructure...${COLOR_NC}"
    kubectl apply -f "$SCRIPT_DIR/ingress-infra.yaml"
    echo -e "${COLOR_GREEN}✅ Infrastructure Ingress routes configured${COLOR_NC}"
    echo ""
}

# Main deployment function
main() {
    check_prerequisites
    verify_files

    # Create namespace first
    setup_namespace

    # Deploy NGINX Ingress Controller (must be first — all services route through it)
    deploy_ingress_controller

    # Deploy Metrics Server (in kube-system, enables kubectl top)
    deploy_metrics_server

    # Deploy components in dependency order
    # Each component is checked before deployment

    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo -e "${COLOR_GREEN}🚀 Deploying Infrastructure Components${COLOR_NC}"
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo ""

    # 1. Redis - Required by all components for service discovery
    deploy_component "Redis" "redis" "redis" "redis.yaml"

    # 2. Loki - Log aggregation (must be up before OTEL Collector routes logs to it)
    deploy_component "Loki" "loki" "loki" "loki.yaml"

    # 3. OTEL Collector - Telemetry pipeline (traces + metrics + logs)
    deploy_component "OTEL Collector" "otel-collector" "otel-collector" "otel-collector.yaml"

    # 3b. OTEL Collector Logs - DaemonSet for pod log collection
    deploy_daemonset "OTEL Log Collector" "otel-collector-logs" "otel-collector-logs.yaml"

    # 4. Prometheus - Metrics storage
    deploy_component "Prometheus" "prometheus" "prometheus" "prometheus.yaml"

    # 5. Jaeger - Distributed tracing
    deploy_component "Jaeger" "jaeger-query" "jaeger" "jaeger.yaml"

    # 6. Grafana - Visualization (datasources: Prometheus, Jaeger, Loki)
    # Apply the dashboards ConfigMap first; the Grafana deployment mounts it as a volume.
    kubectl apply -f "$SCRIPT_DIR/grafana-dashboards-new.yaml" >/dev/null
    deploy_component "Grafana" "grafana" "grafana" "grafana.yaml"

    # 7. Swagger UI - Interactive OpenAPI documentation for all TruvaG3 services
    deploy_component "Swagger UI" "swagger-ui" "swagger-ui" "swagger-ui.yaml"

    # 8. Registry Viewer - Real-time UI for the Redis service registry.
    # Lives under examples/registry-viewer-app/ because it has its own Docker
    # image (not a stock container). We delegate to its setup.sh, which builds,
    # loads into kind, and applies its k8-deployment.yaml.
    local viewer_dir="$SCRIPT_DIR/../registry-viewer-app"
    if [ -d "$viewer_dir" ] && [ -f "$viewer_dir/setup.sh" ]; then
        echo ""
        echo -e "${COLOR_PURPLE}🔍 Deploying Registry Viewer...${COLOR_NC}"
        if (cd "$viewer_dir" && bash ./setup.sh deploy); then
            echo -e "${COLOR_GREEN}✅ Registry Viewer deployed${COLOR_NC}"
        else
            echo -e "${COLOR_YELLOW}⚠️  Registry Viewer deploy failed (continuing)${COLOR_NC}"
        fi
    else
        echo -e "${COLOR_BLUE}ℹ️  Registry Viewer: Skipped (registry-viewer-app/ not found at $viewer_dir)${COLOR_NC}"
    fi

    # 9. Qdrant - Vector DB for Shared Agent Memory (semantic knowledge search)
    # On by default — chat agents (travel-chat-agent, devops-chat-agent) initialize
    # vector-backed memory at startup and log errors when it's missing. Opt out with
    # TRUVAG3_DEPLOY_QDRANT=false for tool-only setups.
    if [ -f "$SCRIPT_DIR/qdrant.yaml" ]; then
        if [ "${TRUVAG3_DEPLOY_QDRANT:-true}" = "true" ]; then
            deploy_component "Qdrant" "qdrant" "qdrant" "qdrant.yaml"
        else
            echo -e "${COLOR_BLUE}ℹ️  Qdrant: Skipped (TRUVAG3_DEPLOY_QDRANT=false)${COLOR_NC}"
        fi
    fi

    # Deploy Ingress routes for infrastructure services (after all services are up)
    deploy_infra_ingress

    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo -e "${COLOR_GREEN}✅ Infrastructure Setup Complete!${COLOR_NC}"
    echo -e "${COLOR_PURPLE}════════════════════════════════════════${COLOR_NC}"
    echo ""

    # Show final status
    show_status

    # Show access information
    echo -e "${COLOR_BLUE}🌐 Access via Ingress (*.localhost):${COLOR_NC}"
    echo -e "   Grafana:         ${COLOR_YELLOW}http://grafana.localhost${COLOR_NC} (admin/admin)"
    echo -e "   Prometheus:      ${COLOR_YELLOW}http://prometheus.localhost${COLOR_NC}"
    echo -e "   Jaeger:          ${COLOR_YELLOW}http://jaeger.localhost${COLOR_NC}"
    echo -e "   Swagger UI:      ${COLOR_YELLOW}http://swagger.localhost${COLOR_NC}"
    echo -e "   Registry Viewer: ${COLOR_YELLOW}http://registry.localhost${COLOR_NC}"
    echo ""

    echo -e "${COLOR_BLUE}🔗 Internal cluster addresses:${COLOR_NC}"
    echo -e "   Redis:          ${COLOR_YELLOW}redis.${NAMESPACE}:6379${COLOR_NC}"
    echo -e "   Qdrant (gRPC):  ${COLOR_YELLOW}qdrant.${NAMESPACE}:6334${COLOR_NC}"
    echo -e "   Qdrant (REST):  ${COLOR_YELLOW}qdrant.${NAMESPACE}:6333${COLOR_NC}"
    echo -e "   Loki:           ${COLOR_YELLOW}loki.${NAMESPACE}:3100${COLOR_NC}"
    echo -e "   OTEL Collector: ${COLOR_YELLOW}otel-collector.${NAMESPACE}:4318${COLOR_NC}"
    echo ""

    echo -e "${COLOR_BLUE}🔧 Useful commands:${COLOR_NC}"
    echo -e "   Status:  ${COLOR_YELLOW}./setup-infrastructure.sh status${COLOR_NC}"
    echo -e "   Logs:    ${COLOR_YELLOW}kubectl logs -n ${NAMESPACE} -l app=<component>${COLOR_NC}"
    echo -e "   Delete:  ${COLOR_YELLOW}kubectl delete namespace ${NAMESPACE}${COLOR_NC}"
    echo ""
}

# Status command
status_only() {
    check_prerequisites
    show_status
}

# Help command
show_help() {
    echo "TruvaG3 Infrastructure Setup Script"
    echo ""
    echo "This script intelligently deploys infrastructure components"
    echo "and never deletes existing resources."
    echo ""
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  setup              Deploy infrastructure (checks existing first, skips healthy) - default"
    echo "  rebuild [target]   Re-apply YAML and restart workload to pick up ConfigMap/RBAC changes."
    echo "                     Omit target to rebuild all. Valid targets:"
    echo "                       otel-collector, otel-collector-logs, loki, prometheus,"
    echo "                       jaeger, grafana, redis, swagger-ui, qdrant"
    echo "  status             Show current infrastructure status"
    echo "  help               Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  NAMESPACE   Kubernetes namespace (default: truvag3-examples)"
    echo ""
    echo "Examples:"
    echo "  $0                                      # Deploy with checks"
    echo "  $0 rebuild otel-collector-logs          # Push config change to the log DaemonSet"
    echo "  $0 rebuild                              # Rebuild every infra workload"
    echo "  $0 status                               # Show current status"
    echo "  NAMESPACE=prod $0                       # Deploy to 'prod' namespace"
    echo ""
    echo "Safety Features:"
    echo "  ✓ Checks if services exist before deploying"
    echo "  ✓ Skips deployment if service is healthy"
    echo "  ✓ Never deletes existing resources"
    echo "  ✓ Shows clear status of what exists vs what's created"
}

# Handle commands
case "${1:-setup}" in
    "setup")
        main
        ;;
    "rebuild")
        check_prerequisites
        rebuild_target "${2:-}"
        ;;
    "status")
        status_only
        ;;
    "help"|"-h"|"--help")
        show_help
        ;;
    *)
        echo -e "${COLOR_RED}❌ Unknown command: $1${COLOR_NC}"
        echo ""
        show_help
        exit 1
        ;;
esac
