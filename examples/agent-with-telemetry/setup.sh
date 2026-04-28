#!/bin/bash
# Agent with Telemetry Setup Script
# Provides commands for building, running, and deploying the research agent with full telemetry

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"
cd "$SCRIPT_DIR"

# Source shared env library for K8s Secret/ConfigMap management
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="research-agent-telemetry"
PORT=${PORT:-8355}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  Agent with Telemetry - $1${NC}"
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

# Check prerequisites
check_prerequisites() { truvag3_check_prerequisites; }

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
build() {
    print_header "Building Agent"

    print_info "Running go mod tidy..."
    GOWORK=off go mod tidy

    print_info "Building binary..."
    GOWORK=off go build -o research-agent-telemetry .

    print_success "Build completed: research-agent-telemetry"
}

# Run the agent locally
run() {
    print_header "Running Agent"

    load_env

    if [ -z "$REDIS_URL" ]; then
        print_error "REDIS_URL environment variable is required"
        print_info "Set it in .env file or export it: export REDIS_URL=redis://localhost:6379"
        exit 1
    fi

    # Build first
    build

    print_info "Starting research-agent-telemetry on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./research-agent-telemetry
}

# Build Docker image (using local workspace modules)
# Usage: docker_build [--no-cache]
docker_build() {
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
cluster() { truvag3_create_cluster; }

# Setup monitoring infrastructure
infra() { truvag3_setup_infra; }

# Setup API keys as Kubernetes secrets (delegates to shared library)
setup_api_keys() {
    truvag3_create_secret "ai-provider-keys-telemetry-agent" "$NAMESPACE"
}

# Setup agent configuration from .env as ConfigMap (delegates to shared library)
setup_agent_config() {
    truvag3_create_configmap "research-agent-telemetry-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Deploy to Kubernetes
deploy() {
    print_header "Deploying to Kubernetes"

    load_env

    # Build Docker image first
    docker_build

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -n $NAMESPACE -f -

    # Setup API keys and agent config
    setup_api_keys
    setup_agent_config

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

# Verify ingress reachability
verify_ingress() {
    truvag3_verify_ingress \
        "telemetry-agent.localhost" \
        "grafana.localhost" "prometheus.localhost" "jaeger.localhost" || true
}

# Full deployment: cluster + infrastructure + agent
full_deploy() {
    print_header "Full Deployment"

    check_prerequisites

    load_env

    # Step 1: Create Kind cluster
    cluster

    # Step 2: Setup monitoring infrastructure
    infra

    # Step 3: Deploy agent
    deploy

    # Step 4: Verify ingress is reachable
    verify_ingress

    echo "Deploy complete. Access at http://telemetry-agent.localhost"
}

# Run tests
test() {
    print_header "Running Tests"

    # Start port forward in background
    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/$APP_NAME 8355:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo "Testing health endpoint..."
    if curl -s http://localhost:8355/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    # Test capabilities
    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:8355/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    # Test research query
    echo ""
    print_info "Testing research query..."
    curl -s -X POST http://localhost:8355/api/capabilities/research_topic \
        -H "Content-Type: application/json" \
        -d '{"topic": "latest AI trends", "ai_synthesis": false}' | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

# Port forward for agent only (background)
forward() {
    truvag3_forward "research-agent-telemetry" 8355 80
}

# Port forward for agent and monitoring
forward_all() {
    truvag3_forward_all \
        "research-agent-telemetry:8355:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

# View logs
logs() {
    print_header "Viewing Logs"

    kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100
}

# Check status
status() {
    print_header "Deployment Status"

    echo "Agent Pod:"
    kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "Agent Service:"
    kubectl get svc -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "Monitoring Pods:"
    kubectl get pods -n $NAMESPACE -l "app in (prometheus,grafana,otel-collector,jaeger)"
}

# Rollout - restart deployment to pick up new secrets/config
rollout() {
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
    setup_agent_config

    # Rebuild if requested
    if [ "$rebuild" = true ]; then
        print_info "Rebuilding Docker image..."
        docker_build

        if command -v kind &> /dev/null; then
            print_info "Loading image into kind cluster..."
            kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
            print_success "Image loaded"
        fi
    fi

    # Apply k8-deployment.yaml to pick up ConfigMap changes
    print_info "Applying k8-deployment.yaml..."
    kubectl apply -f k8-deployment.yaml

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

# Clean up agent only
clean() {
    print_header "Cleaning Up Agent"

    print_info "Removing agent deployment..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found
    print_success "Agent cleanup complete"
}

# Rebuild with no-cache and redeploy
# This ensures fresh local modules are used
rebuild() {
    print_header "Rebuilding with Fresh Dependencies"

    load_env

    # Build Docker image with --no-cache
    print_info "Building Docker image with --no-cache..."
    DOCKER_NO_CACHE=true docker_build

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        # Detect Kind cluster name from current kubectl context
        local context=$(kubectl config current-context 2>/dev/null)
        local cluster_name=""

        if [[ "$context" == kind-* ]]; then
            cluster_name="${context#kind-}"
        elif kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
            cluster_name="$CLUSTER_NAME"
        else
            cluster_name=$(kind get clusters 2>/dev/null | head -1)
        fi

        if [ -n "$cluster_name" ]; then
            print_info "Loading image into kind cluster '$cluster_name'..."
            kind load docker-image $APP_NAME:latest --name "$cluster_name"
            print_success "Image loaded"
        fi
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config from .env
    if type setup_api_keys &>/dev/null; then
        setup_api_keys
    fi
    if type setup_agent_config &>/dev/null; then
        setup_agent_config
    fi

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    # Restart deployment to pick up new image
    print_info "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME rebuilt and deployed with fresh local modules!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

# Clean up everything including cluster
clean_all() {
    print_header "Cleaning Up Everything"

    # Kill port forwards
    pkill -f "kubectl.*port-forward.*$NAMESPACE" 2>/dev/null || true

    # Delete agent
    kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true

    # Delete Kind cluster
    truvag3_delete_cluster

    print_success "Full cleanup complete"
}

# Show help
show_help() {
    echo "Agent with Telemetry Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Local Development Commands:"
    echo "  build         Build the agent binary"
    echo "  run           Build and run the agent locally"
    echo ""
    echo "Kubernetes Cluster Commands:"
    echo "  cluster       Create Kind cluster with port mappings"
    echo "  infra         Setup monitoring infrastructure (Prometheus, Grafana, Jaeger)"
    echo "  full-deploy   Complete deployment: cluster + infra + agent + port forwards"
    echo ""
    echo "Kubernetes Deployment Commands:"
    echo "  docker-build  Build Docker image using local workspace modules"
    echo "  deploy        Build, load, and deploy to Kubernetes"
    echo "  rebuild       Rebuild with --no-cache and redeploy (fresh local modules)"
    echo "  test          Run test requests against deployed agent"
    echo "  forward       Port forward the agent service only"
    echo "  forward-all   Port forward agent + monitoring dashboards"
    echo "  logs          View agent logs"
    echo "  status        Check deployment status"
    echo "  rollout       Restart deployment to pick up new secrets/config"
    echo "                Use --build flag to rebuild Docker image first"
    echo "  clean         Remove agent deployment only"
    echo "  clean-all     Delete Kind cluster and all resources"
    echo "  help          Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  REDIS_URL         Redis connection URL (required for run)"
    echo "  PORT              HTTP server port (default: 8355)"
    echo "  OPENAI_API_KEY    OpenAI API key (optional)"
    echo "  ANTHROPIC_API_KEY Anthropic API key (optional)"
    echo "  GROQ_API_KEY      Groq API key (optional)"
    echo ""
    echo "Examples:"
    echo "  ./setup.sh full-deploy    # One-click full deployment"
    echo "  ./setup.sh deploy         # Deploy to existing cluster"
    echo "  ./setup.sh forward-all    # Access all dashboards"
    echo "  ./setup.sh test           # Run tests"
    echo "  REDIS_URL=redis://localhost:6379 ./setup.sh run"
}

# Main entry point
case "${1:-help}" in
    build)
        build
        ;;
    run)
        run
        ;;
    docker-build)
        docker_build
        ;;
    cluster)
        cluster
        ;;
    infra)
        infra
        ;;
    deploy)
        deploy
        ;;
    rebuild)
        rebuild
        ;;
    full-deploy)
        full_deploy
        ;;
    verify)
        verify_ingress
        ;;
    test)
        test
        ;;
    forward)
        forward
        ;;
    forward-all)
        forward_all
        ;;
    logs)
        logs
        ;;
    status)
        status
        ;;
    rollout)
        rollout "$@"
        ;;
    clean)
        clean
        ;;
    clean-all)
        clean_all
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        print_error "Unknown command: $1"
        show_help
        exit 1
        ;;
esac
