#!/bin/bash

# setup.sh - One-click setup for travel-chat-agent and chat-ui
# This script sets up the local development environment and can deploy to Kubernetes
# Modeled after agent-with-orchestration/setup.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"
CHAT_UI_DIR="$EXAMPLES_DIR/chat-ui"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="travel-chat-agent"

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
    echo -e "${BLUE}║     TruvaG3 Travel Chat Agent + Chat UI                ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Delegate to shared lib (truvag3_check_prerequisites, truvag3_create_cluster, etc.)
# Local aliases for readability in this script
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
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true

        # Start Redis
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" run -d \
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
        echo "PORT=8356" >> .env
        echo "APP_ENV=development" >> .env
        log_success "Created default .env file"
    else
        log_success ".env file already exists"
    fi

    # Check for API keys
    check_api_keys || true

    echo ""
}

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

# Preserve deployment feature flags from .env while preventing local-process
# endpoints from overriding the shared infrastructure's in-cluster addresses.
setup_shared_infra() {
    (
        unset REDIS_URL OTEL_EXPORTER_OTLP_ENDPOINT
        truvag3_setup_infra
    )
}

# Build the application (local only)
build_app() {
    log_info "Building travel-chat-agent..."

    cd "$SCRIPT_DIR"

    # Download dependencies
    GOWORK=off go mod download
    GOWORK=off go mod tidy

    # Build
    GOWORK=off go build -o travel-chat-agent .

    log_success "Application built successfully"
    echo ""
}

# Build Docker images (chat agent and chat UI)
build_docker() {
    log_info "Building Docker images (using local workspace modules)..."
    local no_cache=""
    [ "${DOCKER_NO_CACHE:-}" = "true" ] && no_cache="--no-cache"

    truvag3_build_docker "travel-chat-agent:latest" \
        "$SCRIPT_DIR/Dockerfile.workspace" "$TRUVAG3_ROOT" $no_cache

    if [ -f "$CHAT_UI_DIR/Dockerfile" ]; then
        truvag3_build_docker "chat-ui:latest" \
            "$CHAT_UI_DIR/Dockerfile" "$CHAT_UI_DIR" $no_cache
    else
        log_warn "chat-ui Dockerfile not found"
    fi
}

# Load images to Kind
load_to_kind() {
    truvag3_load_to_kind "travel-chat-agent:latest"
    if "${TRUVAG3_CONTAINER_RUNTIME:-docker}" image inspect chat-ui:latest &>/dev/null; then
        truvag3_load_to_kind "chat-ui:latest"
    fi
}

# Setup API keys as Kubernetes secrets
setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-chat-agent" "$NAMESPACE"
}

