#!/bin/bash
# DevOps Tool Setup Script
# Provides commands for building, running, and deploying the Kubernetes DevOps tool

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="devops-tool"
PORT=${PORT:-8347}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  DevOps Tool - $1${NC}"
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

# Load .env file if it exists
load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

check_command() {
    if ! command -v $1 &> /dev/null; then
        print_error "$1 is not installed"
        echo "Please install $1 and try again"
        exit 1
    fi
}

# Build the tool
cmd_build() {
    print_header "Building DevOps Tool"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o devops-tool .

    print_success "Build completed: devops-tool"
}

# Run the tool locally
cmd_run() {
    print_header "Running DevOps Tool"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        print_info "Set it in .env file or export it: export REDIS_URL=redis://localhost:6379"
        exit 1
    fi

    # Check kubectl is available
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl is not installed"
        print_info "Install kubectl: https://kubernetes.io/docs/tasks/tools/"
        exit 1
    fi

    # Build first
    cmd_build

    print_info "Starting devops-tool on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    print_info "kubectl context: $(kubectl config current-context 2>/dev/null || echo 'not configured')"
    echo ""

    ./devops-tool
}

# Build Docker image
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

# Create Kind cluster with port mappings for monitoring
cmd_cluster() { truvag3_create_cluster; }

# Setup monitoring infrastructure
cmd_infra() { truvag3_setup_infra; }

