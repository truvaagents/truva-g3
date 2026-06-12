#!/bin/bash

# setup.sh - Setup and deployment script for registry-viewer-app
# This is a standalone app that visualizes the TruvaG3 Redis service registry
# No infrastructure setup required - just build, deploy, and run

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"
K8_DEPLOYMENT_DIR="$EXAMPLES_DIR/k8-deployment"

# Shared environment library: container-runtime detection (docker/podman) and a
# kind() wrapper that makes `kind load docker-image` work under podman.
source "$K8_DEPLOYMENT_DIR/setup-env-lib.sh"

# Configuration
NAMESPACE="truvag3-examples"
APP_NAME="registry-viewer"
APP_PORT=8361
IMAGE_NAME="registry-viewer:latest"
REDIS_NAMESPACE="truvag3"

# Extract Redis configuration from k8-deployment/redis.yaml
# Sets REDIS_SERVICE_NAME and REDIS_PORT variables
get_redis_config() {
    local redis_yaml="$K8_DEPLOYMENT_DIR/redis.yaml"

    if [ ! -f "$redis_yaml" ]; then
        REDIS_SERVICE_NAME="redis"
        REDIS_PORT="6379"
        return 1  # Return non-zero to indicate defaults were used
    fi

    # Extract service name from redis.yaml (look for Service kind, then get metadata.name)
    # The Service section has: kind: Service, then metadata: name: redis
    REDIS_SERVICE_NAME=$(awk '/^kind: Service/,/^---/{if(/^  name:/) print $2}' "$redis_yaml" | head -1)
    if [ -z "$REDIS_SERVICE_NAME" ]; then
        REDIS_SERVICE_NAME="redis"
    fi

    # Extract port from service spec (look for "- port: 6379" pattern)
    REDIS_PORT=$(awk '/^kind: Service/,/^---/{if(/- port:/) print $3}' "$redis_yaml" | head -1)
    if [ -z "$REDIS_PORT" ]; then
        REDIS_PORT="6379"
    fi

    return 0
}

# Build Redis URL from config (can be overridden by REDIS_URL env var)
get_redis_url() {
    if [ -n "$REDIS_URL" ]; then
        echo "$REDIS_URL"
    else
        get_redis_config
        echo "redis://${REDIS_SERVICE_NAME}:${REDIS_PORT}"
    fi
}

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

print_header() {
    echo -e "${BLUE}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║       TruvaG3 Registry Viewer                          ║${NC}"
    echo -e "${BLUE}║       Real-time Service Registry Dashboard            ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check Go
    if ! command -v go &> /dev/null; then
        log_error "Go is not installed"
        echo "Please install Go 1.26+ from https://golang.org/dl/"
        exit 1
    fi
    log_success "Go installed: $(go version)"

    # Container runtime (docker or podman) — resolved by the shared lib at source
    # time. Check the resolved runtime, not docker specifically, so a podman-only
    # host isn't wrongly flagged as having no runtime.
    local runtime="${TRUVAG3_CONTAINER_RUNTIME:-docker}"
    if command -v "$runtime" &> /dev/null; then
        log_success "$runtime installed"
        DOCKER_AVAILABLE=true
    else
        log_warn "$runtime not found (required for K8s deployment)"
        DOCKER_AVAILABLE=false
    fi

    # Check kubectl (optional)
    if command -v kubectl &> /dev/null; then
        log_success "kubectl installed"
        KUBECTL_AVAILABLE=true
    else
        log_warn "kubectl not found (required for K8s deployment)"
        KUBECTL_AVAILABLE=false
    fi

    # Check kind (optional)
    if command -v kind &> /dev/null; then
        log_success "Kind installed"
        KIND_AVAILABLE=true
    else
        log_warn "Kind not found (optional)"
        KIND_AVAILABLE=false
    fi

    echo ""
}

# Build the application locally
build_app() {
    log_info "Building registry-viewer..."

    cd "$SCRIPT_DIR"

    # Disable Go workspace to build standalone
    export GOWORK=off

    # Download dependencies
    go mod tidy
    go mod download

    # Build
    go build -o registry-viewer .

    log_success "Application built: $SCRIPT_DIR/registry-viewer"
    echo ""
}