# Setup agent configuration from .env as ConfigMap
setup_agent_config() {
    truvag3_create_configmap "travel-chat-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Reconcile the exact skill packages bound by this agent. The Registry Viewer is
# only the HTTP host; validation, optimistic concurrency, and persistence are
# owned by orchestration's provider-neutral skills handler.
sync_skills() {
    local api_base="${TRUVAG3_SKILLS_API_URL:-http://registry.localhost/api/v1/skills}"
    api_base="${api_base%/}"

    truvag3_check_skill_tools || return 1
    truvag3_sync_skill_package "$api_base" "travel" "action-verification" \
        "$SCRIPT_DIR/skills/action-verification.json" || return 1
    truvag3_sync_skill_package "$api_base" "travel" "travel-search-preparation" \
        "$SCRIPT_DIR/skills/travel-search-preparation.json" || return 1
    truvag3_sync_skill_package "$api_base" "travel" "currency-conversion" \
        "$SCRIPT_DIR/skills/currency-conversion.json" || return 1
    truvag3_sync_skill_package "$api_base" "travel" "travel-readiness-assessment" \
        "$SCRIPT_DIR/skills/travel-readiness-assessment.json" || return 1
    truvag3_sync_skill_package "$api_base" "travel" "weather-assessment" \
        "$SCRIPT_DIR/skills/weather-assessment.json" || return 1
    log_success "All Travel skill packages match Git"
}

check_skills() {
    local api_base="${TRUVAG3_SKILLS_API_URL:-http://registry.localhost/api/v1/skills}"
    local failed=0
    api_base="${api_base%/}"

    truvag3_check_skill_tools || return 1
    truvag3_check_skill_package "$api_base" "travel" "action-verification" \
        "$SCRIPT_DIR/skills/action-verification.json" || failed=1
    truvag3_check_skill_package "$api_base" "travel" "travel-search-preparation" \
        "$SCRIPT_DIR/skills/travel-search-preparation.json" || failed=1
    truvag3_check_skill_package "$api_base" "travel" "currency-conversion" \
        "$SCRIPT_DIR/skills/currency-conversion.json" || failed=1
    truvag3_check_skill_package "$api_base" "travel" "travel-readiness-assessment" \
        "$SCRIPT_DIR/skills/travel-readiness-assessment.json" || failed=1
    truvag3_check_skill_package "$api_base" "travel" "weather-assessment" \
        "$SCRIPT_DIR/skills/weather-assessment.json" || failed=1
    if [ "$failed" -ne 0 ]; then
        log_error "One or more Travel skill packages do not match Git"
        return 1
    fi
    log_success "All Travel skill packages match Git"
}

# Deploy to Kubernetes (both chat agent and chat UI)
deploy_k8s() {
    log_info "Deploying to Kubernetes..."

    # Load environment and setup secrets
    load_env

    # Create namespace if not exists
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Setup secrets and config
    setup_k8s_secrets
    setup_agent_config
    sync_skills

    # Deploy the chat agent
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    log_success "travel-chat-agent deployed"

    # Deploy the chat UI
    if [ -f "$CHAT_UI_DIR/k8-deployment.yaml" ]; then
        kubectl apply -f "$CHAT_UI_DIR/k8-deployment.yaml"
        log_success "chat-ui deployed"
    else
        log_warn "chat-ui k8-deployment.yaml not found, skipping UI deployment"
    fi

    # Force rollout to pick up new images
    log_info "Rolling out new versions..."
    kubectl rollout restart deployment/travel-chat-agent -n $NAMESPACE
    kubectl rollout status deployment/travel-chat-agent -n $NAMESPACE --timeout=120s

    if kubectl get deployment chat-ui -n $NAMESPACE &>/dev/null; then
        kubectl rollout restart deployment/chat-ui -n $NAMESPACE
        kubectl rollout status deployment/chat-ui -n $NAMESPACE --timeout=60s
    fi

    log_info "Waiting for pods to be ready..."
    kubectl wait --for=condition=ready pod -l app=travel-chat-agent -n $NAMESPACE --timeout=120s 2>/dev/null || true

    log_success "Deployment complete!"
}

verify_ingress() {
    truvag3_verify_ingress \
        "travel-chat-agent.localhost" "chat.localhost" \
        "grafana.localhost" "prometheus.localhost" "jaeger.localhost" || true
}

# Print summary after deployment
print_summary() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Setup Complete!                                 ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "Your Travel Chat Agent and UI are now running!"
    echo ""
    echo -e "${BLUE}Chat Application:${NC}"
    echo "  Chat UI:    http://chat.localhost"
    echo "  Chat API:   http://travel-chat-agent.localhost/health"
    echo ""
    echo -e "${BLUE}Monitoring Dashboards:${NC}"
    echo "  Grafana:    http://grafana.localhost (admin/admin)"
    echo "  Prometheus: http://prometheus.localhost"
    echo "  Jaeger:     http://jaeger.localhost"
    echo ""
    echo -e "${BLUE}Test the chat:${NC}"
    echo "  1. Open http://chat.localhost in your browser"
    echo "  2. Or use curl:"
    echo ""
    echo "  # Create a session"
    echo "  curl -X POST http://travel-chat-agent.localhost/chat/session | jq ."
    echo ""
    echo "  # Chat with SSE streaming"
    echo "  curl -N -X POST http://travel-chat-agent.localhost/chat/stream \\"
    echo "    -H \"Content-Type: application/json\" \\"
    echo "    -d '{\"message\": \"What is the weather in Tokyo?\"}'"
    echo ""
    echo -e "${BLUE}Useful commands:${NC}"
    echo "  kubectl get pods -n $NAMESPACE"
    echo "  kubectl logs -n $NAMESPACE -l app=travel-chat-agent -f"
    echo "  kubectl get ingress -n $NAMESPACE"
    echo "  $0 test            - Run API tests"
    echo "  $0 cleanup         - Delete everything"
    echo ""
    echo -e "${BLUE}All services accessible via *.localhost (no port-forwarding needed)${NC}"
}

# Test the API
test_api() {
    local host="${1:-travel-chat-agent.localhost}"

    log_info "Testing travel-chat-agent at $host..."
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
        echo "Sending: 'What is the weather in Tokyo?'"
        echo ""
        curl -N -X POST "http://$host/chat/stream" \
            -H "Content-Type: application/json" \
            -d "{\"session_id\": \"$SESSION_ID\", \"message\": \"What is the weather in Tokyo?\"}" 2>/dev/null || echo "Request sent"
        echo ""
    fi

    log_success "Test complete"
}

