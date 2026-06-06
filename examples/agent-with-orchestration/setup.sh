#!/bin/bash

# setup.sh - One-click setup for travel-research-agent with orchestration
# This script sets up the local development environment and can deploy to Kubernetes

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="travel-research-agent"
AGENT_PORT=8353

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
    echo -e "${BLUE}║  TruvaG3 Travel Research Agent with Orchestration      ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Tool directories
GEOCODING_TOOL_DIR="$EXAMPLES_DIR/geocoding-tool"
WEATHER_TOOL_DIR="$EXAMPLES_DIR/weather-tool-v2"
CURRENCY_TOOL_DIR="$EXAMPLES_DIR/currency-tool"
COUNTRY_INFO_TOOL_DIR="$EXAMPLES_DIR/country-info-tool"
NEWS_TOOL_DIR="$EXAMPLES_DIR/news-tool"
AGENT_DIR="$SCRIPT_DIR"

# Check prerequisites
check_prerequisites() {
    echo "Checking prerequisites..."

    # Check Go
    if ! command -v go &> /dev/null; then
        echo -e "${RED}Error: Go is not installed${NC}"
        echo "Please install Go 1.26+ from https://golang.org/dl/"
        exit 1
    fi
    echo -e "${GREEN}✓ Go installed: $(go version)${NC}"

    # Check Docker (optional)
    if command -v docker &> /dev/null; then
        echo -e "${GREEN}✓ Docker installed${NC}"
        DOCKER_AVAILABLE=true
    else
        echo -e "${YELLOW}! Docker not found (optional for local development)${NC}"
        DOCKER_AVAILABLE=false
    fi

    # Check kubectl (optional)
    if command -v kubectl &> /dev/null; then
        echo -e "${GREEN}✓ kubectl installed${NC}"
        KUBECTL_AVAILABLE=true
    else
        echo -e "${YELLOW}! kubectl not found (optional for K8s deployment)${NC}"
        KUBECTL_AVAILABLE=false
    fi

    # Check kind (optional for k8s)
    if command -v kind &> /dev/null; then
        echo -e "${GREEN}✓ Kind installed${NC}"
        KIND_AVAILABLE=true
    else
        echo -e "${YELLOW}! Kind not found (optional for K8s deployment)${NC}"
        KIND_AVAILABLE=false
    fi

    echo ""
}

# Create Kind cluster with port mappings (like agent-with-telemetry)
create_kind_cluster() { truvag3_create_cluster; }

# Setup monitoring infrastructure (Prometheus, Grafana, Jaeger, OTEL Collector)
setup_monitoring_infrastructure() { truvag3_setup_infra; }

# Setup Redis
setup_redis() {
    echo "Setting up Redis..."

    # Check if Redis is already running
    if command -v redis-cli &> /dev/null; then
        if redis-cli ping &> /dev/null; then
            echo -e "${GREEN}✓ Redis is already running${NC}"
            return 0
        fi
    fi

    # Try Docker Redis
    if [ "$DOCKER_AVAILABLE" = true ]; then
        echo "Starting Redis via Docker..."

        # Stop existing container if any
        docker stop truvag3-redis 2>/dev/null || true
        docker rm truvag3-redis 2>/dev/null || true

        # Start Redis
        docker run -d \
            --name truvag3-redis \
            -p 6379:6379 \
            redis:7-alpine

        echo -e "${GREEN}✓ Redis started on port 6379${NC}"
    else
        echo -e "${RED}Error: Redis not available${NC}"
        echo "Please install Redis or Docker to run Redis"
        echo ""
        echo "Options:"
        echo "  1. Install Redis: brew install redis && brew services start redis"
        echo "  2. Use Docker: docker run -d -p 6379:6379 redis:7-alpine"
        exit 1
    fi

    echo ""
}

