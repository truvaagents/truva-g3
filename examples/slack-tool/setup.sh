#!/bin/bash
# Slack Tool Setup Script
# Provides commands for building, running, and deploying the slack tool

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
APP_NAME="slack-tool"
PORT=${PORT:-8373}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  Slack Tool - $1${NC}"
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

# Create Kind cluster with port mappings
cmd_cluster() { truvag3_create_cluster; }

# Setup infrastructure (Redis + monitoring)
cmd_infra() { truvag3_setup_infra; }

# Build the tool
cmd_build() {
    print_header "Building Slack Tool"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o slack-tool .

    print_success "Build completed: slack-tool"
}

# Run the tool locally
cmd_run() {
    print_header "Running Slack Tool"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        print_info "Set it in .env file or export it: export REDIS_URL=redis://localhost:6379"
        exit 1
    fi

    # Build first
    cmd_build

    print_info "Starting slack-tool on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./slack-tool
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

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + tool-specific vars)
setup_config() {
    truvag3_create_configmap "slack-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
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

    # Setup API keys as secrets
    truvag3_create_tool_secret "slack-tool-secrets" "$NAMESPACE" "SLACK_BOT_TOKEN" "SLACK_USER_TOKEN"
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

# One-click full deployment
cmd_full_deploy() {
    print_header "Full Deployment (One-Click)"

    print_info "This will:"
    echo "  1. Create Kind cluster"
    echo "  2. Deploy infrastructure (Redis + monitoring)"
    echo "  3. Deploy slack tool"
    echo "  4. Set up port forwarding"
    echo ""

    # Step 1: Create cluster
    cmd_cluster

    # Step 2: Setup infrastructure
    cmd_infra

    # Step 3: Deploy application
    cmd_deploy

    # Step 4: Setup port forwarding
    print_header "Setting Up Port Forwarding"
    print_info "Starting port forwards in background..."
    echo "Deploy complete. Tool is accessible within the cluster via ClusterIP."

    print_success "Full deployment complete!"
    echo ""
    print_info "Access points:"
    echo "  Slack Tool:  http://localhost:8373"
    echo "  Grafana:     http://localhost:3000 (admin/admin)"
    echo "  Prometheus:  http://localhost:9090"
    echo "  Jaeger:      http://localhost:16686"
    echo ""
    print_info "To stop port forwarding: pkill -f 'kubectl port-forward'"
    print_info "To view logs: ./setup.sh logs"
    print_info "To cleanup: ./setup.sh clean-all"
}

# Run tests
cmd_test() {
    print_header "Running Tests"

    # Start port forward in background
    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/slack-tool-service 8373:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo "Testing health endpoint..."
    if curl -s http://localhost:8373/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    # Test capabilities
    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:8373/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    # Test list_channels (read-only, safe for automated testing)
    echo ""
    print_info "Testing list_channels..."
    curl -s -X POST http://localhost:8373/api/capabilities/list_channels \
        -H "Content-Type: application/json" \
        -d '{"limit": 5}' | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

# Port forward for local access
cmd_forward() {
    truvag3_forward "slack-tool-service" 8373 80
}

# Port forward all services (tool + monitoring)
cmd_forward_all() {
    truvag3_forward_all \
        "slack-tool-service:8373:80" \
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

    echo "Pods:"
    kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "Service:"
    kubectl get svc -n $NAMESPACE -l app=$APP_NAME
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

    # Update secrets from .env
    truvag3_create_tool_secret "slack-tool-secrets" "$NAMESPACE" "SLACK_BOT_TOKEN" "SLACK_USER_TOKEN"

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

# Clean up deployment only
cmd_clean() {
    print_header "Cleaning Up Deployment"

    print_info "Removing deployment..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found
    print_success "Cleanup complete"
}

# Rebuild with no-cache and redeploy
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

    # Setup secrets from .env
    truvag3_create_tool_secret "slack-tool-secrets" "$NAMESPACE" "SLACK_BOT_TOKEN" "SLACK_USER_TOKEN"
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

# Clean up everything (delete cluster)
cmd_clean_all() {
    print_header "Cleaning Up Everything"

    print_info "This will delete the entire Kind cluster: $CLUSTER_NAME"
    read -p "Are you sure? (y/N) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        # Stop port forwards
        print_info "Stopping port forwards..."
        pkill -f "kubectl port-forward.*$NAMESPACE" 2>/dev/null || true

        # Delete kind cluster
        if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
            print_info "Deleting Kind cluster: $CLUSTER_NAME"
            truvag3_delete_cluster
            print_success "Cluster deleted"
        else
            print_info "Cluster '$CLUSTER_NAME' not found"
        fi

        print_success "Cleanup complete"
    else
        print_info "Cleanup cancelled"
    fi
}

# Show help
cmd_help() {
    echo "Slack Tool Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Quick Start Commands:"
    echo "  full-deploy   ONE-CLICK: Create cluster + infra + deploy + port forwards"
    echo "  cluster       Create Kind cluster with port mappings"
    echo "  infra         Deploy infrastructure (Redis + monitoring stack)"
    echo ""
    echo "Build & Deploy Commands:"
    echo "  build         Build the tool binary"
    echo "  run           Build and run the tool locally"
    echo "  docker-build  Build Docker image"
    echo "  deploy        Build, load, and deploy to Kubernetes"
    echo "  rebuild       Rebuild with --no-cache and redeploy (fresh dependencies)"
    echo ""
    echo "Testing & Access Commands:"
    echo "  test          Run test requests against deployed tool"
    echo "  forward       Port forward the slack tool service only"
    echo "  forward-all   Port forward tool + Grafana + Prometheus + Jaeger"
    echo "  logs          View tool logs"
    echo "  status        Check deployment status"
    echo "  rollout       Restart deployment to pick up new secrets/config"
    echo "                Use --build flag to rebuild Docker image first"
    echo ""
    echo "Cleanup Commands:"
    echo "  clean         Remove slack tool deployment only"
    echo "  clean-all     Delete entire Kind cluster and all resources"
    echo ""
    echo "Help:"
    echo "  help          Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  REDIS_URL         Redis connection URL (required for run)"
    echo "  PORT              HTTP server port (default: 8373)"
    echo "  SLACK_BOT_TOKEN   Slack Bot Token (required)"
    echo "                    Create a Slack App: https://api.slack.com/apps"
    echo "  SLACK_USER_TOKEN  Slack User Token (optional, required for search_messages)"
    echo "                    search.messages API requires xoxp- user token"
    echo ""
    echo "Configuration:"
    echo "  CLUSTER_NAME: $CLUSTER_NAME"
    echo "  NAMESPACE:    $NAMESPACE"
    echo "  APP_NAME:     $APP_NAME"
    echo ""
    echo "Examples:"
    echo "  ./setup.sh full-deploy              # Complete one-click setup"
    echo "  ./setup.sh cluster                  # Create cluster only"
    echo "  ./setup.sh infra                    # Deploy infrastructure only"
    echo "  ./setup.sh deploy                   # Deploy tool only"
    echo "  ./setup.sh forward-all              # Port forward all services"
    echo "  ./setup.sh test                     # Run tests"
    echo "  REDIS_URL=redis://localhost:6379 ./setup.sh run  # Run locally"
    echo ""
    echo "Full Deployment Workflow:"
    echo "  ./setup.sh full-deploy              # Does everything"
    echo "  OR step-by-step:"
    echo "  ./setup.sh cluster                  # 1. Create cluster"
    echo "  ./setup.sh infra                    # 2. Setup infrastructure"
    echo "  ./setup.sh deploy                   # 3. Deploy tool"
    echo "  ./setup.sh forward-all              # 4. Access services"
}

# Main entry point
case "${1:-help}" in
    cluster)
        cmd_cluster
        ;;
    infra)
        cmd_infra
        ;;
    full-deploy)
        cmd_full_deploy
        ;;
    build)
        cmd_build
        ;;
    run)
        cmd_run
        ;;
    docker-build)
        cmd_docker_build
        ;;
    deploy)
        cmd_deploy
        ;;
    rebuild)
        cmd_rebuild
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
