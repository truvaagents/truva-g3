#!/bin/bash
# Hotel Tool Setup Script
# Provides commands for building, running, and deploying the hotel tool

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="hotel-tool"
PORT=${PORT:-8343}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() { echo -e "${BLUE}================================================${NC}"; echo -e "${BLUE}  Hotel Tool - $1${NC}"; echo -e "${BLUE}================================================${NC}"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_error() { echo -e "${RED}[ERROR]${NC} $1"; }
print_info() { echo -e "${YELLOW}[INFO]${NC} $1"; }

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

check_command() { if ! command -v $1 &> /dev/null; then print_error "$1 is not installed"; exit 1; fi; }

cmd_cluster() { truvag3_create_cluster; }

cmd_infra() { truvag3_setup_infra; }

cmd_build() {
    print_header "Building Hotel Tool"
    print_info "Running go mod tidy..."; GOWORK=off go mod tidy
    print_info "Building binary..."; GOWORK=off go build -o hotel-tool .
    print_success "Build completed: hotel-tool"
}

cmd_run() {
    print_header "Running Hotel Tool"
    load_env
    if [ -z "$REDIS_URL" ]; then print_error "REDIS_URL required"; exit 1; fi
    cmd_build
    print_info "Starting hotel-tool on port $PORT..."
    ./hotel-tool
}

cmd_docker_build() {
    local no_cache=""
    [ "${DOCKER_NO_CACHE:-}" = "true" ] && no_cache="--no-cache"

    if [ -f "$SCRIPT_DIR/Dockerfile.workspace" ]; then
        truvag3_build_docker "$APP_NAME:latest" \
            "$SCRIPT_DIR/Dockerfile.workspace" "$(dirname "$EXAMPLES_DIR")" $no_cache
    else
        truvag3_build_docker "$APP_NAME:latest" \
            "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR" $no_cache
    fi
}

setup_api_keys() {
    truvag3_create_secret "ai-provider-keys" "$NAMESPACE"
    truvag3_create_tool_secret "hotel-tool-secrets" "$NAMESPACE" "LITEAPI_KEY"
}

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + tool-specific vars)
setup_config() {
    truvag3_create_configmap "hotel-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env" "OTEL_SERVICE_NAME"
}

cmd_deploy() {
    print_header "Deploying to Kubernetes"
    load_env; cmd_docker_build
    if command -v kind &> /dev/null; then
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"; print_success "Image loaded"
    fi
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
    setup_api_keys
    setup_config
    kubectl wait --for=condition=available --timeout=30s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null || true
    kubectl apply -f k8-deployment.yaml
    if kubectl wait --for=condition=available --timeout=120s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null; then
        print_success "$APP_NAME deployed successfully!"
    else
        print_error "Deployment failed"; kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20; exit 1
    fi
}

cmd_full_deploy() {
    print_header "Full Deployment"
    load_env; cmd_cluster; cmd_infra; cmd_deploy; echo "Deploy complete. Tool is accessible within the cluster via ClusterIP."
}

cmd_test() {
    print_header "Running Tests"
    kubectl port-forward -n $NAMESPACE svc/hotel-tool-service 8343:80 >/dev/null 2>&1 &
    PF_PID=$!; sleep 3
    echo "Testing health..."; curl -s http://localhost:8343/health | grep -q "healthy" && print_success "Health OK" || print_error "Health failed"
    echo "Testing capabilities..."; curl -s http://localhost:8343/api/capabilities | grep -q "capabilities" && print_success "Capabilities OK" || print_error "Capabilities failed"
    echo ""; print_info "Testing hotel search..."
    curl -s -X POST http://localhost:8343/api/capabilities/search_hotels \
        -H "Content-Type: application/json" \
        -d '{"city_name":"Paris","country_code":"FR","check_in":"2026-06-15","check_out":"2026-06-18"}' | jq . 2>/dev/null || echo "(install jq for pretty output)"
    kill $PF_PID 2>/dev/null || true
}

cmd_forward() {
    truvag3_forward "hotel-tool-service" 8343 80
}

cmd_forward_all() {
    truvag3_forward_all \
        "hotel-tool-service:8343:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

cmd_logs() { kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100; }
cmd_status() {
    print_header "Deployment Status"
    echo "Hotel Tool Pod:"; kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""; echo "Hotel Tool Service:"; kubectl get svc -n $NAMESPACE -l app=$APP_NAME
}

cmd_rollout() {
    print_header "Rolling Out Deployment"
    local rebuild=false
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then rebuild=true; fi
    load_env; setup_api_keys; setup_config
    if [ "$rebuild" = true ]; then cmd_docker_build; if command -v kind &> /dev/null; then kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"; fi; fi
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then print_success "Rollout complete!"; else print_error "Rollout failed"; exit 1; fi
}

cmd_clean() { kubectl delete -f k8-deployment.yaml --ignore-not-found; print_success "Tool cleanup complete"; }
cmd_clean_all() {
    pkill -f "kubectl.*port-forward.*$NAMESPACE" 2>/dev/null || true
    kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true
    truvag3_delete_cluster
    print_success "Full cleanup complete"
}

cmd_rebuild() {
    print_header "Rebuilding with Fresh Dependencies"
    load_env; DOCKER_NO_CACHE=true cmd_docker_build
    if command -v kind &> /dev/null; then kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"; fi
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
    setup_api_keys; setup_config; kubectl apply -f k8-deployment.yaml
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then print_success "Rebuilt!"; else print_error "Failed"; exit 1; fi
}

cmd_help() {
    echo "Hotel Tool Setup Script"
    echo ""; echo "Usage: ./setup.sh <command>"
    echo ""; echo "Commands: build run docker-build cluster infra deploy full-deploy test forward forward-all logs status rollout clean clean-all rebuild help"
    echo ""; echo "Environment Variables:"
    echo "  REDIS_URL              Redis connection URL (required for run)"
    echo "  PORT                   HTTP server port (default: 8343)"
    echo "  LITEAPI_KEY            LiteAPI key (hotel data + rates + reviews)"
}

case "${1:-help}" in
    build) cmd_build ;; run) cmd_run ;; docker-build) cmd_docker_build ;; cluster) cmd_cluster ;;
    infra) cmd_infra ;; deploy) cmd_deploy ;; rebuild) cmd_rebuild ;; full-deploy) cmd_full_deploy ;;
    test) cmd_test ;; forward) cmd_forward ;; forward-all) cmd_forward_all ;; logs) cmd_logs ;;
    status) cmd_status ;; rollout) cmd_rollout "$@" ;; clean) cmd_clean ;; clean-all) cmd_clean_all ;;
    help|--help|-h) cmd_help ;; *) print_error "Unknown command: $1"; cmd_help; exit 1 ;;
esac
