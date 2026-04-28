#!/bin/bash
# Travel Advisory Tool Setup Script
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="travel-advisory-tool"
PORT=${PORT:-8345}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() { echo -e "${BLUE}================================================${NC}"; echo -e "${BLUE}  Travel Advisory Tool - $1${NC}"; echo -e "${BLUE}================================================${NC}"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_error() { echo -e "${RED}[ERROR]${NC} $1"; }
print_info() { echo -e "${YELLOW}[INFO]${NC} $1"; }

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }
check_command() { if ! command -v $1 &> /dev/null; then print_error "$1 is not installed"; exit 1; fi; }

cmd_cluster() { truvag3_create_cluster; }

cmd_infra() { truvag3_setup_infra; }

setup_api_keys() {
    # Travel advisory tool has no API keys (free, open API)
    truvag3_create_secret "ai-provider-keys" "$NAMESPACE"
}

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + tool-specific vars)
setup_config() {
    truvag3_create_configmap "travel-advisory-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env" "OTEL_SERVICE_NAME"
}

cmd_build() { cd "$SCRIPT_DIR"; GOWORK=off go build -o $APP_NAME .; print_success "Built $APP_NAME"; }
cmd_run() { cmd_build; export REDIS_URL PORT; ./$APP_NAME; }
cmd_docker_build() {
    local no_cache=""
    [ "${DOCKER_NO_CACHE:-}" = "true" ] && no_cache="--no-cache"
    if [ -f "$SCRIPT_DIR/Dockerfile.workspace" ]; then
        truvag3_build_docker "$APP_NAME:latest" "$SCRIPT_DIR/Dockerfile.workspace" "$(dirname "$EXAMPLES_DIR")" $no_cache
    else
        truvag3_build_docker "$APP_NAME:latest" "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR" $no_cache
    fi
}
cmd_logs() { kubectl logs -n $NAMESPACE -l app=$APP_NAME -f; }
cmd_status() { kubectl get pods -n $NAMESPACE -l app=$APP_NAME; }
cmd_rollout() { load_env; setup_config; kubectl apply -f k8-deployment.yaml; kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE; }
cmd_forward_all() {
    truvag3_forward_all \
        "travel-advisory-service:8345:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

cmd_deploy() {
    print_header "Deploying"; load_env; cmd_docker_build
    if command -v kind &> /dev/null; then kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"; fi
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
    setup_api_keys; setup_config; kubectl apply -f k8-deployment.yaml
    if kubectl wait --for=condition=available --timeout=120s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null; then
        print_success "Deployed!"; else print_error "Failed"; exit 1; fi
}

cmd_full_deploy() { load_env; cmd_cluster; cmd_infra; cmd_deploy; echo "Deploy complete. Tool is accessible within the cluster via ClusterIP."; }

cmd_test() {
    print_header "Running Tests"
    kubectl port-forward -n $NAMESPACE svc/travel-advisory-service 8345:80 >/dev/null 2>&1 &
    PF_PID=$!; sleep 3
    curl -s http://localhost:8345/health | grep -q "healthy" && print_success "Health OK" || print_error "Health failed"
    print_info "Testing travel advisory..."
    curl -s -X POST http://localhost:8345/api/capabilities/get_travel_advisory \
        -H "Content-Type: application/json" \
        -d '{"country":"Thailand"}' | jq . 2>/dev/null || echo "(install jq)"
    echo ""
    print_info "Testing list advisories (level 4)..."
    curl -s -X POST http://localhost:8345/api/capabilities/list_advisories \
        -H "Content-Type: application/json" \
        -d '{"level":4}' | jq . 2>/dev/null || echo "(install jq)"
    kill $PF_PID 2>/dev/null || true
}

cmd_forward() {
    truvag3_forward "travel-advisory-service" 8345 80
}
cmd_clean() { kubectl delete -f k8-deployment.yaml --ignore-not-found; }
cmd_clean_all() { kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true; truvag3_delete_cluster; }
cmd_rebuild() { load_env; DOCKER_NO_CACHE=true cmd_docker_build
    if command -v kind &> /dev/null; then kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"; fi
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -; setup_config; kubectl apply -f k8-deployment.yaml
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE; kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; }

cmd_help() {
    echo "Travel Advisory Tool Setup Script"; echo "Usage: ./setup.sh <command>"
    echo "Commands: build run docker-build cluster infra deploy full-deploy test forward forward-all logs status rollout clean clean-all rebuild help"
    echo "Env: REDIS_URL PORT (No API keys required - State Dept API is free)"
}

case "${1:-help}" in
    build) cmd_build ;; run) cmd_run ;; docker-build) cmd_docker_build ;; cluster) cmd_cluster ;;
    infra) cmd_infra ;; deploy) cmd_deploy ;; rebuild) cmd_rebuild ;; full-deploy) cmd_full_deploy ;;
    test) cmd_test ;; forward) cmd_forward ;; forward-all) cmd_forward_all ;; logs) cmd_logs ;;
    status) cmd_status ;; rollout) cmd_rollout "$@" ;; clean) cmd_clean ;; clean-all) cmd_clean_all ;;
    help|--help|-h) cmd_help ;; *) print_error "Unknown: $1"; cmd_help; exit 1 ;;
esac
