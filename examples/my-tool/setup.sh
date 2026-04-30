#!/bin/bash
# My Tool Setup Script
# Provides commands for building, running, and deploying the tool

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
APP_NAME="my-tool"
PORT=${PORT:-8390}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  My Tool - $1${NC}"
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
    print_header "Building My Tool"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o my-tool .

    print_success "Build completed: my-tool"
}

# Run the tool locally
cmd_run() {
    print_header "Running My Tool"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        print_info "Set it in .env file or export it: export REDIS_URL=redis://localhost:6379"
        exit 1
    fi

    # Build first
    cmd_build

    print_info "Starting my-tool on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./my-tool
}

# Build Docker image
# Usage: cmd_docker_build [--no-cache]
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

# Setup API keys as Kubernetes secrets
setup_api_keys() {
    truvag3_create_secret "ai-provider-keys" "$NAMESPACE"
}

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + tool-specific vars)
setup_config() {
    truvag3_create_configmap "my-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
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

    # Setup API keys and config
    setup_api_keys
    setup_config

    print_info "Waiting for any existing deployment..."
    kubectl wait --for=condition=available --timeout=30s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null || true

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
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

    # Step 1: Create Kind cluster
    cmd_cluster

    # Step 2: Setup monitoring infrastructure
    cmd_infra

    # Step 3: Deploy tool
    cmd_deploy

    # Step 4: Setup port forwards
    echo "Deploy complete. Tool is accessible within the cluster via ClusterIP."
}

# Run tests
cmd_test() {
    print_header "Running Tests"

    # Start port forward in background
    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/my-tool-service 8390:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo "Testing health endpoint..."
    if curl -s http://localhost:8390/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    # Test capabilities
    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:8390/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    # ----------------------------------------------------------------------
    # TODO(developer): Add capability-specific tests below.
    # Pattern (replace <capability> and the JSON body with your tool's shape):
    #
    # echo ""
    # print_info "Testing <capability>..."
    # curl -s -X POST http://localhost:8390/api/capabilities/<capability> \
    #     -H "Content-Type: application/json" \
    #     -d '{"field": "value"}' | jq . 2>/dev/null || echo "(install jq for pretty output)"
    # ----------------------------------------------------------------------

    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

# Port forward for tool only
cmd_forward() {
    truvag3_forward "my-tool-service" 8390 80
}

# Port forward for tool and monitoring
cmd_forward_all() {
    truvag3_forward_all \
        "my-tool-service:8390:80" \
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

    echo "My Tool Pod:"
    kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "My Tool Service:"
    kubectl get svc -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "Monitoring Pods:"
    kubectl get pods -n $NAMESPACE -l "app in (prometheus,grafana,otel-collector,jaeger)"
}

# Rollout - restart deployment to pick up new secrets/config
cmd_rollout() {
    print_header "Rolling Out Deployment"

    local rebuild=false

    # Check for --build flag
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then
        rebuild=true
    fi

    # Load env to update secrets
    load_env

    # Update secrets and config from .env
    print_info "Updating secrets and config from .env..."
    setup_api_keys
    setup_config

    # Rebuild if requested
    if [ "$rebuild" = true ]; then
        print_info "Rebuilding Docker image..."
        cmd_docker_build

        if command -v kind &> /dev/null; then
            print_info "Loading image into kind cluster..."
            kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
            print_success "Image loaded"
        fi
    fi

    # Restart deployment
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
    print_header "Cleaning Up My Tool"

    print_info "Removing tool deployment..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found
    print_success "my-tool cleanup complete"
}

# Clean up everything including cluster
cmd_clean_all() {
    print_header "Cleaning Up Everything"

    # Kill port forwards
    pkill -f "kubectl.*port-forward.*$NAMESPACE" 2>/dev/null || true

    # Delete tool
    kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true

    # Delete Kind cluster
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        print_info "Deleting Kind cluster $CLUSTER_NAME..."
        truvag3_delete_cluster
    fi

    print_success "Full cleanup complete"
}

# Rebuild with no-cache and redeploy
# This ensures fresh dependencies are downloaded from GitHub
cmd_rebuild() {
    print_header "Rebuilding with Fresh Dependencies"

    load_env

    # Build Docker image with --no-cache
    print_info "Building Docker image with --no-cache..."
    DOCKER_NO_CACHE=true cmd_docker_build

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config from .env
    setup_api_keys
    setup_config

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    # Restart deployment to pick up new image
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
    echo "My Tool Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Local Development Commands:"
    echo "  build         Build the my-tool binary"
    echo "  run           Build and run the tool locally"
    echo ""
    echo "Kubernetes Cluster Commands:"
    echo "  cluster       Create Kind cluster with port mappings"
    echo "  infra         Setup monitoring infrastructure (Prometheus, Grafana, Jaeger)"
    echo "  full-deploy   Complete deployment: cluster + infra + tool + port forwards"
    echo ""
    echo "Kubernetes Deployment Commands:"
    echo "  docker-build  Build Docker image"
    echo "  deploy        Build, load, and deploy to Kubernetes"
    echo "  rebuild       Rebuild with --no-cache and redeploy (fresh dependencies)"
    echo "  test          Run test requests against deployed tool"
    echo "  forward       Port forward the tool service only"
    echo "  forward-all   Port forward tool + monitoring dashboards"
    echo "  logs          View tool logs"
    echo "  status        Check deployment status"
    echo "  rollout       Restart deployment to pick up new secrets/config"
    echo "                Use --build flag to rebuild Docker image first"
    echo "  clean         Remove tool deployment only"
    echo "  clean-all     Delete Kind cluster and all resources"
    echo "  help          Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  REDIS_URL         Redis connection URL (required for run)"
    echo "  PORT              HTTP server port (default: 8390)"
    echo "  OPENAI_API_KEY    OpenAI API key (optional)"
    echo "  ANTHROPIC_API_KEY Anthropic API key (optional)"
    echo "  GROQ_API_KEY      Groq API key (optional)"
    echo ""
    echo "Examples:"
    echo "  ./setup.sh full-deploy    # One-click full deployment"
    echo "  ./setup.sh deploy         # Deploy to existing cluster"
    echo "  ./setup.sh forward-all    # Access all dashboards"
    echo "  ./setup.sh test           # Run capability tests"
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
