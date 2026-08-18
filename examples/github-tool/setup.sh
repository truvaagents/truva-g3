#!/bin/bash

# setup.sh - One-click setup for github-tool
# Modeled after examples/travel-chat-agent/setup.sh (gold standard).
# Scope-adapted: tools are internal ClusterIP services with no ingress.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="github-tool"
DEFAULT_PORT=8381

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

print_header() {
    echo -e "${BLUE}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║              TruvaG3 GitHub Tool                       ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

check_prerequisites() { truvag3_check_prerequisites; }

setup_redis() {
    log_info "Setting up Redis..."
    if command -v redis-cli &> /dev/null && redis-cli ping &> /dev/null; then
        log_success "Redis already running"
        return 0
    fi
    if [ "${DOCKER_AVAILABLE:-false}" = true ]; then
        log_info "Starting Redis via Docker..."
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" run -d --name truvag3-redis -p 6379:6379 redis:8.2.8-alpine
        log_success "Redis started on port 6379"
    else
        log_error "Redis not available. Install Redis or Docker."
        exit 1
    fi
}

check_github_token() {
    if [ -n "$GITHUB_TOKEN" ]; then
        log_success "GITHUB_TOKEN found in env"
        return 0
    fi
    if [ -f .env ] && grep -qE "^GITHUB_TOKEN=.+" .env; then
        log_success "GITHUB_TOKEN found in .env"
        return 0
    fi
    log_warn "GITHUB_TOKEN not set — public read-only PRs will work but writes will 401."
    log_warn "Generate one: https://github.com/settings/tokens (pull_requests: read/write, contents: read)"
    return 1
}

setup_env() {
    log_info "Setting up environment..."
    if [ ! -f .env ]; then
        cp .env.example .env
        log_success "Created .env from .env.example — edit it with your GITHUB_TOKEN"
    else
        log_success ".env already exists"
    fi
    check_github_token || true
    echo ""
}

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

build_app() {
    log_info "Building github-tool..."
    cd "$SCRIPT_DIR"
    GOWORK=off go mod download
    GOWORK=off go mod tidy
    GOWORK=off go build -o github-tool .
    log_success "Built ./github-tool"
}

build_docker() {
    log_info "Building Docker image (using local workspace modules)..."
    local no_cache=""
    [ "${DOCKER_NO_CACHE:-}" = "true" ] && no_cache="--no-cache"
    truvag3_build_docker "github-tool:latest" \
        "$SCRIPT_DIR/Dockerfile.workspace" "$TRUVAG3_ROOT" $no_cache
}

load_to_kind() { truvag3_load_to_kind "github-tool:latest"; }

# GITHUB_TOKEN goes in its own Secret named github-tool-secrets so it can be
# rotated independently of any agent's AI provider keys.
setup_k8s_secrets() {
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        kubectl create secret generic github-tool-secrets \
            --namespace="$NAMESPACE" \
            --from-literal=GITHUB_TOKEN="$GITHUB_TOKEN" \
            --dry-run=client -o yaml | kubectl apply -f -
        log_success "GitHub token secret applied"
    else
        log_warn "GITHUB_TOKEN not set — github-tool-secrets not created (writes will fail)"
    fi
}

setup_tool_config() {
    truvag3_create_configmap "github-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

deploy_k8s() {
    log_info "Deploying github-tool to Kubernetes..."
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
    load_env
    setup_k8s_secrets
    setup_tool_config
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    kubectl rollout restart deployment/github-tool -n "$NAMESPACE"
    kubectl rollout status deployment/github-tool -n "$NAMESPACE" --timeout=120s
    log_success "github-tool deployed"
}

print_summary() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Setup Complete!                                 ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${BLUE}This is a tool — accessed only by other components in-cluster.${NC}"
    echo "Service:        github-tool-service.$NAMESPACE:80"
    echo "Capabilities:   /api/capabilities"
    echo ""
    echo -e "${BLUE}Smoke test (via port-forward):${NC}"
    echo "  $0 forward    - port-forward $DEFAULT_PORT to localhost"
    echo "  $0 capabilities - fetch /api/capabilities once"
    echo "  $0 logs       - tail the tool logs"
    echo "  $0 cleanup    - delete deployed resources"
    echo ""
}

# capabilities runs /api/capabilities through a one-shot port-forward and
# pretty-prints the response so users can verify capability registration
# without leaving a port-forward running.
capabilities() {
    log_info "Port-forwarding to fetch /api/capabilities..."
    kubectl port-forward -n "$NAMESPACE" service/github-tool-service "$DEFAULT_PORT:80" >/dev/null 2>&1 &
    local pf_pid=$!
    # Give the port-forward a moment to come up.
    sleep 1
    if curl -sS "http://localhost:$DEFAULT_PORT/api/capabilities" | (jq . 2>/dev/null || cat); then
        echo ""
    else
        log_error "Failed to reach /api/capabilities"
    fi
    kill "$pf_pid" 2>/dev/null || true
    wait "$pf_pid" 2>/dev/null || true
}

run_app() {
    log_info "Starting github-tool locally..."
    if [ -f .env ]; then
        set -a
        # shellcheck disable=SC1091
        source .env
        set +a
    fi
    export REDIS_URL=${REDIS_URL:-redis://localhost:6379}
    export PORT=${PORT:-$DEFAULT_PORT}
    ./github-tool
}

run_all() {
    if ! redis-cli ping 2>/dev/null | grep -q PONG; then
        setup_redis
    fi
    setup_env
    build_app
    run_app
}

full_deploy() {
    print_header
    log_info "Starting full deployment (non-blocking)..."
    truvag3_create_cluster
    truvag3_setup_infra
    load_env
    build_docker
    load_to_kind
    deploy_k8s
    print_summary
}

rebuild() {
    log_info "Rebuilding with --no-cache and redeploying..."
    DOCKER_NO_CACHE=true build_docker
    if command -v kind &> /dev/null; then
        local cluster_name
        cluster_name=$(kubectl config current-context 2>/dev/null | sed 's/kind-//')
        if kind get clusters 2>/dev/null | grep -q "^${cluster_name}$"; then
            load_to_kind
        fi
    fi
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
    load_env
    setup_k8s_secrets
    setup_tool_config
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    kubectl rollout restart deployment/github-tool -n "$NAMESPACE"
    kubectl rollout status deployment/github-tool -n "$NAMESPACE" --timeout=120s
    log_success "Rebuild complete"
}

rollout() {
    print_header
    load_env
    setup_k8s_secrets
    setup_tool_config
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    kubectl rollout restart deployment/github-tool -n "$NAMESPACE"
    log_success "Rollout triggered"
}

logs() {
    log_info "Tailing logs for github-tool..."
    kubectl logs -n "$NAMESPACE" -l app=github-tool --tail 50 -f
}

cleanup() {
    log_info "Removing deployed resources..."
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete secret github-tool-secrets -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true
    rm -f "$SCRIPT_DIR/github-tool"
    log_success "Cleanup complete"
}

cleanup_all() {
    cleanup
    truvag3_delete_cluster
    log_success "Full cleanup complete"
}

show_help() {
    print_header
    cat <<EOF
Usage: $0 <command>

Local development:
  setup           Setup the local environment (.env, deps)
  run             Build and run the tool locally
  run-all         Setup Redis, build, and run (recommended for local dev)
  build           Build the tool binary only
  redis           Start a local Redis container

Kubernetes cluster:
  cluster         Create a Kind cluster with NGINX Ingress
  infra           Deploy monitoring (Prometheus, Grafana, Jaeger, OTEL)
  full-deploy     Cluster + infra + tool deployment (one-shot, non-blocking)

Kubernetes deployment:
  docker          Build the Docker image
  deploy          Build, load, and deploy to Kubernetes
  rebuild         Rebuild with --no-cache and redeploy
  rollout         Re-apply manifest + restart (pick up new env/secrets)
  logs            Tail tool logs
  cleanup         Remove deployed resources
  cleanup-all     Remove resources + delete Kind cluster

Smoke testing:
  capabilities    Fetch /api/capabilities through a one-shot port-forward
  forward         Port-forward $DEFAULT_PORT to localhost (foreground)

This is a tool, not an agent — there is NO Ingress. Other components reach
github-tool via service discovery on the Redis registry. To reach it from
outside the cluster, use 'forward' or 'capabilities'.

Examples:
  $0 run-all                    # local dev
  $0 full-deploy                # one-shot K8s deploy
  $0 capabilities               # verify capabilities are registered
EOF
}

case "${1:-help}" in
    setup)            check_prerequisites; setup_env; build_app ;;
    run)              check_prerequisites; build_app; run_app ;;
    run-all)          check_prerequisites; run_all ;;
    build)            check_prerequisites; build_app ;;
    redis)            check_prerequisites; setup_redis ;;
    cluster)          check_prerequisites; print_header; truvag3_create_cluster ;;
    infra)            check_prerequisites; print_header; truvag3_setup_infra ;;
    docker)           check_prerequisites; build_docker ;;
    deploy)           check_prerequisites; build_docker; load_to_kind; deploy_k8s ;;
    rebuild)          check_prerequisites; rebuild ;;
    rollout)          check_prerequisites; rollout ;;
    full-deploy)      check_prerequisites; full_deploy ;;
    forward)          truvag3_forward "github-tool-service" "$DEFAULT_PORT" 80 ;;
    capabilities)     capabilities ;;
    logs)             logs ;;
    cleanup)          cleanup ;;
    cleanup-all)      cleanup_all ;;
    help|--help|-h)   show_help ;;
    *) echo "Unknown command: $1"; echo ""; show_help; exit 1 ;;
esac
