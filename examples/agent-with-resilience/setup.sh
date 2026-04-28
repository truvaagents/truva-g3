#!/bin/bash

# setup.sh - One-click setup for agent-with-resilience example
# This script sets up the local development environment and can deploy to Kubernetes

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Configuration
CLUSTER_NAME=${CLUSTER_NAME:-"truvag3-demo-$(whoami)"}
NAMESPACE="truvag3-examples"
APP_NAME="research-agent-resilience"
PORT=${PORT:-8354}
REDIS_URL=${REDIS_URL:-redis://localhost:6379}

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
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  Agent with Resilience - $1${NC}"
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

# Component directories (self-contained)
GROCERY_API_DIR="$EXAMPLES_DIR/mock-services/grocery-store-api"
GROCERY_TOOL_DIR="$EXAMPLES_DIR/grocery-tool"
AGENT_DIR="$SCRIPT_DIR"

# Check prerequisites
check_prerequisites() { truvag3_check_prerequisites; }

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
        echo -e "${YELLOW}│  Without an API key, the agent will still work but AI      │${NC}"
        echo -e "${YELLOW}│  capabilities (summarization, analysis) will be disabled.  │${NC}"
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

# Load .env file if it exists
load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

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
    GOWORK=off go build -o research-agent-resilience .

    echo -e "${GREEN}✓ Application built successfully${NC}"
    echo ""
}

# Build all components locally
build_all() {
    log_info "Building all components..."

    # Build grocery-store-api
    log_info "Building grocery-store-api..."
    if [ -d "$GROCERY_API_DIR" ]; then
        (cd "$GROCERY_API_DIR" && GOWORK=off go build -o grocery-store-api .)
        log_success "grocery-store-api built"
    else
        log_error "grocery-store-api not found at $GROCERY_API_DIR"
        exit 1
    fi

    # Build grocery-tool
    log_info "Building grocery-tool..."
    if [ -d "$GROCERY_TOOL_DIR" ]; then
        (cd "$GROCERY_TOOL_DIR" && GOWORK=off go build -o grocery-tool .)
        log_success "grocery-tool built"
    else
        log_warn "grocery-tool not found at $GROCERY_TOOL_DIR (may not be needed)"
    fi

    # Build agent
    log_info "Building research-agent-resilience..."
    (cd "$AGENT_DIR" && GOWORK=off go build -o research-agent-resilience .)
    log_success "research-agent-resilience built"
}

# Build Docker images
# Set DOCKER_NO_CACHE=true to rebuild with fresh dependencies
build_docker() {
    log_info "Building Docker images..."

    local no_cache_flag=""
    if [ "$DOCKER_NO_CACHE" = "true" ]; then
        log_info "Building with --no-cache (fresh dependency download)"
        no_cache_flag="--no-cache"
    fi

    local truvag3_root="$(dirname "$(dirname "$AGENT_DIR")")"

    # Build grocery-store-api (no local replace directives, uses local context)
    if [ -d "$GROCERY_API_DIR" ]; then
        docker build $no_cache_flag -t grocery-store-api:latest "$GROCERY_API_DIR"
        log_success "grocery-store-api:latest built"
    fi

    # Build grocery-tool with Dockerfile.workspace (has local replace directives)
    if [ -d "$GROCERY_TOOL_DIR" ]; then
        log_info "Building grocery-tool with Dockerfile.workspace (using local modules)..."
        docker build $no_cache_flag -f "$GROCERY_TOOL_DIR/Dockerfile.workspace" -t grocery-tool:latest "$truvag3_root"
        log_success "grocery-tool:latest built (from local workspace)"
    fi

    # Build agent with Dockerfile.workspace (has local replace directives)
    log_info "Building agent with Dockerfile.workspace (using local modules)..."
    docker build $no_cache_flag -f "$AGENT_DIR/Dockerfile.workspace" -t research-agent-resilience:latest "$truvag3_root"
    log_success "research-agent-resilience:latest built (from local workspace)"
}

# Load images to Kind
load_to_kind() {
    truvag3_load_to_kind "grocery-store-api:latest"
    truvag3_load_to_kind "grocery-tool:latest"
    truvag3_load_to_kind "research-agent-resilience:latest"
}

# Setup API keys as Kubernetes secrets
setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-resilience-agent" "$NAMESPACE"
}

# Setup agent config as Kubernetes ConfigMap (captures TRUVAG3_*, APP_ENV, DEV_MODE, + agent-specific vars)
setup_agent_config() {
    truvag3_create_configmap "research-agent-resilience-env-config" "$NAMESPACE" "$AGENT_DIR/.env"
}