# Build Docker image using Dockerfile.workspace from truvag3 root
build_docker() {
    log_info "Building Docker image..."

    if [ "$DOCKER_AVAILABLE" != true ]; then
        log_error "Docker is required for building images"
        exit 1
    fi

    local no_cache_flag=""
    if [ "$DOCKER_NO_CACHE" = "true" ]; then
        log_info "Building with --no-cache"
        no_cache_flag="--no-cache"
    fi

    # Build from truvag3 root using Dockerfile.workspace
    # This allows copying local modules (core, orchestration, telemetry) into the build context
    cd "$TRUVAG3_ROOT"
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" build $no_cache_flag \
        -f examples/registry-viewer-app/Dockerfile.workspace \
        -t "$IMAGE_NAME" .

    log_success "Docker image built: $IMAGE_NAME"
    echo ""
}

# Load image to Kind cluster
load_to_kind() {
    if [ "$KIND_AVAILABLE" != true ]; then
        log_warn "Kind not available, skipping image load"
        return
    fi

    # Detect Kind cluster from kubectl context
    local context=$(kubectl config current-context 2>/dev/null)
    local cluster_name=""

    if [[ "$context" == kind-* ]]; then
        cluster_name="${context#kind-}"
        log_info "Detected Kind cluster: $cluster_name"
    else
        # Try to find any Kind cluster
        cluster_name=$(kind get clusters 2>/dev/null | head -1)
        if [ -z "$cluster_name" ]; then
            log_warn "No Kind cluster found, skipping image load"
            return
        fi
        log_info "Using Kind cluster: $cluster_name"
    fi

    log_info "Loading image to Kind cluster '$cluster_name'..."
    kind load docker-image --name "$cluster_name" "$IMAGE_NAME"

    log_success "Image loaded to Kind"
    echo ""
}

# Deploy to Kubernetes
deploy_k8s() {
    log_info "Deploying to Kubernetes..."

    if [ "$KUBECTL_AVAILABLE" != true ]; then
        log_error "kubectl is required for deployment"
        exit 1
    fi

    # Get Redis URL from config or environment
    local redis_url=$(get_redis_url)
    log_info "Using Redis URL: $redis_url"

    # Create namespace if not exists
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

    # Update ConfigMap with correct Redis URL before applying
    # This allows the Redis URL to be derived from k8-deployment/redis.yaml
    # or overridden via REDIS_URL environment variable
    log_info "Configuring registry-viewer-config ConfigMap..."
    kubectl create configmap registry-viewer-config \
        --namespace="$NAMESPACE" \
        --from-literal=REDIS_URL="$redis_url" \
        --from-literal=REDIS_NAMESPACE="${REDIS_NAMESPACE}" \
        --from-literal=USE_MOCK="false" \
        --from-literal=PORT="${APP_PORT}" \
        --from-literal=TRUVAG3_VIEWER_MEMORY_DOMAINS="${TRUVAG3_VIEWER_MEMORY_DOMAINS:-infrastructure}" \
        --dry-run=client -o yaml | kubectl apply -f -

    # Apply deployment (ConfigMap is already applied above)
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    # Restart to pick up new image
    log_info "Rolling out new version..."
    kubectl rollout restart deployment/$APP_NAME -n "$NAMESPACE"

    log_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n "$NAMESPACE" --timeout=120s; then
        log_success "Deployment complete!"
    else
        log_error "Deployment failed"
        kubectl logs -n "$NAMESPACE" -l app=$APP_NAME --tail=20
        exit 1
    fi

    echo ""
    log_info "Run '$0 forward' to access the dashboard"
}

# Port forward
# Port forward with auto-reconnect
port_forward() {
    log_info "Setting up port forward with auto-reconnect..."

    if [ "$KUBECTL_AVAILABLE" != true ]; then
        log_error "kubectl is required for port forwarding"
        exit 1
    fi

    # Kill existing port forwards
    pkill -f "port-forward.*$APP_NAME" 2>/dev/null || true
    sleep 1

    log_success "Port forward established"
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Registry Viewer is Ready!                       ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "  Dashboard: http://localhost:$APP_PORT"
    echo ""
    echo "  API Endpoints:"
    echo "    GET /api/services  - List all registered services"
    echo "    GET /api/health    - Health check"
    echo ""
    echo -e "${YELLOW}Port forwards have auto-reconnect enabled${NC}"
    echo "Press Ctrl+C to stop port forward"
    echo ""

    # Auto-reconnect loop - restarts port forward if it dies (e.g., during rollout)
    while true; do
        kubectl port-forward -n "$NAMESPACE" svc/$APP_NAME-service $APP_PORT:$APP_PORT 2>/dev/null
        exit_code=$?
        if [ $exit_code -eq 130 ] || [ $exit_code -eq 143 ]; then
            # SIGINT (130) or SIGTERM (143) - user cancelled
            log_info "Port forward stopped by user"
            break
        fi
        log_warn "Port forward disconnected (exit code: $exit_code), reconnecting in 3s..."
        sleep 3
    done
}

