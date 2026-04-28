#!/bin/bash
# setup.sh - One-click setup for scheduled-executor
# This script sets up the local development environment and can deploy to Kubernetes.
# It follows the same lifecycle pattern used by the other example agents.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"
cd "$SCRIPT_DIR"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="scheduled-executor"
AGENT_PORT=8380
PORT=${PORT:-8380}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  Scheduled Executor - $1${NC}"
    echo -e "${BLUE}================================================${NC}"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_info() {
    echo -e "${YELLOW}[INFO]${NC} $1"
}

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

check_prerequisites() { truvag3_check_prerequisites; }

cmd_cluster() { truvag3_create_cluster; }

cmd_infra() { truvag3_setup_infra; }

setup_redis() {
    print_info "Setting up Redis..."

    if command -v redis-cli &> /dev/null; then
        if redis-cli ping &> /dev/null; then
            print_success "Redis is already running"
            return 0
        fi
    fi

    if [ "${DOCKER_AVAILABLE:-false}" = true ]; then
        print_info "Starting Redis via Docker..."
        docker stop truvag3-redis 2>/dev/null || true
        docker rm truvag3-redis 2>/dev/null || true
        docker run -d \
            --name truvag3-redis \
            -p 6379:6379 \
            redis:7-alpine
        print_success "Redis started on port 6379"
    else
        print_error "Redis not available"
        echo "Please install Redis or Docker to run Redis"
        echo ""
        echo "Options:"
        echo "  1. Install Redis: brew install redis && brew services start redis"
        echo "  2. Use Docker: docker run -d -p 6379:6379 redis:7-alpine"
        exit 1
    fi
    echo ""
}

setup_env() {
    print_info "Setting up environment..."

    if [ ! -f .env ]; then
        cp .env.example .env
        print_success "Created .env from .env.example"
    else
        print_success ".env file already exists"
    fi
    echo ""
}

cmd_build() {
    print_header "Building Scheduled Executor"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o $APP_NAME .

    print_success "Build completed: $APP_NAME"
}

load_to_kind() { truvag3_load_to_kind "$APP_NAME:latest"; }

