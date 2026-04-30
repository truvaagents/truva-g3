#!/bin/bash

# setup.sh - One-click setup for my-streaming-agent
# This script sets up the local development environment and can deploy to Kubernetes
# Modeled after travel-chat-agent/setup.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="my-streaming-agent"
AGENT_PORT=8391  # fallback port-forward only

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
    echo -e "${BLUE}║     TruvaG3 My Streaming Agent                          ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Check prerequisites
# Delegate common functions to shared lib
check_prerequisites() { truvag3_check_prerequisites; }

# Setup Redis
setup_redis() {
    log_info "Setting up Redis..."

    # Check if Redis is already running
    if command -v redis-cli &> /dev/null; then
        if redis-cli ping &> /dev/null; then
            log_success "Redis is already running"
            return 0
        fi
    fi

    # Try Docker Redis
    if [ "$DOCKER_AVAILABLE" = true ]; then
        log_info "Starting Redis via Docker..."

        # Stop existing container if any
        docker stop truvag3-redis 2>/dev/null || true
        docker rm truvag3-redis 2>/dev/null || true

        # Start Redis
        docker run -d \
            --name truvag3-redis \
            -p 6379:6379 \
            redis:7-alpine

        log_success "Redis started on port 6379"
    else
        log_error "Redis not available"
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

    # Check Groq (priority: 700)
    if [ -n "$GROQ_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Groq (env)"
    elif [ -f .env ] && grep -q "^GROQ_API_KEY=gsk_" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}Groq (.env)"
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
    log_info "Setting up environment..."

    if [ ! -f .env ]; then
        echo "REDIS_URL=redis://localhost:6379" > .env
        echo "PORT=8391" >> .env
        echo "APP_ENV=development" >> .env
        log_success "Created default .env file"
    else
        log_success ".env file already exists"
    fi

    # Check for API keys
    check_api_keys || true

    echo ""
}

# Load environment variables from .env file
load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

# Build the application (local only)
build_app() {
    log_info "Building my-streaming-agent..."

    cd "$SCRIPT_DIR"

    # Download dependencies
    GOWORK=off go mod download
    GOWORK=off go mod tidy

    # Build
    GOWORK=off go build -o my-streaming-agent .

    log_success "Application built successfully"
    echo ""
}

# Build Docker images
build_docker() {
    local no_cache=""
    [ "${DOCKER_NO_CACHE:-}" = "true" ] && no_cache="--no-cache"
    truvag3_build_docker "my-streaming-agent:latest" \
        "$SCRIPT_DIR/Dockerfile.workspace" "$TRUVAG3_ROOT" $no_cache
}

load_to_kind() { truvag3_load_to_kind "my-streaming-agent:latest"; }

# Setup API keys as Kubernetes secrets
setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-my-streaming-agent" "$NAMESPACE"
}

# Setup agent configuration from .env as ConfigMap
setup_agent_config() {
    truvag3_create_configmap "my-streaming-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Deploy to Kubernetes
deploy_k8s() {
    log_info "Deploying to Kubernetes..."

    # Load environment and setup secrets
    load_env

    # Create namespace if not exists
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config
    setup_k8s_secrets
    setup_agent_config

    # Deploy the chat agent
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    log_success "my-streaming-agent deployed"

    # Force rollout to pick up new images
    log_info "Rolling out new versions..."
    kubectl rollout restart deployment/my-streaming-agent -n $NAMESPACE
    kubectl rollout status deployment/my-streaming-agent -n $NAMESPACE --timeout=120s

    log_info "Waiting for pods to be ready..."
    kubectl wait --for=condition=ready pod -l app=my-streaming-agent -n $NAMESPACE --timeout=120s 2>/dev/null || true

    log_success "Deployment complete!"
    log_info "Run '$0 forward' to set up port forwards"
}

# Print summary after deployment
print_summary() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Setup Complete!                                 ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "Your My Streaming Agent is now running!"
    echo ""
    echo -e "${BLUE}Chat Application:${NC}"
    echo "  Chat API:   http://my-streaming-agent.localhost/health"
    # TODO(developer): once you've added a card for this agent to
    # examples/chat-ui/dashboard.html, point users at the specific page
    # (e.g., http://chat.localhost/<your-page>.html) here.
    echo "  Chat UI:    http://chat.localhost  (click your agent's card on the dashboard)"
    echo ""
    echo -e "${BLUE}Monitoring Dashboards:${NC}"
    echo "  Grafana:    http://grafana.localhost (admin/admin)"
    echo "  Prometheus: http://prometheus.localhost"
    echo "  Jaeger:     http://jaeger.localhost"
    echo ""
    echo -e "${BLUE}Test the chat:${NC}"
    echo "  curl -X POST http://my-streaming-agent.localhost/chat/session | jq ."
    echo ""
    echo "  curl -N -X POST http://my-streaming-agent.localhost/chat/stream \"
    echo "    -H \"Content-Type: application/json\" \"
    echo "    -d '{\"message\": \"What is the cluster status?\"}'"
    echo ""
    echo -e "${BLUE}Useful commands:${NC}"
    echo "  kubectl get pods -n $NAMESPACE"
    echo "  kubectl logs -n $NAMESPACE -l app=my-streaming-agent -f"
    echo "  $0 test            - Run API tests"
    echo "  $0 cleanup         - Delete everything"
    echo ""
    echo -e "${BLUE}All services accessible via *.localhost (no port-forwarding needed)${NC}"
}