# Deploy to Kubernetes
deploy_k8s() {
    log_info "Deploying to Kubernetes..."

    # Load environment and setup secrets
    load_env

    # Create namespace if not exists
    kubectl create namespace truvag3-examples --dry-run=client -o yaml | kubectl apply -n $NAMESPACE -f -

    # Setup secrets and config
    setup_k8s_secrets
    setup_agent_config

    # Deploy components
    if [ -f "$GROCERY_API_DIR/k8-deployment.yaml" ]; then
        kubectl apply -f "$GROCERY_API_DIR/k8-deployment.yaml"
        log_success "grocery-store-api deployed"
    fi

    if [ -f "$GROCERY_TOOL_DIR/k8-deployment.yaml" ]; then
        kubectl apply -f "$GROCERY_TOOL_DIR/k8-deployment.yaml"
        log_success "grocery-tool deployed"
    fi

    kubectl apply -f "$AGENT_DIR/k8-deployment.yaml"
    log_success "research-agent-resilience deployed"

    log_info "Waiting for pods to be ready..."
    kubectl wait --for=condition=ready pod -l app=grocery-store-api -n truvag3-examples --timeout=120s 2>/dev/null || true
    kubectl wait --for=condition=ready pod -l app=grocery-tool -n truvag3-examples --timeout=120s 2>/dev/null || true
    kubectl wait --for=condition=ready pod -l app=research-agent-resilience -n truvag3-examples --timeout=120s 2>/dev/null || true

    log_success "Deployment complete!"
    log_info "Run './setup.sh forward-all' to set up port forwards"
}


# Test resilience
test_resilience() {
    log_info "Running resilience test..."
    echo ""

    # Reset to normal mode
    log_info "Step 1: Reset to normal mode"
    curl -s -X POST http://localhost:8081/admin/reset | jq . 2>/dev/null || echo "Reset sent"
    echo ""

    # Test normal operation
    log_info "Step 2: Test normal operation"
    curl -s -X POST http://localhost:8354/api/capabilities/research_topic \
        -H "Content-Type: application/json" \
        -d '{"topic":"groceries","sources":["grocery-service"],"ai_synthesis":false}' | jq '{success_rate, partial}' 2>/dev/null || echo "Request sent"
    echo ""

    # Enable rate limiting
    log_info "Step 3: Enable rate limiting (429 after 1 request)"
    curl -s -X POST http://localhost:8081/admin/inject-error \
        -H "Content-Type: application/json" \
        -d '{"mode":"rate_limit","rate_limit_after":1}' | jq . 2>/dev/null || echo "Rate limit enabled"
    echo ""

    # Make failing requests
    log_info "Step 4: Make requests (should trigger retries and failures)"
    for i in 1 2 3; do
        echo "Request $i:"
        curl -s -X POST http://localhost:8354/api/capabilities/research_topic \
            -H "Content-Type: application/json" \
            -d '{"topic":"groceries","sources":["grocery-service"],"ai_synthesis":false}' | jq '{success_rate, partial}' 2>/dev/null || echo "Request sent"
    done
    echo ""

    # Check circuit breaker
    log_info "Step 5: Check circuit breaker status"
    curl -s http://localhost:8354/health | jq '.circuit_breakers["grocery-service"]' 2>/dev/null || echo "Health check sent"
    echo ""

    # Reset
    log_info "Step 6: Reset and recover"
    curl -s -X POST http://localhost:8081/admin/reset | jq . 2>/dev/null || echo "Reset sent"
    curl -s -X POST http://localhost:8354/api/capabilities/research_topic \
        -H "Content-Type: application/json" \
        -d '{"topic":"groceries","sources":["grocery-service"],"ai_synthesis":false}' | jq '{success_rate, partial}' 2>/dev/null || echo "Recovery request sent"

    log_success "Resilience test complete!"
}

