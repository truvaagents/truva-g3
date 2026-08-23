#!/bin/bash
# OpenClaw Tool Setup Script
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
APP_NAME="openclaw-tool"
PORT=${PORT:-8393}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
# OpenClaw sidecar image (placeholder — a real, pinned OpenClaw gateway image, ANALYSIS.md §6).
OPENCLAW_IMAGE=${OPENCLAW_IMAGE:-openclaw:local}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  OpenClaw Tool - $1${NC}"
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
    print_header "Building OpenClaw Tool"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o openclaw-tool .

    print_success "Build completed: openclaw-tool"
}

# Run the tool locally
cmd_run() {
    print_header "Running OpenClaw Tool"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        print_info "Set it in .env file or export it: export REDIS_URL=redis://localhost:6379"
        exit 1
    fi

    # Build first
    cmd_build

    print_info "Starting openclaw-tool on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./openclaw-tool
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

# Load the OpenClaw sidecar image into kind if it exists locally.
# openclaw:local is a placeholder for a real OpenClaw gateway image (ANALYSIS.md §6); the Go
# adapter still deploys without it, but the `openclaw` container won't start.
load_openclaw_image() {
    command -v kind &> /dev/null || return 0
    if docker image inspect "$OPENCLAW_IMAGE" &> /dev/null || podman image exists "$OPENCLAW_IMAGE" &> /dev/null; then
        print_info "Loading OpenClaw sidecar image $OPENCLAW_IMAGE into kind..."
        kind load docker-image "$OPENCLAW_IMAGE" --name "$CLUSTER_NAME" || print_error "Failed to load $OPENCLAW_IMAGE"
    else
        print_error "OpenClaw sidecar image '$OPENCLAW_IMAGE' not found locally."
        print_info "The adapter will deploy, but the 'openclaw' container stays NotReady until you"
        print_info "build/pull a real OpenClaw image tagged '$OPENCLAW_IMAGE' (or set OPENCLAW_IMAGE=<tag>)."
    fi
}

# Create Kind cluster with port mappings for monitoring
cmd_cluster() { truvag3_create_cluster; }

# Setup monitoring infrastructure
cmd_infra() { truvag3_setup_infra; }

# Setup secrets: OpenClaw gateway token + OpenClaw's own LLM key.
# The adapter needs NO AI provider key of its own (it is a pure wrapper); only the OpenClaw
# sidecar does. We therefore create a tool-scoped secret, not the shared ai-provider-keys.
setup_api_keys() {
    local token="${OPENCLAW_GATEWAY_TOKEN:-}"
    if [ -z "$token" ]; then
        token="$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | xxd -p 2>/dev/null || echo "dev-token-$(date +%s)")"
        print_info "OPENCLAW_GATEWAY_TOKEN not in .env — generated an ephemeral token"
    fi
    local args=(--from-literal=OPENCLAW_GATEWAY_TOKEN="$token")
    # Inject whichever AI provider key(s) are set in .env. openclaw.json's model.apiKeyEnv
    # selects which one OpenClaw actually uses. The adapter gets NONE of these (pure wrapper, §2).
    local found=0
    for k in OPENAI_API_KEY ANTHROPIC_API_KEY OPENROUTER_API_KEY GROQ_API_KEY GEMINI_API_KEY DEEPSEEK_API_KEY XAI_API_KEY MISTRAL_API_KEY TOGETHER_API_KEY QWEN_API_KEY; do
        if [ -n "${!k:-}" ]; then
            args+=(--from-literal="$k=${!k}")
            found=1
        fi
    done
    [ "$found" -eq 0 ] && print_info "No AI provider key set in .env — OpenClaw sidecar will lack an LLM key"
    kubectl create secret generic openclaw-tool-secrets -n "$NAMESPACE" \
        "${args[@]}" --dry-run=client -o yaml | kubectl apply -f -
    print_success "Secret openclaw-tool-secrets applied"
}

# Setup config: env ConfigMap (TRUVAG3_*, APP_ENV, DEV_MODE) + OpenClaw gateway config + seed.
setup_config() {
    truvag3_create_configmap "openclaw-tool-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
    # OpenClaw gateway config (§6 Layer 1)
    kubectl create configmap openclaw-tool-config -n "$NAMESPACE" \
        --from-file=openclaw.json="$SCRIPT_DIR/config/openclaw.json" \
        --dry-run=client -o yaml | kubectl apply -f -
    # Amnesiac workspace seed (§8)
    kubectl create configmap openclaw-tool-seed -n "$NAMESPACE" \
        --from-file="$SCRIPT_DIR/seed" \
        --dry-run=client -o yaml | kubectl apply -f -
    print_success "ConfigMaps openclaw-tool-config + openclaw-tool-seed applied"
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

    # Load the OpenClaw sidecar image (placeholder) into kind
    load_openclaw_image

    # Setup API keys and config
    setup_api_keys
    setup_config

    print_info "Waiting for any existing deployment..."
    kubectl wait --for=condition=available --timeout=30s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null || true

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml
    # NetworkPolicy is advisory on Kind/kindnet (enforced under Cilium/Calico) — §6 Layer 3
    kubectl apply -f networkpolicy.yaml 2>/dev/null || print_info "NetworkPolicy not applied (advisory on Kind)"

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
    kubectl port-forward -n $NAMESPACE svc/openclaw-tool-service 8393:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo "Testing health endpoint..."
    if curl -s http://localhost:8393/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    # Test capabilities
    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:8393/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    # Capability tests (require a working OpenClaw sidecar; with the placeholder image these
    # will return a 5xx/timeout from the adapter, which still exercises the error path).
    echo ""
    print_info "Testing summarize_text..."
    curl -s -X POST http://localhost:8393/api/capabilities/summarize_text \
        -H "Content-Type: application/json" \
        -d '{"text":"TruvaG3 is a Kubernetes-native Go framework for building AI agents and tools that discover and coordinate via Redis. This is a short demonstration input for the summarizer.","style":"tldr"}' \
        | jq . 2>/dev/null || echo "(install jq for pretty output)"

    echo ""
    print_info "Testing answer_over_text..."
    curl -s -X POST http://localhost:8393/api/capabilities/answer_over_text \
        -H "Content-Type: application/json" \
        -d '{"text":"The framework discovers tools via Redis. The default adapter port is 8393.","question":"How do tools get discovered?"}' \
        | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

# Port forward for tool only
cmd_forward() {
    truvag3_forward "openclaw-tool-service" 8393 80
}

# Port forward for tool and monitoring
cmd_forward_all() {
    truvag3_forward_all \
        "openclaw-tool-service:8393:80" \
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

    echo "OpenClaw Tool Pod:"
    kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "OpenClaw Tool Service:"
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
    print_header "Cleaning Up OpenClaw Tool"

    print_info "Removing tool deployment..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found
    print_success "openclaw-tool cleanup complete"
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

    # Load the OpenClaw sidecar image (placeholder) into kind
    load_openclaw_image

    # Setup secrets and config from .env
    setup_api_keys
    setup_config

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml
    kubectl apply -f networkpolicy.yaml 2>/dev/null || print_info "NetworkPolicy not applied (advisory on Kind)"

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
    echo "OpenClaw Tool Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Local Development Commands:"
    echo "  build         Build the openclaw-tool binary"
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
    echo "  REDIS_URL              Redis connection URL (required for run)"
    echo "  PORT                   HTTP server port (default: 8393)"
    echo "  OPENCLAW_GATEWAY_TOKEN Bearer token for the OpenClaw gateway (auto-generated if unset)"
    echo "  ANTHROPIC_API_KEY      OpenClaw sidecar's LLM key (needed for real summarization)"
    echo "  OPENCLAW_IMAGE         OpenClaw sidecar image tag (default: openclaw:local)"
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
