#!/bin/bash

# setup.sh - Setup and deployment script for registry-viewer-app
# This independently deployable app visualizes TruvaG3 runtime data. Its source
# imports framework modules from the repository; setup.sh builds them together.
# No infrastructure setup is performed here: build, deploy, and run the app
# after shared infrastructure is available.

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
REDIS_NAMESPACE="default"
REGISTRY_VIEWER_SECRET_TEMP_DIR=""

cleanup_registry_viewer_secret_temp_dir() {
    case "$REGISTRY_VIEWER_SECRET_TEMP_DIR" in
        /tmp/truvag3-registry-viewer-secrets.*)
            rm -rf -- "$REGISTRY_VIEWER_SECRET_TEMP_DIR"
            ;;
    esac
    REGISTRY_VIEWER_SECRET_TEMP_DIR=""
}

# Credential staging is process-local and always removed, including when a
# later kubectl command fails under `set -e`.
trap cleanup_registry_viewer_secret_temp_dir EXIT

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
    if [ -n "${REDIS_URL:-}" ]; then
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
        echo "Please install Go 1.27+ from https://golang.org/dl/"
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

    # Build this module through the local framework replacements in go.mod.
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

# Reject the removed alias before building, changing a workload, or running Redis mode.
# Whitespace-only values are absent, matching core.ResolveRedisConnectionConfig.
validate_redis_environment() {
    local removed_url="${TRUVAG3_REDIS_URL:-}"
    if [ -n "${removed_url//[[:space:]]/}" ]; then
        log_error "TRUVAG3_REDIS_URL is unsupported; remove it and use REDIS_URL"
        return 1
    fi
}