# Run locally (with mock data by default)
run_local() {
    log_info "Starting registry-viewer locally..."

    cd "$SCRIPT_DIR"

    # Build if binary doesn't exist
    if [ ! -f "./registry-viewer" ]; then
        build_app
    fi

    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Registry Viewer Starting...                     ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "  Dashboard: http://localhost:$APP_PORT"
    echo ""
    echo "  Mode: ${1:-mock}"
    echo ""
    echo "Press Ctrl+C to stop"
    echo ""

    if [ "$1" = "redis" ]; then
        ./registry-viewer -mock=false -redis-url="${REDIS_URL:-redis://localhost:6379}"
    else
        ./registry-viewer -mock=true
    fi
}

# Run with Redis (connects to existing Redis)
run_redis() {
    log_info "Starting registry-viewer with Redis connection..."

    # Check if Redis port-forward exists or Redis is local
    if ! nc -z localhost 6379 2>/dev/null; then
        log_warn "Redis not available on localhost:6379"
        echo ""
        echo "Options:"
        echo "  1. Port-forward Redis from K8s:"
        echo "     kubectl port-forward -n truvag3-examples svc/redis 6379:6379 &"
        echo ""
        echo "  2. Start local Redis:"
        echo "     docker run -d -p 6379:6379 --name redis redis:7-alpine"
        echo ""
        read -p "Continue anyway? (y/n) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi

    run_local redis
}

# Rebuild and redeploy
rebuild() {
    print_header
    log_info "Rebuilding and redeploying..."

    # Build with no-cache
    DOCKER_NO_CACHE=true build_docker

    # Load to Kind if available
    load_to_kind

    # Deploy
    deploy_k8s

    log_success "Rebuild complete!"
}

# Cleanup
cleanup() {
    log_info "Cleaning up..."

    # Stop port forwards
    pkill -f "port-forward.*$APP_NAME" 2>/dev/null || true

    # Delete K8s resources
    if [ "$KUBECTL_AVAILABLE" = true ]; then
        kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    fi

    # Remove local binary
    rm -f "$SCRIPT_DIR/registry-viewer"

    log_success "Cleanup complete"
}

# Show status
status() {
    log_info "Checking status..."

    echo ""
    echo "Configuration:"
    if get_redis_config; then
        log_info "Redis config extracted from k8-deployment/redis.yaml"
    else
        log_warn "Using default Redis config (redis.yaml not found)"
    fi
    echo "  Redis Service: $REDIS_SERVICE_NAME"
    echo "  Redis Port: $REDIS_PORT"
    echo "  Redis URL: redis://${REDIS_SERVICE_NAME}:${REDIS_PORT}"
    if [ -n "$REDIS_URL" ]; then
        echo "  REDIS_URL override: $REDIS_URL"
    fi
    echo "  Redis Namespace: $REDIS_NAMESPACE"
    echo "  App Port: $APP_PORT"

    echo ""
    echo "Local:"
    if [ -f "$SCRIPT_DIR/registry-viewer" ]; then
        echo "  Binary: EXISTS"
    else
        echo "  Binary: NOT FOUND"
    fi

    echo ""
    echo "Docker:"
    if "${TRUVAG3_CONTAINER_RUNTIME:-docker}" image inspect "$IMAGE_NAME" &>/dev/null 2>&1; then
        echo "  Image: EXISTS"
    else
        echo "  Image: NOT FOUND"
    fi

    echo ""
    echo "Kubernetes:"
    if [ "$KUBECTL_AVAILABLE" = true ]; then
        if kubectl get deployment $APP_NAME -n "$NAMESPACE" &>/dev/null 2>&1; then
            echo "  Deployment: EXISTS"
            kubectl get pods -n "$NAMESPACE" -l app=$APP_NAME --no-headers 2>/dev/null | \
                while read line; do echo "    $line"; done

            # Show current ConfigMap values
            echo ""
            echo "  ConfigMap (registry-viewer-config):"
            kubectl get configmap registry-viewer-config -n "$NAMESPACE" -o jsonpath='{.data}' 2>/dev/null | \
                tr ',' '\n' | sed 's/[{}"]//g' | while read line; do echo "    $line"; done
        else
            echo "  Deployment: NOT FOUND"
        fi
    else
        echo "  kubectl not available"
    fi
    echo ""
}