# Setup config as Kubernetes ConfigMap
setup_config() {
    truvag3_create_configmap "devops-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Deploy to Kubernetes
cmd_deploy() {
    print_header "Deploying to Kubernetes"

    load_env

    # Build Docker image first
    cmd_docker_build

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup config
    setup_config

    print_info "Waiting for any existing deployment..."
    kubectl wait --for=condition=available --timeout=30s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null || true

    # Apply Kubernetes manifests (includes RBAC)
    print_info "Applying Kubernetes manifests (includes RBAC)..."
    kubectl apply -f k8-deployment.yaml

    print_info "Waiting for deployment to be ready..."
    if kubectl wait --for=condition=available --timeout=120s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null; then
        print_success "$APP_NAME deployed successfully!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi

    print_info "Check status: kubectl get pods -n $NAMESPACE -l app=$APP_NAME"
}

# Full deployment: cluster + infrastructure + tool
cmd_full_deploy() {
    print_header "Full Deployment"

    load_env

    cmd_cluster
    cmd_infra
    cmd_deploy
    echo "Deploy complete. Tool is accessible within the cluster via ClusterIP."
}

# Run tests
cmd_test() {
    print_header "Running Tests"

    # Start port forward in background
    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/devops-tool-service 8347:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo "Testing health endpoint..."
    if curl -s http://localhost:8347/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    # Test capabilities
    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:8347/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    # Test get_cluster_status
    echo ""
    print_info "Testing get_cluster_status..."
    curl -s -X POST http://localhost:8347/api/capabilities/get_cluster_status \
        -H "Content-Type: application/json" \
        -d '{}' | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Test get_pods
    echo ""
    print_info "Testing get_pods (truvag3-examples namespace)..."
    curl -s -X POST http://localhost:8347/api/capabilities/get_pods \
        -H "Content-Type: application/json" \
        -d '{"namespace": "truvag3-examples"}' | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Test kubectl_command
    echo ""
    print_info "Testing kubectl_command (get nodes)..."
    curl -s -X POST http://localhost:8347/api/capabilities/kubectl_command \
        -H "Content-Type: application/json" \
        -d '{"args": "get nodes -o wide"}' | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

# Port forward for tool only
cmd_forward() {
    truvag3_forward "devops-tool-service" 8347 80
}

# Port forward for tool and monitoring
cmd_forward_all() {
    truvag3_forward_all \
        "devops-tool-service:8347:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

# View logs
cmd_logs() {
    print_header "Viewing Logs"

    kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100
}

# Check status
cmd_status() {
    print_header "Deployment Status"

    echo "DevOps Tool Pod:"
    kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "DevOps Tool Service:"
    kubectl get svc -n $NAMESPACE devops-tool-service
    echo ""
    echo "RBAC Resources:"
    kubectl get serviceaccount devops-tool-sa -n $NAMESPACE 2>/dev/null && echo "" || echo "  ServiceAccount: not found"
    kubectl get clusterrole devops-tool-role 2>/dev/null || echo "  ClusterRole: not found"
    kubectl get clusterrolebinding devops-tool-binding 2>/dev/null || echo "  ClusterRoleBinding: not found"
    echo ""
    echo "Monitoring Pods:"
    kubectl get pods -n $NAMESPACE -l "app in (prometheus,grafana,otel-collector,jaeger)"
}

# Rollout - restart deployment to pick up new secrets/config
cmd_rollout() {
    print_header "Rolling Out Deployment"

    local rebuild=false
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then
        rebuild=true
    fi

    load_env
    print_info "Updating config from .env..."
    setup_config

    if [ "$rebuild" = true ]; then
        print_info "Rebuilding Docker image..."
        cmd_docker_build

        if command -v kind &> /dev/null; then
            print_info "Loading image into kind cluster..."
            kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
            print_success "Image loaded"
        fi
    fi

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

# Clean up tool only
cmd_clean() {
    print_header "Cleaning Up DevOps Tool"

    print_info "Removing tool deployment and RBAC..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found
    print_success "DevOps tool cleanup complete"
}

# Clean up everything including cluster
cmd_clean_all() {
    print_header "Cleaning Up Everything"

    pkill -f "kubectl.*port-forward.*$NAMESPACE" 2>/dev/null || true
    kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true

    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        print_info "Deleting Kind cluster $CLUSTER_NAME..."
        truvag3_delete_cluster
    fi

    print_success "Full cleanup complete"
}

# Rebuild with no-cache and redeploy
cmd_rebuild() {
    print_header "Rebuilding with Fresh Dependencies"

    load_env

    print_info "Building Docker image with --no-cache..."
    DOCKER_NO_CACHE=true cmd_docker_build

    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup config from .env
    if type setup_config &>/dev/null; then
        setup_config
    fi

    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    print_info "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME rebuilt and deployed with fresh dependencies!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

# Show help
cmd_help() {
    echo "DevOps Tool Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Local Development Commands:"
    echo "  build         Build the devops-tool binary"
    echo "  run           Build and run the tool locally"
    echo ""
    echo "Kubernetes Cluster Commands:"
    echo "  cluster       Create Kind cluster with port mappings"
    echo "  infra         Setup monitoring infrastructure (Prometheus, Grafana, Jaeger)"
    echo "  full-deploy   Complete deployment: cluster + infra + tool + port forwards"
    echo ""
    echo "Kubernetes Deployment Commands:"
    echo "  docker-build  Build Docker image (includes kubectl)"
    echo "  deploy        Build, load, and deploy to Kubernetes (includes RBAC)"
    echo "  rebuild       Rebuild with --no-cache and redeploy (fresh dependencies)"
    echo "  test          Run test requests against deployed tool"
    echo "  forward       Port forward the tool service only"
    echo "  forward-all   Port forward tool + monitoring dashboards"
    echo "  logs          View tool logs"
    echo "  status        Check deployment status (includes RBAC)"
    echo "  rollout       Restart deployment to pick up new config"
    echo "                Use --build flag to rebuild Docker image first"
    echo "  clean         Remove tool deployment and RBAC only"
    echo "  clean-all     Delete Kind cluster and all resources"
    echo "  help          Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  REDIS_URL     Redis connection URL (required for run)"
    echo "  PORT          HTTP server port (default: 8347)"
    echo ""
    echo "RBAC Notes:"
    echo "  This tool deploys with a ServiceAccount, ClusterRole, and ClusterRoleBinding."
    echo "  The ClusterRole provides:"
    echo "    - Read-only access to pods, services, deployments, events, nodes, etc."
    echo "    - Write access for deployment scaling and rollout restart only"
    echo ""
    echo "Examples:"
    echo "  ./setup.sh full-deploy    # One-click full deployment"
    echo "  ./setup.sh deploy         # Deploy to existing cluster"
    echo "  ./setup.sh test           # Run kubectl capability tests"
    echo "  ./setup.sh forward-all    # Access all dashboards"
    echo "  REDIS_URL=redis://localhost:6379 ./setup.sh run"
}

# Main entry point
case "${1:-help}" in
    build)
        cmd_build
        ;;
    run)
        cmd_run
        ;;
    docker-build)
        cmd_docker_build
        ;;
    cluster)
        cmd_cluster
        ;;
    infra)
        cmd_infra
        ;;
    deploy)
        cmd_deploy
        ;;
    rebuild)
        cmd_rebuild
        ;;
    full-deploy)
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
    status)
        cmd_status
        ;;
    rollout)
        cmd_rollout "$@"
        ;;
    clean)
        cmd_clean
        ;;
    clean-all)
        cmd_clean_all
        ;;
    help|--help|-h)
        cmd_help
        ;;
    *)
        print_error "Unknown command: $1"
        cmd_help
        exit 1
        ;;
esac