# Deploy to Kubernetes
deploy_k8s() {
    validate_redis_environment
    log_info "Deploying to Kubernetes..."

    if [ "$KUBECTL_AVAILABLE" != true ]; then
        log_error "kubectl is required for deployment"
        exit 1
    fi

    local redis_url=""
    local redis_mode="${TRUVAG3_REDIS_MODE:-}"
    local redis_addrs="${TRUVAG3_REDIS_ADDRS:-}"
    local redis_db=""
    local structured_source=false
    local connection_name=""
    for connection_name in \
        TRUVAG3_REDIS_MODE \
        TRUVAG3_REDIS_ADDRS \
        TRUVAG3_REDIS_MASTER_NAME \
        TRUVAG3_REDIS_USERNAME \
        TRUVAG3_REDIS_PASSWORD \
        TRUVAG3_REDIS_SENTINEL_USERNAME \
        TRUVAG3_REDIS_SENTINEL_PASSWORD \
        TRUVAG3_REDIS_TLS_ENABLED \
        TRUVAG3_REDIS_TLS_SERVER_NAME \
        TRUVAG3_REDIS_CA_FILE \
        TRUVAG3_REDIS_DB; do
        if [ -n "${!connection_name}" ]; then
            structured_source=true
            break
        fi
    done
    if [ -n "${REDIS_URL:-}" ] && [ "$structured_source" = true ]; then
        log_error "URL shorthand cannot be combined with structured TRUVAG3_REDIS_* connection settings"
        exit 1
    fi
    if [ "$structured_source" = true ]; then
        if [ -z "$redis_mode" ]; then
            log_error "TRUVAG3_REDIS_MODE is required with structured Redis connection settings"
            exit 1
        fi
        if [ -z "$redis_addrs" ]; then
            log_error "TRUVAG3_REDIS_ADDRS is required with TRUVAG3_REDIS_MODE"
            exit 1
        fi
        redis_mode=$(printf '%s' "$redis_mode" | tr '[:upper:]' '[:lower:]')
        case "$redis_mode" in
            standalone|sentinel|cluster) ;;
            *)
                log_error "TRUVAG3_REDIS_MODE must be standalone, sentinel, or cluster"
                exit 1
                ;;
        esac
        if [ "$redis_mode" = "sentinel" ] && [ -z "${TRUVAG3_REDIS_MASTER_NAME:-}" ]; then
            log_error "TRUVAG3_REDIS_MASTER_NAME is required in Sentinel mode"
            exit 1
        fi
        redis_db="${TRUVAG3_REDIS_DB:-0}"
        if [ "$redis_db" != "0" ]; then
            log_error "Registry Viewer requires TRUVAG3_REDIS_DB=0 in every Redis topology"
            exit 1
        fi
        if [ -n "${TRUVAG3_REDIS_CA_FILE:-}" ] && [ ! -r "$TRUVAG3_REDIS_CA_FILE" ]; then
            log_error "TRUVAG3_REDIS_CA_FILE must name a readable PEM file"
            exit 1
        fi
        log_info "Using structured Redis topology: $redis_mode"
    else
        redis_url=$(get_redis_url)
        log_info "Using standalone Redis URL shorthand"
    fi

    # Create namespace if not exists
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

    # Update the non-secret topology and Viewer configuration. REDIS_URL and
    # ACL credentials are written to a Secret below because the URL may itself
    # contain credentials. These objects must exist before a new workload pod
    # can start, otherwise it could briefly connect with the portable defaults
    # embedded in k8-deployment.yaml.
    log_info "Configuring registry-viewer-config ConfigMap..."
    kubectl create configmap registry-viewer-config \
        --namespace="$NAMESPACE" \
        --from-literal=TRUVAG3_REDIS_MODE="$redis_mode" \
        --from-literal=TRUVAG3_REDIS_ADDRS="$redis_addrs" \
        --from-literal=TRUVAG3_REDIS_MASTER_NAME="${TRUVAG3_REDIS_MASTER_NAME:-}" \
        --from-literal=TRUVAG3_REDIS_DB="$redis_db" \
        --from-literal=TRUVAG3_REDIS_NAMESPACE="${TRUVAG3_REDIS_NAMESPACE:-$REDIS_NAMESPACE}" \
        --from-literal=TRUVAG3_REDIS_TLS_ENABLED="${TRUVAG3_REDIS_TLS_ENABLED:-}" \
        --from-literal=TRUVAG3_REDIS_TLS_SERVER_NAME="${TRUVAG3_REDIS_TLS_SERVER_NAME:-}" \
        --from-literal=TRUVAG3_REDIS_CA_FILE="${TRUVAG3_REDIS_CA_FILE:+/etc/truvag3/redis-ca/ca.crt}" \
        --from-literal=TRUVAG3_REDIS_POOL_SIZE="${TRUVAG3_REDIS_POOL_SIZE:-}" \
        --from-literal=TRUVAG3_REDIS_MIN_IDLE_CONNS="${TRUVAG3_REDIS_MIN_IDLE_CONNS:-}" \
        --from-literal=TRUVAG3_REDIS_DIAL_TIMEOUT="${TRUVAG3_REDIS_DIAL_TIMEOUT:-}" \
        --from-literal=TRUVAG3_REDIS_READ_TIMEOUT="${TRUVAG3_REDIS_READ_TIMEOUT:-}" \
        --from-literal=TRUVAG3_REDIS_WRITE_TIMEOUT="${TRUVAG3_REDIS_WRITE_TIMEOUT:-}" \
        --from-literal=TRUVAG3_REDIS_MAX_RETRIES="${TRUVAG3_REDIS_MAX_RETRIES:-}" \
        --from-literal=USE_MOCK="false" \
        --from-literal=PORT="${APP_PORT}" \
        --from-literal=APP_ENV="${APP_ENV:-development}" \
        --from-literal=OTEL_EXPORTER_OTLP_ENDPOINT="${OTEL_EXPORTER_OTLP_ENDPOINT:-http://otel-collector:4318}" \
        --from-literal=JAEGER_UI_URL="${JAEGER_UI_URL:-http://jaeger.localhost}" \
        --from-literal=TRUVAG3_VIEWER_MEMORY_DOMAINS="${TRUVAG3_VIEWER_MEMORY_DOMAINS:-infrastructure}" \
        --dry-run=client -o yaml | kubectl apply -f -

    log_info "Configuring Registry Viewer Redis credentials Secret..."
    REGISTRY_VIEWER_SECRET_TEMP_DIR=$(mktemp -d "/tmp/truvag3-registry-viewer-secrets.XXXXXX")
    chmod 700 "$REGISTRY_VIEWER_SECRET_TEMP_DIR"
    printf '%s' "$redis_url" > "$REGISTRY_VIEWER_SECRET_TEMP_DIR/REDIS_URL"
    printf '%s' "${TRUVAG3_REDIS_USERNAME:-}" > "$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_USERNAME"
    printf '%s' "${TRUVAG3_REDIS_PASSWORD:-}" > "$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_PASSWORD"
    printf '%s' "${TRUVAG3_REDIS_SENTINEL_USERNAME:-}" > "$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_SENTINEL_USERNAME"
    printf '%s' "${TRUVAG3_REDIS_SENTINEL_PASSWORD:-}" > "$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_SENTINEL_PASSWORD"
    chmod 600 "$REGISTRY_VIEWER_SECRET_TEMP_DIR"/*
    kubectl create secret generic registry-viewer-redis-credentials \
        --namespace="$NAMESPACE" \
        --from-file=REDIS_URL="$REGISTRY_VIEWER_SECRET_TEMP_DIR/REDIS_URL" \
        --from-file=TRUVAG3_REDIS_USERNAME="$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_USERNAME" \
        --from-file=TRUVAG3_REDIS_PASSWORD="$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_PASSWORD" \
        --from-file=TRUVAG3_REDIS_SENTINEL_USERNAME="$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_SENTINEL_USERNAME" \
        --from-file=TRUVAG3_REDIS_SENTINEL_PASSWORD="$REGISTRY_VIEWER_SECRET_TEMP_DIR/TRUVAG3_REDIS_SENTINEL_PASSWORD" \
        --dry-run=client -o yaml | kubectl apply -f -
    cleanup_registry_viewer_secret_temp_dir

    log_info "Configuring Registry Viewer Redis CA Secret..."
    if [ -n "${TRUVAG3_REDIS_CA_FILE:-}" ]; then
        kubectl create secret generic registry-viewer-redis-ca \
            --namespace="$NAMESPACE" \
            --from-file=ca.crt="$TRUVAG3_REDIS_CA_FILE" \
            --dry-run=client -o yaml | kubectl apply -f -
    else
        kubectl create secret generic registry-viewer-redis-ca \
            --namespace="$NAMESPACE" \
            --from-literal=ca.crt="" \
            --dry-run=client -o yaml | kubectl apply -f -
    fi

    # Apply only the workload objects from the portable manifest. Its ConfigMap
    # is useful for direct `kubectl apply`, but setup.sh has already installed
    # the authoritative environment-specific ConfigMap above and must not
    # overwrite it with standalone/default values.
    kubectl apply \
        --selector='truvag3.io/setup-workload=true' \
        -f "$SCRIPT_DIR/k8-deployment.yaml"

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
    if [ "${1:-}" = "redis" ]; then
        validate_redis_environment
    fi
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
        if [ -z "${TRUVAG3_REDIS_MODE:-}" ] && [ -z "${REDIS_URL:-}" ]; then
            export REDIS_URL="redis://localhost:6379"
        fi
        ./registry-viewer -port="$APP_PORT" -mock=false
    else
        ./registry-viewer -port="$APP_PORT" -mock=true
    fi
}

# Run with Redis (connects to existing Redis)
run_redis() {
    validate_redis_environment
    log_info "Starting registry-viewer with Redis connection..."

    # Check the conventional local endpoint only when no explicit connection
    # source was supplied. Remote URLs and structured topologies must not be
    # mistaken for a missing localhost port-forward.
    if [ -z "${TRUVAG3_REDIS_MODE:-}" ] && [ -z "${REDIS_URL:-}" ] && \
        ! nc -z localhost 6379 2>/dev/null; then
        log_warn "Redis not available on localhost:6379"
        echo ""
        echo "Options:"
        echo "  1. Port-forward Redis from K8s:"
        echo "     kubectl port-forward -n truvag3-examples svc/redis 6379:6379 &"
        echo ""
        echo "  2. Start local Redis:"
        echo "     docker run -d -p 6379:6379 --name redis redis:8.2.8-alpine"
        echo ""
        read -p "Continue anyway? (y/n) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi

    run_local redis
}

# Verify every Redis-backed Viewer API against the running application. The
# script is read-only and accepts expected IDs through environment variables so
# evidence is always produced by framework agents/tools rather than raw keys.
verify_live_data() {
    local strict_arg="${1:-}"
    bash "$SCRIPT_DIR/scripts/verify-live-data.sh" "$strict_arg"
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
        kubectl delete secret registry-viewer-redis-credentials registry-viewer-redis-ca \
            -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
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
    echo "  Default Redis endpoint: ${REDIS_SERVICE_NAME}:${REDIS_PORT}"
    if [ -n "${REDIS_URL:-}" ]; then
        echo "  REDIS_URL override: configured (value hidden)"
    fi
    if [ -n "${TRUVAG3_REDIS_URL:-}" ]; then
        echo "  Unsupported TRUVAG3_REDIS_URL is set; remove it and use REDIS_URL"
    fi
    if [ -n "${TRUVAG3_REDIS_MODE:-}" ]; then
        echo "  Structured Redis mode: $TRUVAG3_REDIS_MODE"
        echo "  Structured Redis seeds: configured (values hidden)"
    fi
    echo "  Redis Namespace: ${TRUVAG3_REDIS_NAMESPACE:-$REDIS_NAMESPACE}"
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
  verify        Read-only verification of every Viewer data path
  verify-all    Strict verification using all TRUVAG3_VIEWER_EXPECT_* IDs
  status        Show status of local/docker/k8s resources

Docker:
  docker             Build Docker image
  docker-run         Run Docker container locally with mock data
  docker-run redis   Run Docker container with URL or structured Redis configuration

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

  # In another terminal, verify the live data paths
  $0 verify

  # Full rebuild and deploy
  $0 rebuild
  $0 forward

  # Deploy with custom Redis URL
  REDIS_URL=redis://my-redis:6379 $0 deploy

Environment Variables:
  REDIS_URL         Override Redis connection URL
                    Default: Extracted from ../k8-deployment/redis.yaml
                    (service name + port from Redis Service definition)
  REDIS_NAMESPACE   Redis key namespace (default: default)
  TRUVAG3_REDIS_MODE, TRUVAG3_REDIS_ADDRS
                    Structured standalone, Sentinel, or cluster topology.
                    Do not combine these with REDIS_URL.
  TRUVAG3_REDIS_MASTER_NAME
                    Required in Sentinel mode.
  TRUVAG3_REDIS_DB   Must be 0 for every Registry Viewer Redis topology.
  TRUVAG3_REDIS_NAMESPACE
                    Versioned DB-0 deployment namespace.
  TRUVAG3_REDIS_USERNAME, TRUVAG3_REDIS_PASSWORD
                    Data-node ACL credentials; stored in a Kubernetes Secret.
  TRUVAG3_REDIS_SENTINEL_USERNAME, TRUVAG3_REDIS_SENTINEL_PASSWORD
                    Sentinel ACL credentials; stored in a Kubernetes Secret.
  TRUVAG3_REDIS_TLS_ENABLED, TRUVAG3_REDIS_TLS_SERVER_NAME
                    TLS settings for structured topology.
  TRUVAG3_REDIS_CA_FILE
                    Local PEM CA file mounted read-only into the Viewer pod.
  TRUVAG3_VIEWER_URL
                    Base URL used by verify/verify-all (default: http://localhost:$APP_PORT).
  JAEGER_UI_URL     Browser-facing Jaeger UI base URL (default: http://jaeger.localhost)
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
    if [ "${1:-}" = "redis" ]; then
        validate_redis_environment
    fi
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

    local app_args=(-mock=true)
    local redis_env_args=()
    if [ "$1" = "redis" ]; then
        app_args=(-mock=false)

        # Pass Redis settings by environment-variable name so credentials do
        # not appear in the docker command line. If no topology was provided,
        # retain the convenient local standalone default.
        if [ -z "${TRUVAG3_REDIS_MODE:-}" ] && [ -z "${REDIS_URL:-}" ]; then
            redis_env_args+=(--env "REDIS_URL=redis://host.docker.internal:6379")
        fi

        local redis_env_name=""
        for redis_env_name in \
            REDIS_URL \
            TRUVAG3_REDIS_MODE \
            TRUVAG3_REDIS_ADDRS \
            TRUVAG3_REDIS_MASTER_NAME \
            TRUVAG3_REDIS_DB \
            TRUVAG3_REDIS_NAMESPACE \
            TRUVAG3_REDIS_USERNAME \
            TRUVAG3_REDIS_PASSWORD \
            TRUVAG3_REDIS_SENTINEL_USERNAME \
            TRUVAG3_REDIS_SENTINEL_PASSWORD \
            TRUVAG3_REDIS_TLS_ENABLED \
            TRUVAG3_REDIS_TLS_SERVER_NAME \
            TRUVAG3_REDIS_POOL_SIZE \
            TRUVAG3_REDIS_MIN_IDLE_CONNS \
            TRUVAG3_REDIS_DIAL_TIMEOUT \
            TRUVAG3_REDIS_READ_TIMEOUT \
            TRUVAG3_REDIS_WRITE_TIMEOUT \
            TRUVAG3_REDIS_MAX_RETRIES; do
            if [ -n "${!redis_env_name}" ]; then
                redis_env_args+=(--env "$redis_env_name")
            fi
        done

        if [ -n "${TRUVAG3_REDIS_CA_FILE:-}" ]; then
            if [ ! -r "$TRUVAG3_REDIS_CA_FILE" ]; then
                log_error "TRUVAG3_REDIS_CA_FILE must name a readable PEM file"
                exit 1
            fi
            redis_env_args+=(
                --volume "$TRUVAG3_REDIS_CA_FILE:/etc/truvag3/redis-ca/ca.crt:ro"
                --env "TRUVAG3_REDIS_CA_FILE=/etc/truvag3/redis-ca/ca.crt"
            )
        fi
        log_info "Running with Redis connection"
    else
        log_info "Running with mock data"
    fi

    echo ""
    echo "Dashboard: http://localhost:$APP_PORT"
    echo "Press Ctrl+C to stop"
    echo ""

    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" run --rm \
        -p "$APP_PORT:$APP_PORT" \
        --name "$APP_NAME" \
        "${redis_env_args[@]}" \
        "$IMAGE_NAME" \
        "${app_args[@]}"
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
    verify)
        verify_live_data
        ;;
    verify-all)
        verify_live_data --strict
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
        validate_redis_environment
        check_prerequisites
        print_header
        build_docker
        load_to_kind
        deploy_k8s
        ;;
    rebuild)
        validate_redis_environment
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