# Show logs
logs() {
    if [ "$KUBECTL_AVAILABLE" != true ]; then
        log_error "kubectl is required to view logs"
        exit 1
    fi

    kubectl logs -n "$NAMESPACE" -l app=$APP_NAME -f --tail=100
}

# Show help
show_help() {
    print_header
    cat << EOF
Usage: $0 <command>

Local Development:
  build         Build the application locally
  run           Run locally with mock data (default)
  run-redis     Run locally connected to Redis
  status        Show status of local/docker/k8s resources

Docker:
  docker        Build Docker image
  docker-run    Run Docker container locally

Kubernetes Deployment:
  deploy        Build, load to Kind, and deploy to K8s
  rebuild       Rebuild with --no-cache and redeploy
  forward       Port forward from K8s to localhost:$APP_PORT
  logs          Stream logs from K8s pod
  cleanup       Remove deployed resources

Examples:
  # Quick local demo with mock data
  $0 run

  # Connect to Redis in Kind cluster
  kubectl port-forward -n truvag3-examples svc/redis 6379:6379 &
  $0 run-redis

  # Deploy to existing Kind cluster
  $0 deploy
  $0 forward

  # Full rebuild and deploy
  $0 rebuild
  $0 forward

  # Deploy with custom Redis URL
  REDIS_URL=redis://my-redis:6379 $0 deploy

Environment Variables:
  REDIS_URL         Override Redis connection URL
                    Default: Extracted from ../k8-deployment/redis.yaml
                    (service name + port from Redis Service definition)
  REDIS_NAMESPACE   Redis key namespace (default: truvag3)
  DOCKER_NO_CACHE   Set to 'true' to build Docker with --no-cache

Redis Configuration:
  The deploy command automatically extracts Redis service info from
  ../k8-deployment/redis.yaml and configures the app accordingly.
  This ensures the registry viewer connects to the same Redis instance
  used by other TruvaG3 examples.

  To override, set REDIS_URL environment variable before deploying.

Port: $APP_PORT (no conflicts with other examples which use 8333-8369)
EOF
}

# Docker run locally
docker_run() {
    log_info "Running Docker container locally..."

    if [ "$DOCKER_AVAILABLE" != true ]; then
        log_error "Docker is required"
        exit 1
    fi

    # Build if image doesn't exist
    if ! "${TRUVAG3_CONTAINER_RUNTIME:-docker}" image inspect "$IMAGE_NAME" &>/dev/null 2>&1; then
        build_docker
    fi

    # Stop existing container
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop $APP_NAME 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm $APP_NAME 2>/dev/null || true

    local redis_arg=""
    if [ "$1" = "redis" ]; then
        redis_arg="-mock=false -redis-url=redis://host.docker.internal:6379"
        log_info "Running with Redis connection"
    else
        redis_arg="-mock=true"
        log_info "Running with mock data"
    fi

    echo ""
    echo "Dashboard: http://localhost:$APP_PORT"
    echo "Press Ctrl+C to stop"
    echo ""

    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" run --rm -p $APP_PORT:$APP_PORT --name $APP_NAME "$IMAGE_NAME" $redis_arg
}

# Handle arguments
case "${1:-help}" in
    build)
        check_prerequisites
        build_app
        ;;
    run)
        check_prerequisites
        run_local mock
        ;;
    run-redis)
        check_prerequisites
        run_redis
        ;;
    docker)
        check_prerequisites
        build_docker
        ;;
    docker-run)
        check_prerequisites
        docker_run "$2"
        ;;
    deploy)
        check_prerequisites
        print_header
        build_docker
        load_to_kind
        deploy_k8s
        ;;
    rebuild)
        check_prerequisites
        rebuild
        ;;
    forward)
        check_prerequisites
        port_forward
        ;;
    logs)
        logs
        ;;
    status)
        check_prerequisites
        status
        ;;
    cleanup)
        check_prerequisites
        cleanup
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        echo "Unknown command: $1"
        echo ""
        show_help
        exit 1
        ;;
esac