# Cleanup
cleanup() {
    log_info "Cleaning up..."

    # Stop port forwards
    pkill -f "port-forward.*8081" 2>/dev/null || true
    pkill -f "port-forward.*8083" 2>/dev/null || true
    pkill -f "port-forward.*8354" 2>/dev/null || true

    # Delete K8s resources
    kubectl delete -f "$AGENT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$GROCERY_TOOL_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$GROCERY_API_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    # Stop local Redis
    docker stop truvag3-redis 2>/dev/null || true
    docker rm truvag3-redis 2>/dev/null || true

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
    echo "Starting Research Agent with Resilience..."
    echo ""
    echo "The agent will be available at: http://localhost:8354"
    echo ""
    echo "Endpoints:"
    echo "  POST /api/capabilities/research_topic - Resilient research"
    echo "  GET  /api/capabilities/discover_tools - Tool discovery"
    echo "  GET  /health                          - Health with CB states"
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
    export PORT=${PORT:-8354}

    ./research-agent-resilience
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

    # 3. Build all components
    build_all

    # 4. Start grocery-store-api if not already running
    if check_service_available 8081 "grocery-store-api"; then
        log_info "Using existing grocery-store-api on port 8081"
    else
        log_info "Starting grocery-store-api on port 8081..."
        (cd "$GROCERY_API_DIR" && PORT=8081 ./grocery-store-api) &
        started_pids+=($!)
        sleep 2
    fi

    # 5. Start grocery-tool if not already running
    if check_service_available 8083 "grocery-tool"; then
        log_info "Using existing grocery-tool on port 8083"
    else
        if [ -f "$GROCERY_TOOL_DIR/grocery-tool" ]; then
            log_info "Starting grocery-tool on port 8083..."
            (cd "$GROCERY_TOOL_DIR" && PORT=8083 REDIS_URL="${REDIS_URL:-redis://localhost:6379}" ./grocery-tool) &
            started_pids+=($!)
            sleep 2
        else
            log_warn "grocery-tool not found, skipping (tool discovery may be limited)"
        fi
    fi

    # 6. Verify services are up
    echo ""
    log_info "Service Status:"
    if nc -z localhost 8081 2>/dev/null; then
        echo "  ✓ grocery-store-api: http://localhost:8081"
    else
        echo "  ✗ grocery-store-api: NOT RUNNING"
    fi
    if nc -z localhost 8083 2>/dev/null; then
        echo "  ✓ grocery-tool:      http://localhost:8083"
    else
        echo "  - grocery-tool:      NOT RUNNING (optional)"
    fi
    echo "  → agent:             http://localhost:8354 (starting...)"
    echo ""

    # 7. Run the agent in foreground
    run_app
}

#############################################
# STANDARDIZED 1-CLICK DEPLOYMENT COMMANDS
#############################################

# Create Kind cluster with port mappings
cluster() { truvag3_create_cluster; }

# Setup infrastructure (Redis, OTEL, Prometheus, Jaeger, Grafana)
infra() { truvag3_setup_infra; }

# Build Docker image
docker_build() {
    print_header "Building Docker Image"

    build_docker

    print_success "Docker images built"
}

# Deploy to Kubernetes (standardized)
deploy() {
    print_header "Deploying to Kubernetes"

    load_env

    # Build Docker images first
    docker_build

    # Load images into kind cluster if available
    if command -v kind &> /dev/null; then
        print_info "Loading images into kind cluster..."
        load_to_kind
        print_success "Images loaded"
    fi

    # Deploy to Kubernetes
    deploy_k8s

    print_success "Deployment complete!"
    print_info "Run './setup.sh forward-all' to access services"
}

# Full deployment: cluster + infrastructure + agent
full_deploy() {
    cluster
    infra
    deploy
    verify_ingress
}

# Verify ingress reachability
verify_ingress() {
    truvag3_verify_ingress \
        "resilience-agent.localhost" \
        "grafana.localhost" "jaeger.localhost" || true
}

# Port forward for agent only (standardized)
forward() {
    truvag3_forward "research-agent-resilience-service" 8354 80
}