# Check for API keys
check_api_keys() {
    local found_keys=""

    # Check OpenAI (priority: 1000)
    if [ -n "$OPENAI_API_KEY" ]; then
        found_keys="OpenAI (env)"
    elif [ -f .env ] && grep -q "^OPENAI_API_KEY=sk-" .env; then
        found_keys="OpenAI (.env)"
    fi

    # Check Anthropic (priority: 900)
    if [ -n "$ANTHROPIC_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Anthropic (env)"
    elif [ -f .env ] && grep -q "^ANTHROPIC_API_KEY=sk-ant-" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Anthropic (.env)"
    fi

    # Check Gemini (priority: 800)
    if [ -n "$GEMINI_API_KEY" ] || [ -n "$GOOGLE_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Gemini (env)"
    elif [ -f .env ] && (grep -q "^GEMINI_API_KEY=" .env || grep -q "^GOOGLE_API_KEY=" .env); then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Gemini (.env)"
    fi

    # Check Groq (priority: 700)
    if [ -n "$GROQ_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Groq (env)"
    elif [ -f .env ] && grep -q "^GROQ_API_KEY=gsk_" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Groq (.env)"
    fi

    # Check DeepSeek (priority: 600)
    if [ -n "$DEEPSEEK_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}DeepSeek (env)"
    elif [ -f .env ] && grep -q "^DEEPSEEK_API_KEY=" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}DeepSeek (.env)"
    fi

    # Check xAI (priority: 500)
    if [ -n "$XAI_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}xAI (env)"
    elif [ -f .env ] && grep -q "^XAI_API_KEY=" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}xAI (.env)"
    fi

    # Check Mistral (priority: 450)
    if [ -n "$MISTRAL_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Mistral (env)"
    elif [ -f .env ] && grep -q "^MISTRAL_API_KEY=" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Mistral (.env)"
    fi

    # Check Qwen (priority: 400)
    if [ -n "$QWEN_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Qwen (env)"
    elif [ -f .env ] && grep -q "^QWEN_API_KEY=" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Qwen (.env)"
    fi

    # Check Together AI (priority: 300)
    if [ -n "$TOGETHER_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Together (env)"
    elif [ -f .env ] && grep -q "^TOGETHER_API_KEY=" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Together (.env)"
    fi

    if [ -n "$found_keys" ]; then
        log_success "AI provider key(s) found: $found_keys"
        return 0
    else
        log_warn "No AI provider API keys configured"
        echo ""
        echo -e "${YELLOW}┌────────────────────────────────────────────────────────────┐${NC}"
        echo -e "${YELLOW}│  AI Features Require an API Key                            │${NC}"
        echo -e "${YELLOW}├────────────────────────────────────────────────────────────┤${NC}"
        echo -e "${YELLOW}│  Without an API key, predefined workflows will work but    │${NC}"
        echo -e "${YELLOW}│  natural language orchestration will be limited.           │${NC}"
        echo -e "${YELLOW}│                                                            │${NC}"
        echo -e "${YELLOW}│  Configure at least ONE provider in your .env file:        │${NC}"
        echo -e "${YELLOW}│                                                            │${NC}"
        echo -e "${YELLOW}│    OPENAI_API_KEY=sk-your-key                              │${NC}"
        echo -e "${YELLOW}│    ANTHROPIC_API_KEY=sk-ant-your-key                       │${NC}"
        echo -e "${YELLOW}│    GROQ_API_KEY=gsk_your-key                               │${NC}"
        echo -e "${YELLOW}│                                                            │${NC}"
        echo -e "${YELLOW}│  Multiple providers enable automatic failover.             │${NC}"
        echo -e "${YELLOW}└────────────────────────────────────────────────────────────┘${NC}"
        echo ""
        return 1
    fi
}

# Create .env file
setup_env() {
    echo "Setting up environment..."

    # Auto-bootstrap and source .env (.env.example → .env on fresh checkouts)
    # is handled by truvag3_load_env in setup-env-lib.sh.
    load_env

    # Check for API keys
    check_api_keys || true

    echo ""
}

# Build the application (local only)
build_app() {
    echo "Building application..."

    # Download dependencies
    GOWORK=off go mod download
    GOWORK=off go mod tidy

    # Build
    GOWORK=off go build -o travel-research-agent .

    echo -e "${GREEN}✓ Application built successfully${NC}"
    echo ""
}

# Build all travel tools locally
build_tools() {
    log_info "Building travel tools..."

    local tools=("geocoding-tool" "weather-tool-v2" "currency-tool" "country-info-tool" "news-tool")

    for tool in "${tools[@]}"; do
        local tool_dir="$EXAMPLES_DIR/$tool"
        if [ -d "$tool_dir" ]; then
            log_info "Building $tool..."
            (cd "$tool_dir" && GOWORK=off go build -o "$tool" . 2>/dev/null) && log_success "$tool built" || log_warn "$tool build failed (may not exist yet)"
        else
            log_warn "$tool directory not found"
        fi
    done
}

# Build Docker images (using local workspace modules)
# Set DOCKER_NO_CACHE=true to rebuild with fresh dependencies
build_docker() {
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

# Load images to Kind
load_to_kind() { truvag3_load_to_kind "$APP_NAME:latest"; }

# Load environment variables from .env file
load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

# Setup API keys as Kubernetes secrets
setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-travel-agent" "$NAMESPACE"
}

# Setup agent configuration from .env as ConfigMap
setup_agent_config() {
    truvag3_create_configmap "travel-research-agent-env-config" "$NAMESPACE" "$AGENT_DIR/.env"
}

# Deploy to Kubernetes
deploy_k8s() {
    log_info "Deploying to Kubernetes..."

    # Load environment and setup secrets
    load_env

    # Create namespace if not exists
    kubectl create namespace truvag3-examples --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config
    setup_k8s_secrets
    setup_agent_config

    # Deploy the agent
    kubectl apply -f "$AGENT_DIR/k8-deployment.yaml"
    log_success "travel-research-agent deployed"

    # Force rollout to pick up new image (needed when using :latest tag)
    log_info "Rolling out new version..."
    kubectl rollout restart deployment/travel-research-agent -n truvag3-examples
    kubectl rollout status deployment/travel-research-agent -n truvag3-examples --timeout=120s

    log_info "Waiting for pods to be ready..."
    kubectl wait --for=condition=ready pod -l app=travel-research-agent -n truvag3-examples --timeout=120s 2>/dev/null || true

    log_success "Deployment complete!"
    log_info "Run '$0 forward' to set up port forwards"
}

# Port forward (agent only)
# Port forward with auto-reconnect
port_forward() {
    truvag3_forward "travel-research-agent-service" 8353 80
}

# Port forward with monitoring and auto-reconnect
port_forward_all() {
    truvag3_forward_all \
        "travel-research-agent-service:8353:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

verify_ingress() {
    truvag3_verify_ingress \
        "orchestration-agent.localhost" \
        "grafana.localhost" "prometheus.localhost" "jaeger.localhost" || true
}

# Print summary after deployment
print_summary() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Setup Complete! 🎉                              ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "Your Travel Research Agent with Orchestration is now running!"
    echo ""
    echo -e "${BLUE}🚀 Agent Endpoint:${NC}"
    echo "  http://localhost:$AGENT_PORT/health"
    echo ""
    echo -e "${BLUE}📊 Monitoring Dashboards:${NC}"
    echo "  Grafana:    http://localhost:3000 (admin/admin)"
    echo "  Prometheus: http://localhost:9090"
    echo "  Jaeger:     http://localhost:16686"
    echo ""
    echo -e "${BLUE}🧪 Test the orchestration:${NC}"
    echo "  # List available workflows"
    echo "  curl http://localhost:$AGENT_PORT/orchestrate/workflows | jq ."
    echo ""
    echo "  # Execute travel research workflow"
    echo "  curl -X POST http://localhost:$AGENT_PORT/orchestrate/travel-research \\"
    echo "    -H \"Content-Type: application/json\" \\"
    echo "    -d '{\"destination\": \"Tokyo, Japan\", \"country\": \"Japan\", \"base_currency\": \"USD\", \"amount\": 1000}'"
    echo ""
    echo -e "${BLUE}📈 View telemetry:${NC}"
    echo "  1. Open Grafana: http://localhost:3000"
    echo "  2. Traces in Jaeger: http://localhost:16686"
    echo "  3. Metrics in Prometheus: http://localhost:9090"
    echo ""
    echo -e "${BLUE}🔧 Useful commands:${NC}"
    echo "  kubectl get pods -n $NAMESPACE"
    echo "  kubectl logs -n $NAMESPACE -l app=$APP_NAME -f"
    echo "  $0 test            - Run orchestration test"
    echo "  $0 cleanup         - Delete everything"
    echo ""
    echo -e "${YELLOW}💡 Port forwards are running in the background${NC}"
    echo "   To stop them: pkill -f 'kubectl.*port-forward.*$NAMESPACE'"
}

# Test orchestration
test_orchestration() {
    log_info "Running orchestration test..."
    echo ""

    # Test list workflows
    log_info "Step 1: List available workflows"
    curl -s http://localhost:8353/orchestrate/workflows | jq . 2>/dev/null || echo "Request sent"
    echo ""

    # Test discover tools
    log_info "Step 2: Discover available tools"
    curl -s http://localhost:8353/discover | jq '.discovery_summary' 2>/dev/null || echo "Request sent"
    echo ""

    # Test health
    log_info "Step 3: Check health"
    curl -s http://localhost:8353/health | jq '{status, orchestrator, ai}' 2>/dev/null || echo "Request sent"
    echo ""

    # Test natural language (if AI is configured)
    log_info "Step 4: Test natural language orchestration"
    curl -s -X POST http://localhost:8353/orchestrate/natural \
        -H "Content-Type: application/json" \
        -d '{"request":"What is the weather like in Tokyo?","ai_synthesis":true}' | jq '{request_id, tools_used, confidence}' 2>/dev/null || echo "Request sent"
    echo ""

    log_success "Orchestration test complete!"
}

# Rollout - restart deployment to pick up new secrets/config from .env
rollout() {
    print_header
    log_info "Rolling out deployment..."

    local rebuild=false

    # Check for --build flag
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then
        rebuild=true
    fi

    # Load env to update secrets and config
    load_env

    # Update secrets and config from .env
    log_info "Updating secrets and config from .env..."
    setup_k8s_secrets
    setup_agent_config

    # Rebuild if requested
    if [ "$rebuild" = true ]; then
        log_info "Rebuilding Docker image..."
        build_docker

        if command -v kind &> /dev/null; then
            log_info "Loading image into kind cluster..."
            load_to_kind
            log_success "Image loaded"
        fi
    fi

    # Apply k8-deployment.yaml to pick up ConfigMap changes
    log_info "Applying k8-deployment.yaml..."
    kubectl apply -f "$AGENT_DIR/k8-deployment.yaml"

    # Restart deployment
    log_info "Restarting deployment..."
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE

    log_info "Waiting for rollout to complete..."
    if kubectl rollout status deployment/$APP_NAME -n $NAMESPACE --timeout=120s; then
        log_success "Rollout complete!"
    else
        log_error "Rollout failed"
        kubectl logs -n $NAMESPACE -l app=$APP_NAME --tail=20
        exit 1
    fi
}

# View logs
logs() {
    log_info "Viewing Logs"
    kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100
}

# Check status
status() {
    log_info "Deployment Status"
    echo ""
    echo "Pods:"
    kubectl get pods -n $NAMESPACE -l app=$APP_NAME
    echo ""
    echo "Service:"
    kubectl get svc -n $NAMESPACE -l app=$APP_NAME
}

# Cleanup
cleanup() {
    log_info "Cleaning up..."

    # Delete K8s resources
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    # Stop local Redis
    docker stop truvag3-redis 2>/dev/null || true
    docker rm truvag3-redis 2>/dev/null || true

    # Remove local binary
    rm -f "$SCRIPT_DIR/travel-research-agent"

    log_success "Cleanup complete"
}

# Check if a service is available (local or K8s port-forward)
check_service_available() {
    local port=$1
    local name=$2
    if nc -z localhost "$port" 2>/dev/null; then
        log_success "$name already available on port $port"
        return 0
    fi
    return 1
}

# Check if Redis is available (local, Docker, or K8s)
check_redis_available() {
    # Check local Redis
    if redis-cli ping 2>/dev/null | grep -q PONG; then
        log_success "Redis available (local)"
        export REDIS_URL="redis://localhost:6379"
        return 0
    fi

    # Check Docker Redis
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -q truvag3-redis; then
        log_success "Redis available (Docker: truvag3-redis)"
        export REDIS_URL="redis://localhost:6379"
        return 0
    fi

    # Check K8s Redis via port-forward or service
    if nc -z localhost 6379 2>/dev/null; then
        log_success "Redis available on port 6379 (existing connection)"
        export REDIS_URL="redis://localhost:6379"
        return 0
    fi

    return 1
}

# Ensure Redis is available, starting it only if needed
ensure_redis() {
    if check_redis_available; then
        return 0
    fi

    log_info "Redis not found, starting..."
    setup_redis
}

# Run the application
run_app() {
    echo "Starting Travel Research Agent with Orchestration..."
    echo ""
    echo "The agent will be available at: http://localhost:8353"
    echo ""
    echo "Endpoints:"
    echo "  POST /orchestrate/natural         - Natural language requests"
    echo "  POST /orchestrate/travel-research - Predefined travel workflow"
    echo "  POST /orchestrate/custom          - Custom workflow execution"
    echo "  GET  /orchestrate/workflows       - List available workflows"
    echo "  GET  /orchestrate/history         - Execution history"
    echo "  GET  /discover                        - Discover tools"
    echo "  GET  /health                          - Health with orchestrator status"
    echo ""
    echo "Press Ctrl+C to stop"
    echo "=============================================="
    echo ""

    # Load .env if exists
    if [ -f .env ]; then
        export $(cat .env | grep -v '^#' | xargs)
    fi

    # Set defaults if not set
    export REDIS_URL=${REDIS_URL:-"redis://localhost:6379"}
    export PORT=${PORT:-8353}

    ./travel-research-agent
}

# Run all components locally (smart - reuses existing infrastructure)
run_all() {
    log_info "Starting all components for local development..."
    echo ""

    # Track what we started (for cleanup on exit)
    local started_pids=()

    # Trap to cleanup background processes on exit
    cleanup_local() {
        echo ""
        log_info "Shutting down..."
        for pid in "${started_pids[@]}"; do
            kill "$pid" 2>/dev/null || true
        done
        log_success "All components stopped"
    }
    trap cleanup_local EXIT INT TERM

    # 1. Ensure Redis is available
    ensure_redis

    # 2. Load environment
    setup_env

    # 3. Build agent (tools should already be deployed)
    build_app

    # 4. Verify travel tools are available (port forward or check K8s)
    echo ""
    log_info "Checking for deployed travel tools..."
    local tools_available=0

    # Check if tools are accessible via K8s port-forwards
    for port in 8335 8339 8334 8338 8337; do
        if nc -z localhost "$port" 2>/dev/null; then
            tools_available=$((tools_available + 1))
        fi
    done

    if [ $tools_available -gt 0 ]; then
        log_success "Found $tools_available travel tools available"
    else
        log_warn "No travel tools found on expected ports"
        echo "  The agent will work but workflows may fail without tools"
        echo "  Deploy tools using: kubectl apply -f examples/*/k8-deployment.yaml"
    fi

    # 5. Run the agent in foreground
    run_app
}

# Main setup
main() {
    check_prerequisites
    setup_redis
    setup_env
    build_app

    echo "=============================================="
    echo "Setup complete!"
    echo "=============================================="
    echo ""
    echo "To start the agent:"
    echo "  ./setup.sh run"
    echo ""

    # Check for run argument
    if [ "$1" = "run" ]; then
        run_app
    fi
}

# Show help
show_help() {
    print_header
    cat << EOF
Usage: $0 <command>

Local Development Commands:
  setup      Setup the local development environment (default)
  run        Setup and run the agent only
  run-all    Build and run with smart tool detection (recommended)
             - Reuses existing Redis/services if available
             - Detects deployed travel tools
  redis      Setup Redis only
  build      Build the agent only
  tools      Build all travel tools locally

Kubernetes Cluster Commands:
  cluster        Create a Kind cluster with port mappings
  infra          Setup monitoring infrastructure (Prometheus, Grafana, Jaeger, OTEL)
  full-deploy    Complete deployment: cluster + infra + agent

Kubernetes Deployment Commands:
  docker         Build Docker images
  deploy         Build, load to Kind, and deploy to Kubernetes
  rebuild        Rebuild with --no-cache and redeploy (fresh dependencies)
  forward        Port forward agent only
  forward-all    Port forward agent + monitoring (recommended)
  logs           View agent logs (follows)
  status         Check deployment status (pods, services)
  test           Run the orchestration test scenario
  rollout        Restart deployment to pick up new secrets/config
                 Use --build flag to rebuild Docker image first
  cleanup        Remove deployed resources
  cleanup-all    Delete Kind cluster and all resources

Examples:
  # Quick local development
  $0 run-all          # Run with existing tools

  # Full Kubernetes deployment (recommended)
  $0 full-deploy      # Creates cluster, infrastructure, and deploys agent

  # Step-by-step deployment
  $0 cluster          # Create Kind cluster
  $0 infra            # Setup monitoring
  $0 deploy           # Deploy agent
  $0 forward-all      # Port forward everything

  # Test and observe
  $0 test             # Run orchestration test
  # Open Grafana: http://localhost:3000
  # Open Jaeger:  http://localhost:16686
EOF
}

# Full deployment: cluster + infrastructure + agent
full_deploy() {
    print_header
    log_info "Starting full deployment..."
    echo ""

    # Step 1: Create Kind cluster
    create_kind_cluster

    # Step 2: Setup monitoring infrastructure
    setup_monitoring_infrastructure

    # Step 3: Load environment for secrets
    load_env

    # Step 4: Build and deploy agent
    build_docker
    load_to_kind
    deploy_k8s

    # Step 5: Verify ingress is reachable
    verify_ingress

    echo "Deploy complete. Access at http://orchestration-agent.localhost"
}

# Cleanup everything including Kind cluster
cleanup_all() {
    log_info "Cleaning up everything..."

    cleanup

    truvag3_delete_cluster
    log_success "Full cleanup complete"
}

# Rebuild with no-cache and redeploy
rebuild() {
    log_info "Rebuilding with Fresh Dependencies"

    # Load environment for secrets
    load_env

    # Build Docker image with --no-cache
    log_info "Building Docker image with --no-cache..."
    DOCKER_NO_CACHE=true build_docker

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        local cluster_name=$(kubectl config current-context 2>/dev/null | sed 's/kind-//')
        if kind get clusters 2>/dev/null | grep -q "^${cluster_name}$"; then
            log_info "Loading image into kind cluster..."
            load_to_kind
            log_success "Image loaded"
        fi
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config from .env file
    setup_k8s_secrets
    setup_agent_config

    # Apply Kubernetes manifests
    log_info "Applying Kubernetes manifests..."
    kubectl apply -f "$AGENT_DIR/k8-deployment.yaml"

    # Restart deployment to pick up new image
    log_info "Restarting deployment..."
    kubectl rollout restart deployment/travel-research-agent -n $NAMESPACE

    log_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/travel-research-agent -n $NAMESPACE --timeout=120s; then
        log_success "travel-research-agent rebuilt and deployed with fresh dependencies!"
    else
        log_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=travel-research-agent --tail=20
        exit 1
    fi
}

# Handle arguments
case "${1:-setup}" in
    setup)
        main
        ;;
    run)
        main run
        ;;
    run-all)
        check_prerequisites
        run_all
        ;;
    redis)
        setup_redis
        ;;
    build)
        build_app
        ;;
    tools)
        check_prerequisites
        build_tools
        ;;
    cluster)
        check_prerequisites
        print_header
        truvag3_create_cluster
        ;;
    infra)
        check_prerequisites
        print_header
        truvag3_setup_infra
        ;;
    docker)
        check_prerequisites
        build_docker
        ;;
    deploy)
        check_prerequisites
        build_docker
        load_to_kind
        deploy_k8s
        ;;
    rebuild)
        check_prerequisites
        rebuild
        ;;
    full-deploy)
        check_prerequisites
        full_deploy
        ;;
    verify)
        verify_ingress
        ;;
    forward)
        port_forward
        ;;
    forward-all)
        port_forward_all
        ;;
    test)
        test_orchestration
        ;;
    logs)
        logs
        ;;
    status)
        status
        ;;
    cleanup)
        cleanup
        ;;
    rollout)
        rollout "$@"
        ;;
    cleanup-all)
        cleanup_all
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
