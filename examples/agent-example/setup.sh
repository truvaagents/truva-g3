#!/bin/bash

# Truva-G3 Agent Example - Standardized Setup Script
# One-click deployment with standardized cmd_* pattern

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
NC='\033[0m' # No Color

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="research-agent"
PORT=${PORT:-8350}
REDIS_URL=${REDIS_URL:-redis://redis.${NAMESPACE}:6379}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
K8_INFRA_DIR="$(cd "$SCRIPT_DIR/../k8-deployment" && pwd)"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Print functions
print_header() {
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║     Truva-G3 Agent Example Setup         ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
    echo ""
}

print_step() {
    echo -e "${BLUE}▶ $1${NC}"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

# Canonical log helpers (parity with other example setup scripts).
log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Load environment variables. truvag3_load_env auto-bootstraps .env from
# .env.example on fresh checkouts.
load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

# Check if command exists
check_command() {
    if ! command -v $1 &> /dev/null; then
        print_error "$1 is not installed"
        echo "Please install $1 and try again"
        echo "Installation guide: $2"
        exit 1
    fi
}

# Check prerequisites
check_prerequisites() {
    print_step "Checking prerequisites..."
    check_command "go" "https://go.dev/doc/install"
    check_command "docker" "https://docs.docker.com/get-docker/"
    check_command "kind" "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
    check_command "kubectl" "https://kubernetes.io/docs/tasks/tools/"
    print_success "All prerequisites installed"
    echo ""
}

#########################################
# COMMAND FUNCTIONS
#########################################

cmd_build() {
    print_header
    print_step "Building agent binary..."

    cd "$SCRIPT_DIR"
    go build -o "$APP_NAME" .

    print_success "Binary built: $SCRIPT_DIR/$APP_NAME"
    echo ""
}

cmd_run() {
    print_header
    load_env

    print_step "Building and running agent locally..."

    # Build first
    cd "$SCRIPT_DIR"
    go build -o "$APP_NAME" .
    print_success "Binary built"

    # Check for API keys
    if [ -z "$OPENAI_API_KEY" ] && [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$GROQ_API_KEY" ]; then
        print_warning "No AI API keys found in .env file"
        echo "Add at least one API key to enable AI features"
        echo ""
    fi

    # Run locally
    print_step "Starting agent on port $PORT..."
    echo ""
    echo -e "${GREEN}Agent running at: http://localhost:$PORT${NC}"
    echo -e "${GREEN}Health check: http://localhost:$PORT/health${NC}"
    echo ""

    export PORT="$PORT"
    export REDIS_URL="$REDIS_URL"

    "./$APP_NAME"
}

# Usage: cmd_docker_build [--no-cache]
cmd_docker_build() {
    print_header
    print_step "Building Docker image..."

    local no_cache_flag=""

    for arg in "$@"; do
        case "$arg" in
            --no-cache) no_cache_flag="--no-cache" ;;
        esac
    done

    if [ "$DOCKER_NO_CACHE" = "true" ]; then
        no_cache_flag="--no-cache"
    fi

    if [ -n "$no_cache_flag" ]; then
        print_step "Building with --no-cache (fresh dependency download)"
    fi

    print_step "Building with Dockerfile.workspace (using local modules)..."
    local truvag3_root="$(dirname "$(dirname "$SCRIPT_DIR")")"
    docker build $no_cache_flag -f "$SCRIPT_DIR/Dockerfile.workspace" -t "$APP_NAME:latest" "$truvag3_root"

    print_success "Docker image built: $APP_NAME:latest (from local workspace)"
    echo ""
}

cmd_cluster() {
    print_header
    print_step "Creating Kind cluster ($CLUSTER_NAME)..."

    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        print_success "Cluster $CLUSTER_NAME already exists"
    else
        cat <<EOF | kind create cluster --name $CLUSTER_NAME --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30350
    hostPort: 8350
    protocol: TCP
  - containerPort: 30000
    hostPort: 3000
    protocol: TCP
  - containerPort: 30909
    hostPort: 9090
    protocol: TCP
  - containerPort: 31686
    hostPort: 16686
    protocol: TCP
EOF
        print_success "Kind cluster created"
    fi

    kubectl config use-context kind-$CLUSTER_NAME
    print_success "Context switched to kind-$CLUSTER_NAME"
    echo ""
}

cmd_infra() {
    print_header
    print_step "Deploying infrastructure components..."
    echo ""

    if [ ! -f "$K8_INFRA_DIR/setup-infrastructure.sh" ]; then
        print_error "Infrastructure setup script not found at: $K8_INFRA_DIR/setup-infrastructure.sh"
        exit 1
    fi

    # Set namespace and run infrastructure setup
    export NAMESPACE="$NAMESPACE"
    bash "$K8_INFRA_DIR/setup-infrastructure.sh" setup

    print_success "Infrastructure deployment complete"
    echo ""
}

setup_api_keys() {
    truvag3_create_secret "ai-provider-keys-research-agent" "$NAMESPACE"
}

# Setup config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + agent-specific vars)
setup_config() {
    truvag3_create_configmap "research-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

cmd_deploy() {
    print_header
    check_prerequisites

    # Ensure namespace exists
    print_step "Ensuring namespace exists..."
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -n $NAMESPACE -f -
    print_success "Namespace ready"
    echo ""

    # Setup API keys and config
    load_env
    setup_api_keys
    setup_config

    # Build Docker image
    cmd_docker_build
    echo ""

    # Load image into Kind
    print_step "Loading image into Kind cluster..."
    if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        print_error "Kind cluster not found. Run './setup.sh cluster' first"
        exit 1
    fi
    kind load docker-image "$APP_NAME:latest" --name $CLUSTER_NAME
    print_success "Image loaded into Kind"
    echo ""

    # Deploy to K8s
    print_step "Deploying agent to Kubernetes..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    print_step "Waiting for deployment to be ready..."
    if kubectl wait --for=condition=available --timeout=120s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null; then
        print_success "Agent deployed successfully!"
    else
        print_error "Deployment failed or timed out. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=30
        exit 1
    fi
    echo ""
}

cmd_full_deploy() {
    print_header
    echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  ONE-CLICK DEPLOYMENT - Full Stack Setup     ${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
    echo ""

    check_prerequisites

    # Step 1: Create cluster
    print_step "Step 1/4: Creating Kind cluster..."
    cmd_cluster

    # Step 2: Deploy infrastructure
    print_step "Step 2/4: Deploying infrastructure..."
    cmd_infra

    # Step 3: Deploy agent
    print_step "Step 3/4: Deploying agent..."
    cmd_deploy

    # Step 4: Port forward all services
    print_step "Step 4/4: Setting up port forwarding..."
    cmd_forward_all
}

cmd_test() {
    print_header
    print_step "Testing deployed agent..."
    echo ""

    # Check if agent is deployed
    if ! kubectl get deployment $APP_NAME -n $NAMESPACE &>/dev/null; then
        print_error "Agent not deployed. Run './setup.sh deploy' first"
        exit 1
    fi

    # Start port forward in background
    print_step "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/$APP_NAME-service 8350:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo -e "${BLUE}Testing health endpoint...${NC}"
    if curl -s http://localhost:8350/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi
    echo ""

    # Test capabilities
    echo -e "${BLUE}Testing capabilities endpoint...${NC}"
    if curl -s http://localhost:8350/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_warning "Capabilities endpoint not responding"
    fi
    echo ""

    # Kill port forward
    kill $PF_PID 2>/dev/null || true

    print_success "Tests complete"
    echo ""
}

# Port forward with auto-reconnect
cmd_forward() {
    print_step "Setting up agent port forward with auto-reconnect..."

    # Kill existing port forwards for agent
    pkill -f "port-forward.*$APP_NAME" 2>/dev/null || true

    sleep 1

    print_success "Port forward established:"
    echo "  - Agent: http://localhost:$PORT"
    echo ""
    echo "Available Endpoints:"
    echo "  POST /api/capabilities/research_topic    - Research a topic"
    echo "  POST /api/capabilities/analyze_data      - AI-powered data analysis"
    echo "  POST /api/capabilities/orchestrate_workflow - Execute multi-step workflows"
    echo "  GET  /api/capabilities                   - List capabilities with schemas"
    echo "  GET  /health                             - Health check"
    echo ""
    echo -e "${YELLOW}Port forwards have auto-reconnect enabled${NC}"
    echo "Press Ctrl+C to stop port forwards"
    echo ""

    # Auto-reconnect loop - restarts port forward if it dies (e.g., during rollout)
    while true; do
        kubectl port-forward -n $NAMESPACE svc/$APP_NAME-service $PORT:80 2>/dev/null
        exit_code=$?
        if [ $exit_code -eq 130 ] || [ $exit_code -eq 143 ]; then
            # SIGINT (130) or SIGTERM (143) - user cancelled
            print_step "Port forward stopped by user"
            break
        fi
        print_warning "Agent port forward disconnected (exit code: $exit_code), reconnecting in 3s..."
        sleep 3
    done
}

# Port forward all services with auto-reconnect
cmd_forward_all() {
    print_header
    print_step "Setting up port forwarding for all services with auto-reconnect..."

    echo ""
    echo -e "${GREEN}Starting port forwards...${NC}"
    echo ""

    # Only kill this agent's port forward (preserve shared services for other agents)
    pkill -f "port-forward.*$APP_NAME" 2>/dev/null || true
    sleep 1

    # Port forward Grafana - only if not already forwarded (stable, doesn't restart often)
    if kubectl get svc grafana -n $NAMESPACE &>/dev/null; then
        if ! nc -z localhost 3000 2>/dev/null; then
            kubectl port-forward -n $NAMESPACE svc/grafana 3000:80 >/dev/null 2>&1 &
            print_success "Grafana: http://localhost:3000"
        else
            print_success "Grafana: http://localhost:3000 (already forwarded, reusing)"
        fi
    else
        print_warning "Grafana service not found"
    fi

    # Port forward Prometheus - only if not already forwarded
    if kubectl get svc prometheus -n $NAMESPACE &>/dev/null; then
        if ! nc -z localhost 9090 2>/dev/null; then
            kubectl port-forward -n $NAMESPACE svc/prometheus 9090:9090 >/dev/null 2>&1 &
            print_success "Prometheus: http://localhost:9090"
        else
            print_success "Prometheus: http://localhost:9090 (already forwarded, reusing)"
        fi
    else
        print_warning "Prometheus service not found"
    fi

    # Port forward Jaeger - only if not already forwarded
    if kubectl get svc jaeger-query -n $NAMESPACE &>/dev/null; then
        if ! nc -z localhost 16686 2>/dev/null; then
            kubectl port-forward -n $NAMESPACE svc/jaeger-query 16686:80 >/dev/null 2>&1 &
            print_success "Jaeger: http://localhost:16686"
        else
            print_success "Jaeger: http://localhost:16686 (already forwarded, reusing)"
        fi
    else
        print_warning "Jaeger service not found"
    fi

    # Check if agent service exists
    if ! kubectl get svc $APP_NAME-service -n $NAMESPACE &>/dev/null; then
        print_warning "Agent service not found"
        echo ""
        echo -e "${YELLOW}Press Ctrl+C to stop all port forwards${NC}"
        wait
        return
    fi

    print_success "Agent: http://localhost:$PORT"

    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  All services are now accessible!             ${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${YELLOW}Port forwards have auto-reconnect enabled${NC}"
    echo -e "${YELLOW}Press Ctrl+C to stop all port forwards${NC}"
    echo ""

    # Auto-reconnect loop for agent port forward
    # Monitoring forwards are stable, but agent pod may restart during rollout
    while true; do
        kubectl port-forward -n $NAMESPACE svc/$APP_NAME-service $PORT:80 2>/dev/null
        exit_code=$?
        if [ $exit_code -eq 130 ] || [ $exit_code -eq 143 ]; then
            # SIGINT (130) or SIGTERM (143) - user cancelled
            print_step "Port forward stopped by user"
            break
        fi
        print_warning "Agent port forward disconnected (exit code: $exit_code), reconnecting in 3s..."
        sleep 3
    done
}

cmd_logs() {
    print_header

    if ! kubectl get deployment $APP_NAME -n $NAMESPACE &>/dev/null; then
        print_error "Agent not deployed"
        exit 1
    fi

    print_step "Streaming logs from $APP_NAME..."
    echo ""

    kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100
}

cmd_status() {
    print_header
    print_step "Checking deployment status..."
    echo ""

    # Check cluster
    echo -e "${BLUE}Cluster:${NC}"
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        print_success "Kind cluster '$CLUSTER_NAME' is running"
    else
        print_warning "Kind cluster '$CLUSTER_NAME' not found"
    fi
    echo ""

    # Check namespace
    echo -e "${BLUE}Namespace:${NC}"
    if kubectl get namespace $NAMESPACE &>/dev/null; then
        print_success "Namespace '$NAMESPACE' exists"
    else
        print_warning "Namespace '$NAMESPACE' not found"
    fi
    echo ""

    # Check deployments
    echo -e "${BLUE}Deployments in $NAMESPACE:${NC}"
    kubectl get deployments -n $NAMESPACE -o wide 2>/dev/null || echo "No deployments found"
    echo ""

    # Check services
    echo -e "${BLUE}Services in $NAMESPACE:${NC}"
    kubectl get services -n $NAMESPACE -o wide 2>/dev/null || echo "No services found"
    echo ""

    # Check pods
    echo -e "${BLUE}Pods in $NAMESPACE:${NC}"
    kubectl get pods -n $NAMESPACE -o wide 2>/dev/null || echo "No pods found"
    echo ""

    # Check agent specifically
    if kubectl get deployment $APP_NAME -n $NAMESPACE &>/dev/null; then
        echo -e "${BLUE}Agent Status:${NC}"
        kubectl get deployment $APP_NAME -n $NAMESPACE
        echo ""

        # Check if agent is ready
        READY=$(kubectl get deployment $APP_NAME -n $NAMESPACE -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)
        if [ "$READY" = "True" ]; then
            print_success "Agent is ready and healthy"
        else
            print_warning "Agent is not ready"
        fi
        echo ""
    fi
}

cmd_rollout() {
    print_header
    print_step "Rolling out deployment..."

    local rebuild=false

    # Check for --build flag
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then
        rebuild=true
    fi

    # Load env to update secrets
    load_env

    # Update secrets and config from .env
    print_step "Updating secrets and config from .env..."
    setup_api_keys
    setup_config

    # Rebuild if requested
    if [ "$rebuild" = true ]; then
        print_step "Rebuilding Docker image..."
        cmd_docker_build

        if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
            print_step "Loading image into kind cluster..."
            kind load docker-image "$APP_NAME:latest" --name $CLUSTER_NAME
            print_success "Image loaded"
        fi
    fi

    # Restart deployment
    print_step "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_step "Waiting for rollout to complete..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "Rollout complete!"
    else
        print_error "Rollout failed"
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
    echo ""
}

cleanup() {
    log_info "Cleaning up..."

    # Delete K8s resources
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    # Stop local Redis
    docker stop truvag3-redis 2>/dev/null || true
    docker rm truvag3-redis 2>/dev/null || true

    # Remove local binary
    rm -f "$SCRIPT_DIR/research-agent"

    log_success "Cleanup complete"
}

# Rebuild with no-cache and redeploy
# This ensures fresh dependencies are downloaded
cmd_rebuild() {
    print_header
    print_step "Rebuilding with Fresh Dependencies"

    load_env

    # Build Docker image with --no-cache
    print_step "Building Docker image with --no-cache..."
    DOCKER_NO_CACHE=true cmd_docker_build

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        print_step "Loading image into kind cluster..."
        kind load docker-image $APP_NAME:latest --name "$CLUSTER_NAME"
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup API keys and config if functions exist
    if type setup_api_keys &>/dev/null; then
        setup_api_keys
    fi
    if type setup_config &>/dev/null; then
        setup_config
    fi

    # Apply Kubernetes manifests
    print_step "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    # Restart deployment to pick up new image
    print_step "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    print_step "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        print_success "$APP_NAME rebuilt and deployed with fresh dependencies!"
    else
        print_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

cleanup_all() {
    log_info "Cleaning up everything..."

    cleanup

    truvag3_delete_cluster
    log_success "Full cleanup complete"
}

cmd_help() {
    print_header

    cat <<EOF
${GREEN}Truva-G3 Agent Example - Setup Script${NC}

${BLUE}Usage:${NC}
  ./setup.sh [COMMAND]

${BLUE}Build & Run Commands:${NC}
  ${YELLOW}build${NC}              Build agent binary locally
  ${YELLOW}run${NC}                Build and run agent locally (no K8s)
  ${YELLOW}docker-build${NC}       Build Docker image (uses workspace modules)

${BLUE}Infrastructure Commands:${NC}
  ${YELLOW}cluster${NC}            Create Kind cluster with port mappings
  ${YELLOW}infra${NC}              Deploy infrastructure (Redis, OTEL, Prometheus, Jaeger, Grafana)

${BLUE}Deployment Commands:${NC}
  ${YELLOW}deploy${NC}             Build, load image, and deploy agent to K8s
  ${YELLOW}rebuild${NC}            Rebuild with --no-cache and redeploy (fresh dependencies)
  ${YELLOW}full-deploy${NC}        ${GREEN}ONE-CLICK: cluster + infra + deploy + forward_all${NC}

${BLUE}Testing & Monitoring:${NC}
  ${YELLOW}test${NC}               Run health tests against deployed agent
  ${YELLOW}forward${NC}            Port forward agent only (8350)
  ${YELLOW}forward-all${NC}        Port forward all services (agent + monitoring)
  ${YELLOW}logs${NC}               View agent logs
  ${YELLOW}status${NC}             Check deployment status
  ${YELLOW}rollout${NC}            Restart deployment to pick up new secrets/config
                     Use --build flag to rebuild Docker image first

${BLUE}Cleanup Commands:${NC}
  ${YELLOW}cleanup${NC}            Remove deployed resources
  ${YELLOW}cleanup-all${NC}        Delete Kind cluster and all resources

${BLUE}Configuration:${NC}
  Cluster:     ${CLUSTER_NAME}
  Namespace:   ${NAMESPACE}
  App:         ${APP_NAME}
  Port:        ${PORT}
  Redis:       ${REDIS_URL}

${BLUE}Environment Variables:${NC}
  PORT            Agent port (default: 8350)
  REDIS_URL       Redis connection string

  ${GREEN}AI Provider Keys (set in .env file):${NC}
  OPENAI_API_KEY
  ANTHROPIC_API_KEY
  GROQ_API_KEY

${BLUE}Examples:${NC}
  ./setup.sh full-deploy    # Complete deployment (recommended)
  ./setup.sh run            # Run locally for development
  ./setup.sh status         # Check what's running
  ./setup.sh logs           # View agent logs
  ./setup.sh cleanup-all    # Delete everything

${BLUE}Port Mappings:${NC}
  8350   - Research Agent
  3000   - Grafana
  9090   - Prometheus
  16686  - Jaeger UI

${BLUE}Quick Start:${NC}
  1. Copy .env.example to .env and add API keys
  2. Run: ./setup.sh full-deploy
  3. Access agent at: http://localhost:8350

EOF
}

#########################################
# MAIN EXECUTION
#########################################

# Handle Ctrl+C
trap 'echo -e "\n${YELLOW}Operation interrupted${NC}"; exit 1' INT

# Parse command
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
    cleanup)
        cleanup
        ;;
    cleanup-all)
        cleanup_all
        ;;
    help|--help|-h)
        cmd_help
        ;;
    *)
        print_error "Unknown command: $1"
        echo ""
        cmd_help
        exit 1
        ;;
esac
