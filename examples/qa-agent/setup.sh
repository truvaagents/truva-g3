#!/bin/bash
# QA Agent Setup Script
# Provides commands for building, running, and deploying the QA agent

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
APP_NAME="qa-agent"
AGENT_PORT=${PORT:-8358}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  QA Agent - $1${NC}"
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

# Build the agent
cmd_build() {
    print_header "Building QA Agent"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o qa-agent .

    print_success "Build completed: qa-agent"
}

# Run the agent locally
cmd_run() {
    print_header "Running QA Agent"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        exit 1
    fi

    cmd_build

    print_info "Starting qa-agent on port $AGENT_PORT..."
    ./qa-agent
}

# Build Docker image
cmd_docker_build() {
    print_header "Building Docker Image"

    local no_cache_flag=""
    if [ "$DOCKER_NO_CACHE" = "true" ] || [ "$1" = "--no-cache" ]; then
        print_info "Building with --no-cache"
        no_cache_flag="--no-cache"
    fi

    local truvag3_root="$(dirname "$(dirname "$SCRIPT_DIR")")"
    print_info "Building from truvag3 root: $truvag3_root"
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" build $no_cache_flag \
        -f "$SCRIPT_DIR/Dockerfile.workspace" \
        -t $APP_NAME:latest \
        "$truvag3_root"

    print_success "Docker image built: $APP_NAME:latest"
}

# Setup API keys as Kubernetes secrets (handles all AI providers automatically)
setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-qa-agent" "$NAMESPACE"
}

# Setup agent configuration from .env as ConfigMap
setup_agent_config() {
    truvag3_create_configmap "qa-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Best-effort reconciliation used by deployment commands. The explicit
# skills-sync command below remains strict for operator and CI use.
prepare_skills() {
    truvag3_prepare_agent_skills "$SCRIPT_DIR/skills/packages"
}

# Strictly reconcile every package stored under
# skills/packages/<namespace>/<name>.json.
sync_skills() {
    truvag3_sync_agent_skills "$SCRIPT_DIR/skills/packages"
}

check_skills() {
    truvag3_check_agent_skills "$SCRIPT_DIR/skills/packages"
}

# Deploy to Kubernetes
cmd_deploy() {
    print_header "Deploying to Kubernetes"

    load_env

    # Build Docker image first
    cmd_docker_build

    # Load image into kind cluster
    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config from .env
    setup_k8s_secrets
    setup_agent_config
    prepare_skills

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    # Force rollout to pick up new images
    print_info "Rolling out new version..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE
    kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s

    print_info "Waiting for pods to be ready..."
    kubectl wait --for=condition=ready pod -l app=$APP_NAME -n $NAMESPACE --timeout=120s 2>/dev/null || true

    print_success "Deployment complete!"
    print_info "Run './setup.sh forward' to set up port forwards"
}

# Run tests
cmd_test() {
    print_header "Running Tests"

    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/qa-agent-service $AGENT_PORT:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    echo "Testing health endpoint..."
    if curl -s http://localhost:$AGENT_PORT/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:$AGENT_PORT/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    echo ""
    print_info "Testing discover endpoint..."
    curl -s http://localhost:$AGENT_PORT/discover | jq . 2>/dev/null || echo "(install jq for pretty output)"

    kill $PF_PID 2>/dev/null || true
}

# Port forward with auto-reconnect
cmd_forward() {
    truvag3_forward "qa-agent-service" 8358 80
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

# Rollout
cmd_rollout() {
    print_header "Rolling Out Deployment"

    local rebuild=false
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then
        rebuild=true
    fi

    load_env
    setup_k8s_secrets
    setup_agent_config
    prepare_skills

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

    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "Rollout complete!"
    else
        print_error "Rollout failed"
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

# Rebuild with no-cache
cmd_rebuild() {
    print_header "Rebuilding with Fresh Dependencies"

    load_env
    DOCKER_NO_CACHE=true cmd_docker_build

    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
    setup_k8s_secrets
    setup_agent_config
    prepare_skills

    kubectl apply -f k8-deployment.yaml
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME rebuilt and deployed!"
    else
        print_error "Deployment failed"
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

# Clean up
cmd_clean() {
    print_header "Cleaning Up Deployment"
    kubectl delete -f k8-deployment.yaml --ignore-not-found
    print_success "Cleanup complete"
}

# Help
cmd_help() {
    echo "QA Agent Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Build & Deploy Commands:"
    echo "  build         Build the agent binary"
    echo "  run           Build and run the agent locally"
    echo "  docker-build  Build Docker image"
    echo "  deploy        Build, load, and deploy to Kubernetes"
    echo "  rebuild       Rebuild with --no-cache and redeploy"
    echo "  skills-check  Compare published skill packages with Git"
    echo "  skills-sync   Reconcile and verify skill packages without restarting"
    echo ""
    echo "Testing & Access Commands:"
    echo "  test          Run smoke tests against deployed agent"
    echo "  forward       Port forward the QA agent service (auto-reconnect)"
    echo "  logs          View agent logs"
    echo "  status        Check deployment status"
    echo "  rollout       Restart deployment (use --build to rebuild first)"
    echo ""
    echo "Cleanup Commands:"
    echo "  clean         Remove QA agent deployment only"
    echo ""
    echo "Environment Variables (set in .env):"
    echo "  REDIS_URL             Redis connection URL (required)"
    echo "  PORT                  HTTP server port (default: 8358)"
    echo "  OPENAI_API_KEY        OpenAI API key"
    echo "  ANTHROPIC_API_KEY     Anthropic API key"
    echo "  GROQ_API_KEY          Groq API key"
    echo "  TRUVAG3_GROQ_MODEL_*   Groq model overrides"
    echo ""
    echo "Examples:"
    echo "  ./setup.sh deploy    # Build and deploy to K8s"
    echo "  ./setup.sh forward   # Port forward agent (auto-reconnect)"
    echo "  ./setup.sh test      # Run smoke tests"
}

# Main entry point
case "${1:-help}" in
    build)      cmd_build ;;
    run)        cmd_run ;;
    docker-build) cmd_docker_build ;;
    deploy)     cmd_deploy ;;
    rebuild)    cmd_rebuild ;;
    skills-check) load_env; check_skills ;;
    skills-sync) load_env; sync_skills ;;
    test)       cmd_test ;;
    forward)    cmd_forward ;;
    logs)       cmd_logs ;;
    status)     cmd_status ;;
    rollout)    cmd_rollout "$@" ;;
    clean)      cmd_clean ;;
    help|--help|-h) cmd_help ;;
    *)
        print_error "Unknown command: $1"
        cmd_help
        exit 1
        ;;
esac