# Port forward for agent and monitoring
forward_all() {
    truvag3_forward_all \
        "research-agent-resilience-service:8354:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

# Run tests (standardized)
test() {
    print_header "Running Tests"

    # Use the existing test_resilience function
    test_resilience
}

# Clean up agent only (standardized)
clean() {
    print_header "Cleaning Up Agent"

    cleanup

    print_success "Agent cleanup complete"
}

# Clean up everything including cluster (standardized)
clean_all() {
    print_header "Cleaning Up Everything"

    # Kill port forwards
    pkill -f "kubectl.*port-forward.*$NAMESPACE" 2>/dev/null || true

    # Delete agent
    kubectl delete -f "$AGENT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$GROCERY_TOOL_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$GROCERY_API_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    # Stop local Redis
    docker stop truvag3-redis 2>/dev/null || true
    docker rm truvag3-redis 2>/dev/null || true

    # Delete Kind cluster
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        print_info "Deleting Kind cluster $CLUSTER_NAME..."
        kind delete cluster --name $CLUSTER_NAME
        print_success "Kind cluster deleted"
    fi

    print_success "Full cleanup complete"
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
    echo "Dependencies:"
    kubectl get pods -n $NAMESPACE -l "app in (grocery-store-api,grocery-tool)"
    echo ""
    echo "Infrastructure:"
    kubectl get pods -n $NAMESPACE -l "app in (redis,prometheus,grafana,otel-collector,jaeger)"
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
    setup_k8s_secrets
    setup_agent_config

    # Rebuild if requested
    if [ "$rebuild" = true ]; then
        print_info "Rebuilding Docker images..."
        build_docker

        if command -v kind &> /dev/null; then
            print_info "Loading images into kind cluster..."
            load_to_kind
            print_success "Images loaded"
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

# Rebuild with no-cache and redeploy
# This ensures fresh dependencies are downloaded
rebuild() {
    print_header "Rebuilding with Fresh Dependencies"

    load_env

    # Build Docker image with --no-cache
    print_info "Building Docker image with --no-cache..."
    DOCKER_NO_CACHE=true build_docker

    # Load image into kind cluster if available
    if command -v kind &> /dev/null; then
        print_info "Loading image into kind cluster..."
        load_to_kind
        print_success "Image loaded"
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup API keys if function exists
    if type setup_k8s_secrets &>/dev/null; then
        setup_k8s_secrets
    fi

    # Refresh ConfigMap so dev-default env vars (e.g. TRUVAG3_ENABLE_OPENAPI)
    # are available on the pod that comes up from the rebuilt image.
    setup_agent_config

    # Apply Kubernetes manifests
    print_info "Applying Kubernetes manifests..."
    kubectl apply -f "$AGENT_DIR/k8-deployment.yaml"

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

# Build the agent (standardized)
build() {
    print_header "Building Agent"

    build_app

    print_success "Build completed: research-agent-resilience"
}

# Run the agent locally (standardized)
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

    print_info "Starting research-agent-resilience on port $PORT..."
    print_info "Redis URL: $REDIS_URL"
    echo ""

    run_app
}

#############################################
# LEGACY COMMANDS (kept for compatibility)
#############################################

# Main setup (legacy)
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
    cat << EOF
Usage: $0 <command>

STANDARDIZED 1-CLICK DEPLOYMENT COMMANDS:
  cluster       Create Kind cluster with port mappings
  infra         Setup infrastructure (Redis, Prometheus, Grafana, Jaeger)
  full-deploy   ONE-CLICK: cluster + infra + deploy + port forwards
  deploy        Build Docker images and deploy to Kubernetes
  rebuild       Rebuild with --no-cache and redeploy (fresh dependencies)
  forward       Port forward the agent service only
  forward-all   Port forward agent + monitoring dashboards
  test          Run resilience test scenario
  rollout       Restart deployment to pick up new secrets/config
                Use --build flag to rebuild Docker image first
  clean         Remove agent deployment only
  clean-all     Delete Kind cluster and all resources

Local Development Commands:
  build         Build the agent binary
  run           Build and run the agent locally
  run-all       Build and run ALL components locally (recommended)
                - Reuses existing Redis/services if available
                - Starts grocery-store-api + grocery-tool + agent

Kubernetes Deployment Commands:
  docker-build  Build Docker images for all components
  logs          View agent logs
  status        Check deployment status

Legacy Commands:
  setup         Setup the local development environment (default)
  redis         Setup Redis only
  build-all     Build all components (agent + mock-services)
  docker        Build Docker images (alias for docker-build)
  forward       Set up port forwards (legacy - use forward-all)
  cleanup       Remove deployed resources (alias for clean)

Environment Variables:
  CLUSTER_NAME      Kind cluster name (default: truvag3-demo-\$(whoami))
  NAMESPACE         Kubernetes namespace (default: truvag3-examples)
  PORT              HTTP server port (default: 8354)
  REDIS_URL         Redis connection URL (default: redis://localhost:6379)
  OPENAI_API_KEY    OpenAI API key (optional)
  ANTHROPIC_API_KEY Anthropic API key (optional)
  GROQ_API_KEY      Groq API key (optional)

Examples:
  $0 full-deploy    # ONE-CLICK: Complete deployment with monitoring
  $0 cluster        # Create Kind cluster only
  $0 infra          # Setup infrastructure only
  $0 deploy         # Deploy to existing cluster
  $0 forward-all    # Access all dashboards
  $0 test           # Run resilience tests
  $0 clean-all      # Delete everything

  $0 run-all        # Quick start: run everything locally
  REDIS_URL=redis://localhost:6379 $0 run
EOF
}

# Handle arguments
case "${1:-help}" in
    # Standardized commands
    cluster)
        cluster
        ;;
    infra)
        infra
        ;;
    full-deploy)
        full_deploy
        ;;
    verify)
        verify_ingress
        ;;
    deploy)
        deploy
        ;;
    rebuild)
        rebuild
        ;;
    forward)
        forward
        ;;
    forward-all)
        forward_all
        ;;
    test)
        test
        ;;
    clean)
        clean
        ;;
    clean-all)
        clean_all
        ;;
    build)
        build
        ;;
    run)
        run
        ;;
    docker-build)
        docker_build
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
    # Legacy commands (kept for compatibility)
    setup)
        main
        ;;
    run-all)
        check_prerequisites
        run_all
        ;;
    redis)
        setup_redis
        ;;
    build-all)
        check_prerequisites
        build_all
        ;;
    docker)
        check_prerequisites
        build_docker
        ;;
    cleanup)
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
