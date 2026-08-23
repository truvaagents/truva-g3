#!/bin/bash
# Event-Driven Agent Setup Script
# Provides commands for building, running, and deploying the event-driven agent with AlertManager

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
APP_NAME="event-driven-agent"
MOCK_SERVICE_DIR="$EXAMPLES_DIR/mock-services/product-catalog-api"
PORT=${PORT:-8372}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}

print_header() {
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  Event-Driven Agent - $1${NC}"
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

# Preserve deployment feature flags from .env while preventing local-process
# endpoints from overriding the shared infrastructure's in-cluster addresses.
setup_shared_infra() {
    (
        unset REDIS_URL OTEL_EXPORTER_OTLP_ENDPOINT
        truvag3_setup_infra
    )
}

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
    GOWORK=off go build -o event-driven-agent .

    print_success "Build completed: event-driven-agent"
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

    print_info "Starting event-driven-agent on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    ./event-driven-agent
}

# Build Docker image (using local workspace modules)
# Usage: docker_build [--no-cache]
docker_build() {
    print_header "Building Docker Image"

    local no_cache_flag=""
    if [ "$DOCKER_NO_CACHE" = "true" ] || [ "$1" = "--no-cache" ]; then
        print_info "Building with --no-cache (fresh build)"
        no_cache_flag="--no-cache"
    fi

    print_info "Building with Dockerfile.workspace (using local modules)..."

    # Build from truvag3 root using Dockerfile.workspace (local modules)
    cd "$TRUVAG3_ROOT"
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" build $no_cache_flag \
        -f examples/event-driven-agent/Dockerfile.workspace \
        -t $APP_NAME:latest .
    cd "$SCRIPT_DIR"

    print_success "Docker image built: $APP_NAME:latest (from local workspace)"
}

# Create Kind cluster with port mappings for monitoring
cluster() { truvag3_create_cluster; }

# Setup monitoring infrastructure
infra() { load_env; setup_shared_infra; }

# Deploy AlertManager (config + deployment + service)
deploy_alertmanager() {
    print_info "Deploying AlertManager..."
    kubectl apply -f "$SCRIPT_DIR/alertmanager-config.yaml"
    kubectl apply -f "$SCRIPT_DIR/alertmanager.yaml"
    kubectl rollout status deployment/alertmanager -n $NAMESPACE --timeout=60s
    print_success "AlertManager deployed"
}

# Setup API keys as Kubernetes secrets (delegates to shared library)
setup_api_keys() {
    truvag3_create_secret "ai-provider-keys-event-agent" "$NAMESPACE"
}

