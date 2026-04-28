#!/bin/bash
# Geocoding Tool Setup Script
# Provides commands for building, running, and deploying the geocoding tool

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
APP_NAME="geocoding-tool"
PORT=${PORT:-8335}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  Geocoding Tool - $1${NC}"
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

# Create Kind cluster with port mappings
cmd_cluster() { truvag3_create_cluster; }

# Setup infrastructure (Grafana, Prometheus, Jaeger, etc.)
cmd_infra() { truvag3_setup_infra; }

# Setup API keys secret - SKIPPED for geocoding-tool
# Geocoding-tool uses the free Nominatim API and does NOT need AI API keys
# We intentionally do NOT create/modify any shared secrets to avoid conflicts
setup_api_keys() {
    print_info "Geocoding-tool uses free Nominatim API - no AI keys needed"
    print_info "Skipping secret creation (this tool doesn't use AI)"

    load_env

    # Create namespace if it doesn't exist
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

    print_success "Namespace ready (no secrets created)"
}

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + tool-specific vars)
setup_config() {
    truvag3_create_configmap "geocoding-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Build the tool
cmd_build() {
    print_header "Building Geocoding Tool"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o geocoding-tool .

    print_success "Build completed: geocoding-tool"
}

# Run the tool locally
cmd_run() {
    print_header "Running Geocoding Tool"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        print_info "Set it in .env file or export it: export REDIS_URL=redis://localhost:6379"
        exit 1
    fi

    # Build first
    cmd_build

    print_info "Starting geocoding-tool on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./geocoding-tool
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

# Deploy to Kubernetes
cmd_deploy() {
    print_header "Deploying to Kubernetes"

    # Load environment
    load_env

    # Build Docker image
    cmd_docker_build

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    setup_config

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    print_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME deployed successfully!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
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

# Run tests
cmd_test() {
    print_header "Running Tests"

    print_info "Testing geocode endpoint..."
    curl -s -X POST http://localhost:$PORT/api/capabilities/geocode_location \
        -H "Content-Type: application/json" \
        -d '{"location": "Tokyo, Japan"}' | jq .

    echo ""
    print_info "Testing reverse geocode endpoint..."
    curl -s -X POST http://localhost:$PORT/api/capabilities/reverse_geocode \
        -H "Content-Type: application/json" \
        -d '{"lat": 35.6762, "lon": 139.6503}' | jq .
}

# Show help
cmd_help() {
    echo "Geocoding Tool Setup Script - 1-Click Deployment"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "=== ONE-CLICK DEPLOYMENT ==="
    echo "  full-deploy   Complete deployment: cluster + infra + deploy + port-forwarding"
    echo ""
    echo "=== CLUSTER MANAGEMENT ==="
    echo "  cluster       Create Kind cluster with port mappings"
    echo "  infra         Setup infrastructure (Grafana, Prometheus, Jaeger)"
    echo "  clean         Remove tool deployment only (keep cluster)"
    echo "  clean-all     Delete Kind cluster and all resources"
    echo ""
    echo "=== APPLICATION COMMANDS ==="
    echo "  build         Build the tool binary"
    echo "  run           Build and run the tool locally"
    echo "  docker-build  Build Docker image"
    echo "  deploy        Deploy to Kubernetes cluster"
    echo "  rebuild       Rebuild with --no-cache and redeploy (fresh dependencies)"
    echo "  test          Run test requests against the service"
    echo ""
    echo "=== MONITORING & DEBUG ==="
    echo "  forward       Port forward tool service only (8335:80)"
    echo "  forward-all   Port forward all services (tool + Grafana + Prometheus + Jaeger)"
    echo "  logs          View application logs"
    echo "  status        Show deployment status"
    echo "  rollout       Restart deployment to pick up new secrets/config"
    echo "                Use --build flag to rebuild Docker image first"
    echo "  help          Show this help message"
    echo ""
    echo "Configuration:"
    echo "  CLUSTER_NAME: ${CLUSTER_NAME}"
    echo "  NAMESPACE: ${NAMESPACE}"
    echo "  APP_NAME: ${APP_NAME}"
    echo "  PORT: ${PORT}"
    echo "  REDIS_URL: ${REDIS_URL}"
    echo ""
    echo "Environment Variables (.env file):"
    echo "  REDIS_URL           Redis connection URL (required for run)"
    echo "  PORT                HTTP server port (default: 8335)"
    echo ""
    echo "  NOTE: This tool uses the free Nominatim geocoding API."
    echo "        No AI API keys are required for basic functionality."
    echo ""
    echo "  OPENAI_API_KEY      OpenAI API key (optional, for AI-enhanced features)"
    echo "  ANTHROPIC_API_KEY   Anthropic API key (optional, for AI-enhanced features)"
    echo "  GROQ_API_KEY        Groq API key (optional, for AI-enhanced features)"
    echo ""
    echo "Examples:"
    echo "  # Complete 1-click deployment"
    echo "  ./setup.sh full-deploy"
    echo ""
    echo "  # Step-by-step deployment"
    echo "  ./setup.sh cluster        # Create cluster"
    echo "  ./setup.sh infra          # Setup monitoring"
    echo "  ./setup.sh deploy         # Deploy application"
    echo "  ./setup.sh forward-all    # Setup port forwarding"
    echo ""
    echo "  # Local development"
    echo "  ./setup.sh build"
    echo "  REDIS_URL=redis://localhost:6379 ./setup.sh run"
    echo ""
    echo "  # Testing and monitoring"
    echo "  ./setup.sh test           # Test endpoints"
    echo "  ./setup.sh logs           # View logs"
    echo "  ./setup.sh status         # Check status"
    echo ""
    echo "  # Cleanup"
    echo "  ./setup.sh clean          # Remove app only"
    echo "  ./setup.sh clean-all      # Remove everything"
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
    test)
        cmd_test
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