setup_agent_config() {
    truvag3_create_configmap "${APP_NAME}-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

cmd_run() {
    print_header "Running Scheduled Executor"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        exit 1
    fi

    cmd_build

    print_info "Starting $APP_NAME on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./$APP_NAME
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

cmd_deploy() {
    print_header "Deploying to Kubernetes"

    load_env

    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
    setup_agent_config
    cmd_docker_build
    load_to_kind

    print_info "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    print_info "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME deployed successfully!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi

    print_info "Check status: kubectl get pods -n $NAMESPACE -l app=$APP_NAME"
}

cmd_full_deploy() {
    print_header "Full Deployment (One-Click)"

    print_info "This will:"
    echo "  1. Create Kind cluster"
    echo "  2. Deploy infrastructure (Redis + monitoring)"
    echo "  3. Deploy scheduled executor"
    echo ""

    cmd_cluster
    cmd_infra
    cmd_deploy

    print_success "Full deployment complete!"
    echo ""
    print_info "Internal service address in cluster:"
    echo "  http://scheduled-executor-service.truvag3-examples"
    echo ""
    print_info "To access from your laptop, run:"
    echo "  ./setup.sh forward"
    echo "  ./setup.sh forward-all"
    echo ""
    print_info "To view logs: ./setup.sh logs"
    print_info "To run smoke tests: ./setup.sh test"
    print_info "To cleanup: ./setup.sh clean-all"
}

rebuild() {
    print_header "Rebuilding with Fresh Dependencies"

    DOCKER_NO_CACHE=true cmd_docker_build
    load_to_kind

    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    load_env
    setup_agent_config

    print_info "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    print_info "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME rebuilt and deployed!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

rollout() {
    print_header "Rolling out deployment"

    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    load_env
    setup_agent_config

    print_info "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    print_info "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_info "Waiting for rollout to complete..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "Rollout complete!"
    else
        print_error "Rollout failed"
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

run_app() {
    print_info "Starting Scheduled Executor..."
    echo ""
    echo "The agent will be available at: http://localhost:$AGENT_PORT"
    echo ""
    echo "Endpoints:"
    echo "  GET /health"
    echo "  GET /api/capabilities"
    echo ""
    echo "Press Ctrl+C to stop"
    echo "=============================================="
    echo ""

    if [ -f .env ]; then
        export $(cat .env | grep -v '^#' | xargs)
    fi

    export REDIS_URL=${REDIS_URL:-"redis://localhost:6379"}
    export PORT=${PORT:-$AGENT_PORT}

    ./$APP_NAME
}

run_all() {
    print_info "Starting all components for local development..."
    echo ""

    if ! redis-cli ping 2>/dev/null | grep -q PONG; then
        setup_redis
    else
        print_success "Redis already running"
    fi

    setup_env
    cmd_build
    run_app
}

cmd_test() {
    print_header "Running Smoke Tests"

    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/scheduled-executor-service $PORT:80 >/dev/null 2>&1 &
    PF_PID=$!
    trap "kill $PF_PID 2>/dev/null || true" EXIT
    sleep 3

    echo "Testing health endpoint..."
    if curl -fsS http://localhost:$PORT/health >/dev/null; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
        exit 1
    fi

    echo "Testing capabilities endpoint..."
    if curl -fsS http://localhost:$PORT/api/capabilities >/dev/null; then
        print_success "Capabilities endpoint reachable"
    else
        print_error "Capabilities endpoint failed"
        exit 1
    fi

    kill $PF_PID 2>/dev/null || true
    trap - EXIT
}

cmd_forward() {
    truvag3_forward "scheduled-executor-service" $PORT 80
}

cmd_forward_all() {
    truvag3_forward_all \
        "scheduled-executor-service:$PORT:80" \
        "grafana:3000:3000" \
        "prometheus:9090:9090" \
        "jaeger:16686:16686"
}

cmd_logs() {
    kubectl logs -n $NAMESPACE -l app=$APP_NAME -f
}

cmd_cleanup() {
    print_header "Cleaning up Scheduled Executor"
    kubectl delete -f k8-deployment.yaml --ignore-not-found=true
    print_success "Scheduled executor resources deleted"
}

cmd_clean_all() {
    cmd_cleanup
    truvag3_delete_cluster
}

usage() {
    cat <<EOF
Usage: $0 <command>

Commands:
  setup         Setup the local development environment
  run           Build and run the agent locally
  run-all       Setup Redis, build, and run locally
  build         Build the local binary
  redis         Setup Redis only
  cluster       Create a Kind cluster with NGINX Ingress Controller
  infra         Setup monitoring infrastructure
  docker        Build Docker image
  deploy        Build, load to Kind, and deploy to Kubernetes
  rebuild       Rebuild with --no-cache and redeploy
  rollout       Restart deployment to pick up new config from .env
  full-deploy   Create cluster, infra, and deploy app
  test          Run smoke tests
  forward       Port-forward the executor service
  forward-all   Port-forward executor + monitoring stack
  logs          Follow Kubernetes logs
  cleanup       Delete app resources
  cleanup-all   Delete app plus shared infra/cluster helpers
  help          Show this help
EOF
}

case "${1:-help}" in
    setup)
        check_prerequisites
        setup_env
        cmd_build
        print_success "Setup complete! Run '$0 run' to start the agent"
        ;;
    run)
        check_prerequisites
        cmd_build
        run_app
        ;;
    run-all)
        check_prerequisites
        run_all
        ;;
    build)
        check_prerequisites
        cmd_build
        ;;
    redis)
        check_prerequisites
        setup_redis
        ;;
    cluster)
        check_prerequisites
        print_header "Create Cluster"
        cmd_cluster
        ;;
    infra)
        check_prerequisites
        print_header "Setup Infrastructure"
        load_env
        cmd_infra
        ;;
    docker)
        check_prerequisites
        cmd_docker_build
        ;;
    deploy)
        check_prerequisites
        cmd_deploy
        ;;
    rebuild)
        check_prerequisites
        rebuild
        ;;
    rollout)
        check_prerequisites
        rollout
        ;;
    full-deploy)
        check_prerequisites
        cmd_full_deploy
        ;;
    test)
        cmd_test
        ;;
    forward)
        cmd_forward
        ;;
    forward-all)
        cmd_forward_all
        ;;
    logs)
        cmd_logs
        ;;
    cleanup)
        cmd_cleanup
        ;;
    cleanup-all)
        cmd_clean_all
        ;;
    help|--help|-h) usage ;;
    *)
        print_error "Unknown command: $1"
        echo ""
        usage
        exit 1
        ;;
esac