# Run the application locally
run_app() {
    log_info "Starting Travel Chat Agent..."
    echo ""
    echo "The agent will be available at: http://localhost:8356"
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
    export PORT=${PORT:-8356}

    ./travel-chat-agent
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

# Full deployment: cluster + infrastructure + agent + UI
full_deploy() {
    print_header
    log_info "Starting full deployment..."
    echo ""

    # Load environment before infrastructure so cold-start feature flags are
    # honored. deploy_k8s reloads it before creating the agent configuration.
    load_env
    truvag3_create_cluster
    setup_shared_infra
    build_docker
    load_to_kind
    deploy_k8s

    truvag3_verify_ingress \
        "travel-chat-agent.localhost" "chat.localhost" \
        "grafana.localhost" "prometheus.localhost" "jaeger.localhost" || true
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
    sync_skills

    # Apply Kubernetes manifests
    log_info "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    if [ -f "$CHAT_UI_DIR/k8-deployment.yaml" ]; then
        kubectl apply -f "$CHAT_UI_DIR/k8-deployment.yaml"
    fi

    # Restart deployments
    log_info "Restarting deployments..."
    kubectl rollout restart deployment/travel-chat-agent -n $NAMESPACE

    if kubectl get deployment chat-ui -n $NAMESPACE &>/dev/null; then
        kubectl rollout restart deployment/chat-ui -n $NAMESPACE
    fi

    log_info "Waiting for deployments to be ready..."
    if kubectl rollout status deployment/travel-chat-agent -n $NAMESPACE --timeout=120s; then
        log_success "travel-chat-agent rebuilt and deployed!"
    else
        log_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=travel-chat-agent --tail=20
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
    sync_skills

    # Apply k8-deployment.yaml to pick up ConfigMap changes
    log_info "Applying k8-deployment.yaml..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    # Restart deployment
    log_info "Restarting deployment..."
    kubectl rollout restart deployment/travel-chat-agent -n $NAMESPACE

    log_info "Waiting for rollout to complete..."
    if kubectl rollout status deployment/travel-chat-agent -n $NAMESPACE --timeout=120s; then
        log_success "Rollout complete!"
    else
        log_error "Rollout failed"
        kubectl logs -n $NAMESPACE -l app=travel-chat-agent --tail=20
        exit 1
    fi
}

# Show logs
logs() {
    log_info "Showing logs for travel-chat-agent..."
    kubectl logs -n "$NAMESPACE" -l app=travel-chat-agent -f
}

# Cleanup
cleanup() {
    log_info "Cleaning up..."

    # Delete K8s resources (agent only — chat-ui is a shared concern, leave it for its own setup)
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    # Stop local Redis
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true

    # Remove local binary
    rm -f "$SCRIPT_DIR/travel-chat-agent"

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
  full-deploy    Complete deployment: cluster + infra + agent + UI (recommended)

Kubernetes Deployment Commands:
  docker         Build Docker images (agent + UI)
  deploy         Build, load to Kind, and deploy to Kubernetes
  rebuild        Rebuild with --no-cache and redeploy (fresh dependencies)
  rollout        Restart deployment to pick up new secrets/config from .env
  skills-check   Compare published skills with the packages in Git (read-only)
  skills-sync    Reconcile and verify skills from Git without restarting the agent
  verify         Verify all ingress routes are reachable
  forward        Port-forward agent only (fallback if ingress unavailable)
  forward-all    Port-forward agent + UI + monitoring
  test           Run API tests
  logs           Show agent logs
  cleanup        Remove deployed resources
  cleanup-all    Delete Kind cluster and all resources

Access (via NGINX Ingress — no port-forwarding needed):
  Chat UI:       http://chat.localhost
  Chat API:      http://travel-chat-agent.localhost
  Grafana:       http://grafana.localhost
  Prometheus:    http://prometheus.localhost
  Jaeger:        http://jaeger.localhost

Examples:
  # Quick local development
  $0 run-all          # Setup Redis, build, and run locally

  # Full Kubernetes deployment (recommended)
  $0 full-deploy      # Creates cluster, infrastructure, deploys agent + UI

  # Step-by-step deployment
  $0 cluster          # Create Kind cluster
  $0 infra            # Setup monitoring
  $0 docker           # Build Docker images
  $0 deploy           # Deploy to K8s

  # Test the chat
  $0 test             # Run API tests
  # Open Chat UI: http://chat.localhost
  # Open Jaeger:  http://jaeger.localhost
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
        setup_shared_infra
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
    skills-check)
        load_env
        check_skills
        ;;
    skills-sync)
        load_env
        sync_skills
        ;;
    full-deploy)
        check_prerequisites
        full_deploy
        ;;
    verify)
        verify_ingress
        ;;
    forward)
        truvag3_forward "travel-chat-agent-service" 8356 80
        ;;
    forward-all)
        truvag3_forward_all \
            "travel-chat-agent-service:8356:80" \
            "chat-ui-service:8360:80" \
            "grafana:3000:80" \
            "prometheus:9090:9090" \
            "jaeger-query:16686:80"
        ;;
    test)
        test_api "${2:-travel-chat-agent.localhost}"
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
