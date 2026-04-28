#!/bin/bash

################################################################################
# Country Info Tool - Complete Setup Script
# Provides 1-click deployment with full observability stack
################################################################################

set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="country-info-tool"
PORT=${PORT:-8333}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

################################################################################
# Utility Functions
################################################################################

print_header() {
    echo -e "\n${BLUE}===================================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}===================================================${NC}\n"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

print_info() {
    echo -e "${YELLOW}ℹ $1${NC}"
}

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

check_command() {
    if ! command -v "$1" &> /dev/null; then
        print_error "$1 is not installed. Please install it first."
        return 1
    fi
    print_success "$1 is installed"
    return 0
}

check_redis() {
    print_info "Checking Redis connection..."
    if redis-cli -u "$REDIS_URL" ping &> /dev/null; then
        print_success "Redis is running and accessible"
        return 0
    else
        print_error "Redis is not accessible at $REDIS_URL"
        print_info "Please start Redis or update REDIS_URL in .env"
        return 1
    fi
}

################################################################################
# Build Commands
################################################################################

cmd_build() {
    print_header "Building $APP_NAME"

    print_info "Tidying Go modules..."
    GOWORK=off go mod tidy

    print_info "Building application..."
    GOWORK=off go build -o "$APP_NAME" .

    print_success "Build complete: ./$APP_NAME"
}

cmd_run() {
    print_header "Running $APP_NAME locally"

    load_env

    # Check Redis first
    if ! check_redis; then
        print_error "Cannot start application without Redis"
        print_info "Start Redis with: docker run -d -p 6379:6379 redis:alpine"
        exit 1
    fi

    # Build if needed
    if [ ! -f "$APP_NAME" ]; then
        cmd_build
    fi

    print_info "Starting $APP_NAME on port $PORT..."
    print_info "Press Ctrl+C to stop"
    ./"$APP_NAME"
}

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

################################################################################
# Cluster Management
################################################################################

cmd_cluster() { truvag3_create_cluster; }

cmd_infra() { truvag3_setup_infra; }

setup_api_keys() {
    print_header "Setting up configuration"

    load_env

    # Country Info Tool uses the free RestCountries API - no API keys needed
    print_info "Country Info Tool uses the free RestCountries API - no API keys required"

    # Skip secret creation for this tool
    print_success "Configuration complete (no API keys needed)"
}

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + tool-specific vars)
setup_config() {
    truvag3_create_configmap "country-info-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

################################################################################
# Deployment Commands
################################################################################

cmd_deploy() {
    print_header "Deploying $APP_NAME to Kubernetes"

    # Check prerequisites
    if ! check_command kubectl; then
        exit 1
    fi

    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster"
        print_info "Create cluster with: ./setup.sh cluster"
        exit 1
    fi

    # Build docker image
    cmd_docker_build

    # Load image to Kind
    print_info "Loading image to Kind cluster..."
    if command -v kind &> /dev/null && kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        kind load docker-image "$APP_NAME:latest" --name "$CLUSTER_NAME"
        print_success "Image loaded to Kind cluster"
    else
        print_info "Not a Kind cluster or cluster not found, skipping image load"
    fi

    # Create namespace if needed
    if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
        print_info "Creating namespace: $NAMESPACE"
        kubectl create namespace "$NAMESPACE"
    fi

    # Setup secrets and config
    setup_api_keys
    setup_config

    # Apply manifests
    if [ ! -f "k8-deployment.yaml" ]; then
        print_error "k8-deployment.yaml not found"
        exit 1
    fi

    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    # Wait for deployment
    print_info "Waiting for deployment to be ready..."
    kubectl wait --for=condition=available --timeout=120s deployment/"$APP_NAME" -n "$NAMESPACE" 2>/dev/null || true

    # Check status
    print_success "$APP_NAME deployed successfully"
    kubectl get pods -n "$NAMESPACE" -l app="$APP_NAME"
}

cmd_full_deploy() {
    print_header "ONE-CLICK DEPLOYMENT: Complete setup"

    print_info "This will:"
    print_info "  1. Create Kind cluster"
    print_info "  2. Setup infrastructure (Redis, Grafana, Prometheus, Jaeger)"
    print_info "  3. Deploy $APP_NAME"
    print_info "  4. Setup port forwarding"
    echo

    # Create cluster
    cmd_cluster

    # Wait a moment for cluster to stabilize
    sleep 5

    # Setup infrastructure
    cmd_infra

    # Deploy application
    cmd_deploy

    # Setup port forwarding in background
    print_info "Setting up port forwarding..."
    echo "Deploy complete. Tool is accessible within the cluster via ClusterIP." &

    sleep 3

    print_header "DEPLOYMENT COMPLETE"
    print_success "Cluster: $CLUSTER_NAME"
    print_success "Namespace: $NAMESPACE"
    print_success "Application: $APP_NAME"
    echo
    print_info "Access points:"
    print_info "  - Country Info Tool: http://localhost:8333"
    print_info "  - Grafana: http://localhost:3000 (admin/admin)"
    print_info "  - Prometheus: http://localhost:9090"
    print_info "  - Jaeger: http://localhost:16686"
    echo
    print_info "Test with: ./setup.sh test"
    print_info "View logs: ./setup.sh logs"
    print_info "Clean up: ./setup.sh clean-all"
}

################################################################################
# Testing Commands
################################################################################

cmd_test() {
    print_header "Testing $APP_NAME"

    if ! check_command curl; then
        print_error "curl is required for testing"
        exit 1
    fi

    local endpoint="http://localhost:${PORT}/api/capabilities/get_country_info"

    print_info "Testing endpoint: $endpoint"
    print_info "Request: Get country info for Japan"
    echo

    local response=$(curl -s -X POST "$endpoint" \
        -H "Content-Type: application/json" \
        -d '{"country":"Japan"}')

    if [ $? -eq 0 ]; then
        if command -v jq &> /dev/null; then
            echo "$response" | jq .
        else
            echo "$response"
        fi
        print_success "Test completed successfully"
    else
        print_error "Test failed - is the service running?"
        print_info "Start locally: ./setup.sh run"
        print_info "Or port forward: ./setup.sh forward"
        exit 1
    fi
}

################################################################################
# Port Forwarding Commands
################################################################################

cmd_forward() {
    truvag3_forward "country-info-tool-service" 8333 80
}

cmd_forward_all() {
    truvag3_forward_all \
        "country-info-tool-service:8333:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

################################################################################
# Monitoring Commands
################################################################################

cmd_logs() {
    print_header "Viewing logs for $APP_NAME"

    if ! kubectl get deployment "$APP_NAME" -n "$NAMESPACE" &> /dev/null; then
        print_error "Deployment $APP_NAME not found in namespace $NAMESPACE"
        exit 1
    fi

    print_info "Streaming logs... (Press Ctrl+C to stop)"
    kubectl logs -n "$NAMESPACE" -l app="$APP_NAME" --follow --tail=100
}

cmd_status() {
    print_header "Status of $APP_NAME"

    if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
        print_error "Namespace $NAMESPACE not found"
        exit 1
    fi

    echo -e "${YELLOW}Deployments:${NC}"
    kubectl get deployments -n "$NAMESPACE" -l app="$APP_NAME"
    echo

    echo -e "${YELLOW}Pods:${NC}"
    kubectl get pods -n "$NAMESPACE" -l app="$APP_NAME"
    echo

    echo -e "${YELLOW}Services:${NC}"
    kubectl get services -n "$NAMESPACE" -l app="$APP_NAME"
    echo

    # Show recent events
    echo -e "${YELLOW}Recent Events:${NC}"
    kubectl get events -n "$NAMESPACE" --sort-by='.lastTimestamp' | tail -10
}

################################################################################
# Rollout Command
################################################################################

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

        if command -v kind &> /dev/null && kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
            print_info "Loading image into kind cluster..."
            kind load docker-image "$APP_NAME:latest" --name "$CLUSTER_NAME"
            print_success "Image loaded"
        fi
    fi

    # Restart deployment
    print_info "Restarting deployment..."
    kubectl rollout restart deployment/"$APP_NAME" -n "$NAMESPACE"

    print_info "Waiting for rollout to complete..."
    if kubectl rollout status deployment/"$APP_NAME" -n "$NAMESPACE" --timeout=120s; then
        print_success "Rollout complete!"
    else
        print_error "Rollout failed"
        kubectl logs -n "$NAMESPACE" -l app="$APP_NAME" --tail=20
        exit 1
    fi
}

################################################################################
# Cleanup Commands
################################################################################

cmd_clean() {
    print_header "Cleaning up $APP_NAME"

    if kubectl get deployment "$APP_NAME" -n "$NAMESPACE" &> /dev/null; then
        print_info "Deleting Kubernetes resources..."
        kubectl delete -f k8-deployment.yaml 2>/dev/null || true
        print_success "Kubernetes resources deleted"
    else
        print_info "No Kubernetes resources found"
    fi

    if [ -f "$APP_NAME" ]; then
        print_info "Removing local binary..."
        rm "$APP_NAME"
        print_success "Local binary removed"
    fi

    print_success "Cleanup complete"
}

cmd_clean_all() {
    print_header "Complete cleanup - Removing cluster and all resources"

    print_info "This will delete:"
    print_info "  - Kind cluster: $CLUSTER_NAME"
    print_info "  - All deployed applications"
    print_info "  - All monitoring infrastructure"
    echo

    read -p "Are you sure? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Cleanup cancelled"
        exit 0
    fi

    # Stop port forwarding
    print_info "Stopping port forwarding..."
    pkill -f "kubectl port-forward" 2>/dev/null || true

    # Delete Kind cluster
    truvag3_delete_cluster

    # Clean local files
    if [ -f "$APP_NAME" ]; then
        rm "$APP_NAME"
        print_success "Local binary removed"
    fi

    print_success "Complete cleanup finished"
}

################################################################################
# Rebuild Command
################################################################################

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

################################################################################
# Help Command
################################################################################

cmd_help() {
    cat <<EOF

${BLUE}Country Info Tool - Setup Script${NC}

${YELLOW}USAGE:${NC}
    ./setup.sh [COMMAND]

${YELLOW}BUILD COMMANDS:${NC}
    ${GREEN}build${NC}               Build the application locally
    ${GREEN}run${NC}                 Build and run the application locally (requires Redis)
    ${GREEN}docker-build${NC}        Build Docker image

${YELLOW}CLUSTER COMMANDS:${NC}
    ${GREEN}cluster${NC}             Create Kind cluster with port mappings
    ${GREEN}infra${NC}               Setup infrastructure (Redis, Grafana, Prometheus, Jaeger)

${YELLOW}DEPLOYMENT COMMANDS:${NC}
    ${GREEN}deploy${NC}              Deploy application to Kubernetes
    ${GREEN}rebuild${NC}             Rebuild with --no-cache and redeploy (fresh dependencies)
    ${GREEN}full-deploy${NC}         ONE-CLICK: Create cluster + infra + deploy + port forward

${YELLOW}TESTING COMMANDS:${NC}
    ${GREEN}test${NC}                Test the application with sample request

${YELLOW}PORT FORWARDING:${NC}
    ${GREEN}forward${NC}             Port forward application only
    ${GREEN}forward-all${NC}         Port forward application + monitoring services

${YELLOW}MONITORING COMMANDS:${NC}
    ${GREEN}logs${NC}                View application logs
    ${GREEN}status${NC}              Show deployment status
    ${GREEN}rollout${NC}             Restart deployment to pick up new secrets/config
                        Use --build flag to rebuild Docker image first

${YELLOW}CLEANUP COMMANDS:${NC}
    ${GREEN}clean${NC}               Remove application deployment
    ${GREEN}clean-all${NC}           Delete Kind cluster and all resources

${YELLOW}HELP:${NC}
    ${GREEN}help${NC}                Show this help message

${YELLOW}CONFIGURATION:${NC}
    Cluster Name:    $CLUSTER_NAME
    Namespace:       $NAMESPACE
    Application:     $APP_NAME
    Port:            $PORT
    Redis URL:       $REDIS_URL

${YELLOW}EXAMPLES:${NC}
    # One-click deployment
    ./setup.sh full-deploy

    # Build and test locally
    ./setup.sh build
    ./setup.sh run

    # Manual deployment
    ./setup.sh cluster
    ./setup.sh infra
    ./setup.sh deploy
    ./setup.sh forward-all

    # Monitoring
    ./setup.sh status
    ./setup.sh logs

    # Cleanup
    ./setup.sh clean-all

${YELLOW}ACCESS POINTS (after deployment):${NC}
    - Country Info Tool: http://localhost:$PORT
    - Grafana:          http://localhost:3000 (admin/admin)
    - Prometheus:       http://localhost:9090
    - Jaeger:           http://localhost:16686

EOF
}

################################################################################
# Main Entry Point
################################################################################

main() {
    local command=${1:-help}

    case "$command" in
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
            print_error "Unknown command: $command"
            echo
            cmd_help
            exit 1
            ;;
    esac
}

# Run main function
main "$@"