# Test the API
test_api() {
    local host="${1:-my-streaming-agent.localhost}"

    log_info "Testing my-streaming-agent at $host..."
    echo ""

    # Health check
    log_info "Step 1: Health check"
    curl -s "http://$host/health" | jq . 2>/dev/null || echo "Request sent"
    echo ""

    # Create session
    log_info "Step 2: Create session"
    SESSION_RESPONSE=$(curl -s -X POST "http://$host/chat/session")
    echo "$SESSION_RESPONSE" | jq . 2>/dev/null || echo "$SESSION_RESPONSE"
    SESSION_ID=$(echo "$SESSION_RESPONSE" | jq -r '.session_id' 2>/dev/null)
    echo ""

    if [ "$SESSION_ID" != "null" ] && [ -n "$SESSION_ID" ]; then
        log_info "Session created: $SESSION_ID"
        echo ""

        # Test streaming chat
        log_info "Step 3: Test SSE chat stream"
        echo "Sending: 'What is the cluster status?'"
        echo ""
        curl -N -X POST "http://$host/chat/stream" \
            -H "Content-Type: application/json" \
            -d "{\"session_id\": \"$SESSION_ID\", \"message\": \"What is the cluster status?\"}" 2>/dev/null || echo "Request sent"
        echo ""
    fi

    log_success "Test complete"
}

# Run the application locally
run_app() {
    log_info "Starting My Streaming Agent..."
    echo ""
    echo "The agent will be available at: http://localhost:$AGENT_PORT"
    echo ""
    echo "Endpoints:"
    echo "  POST /chat/stream           - SSE streaming chat"
    echo "  POST /chat/session          - Create session"
    echo "  GET  /chat/session/{id}     - Get session info"
    echo "  GET  /health                - Health check"
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
    export PORT=${PORT:-8391}

    ./my-streaming-agent
}

# Run with Redis setup
run_all() {
    log_info "Starting all components for local development..."
    echo ""

    # 1. Ensure Redis is available
    if ! redis-cli ping 2>/dev/null | grep -q PONG; then
        setup_redis
    else
        log_success "Redis already running"
    fi

    # 2. Load environment
    setup_env

    # 3. Build agent
    build_app

    # 4. Run the agent
    run_app
}

# Full deployment: cluster + infrastructure + agent
full_deploy() {
    print_header
    log_info "Starting full deployment..."
    echo ""

    # Load environment early so infra flags (e.g., TRUVAG3_DEPLOY_QDRANT) are
    # available. truvag3_load_env auto-bootstraps .env from .env.example on
    # fresh checkouts, so no manual cp is needed here.
    load_env

    truvag3_create_cluster
    truvag3_setup_infra

    # Step 4: Build and deploy
    build_docker
    load_to_kind
    deploy_k8s

    truvag3_verify_ingress "my-streaming-agent.localhost" "grafana.localhost" "jaeger.localhost" || true
    print_summary
}

# Rebuild with no-cache and redeploy
rebuild() {
    log_info "Rebuilding with Fresh Dependencies"

    # Build Docker images with --no-cache
    log_info "Building Docker images with --no-cache..."
    DOCKER_NO_CACHE=true build_docker

    # Load images into kind cluster if available
    if command -v kind &> /dev/null; then
        local cluster_name=$(kubectl config current-context 2>/dev/null | sed 's/kind-//')
        if kind get clusters 2>/dev/null | grep -q "^${cluster_name}$"; then
            log_info "Loading images into kind cluster..."
            load_to_kind
            log_success "Images loaded"
        fi
    fi

    # Create namespace
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Load env and setup secrets/config from .env file
    load_env
    setup_k8s_secrets
    setup_agent_config

    # Apply Kubernetes manifests
    log_info "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    # Restart deployments
    log_info "Restarting deployments..."
    kubectl rollout restart deployment/my-streaming-agent -n $NAMESPACE

    log_info "Waiting for deployments to be ready..."
    if kubectl rollout status deployment/my-streaming-agent -n $NAMESPACE --timeout=120s; then
        log_success "my-streaming-agent rebuilt and deployed!"
    else
        log_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=my-streaming-agent --tail=20
        exit 1
    fi
}