# Setup agent configuration from .env as ConfigMap (delegates to shared library)
setup_agent_config() {
    truvag3_create_configmap "event-driven-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
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
    prepare_skills

    print_info "Waiting for any existing deployment..."
    kubectl wait --for=condition=available --timeout=30s deployment/$APP_NAME -n $NAMESPACE 2>/dev/null || true

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml

    # Deploy AlertManager
    deploy_alertmanager

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

# Full deployment: cluster + infrastructure + agent
full_deploy() {
    print_header "Full Deployment"

    check_prerequisites

    # truvag3_load_env auto-bootstraps .env from .env.example on fresh
    # checkouts, so no manual cp is needed here.
    load_env

    # Step 1: Create Kind cluster
    cluster

    # Step 2: Setup monitoring infrastructure (includes AlertManager)
    infra

    # Step 3: Deploy agent
    deploy

    # Step 4: Deploy product-catalog-api mock service (for E2E testing)
    if [ -f "$MOCK_SERVICE_DIR/setup.sh" ]; then
        mock_service "" deploy
    else
        print_info "Skipping product-catalog-api (not found at $MOCK_SERVICE_DIR)"
    fi

    # Step 5: Verify ingress is reachable
    verify_ingress
}

# Verify ingress reachability
verify_ingress() {
    truvag3_verify_ingress \
        "event-driven-agent.localhost" "alertmanager.localhost" \
        "grafana.localhost" "jaeger.localhost" || true
}

# Run tests
test() {
    print_header "Running Tests"

    # Start port forward in background
    print_info "Starting port forward..."
    kubectl port-forward -n $NAMESPACE svc/$APP_NAME $PORT:80 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    # Test health endpoint
    echo "Testing health endpoint..."
    if curl -s http://localhost:$PORT/health | grep -q "healthy"; then
        print_success "Health check passed"
    else
        print_error "Health check failed"
    fi

    # Test capabilities
    echo "Testing capabilities endpoint..."
    if curl -s http://localhost:$PORT/api/capabilities | grep -q "capabilities"; then
        print_success "Capabilities endpoint working"
    else
        print_error "Capabilities endpoint not responding"
    fi

    # Test manual trigger (agent-specific capability)
    echo ""
    print_info "Testing manual trigger..."
    curl -s -X POST http://localhost:$PORT/trigger \
        -H "Content-Type: application/json" \
        -d '{
            "alertname": "TruvaG3ComponentDown",
            "severity": "critical",
            "instance": "stock-market-tool-xyz:8348",
            "summary": "TruvaG3 component truvag3-tools is down"
        }' | jq . 2>/dev/null || echo "(install jq for pretty output)"

    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

# Port forward for agent only (background)
forward() {
    truvag3_forward "event-driven-agent-service" 8372 80
}

# Port forward for agent and monitoring
forward_all() {
    truvag3_forward_all \
        "event-driven-agent-service:8372:80" \
        "alertmanager:9093:9093" \
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
    echo "AlertManager Pod:"
    kubectl get pods -n $NAMESPACE -l app=alertmanager
    echo ""
    echo "Product Catalog API Pod:"
    kubectl get pods -n $NAMESPACE -l app=product-catalog-api 2>/dev/null || echo "  Not deployed"
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
    prepare_skills

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

# Deploy/manage product-catalog-api mock service for E2E testing
mock_service() {
    local action="${2:-deploy}"

    case "$action" in
        deploy)
            print_header "Deploying Product Catalog API"
            if [ ! -f "$MOCK_SERVICE_DIR/setup.sh" ]; then
                print_error "product-catalog-api not found at $MOCK_SERVICE_DIR"
                exit 1
            fi
            "$MOCK_SERVICE_DIR/setup.sh" deploy
            print_success "Product Catalog API deployed"
            print_info "Trigger degradation: ./setup.sh mock-service degrade"
            print_info "Recover: ./setup.sh mock-service recover"
            ;;
        rebuild)
            print_header "Rebuilding Product Catalog API"
            "$MOCK_SERVICE_DIR/setup.sh" rebuild
            ;;
        degrade)
            print_header "Triggering Service Degradation"
            "$MOCK_SERVICE_DIR/setup.sh" degrade
            print_info "P90 latency jumps to 1.2-2s, memory ramps +2MB/sec"
            print_info "Alert fires in ~60-90 seconds (1m for + scrape alignment)"
            print_info "Watch: Prometheus → AlertManager → event-driven-agent → HITL gate"
            ;;
        recover)
            print_header "Recovering Service"
            "$MOCK_SERVICE_DIR/setup.sh" recover
            ;;
        status)
            "$MOCK_SERVICE_DIR/setup.sh" status
            ;;
        logs)
            "$MOCK_SERVICE_DIR/setup.sh" logs
            ;;
        normal-load)
            "$MOCK_SERVICE_DIR/setup.sh" normal-load
            ;;
        heavy-load)
            "$MOCK_SERVICE_DIR/setup.sh" heavy-load
            ;;
        clean)
            print_header "Cleaning Product Catalog API"
            "$MOCK_SERVICE_DIR/setup.sh" clean
            ;;
        *)
            echo "Usage: ./setup.sh mock-service {deploy|rebuild|degrade|recover|status|logs|normal-load|heavy-load|clean}"
            ;;
    esac
}

# Clean up agent only
clean() {
    print_header "Cleaning Up Agent"

    print_info "Removing agent deployment..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found

    print_info "Removing AlertManager..."
    kubectl delete -f "$SCRIPT_DIR/alertmanager.yaml" --ignore-not-found
    kubectl delete -f "$SCRIPT_DIR/alertmanager-config.yaml" --ignore-not-found

    # Clean up product-catalog-api if it exists
    if [ -f "$MOCK_SERVICE_DIR/k8-deployment.yaml" ]; then
        print_info "Removing product-catalog-api..."
        kubectl delete -f "$MOCK_SERVICE_DIR/k8-deployment.yaml" --ignore-not-found
    fi

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
    prepare_skills

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

# Deploy in split mode (separate API + Worker pods)
deploy_split() {
    print_header "Deploying in Split Mode (API + Worker)"

    load_env

    # Build Docker image
    DOCKER_NO_CACHE=true docker_build

    # Load image into kind cluster
    if command -v kind &> /dev/null; then
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
    prepare_skills

    # Remove embedded deployment if it exists
    print_info "Cleaning up embedded deployment (if exists)..."
    kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true

    # Apply split manifests
    print_info "Applying API deployment..."
    kubectl apply -f k8-deployment-api.yaml

    print_info "Applying Worker deployment..."
    kubectl apply -f k8-deployment-worker.yaml

    # Restart both deployments
    print_info "Restarting API deployment..."
    kubectl rollout restart deployment/event-driven-agent-api -n $NAMESPACE
    print_info "Restarting Worker deployment..."
    kubectl rollout restart deployment/event-driven-agent-worker -n $NAMESPACE

    # Wait for both
    print_info "Waiting for API deployment..."
    if kubectl rollout status deployment/event-driven-agent-api -n $NAMESPACE --timeout=120s; then
        print_success "API pod ready"
    else
        print_error "API deployment failed"
        kubectl logs -n $NAMESPACE -l app=event-driven-agent-api --tail=20
        exit 1
    fi

    print_info "Waiting for Worker deployment..."
    if kubectl rollout status deployment/event-driven-agent-worker -n $NAMESPACE --timeout=120s; then
        print_success "Worker pod ready"
    else
        print_error "Worker deployment failed"
        kubectl logs -n $NAMESPACE -l app=event-driven-agent-worker --tail=20
        exit 1
    fi

    print_success "Split mode deployed! API and Worker running as separate pods."
    print_info "API pod handles: AlertManager webhooks, HITL commands"
    print_info "Worker pod handles: Task processing, AI orchestration"
    print_info ""
    print_info "Check status:"
    print_info "  kubectl get pods -n $NAMESPACE -l component=api"
    print_info "  kubectl get pods -n $NAMESPACE -l component=worker"
}

# Switch back to embedded mode (single pod)
deploy_embedded() {
    print_header "Switching to Embedded Mode (Single Pod)"

    # Remove split deployments
    print_info "Cleaning up split deployments..."
    kubectl delete -f k8-deployment-api.yaml --ignore-not-found 2>/dev/null || true
    kubectl delete -f k8-deployment-worker.yaml --ignore-not-found 2>/dev/null || true

    # Deploy embedded
    rebuild
}

# Clean up everything including cluster
clean_all() {
    print_header "Cleaning Up Everything"

    # Kill port forwards
    pkill -f "kubectl.*port-forward.*$NAMESPACE" 2>/dev/null || true

    # Delete agent (embedded and split modes)
    kubectl delete -f k8-deployment.yaml --ignore-not-found 2>/dev/null || true
    kubectl delete -f k8-deployment-api.yaml --ignore-not-found 2>/dev/null || true
    kubectl delete -f k8-deployment-worker.yaml --ignore-not-found 2>/dev/null || true

    # Delete AlertManager
    kubectl delete -f "$SCRIPT_DIR/alertmanager.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$SCRIPT_DIR/alertmanager-config.yaml" --ignore-not-found 2>/dev/null || true

    # Delete product-catalog-api
    kubectl delete -f "$MOCK_SERVICE_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    truvag3_delete_cluster

    print_success "Full cleanup complete"
}

# Show help
show_help() {
    echo "Event-Driven Agent Setup Script"
    echo ""
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Local Development Commands:"
    echo "  build         Build the agent binary"
    echo "  run           Build and run the agent locally"
    echo ""
    echo "Kubernetes Cluster Commands:"
    echo "  cluster       Create Kind cluster with port mappings"
    echo "  infra         Setup monitoring infrastructure (Prometheus, Grafana, Jaeger, AlertManager)"
    echo "  full-deploy   Complete deployment: cluster + infra + agent + port forwards"
    echo ""
    echo "Kubernetes Deployment Commands:"
    echo "  docker-build    Build Docker image using local workspace modules"
    echo "  deploy          Build, load, and deploy to Kubernetes (includes AlertManager)"
    echo "  rebuild         Rebuild with --no-cache and redeploy (fresh local modules)"
    echo "  skills-check    Compare published skill packages with Git"
    echo "  skills-sync     Reconcile and verify skill packages without restarting"
    echo "  deploy-split    Deploy in split mode (separate API + Worker pods)"
    echo "  deploy-embedded Switch back to embedded mode (single pod)"
    echo "  test          Run test requests against deployed agent"
    echo "  forward       Port forward the agent service only"
    echo "  forward-all   Port forward agent + AlertManager + monitoring dashboards"
    echo "  logs          View agent logs"
    echo "  status        Check deployment status"
    echo "  rollout       Restart deployment to pick up new secrets/config"
    echo "                Use --build flag to rebuild Docker image first"
    echo ""
    echo "Mock Service (E2E Testing) Commands:"
    echo "  mock-service deploy     Deploy product-catalog-api"
    echo "  mock-service rebuild    Rebuild product-catalog-api from scratch"
    echo "  mock-service degrade    Trigger service degradation (alert → HITL flow)"
    echo "  mock-service recover    Recover service to normal"
    echo "  mock-service status     Show pod and service state"
    echo "  mock-service logs       Tail pod logs"
    echo "  mock-service normal-load  Baseline traffic ~3 req/s"
    echo "  mock-service heavy-load   Spike traffic ~30 req/s"
    echo "  mock-service clean      Remove deployment"
    echo ""
    echo "Cleanup Commands:"
    echo "  clean         Remove agent, AlertManager, and mock service deployments"
    echo "  clean-all     Delete Kind cluster and all resources"
    echo "  help          Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  REDIS_URL         Redis connection URL (required for run)"
    echo "  PORT              HTTP server port (default: 8372)"
    echo "  OPENAI_API_KEY    OpenAI API key (optional)"
    echo "  ANTHROPIC_API_KEY Anthropic API key (optional)"
    echo "  OPENROUTER_API_KEY OpenRouter API key (optional)"
    echo "  GROQ_API_KEY      Groq API key (optional)"
    echo ""
    echo "Examples:"
    echo "  ./setup.sh full-deploy              # One-click full deployment (includes product-catalog-api)"
    echo "  ./setup.sh deploy                   # Deploy to existing cluster"
    echo "  ./setup.sh forward-all              # Access all dashboards"
    echo "  ./setup.sh test                     # Run tests"
    echo "  ./setup.sh mock-service degrade     # Trigger E2E HITL flow"
    echo "  ./setup.sh mock-service recover     # Recover service"
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
    skills-check)
        load_env
        check_skills
        ;;
    skills-sync)
        load_env
        sync_skills
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
    deploy-split)
        deploy_split
        ;;
    deploy-embedded)
        deploy_embedded
        ;;
    mock-service)
        mock_service "$@"
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