# Rollout - restart deployment to pick up new secrets/config from .env
rollout() {
    print_header
    log_info "Rolling out deployment..."

    # Load env to update secrets and config
    load_env

    # Update secrets and config from .env
    log_info "Updating secrets and config from .env..."
    setup_k8s_secrets
    setup_agent_config

    # Apply k8-deployment.yaml to pick up ConfigMap changes
    log_info "Applying k8-deployment.yaml..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    # Restart deployment
    log_info "Restarting deployment..."
    kubectl rollout restart deployment/my-streaming-agent -n $NAMESPACE

    log_info "Waiting for rollout to complete..."
    if kubectl rollout status deployment/my-streaming-agent -n $NAMESPACE --timeout=120s; then
        log_success "Rollout complete!"
    else
        log_error "Rollout failed"
        kubectl logs -n $NAMESPACE -l app=my-streaming-agent --tail=20
        exit 1
    fi
}

# Show logs
logs() {
    log_info "Showing logs for my-streaming-agent..."
    kubectl logs -n "$NAMESPACE" -l app=my-streaming-agent -f
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
    rm -f "$SCRIPT_DIR/my-streaming-agent"

    log_success "Cleanup complete"
}

# Cleanup everything including Kind cluster
cleanup_all() {
    log_info "Cleaning up everything..."

    cleanup

    truvag3_delete_cluster
    log_success "Full cleanup complete"
}

# Show help
show_help() {
    print_header
    cat << EOF
Usage: $0 <command>

Local Development Commands:
  setup      Setup the local development environment
  run        Build and run the agent locally
  run-all    Setup Redis, build, and run (recommended for local dev)
  build      Build the agent only
  redis      Setup Redis only

Kubernetes Cluster Commands:
  cluster        Create a Kind cluster with NGINX Ingress Controller
  infra          Setup monitoring infrastructure (Prometheus, Grafana, Jaeger, OTEL)
  full-deploy    Complete deployment: cluster + infra + agent (recommended)

Kubernetes Deployment Commands:
  docker         Build Docker images
  deploy         Build, load to Kind, and deploy to Kubernetes
  rebuild        Rebuild with --no-cache and redeploy (fresh dependencies)
  rollout        Restart deployment to pick up new secrets/config from .env
  forward        Port forward agent only
  forward-all    Port forward agent + monitoring (recommended)
  test           Run API tests
  logs           Show agent logs
  cleanup        Remove deployed resources
  cleanup-all    Delete Kind cluster and all resources

Examples:
  # Quick local development
  $0 run-all          # Setup Redis, build, and run locally

  # Full Kubernetes deployment (recommended)
  $0 full-deploy      # Creates cluster, infrastructure, deploys agent

  # Step-by-step deployment
  $0 cluster          # Create Kind cluster
  $0 infra            # Setup monitoring
  $0 docker           # Build Docker images
  $0 deploy           # Deploy to K8s
  $0 verify           # Verify ingress routes

  # Test the chat
  $0 test             # Run API tests

  # TODO(developer): replace with sample queries that exercise your agent's
  # capabilities and the tools it orchestrates. For example:
  #   "<sample query 1>"
  #   "<sample query 2>"
EOF
}

# Handle arguments
case "${1:-help}" in
    setup)
        check_prerequisites
        setup_env
        build_app
        log_success "Setup complete! Run '$0 run' to start the agent"
        ;;
    run)
        check_prerequisites
        build_app
        run_app
        ;;
    run-all)
        check_prerequisites
        run_all
        ;;
    build)
        check_prerequisites
        build_app
        ;;
    redis)
        check_prerequisites
        setup_redis
        ;;
    cluster)
        check_prerequisites
        print_header
        truvag3_create_cluster
        ;;
    infra)
        check_prerequisites
        print_header
        load_env
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
    rollout)
        check_prerequisites
        rollout
        ;;
    full-deploy)
        check_prerequisites
        full_deploy
        ;;
    forward)
        truvag3_forward "my-streaming-agent-service" $AGENT_PORT 80
        ;;
    forward-all)
        truvag3_forward_all \
            "my-streaming-agent-service:$AGENT_PORT:80" \
            "grafana:3000:80" \
            "prometheus:9090:9090" \
            "jaeger-query:16686:80"
        ;;
    test)
        test_api "${2:-my-streaming-agent.localhost}"
        ;;
    logs)
        logs
        ;;
    cleanup)
        cleanup
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
