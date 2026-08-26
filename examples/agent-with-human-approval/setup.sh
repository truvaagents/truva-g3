#!/bin/bash

# setup.sh - One-click setup for agent-with-human-approval (HITL Agent)
# This script sets up the local development environment and can deploy to Kubernetes
# Modeled after travel-chat-agent/setup.sh

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
APP_NAME="agent-with-human-approval"
AGENT_PORT=8352
UI_PORT=8362  # Different from travel-chat-agent UI

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
    echo -e "${BLUE}║     TruvaG3 Agent with Human Approval (HITL)           ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Check prerequisites
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
            redis:8.2.8-alpine

        log_success "Redis started on port 6379"
    else
        log_error "Redis not available"
        echo "Please install Redis or Docker to run Redis"
        echo ""
        echo "Options:"
        echo "  1. Install Redis: brew install redis && brew services start redis"
        echo "  2. Use Docker: docker run -d -p 6379:6379 redis:8.2.8-alpine"
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

    # Check OpenRouter (priority: 850)
    if [ -n "$OPENROUTER_API_KEY" ]; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}OpenRouter (env)"
    elif [ -f .env ] && grep -q "^OPENROUTER_API_KEY=" .env; then
        [ -n "$found_keys" ] && found_keys="$found_keys, "
        found_keys="${found_keys}OpenRouter (.env)"
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
        echo -e "${YELLOW}│    OPENROUTER_API_KEY=your-key                             │${NC}"
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

    # Auto-bootstrap and source .env (.env.example → .env on fresh checkouts)
    # is handled by truvag3_load_env in setup-env-lib.sh.
    load_env

    # Check for API keys
    check_api_keys || true

    echo ""
}

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

# Build the application using Go workspace (local modules)
build_app() {
    log_info "Building agent-with-human-approval (using local workspace)..."

    cd "$SCRIPT_DIR"

    # Create temporary go.work to use local framework modules
    log_info "Creating temporary go.work for local module resolution..."
    cat > go.work << 'GOWORK'
go 1.27.0

use (
    .
    ../../ai
    ../../core
    ../../orchestration
    ../../telemetry
    ../../resilience
)
GOWORK

    # Download external dependencies and build
    go mod download
    go build -o agent-with-human-approval .

    # Clean up temporary go.work (keep go.mod clean for commits)
    rm -f go.work go.work.sum

    log_success "Application built successfully (using local workspace modules)"
    echo ""
}

# Build Docker image using Dockerfile.workspace (local modules)
build_docker() {
    log_info "Building Docker image (using local workspace modules)..."

    local no_cache_flag=""
    if [ "$DOCKER_NO_CACHE" = "true" ]; then
        log_info "Building with --no-cache (fresh build)"
        no_cache_flag="--no-cache"
    fi

    # Build from truvag3 root using Dockerfile.workspace
    cd "$TRUVAG3_ROOT"
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" build $no_cache_flag \
        -f examples/agent-with-human-approval/Dockerfile.workspace \
        -t agent-with-human-approval:latest .

    log_success "agent-with-human-approval:latest built (from local workspace)"
}

load_to_kind() { truvag3_load_to_kind "agent-with-human-approval:latest"; }

# Setup API keys as Kubernetes secrets
setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-hitl-agent" "$NAMESPACE"
}

# Setup standard env-config ConfigMap from .env (framework tunables, timeouts,
# model aliases). HITL-specific keys live in hitl-config (see setup_hitl_config)
# and win on conflict via envFrom ordering in k8-deployment.yaml.
setup_agent_config() {
    truvag3_create_configmap "agent-with-human-approval-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Setup HITL configuration as Kubernetes ConfigMap from .env file
setup_hitl_config() {
    log_info "Setting up HITL configuration from .env..."

    # Read HITL config from .env file with defaults
    # NOTE: Some variables intentionally have no defaults so the Go code's fail-safe defaults apply
    # - HITL_DEFAULT_ACTION: no default -> Go uses "reject" (fail-safe)
    # - HITL_STREAMING_EXPIRY: no default -> Go uses "implicit_deny" for streaming
    # - HITL_NON_STREAMING_EXPIRY: no default -> Go uses "apply_default" for non-streaming
    local HITL_ENABLED="true"
    local HITL_REQUIRE_PLAN_APPROVAL="false"
    local HITL_SENSITIVE_CAPABILITIES=""
    local HITL_STEP_SENSITIVE_CAPABILITIES=""
    local HITL_SENSITIVE_AGENTS=""
    local HITL_STEP_SENSITIVE_AGENTS=""
    local HITL_DEFAULT_TIMEOUT="5m"
    local HITL_DEFAULT_ACTION=""  # Empty = Go uses fail-safe default (reject)
    local HITL_STREAMING_EXPIRY=""  # Empty = Go uses default (implicit_deny for streaming)
    local HITL_NON_STREAMING_EXPIRY=""  # Empty = Go uses default (apply_default for non-streaming)
    local HITL_ESCALATE_AFTER_RETRIES="3"
    local HITL_REDIS_DB="6"
    local HITL_KEY_PREFIX="truvag3:hitl"
    # K8s webhook URL uses service name
    local HITL_WEBHOOK_URL="http://agent-with-human-approval-service.truvag3-examples:80/internal/hitl-webhook"
    # Execution Debug Store (for DAG visualization in Registry Viewer)
    local EXECUTION_DEBUG_STORE_ENABLED="false"

    if [ -f "$SCRIPT_DIR/.env" ]; then
        # Read values from .env, using defaults if not found
        local val
        val=$(grep "^TRUVAG3_HITL_ENABLED=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_ENABLED="$val"

        val=$(grep "^TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_REQUIRE_PLAN_APPROVAL="$val"

        # Plan-level triggers (capabilities and agents)
        val=$(grep "^TRUVAG3_HITL_SENSITIVE_CAPABILITIES=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_SENSITIVE_CAPABILITIES="$val"

        val=$(grep "^TRUVAG3_HITL_SENSITIVE_AGENTS=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_SENSITIVE_AGENTS="$val"

        # Step-level triggers (capabilities and agents) - Scenario 2 with resolved parameters
        val=$(grep "^TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_STEP_SENSITIVE_CAPABILITIES="$val"

        val=$(grep "^TRUVAG3_HITL_STEP_SENSITIVE_AGENTS=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_STEP_SENSITIVE_AGENTS="$val"

        val=$(grep "^TRUVAG3_HITL_DEFAULT_TIMEOUT=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_DEFAULT_TIMEOUT="$val"

        val=$(grep "^TRUVAG3_HITL_DEFAULT_ACTION=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_DEFAULT_ACTION="$val"

        val=$(grep "^TRUVAG3_HITL_STREAMING_EXPIRY=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_STREAMING_EXPIRY="$val"

        val=$(grep "^TRUVAG3_HITL_NON_STREAMING_EXPIRY=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_NON_STREAMING_EXPIRY="$val"

        val=$(grep "^TRUVAG3_HITL_ESCALATE_AFTER_RETRIES=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_ESCALATE_AFTER_RETRIES="$val"

        val=$(grep "^TRUVAG3_HITL_REDIS_DB=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_REDIS_DB="$val"

        val=$(grep "^TRUVAG3_HITL_KEY_PREFIX=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && HITL_KEY_PREFIX="$val"

        # Execution Debug Store
        val=$(grep "^TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && EXECUTION_DEBUG_STORE_ENABLED="$val"

        # LLM Debug
        val=$(grep "^TRUVAG3_LLM_DEBUG_ENABLED=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && LLM_DEBUG_ENABLED="$val"

        # Model Alias Overrides
        val=$(grep "^TRUVAG3_OPENAI_MODEL_DEFAULT=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && OPENAI_MODEL_DEFAULT="$val"

        val=$(grep "^TRUVAG3_OPENAI_MODEL_SMART=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && OPENAI_MODEL_SMART="$val"

        val=$(grep "^TRUVAG3_ANTHROPIC_MODEL_DEFAULT=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && ANTHROPIC_MODEL_DEFAULT="$val"

        val=$(grep "^TRUVAG3_ANTHROPIC_MODEL_SMART=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && ANTHROPIC_MODEL_SMART="$val"

        val=$(grep "^TRUVAG3_GROQ_MODEL_SMART=" "$SCRIPT_DIR/.env" 2>/dev/null | cut -d'=' -f2)
        [ -n "$val" ] && GROQ_MODEL_SMART="$val"

        # Note: webhook URL for K8s uses service name, ignore .env value for K8s deployment
    fi

    log_info "HITL Configuration:"
    echo "  TRUVAG3_HITL_ENABLED=$HITL_ENABLED"
    echo "  TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=$HITL_REQUIRE_PLAN_APPROVAL"
    [ -n "$HITL_SENSITIVE_CAPABILITIES" ] && echo "  TRUVAG3_HITL_SENSITIVE_CAPABILITIES=$HITL_SENSITIVE_CAPABILITIES"
    [ -n "$HITL_STEP_SENSITIVE_CAPABILITIES" ] && echo "  TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES=$HITL_STEP_SENSITIVE_CAPABILITIES"
    [ -n "$HITL_SENSITIVE_AGENTS" ] && echo "  TRUVAG3_HITL_SENSITIVE_AGENTS=$HITL_SENSITIVE_AGENTS"
    [ -n "$HITL_STEP_SENSITIVE_AGENTS" ] && echo "  TRUVAG3_HITL_STEP_SENSITIVE_AGENTS=$HITL_STEP_SENSITIVE_AGENTS"
    echo "  TRUVAG3_HITL_DEFAULT_TIMEOUT=$HITL_DEFAULT_TIMEOUT"
    if [ -n "$HITL_DEFAULT_ACTION" ]; then
        echo "  TRUVAG3_HITL_DEFAULT_ACTION=$HITL_DEFAULT_ACTION"
    else
        echo "  TRUVAG3_HITL_DEFAULT_ACTION=(not set, Go uses fail-safe: reject)"
    fi
    if [ -n "$HITL_STREAMING_EXPIRY" ]; then
        echo "  TRUVAG3_HITL_STREAMING_EXPIRY=$HITL_STREAMING_EXPIRY"
    else
        echo "  TRUVAG3_HITL_STREAMING_EXPIRY=(not set, Go uses: implicit_deny)"
    fi
    if [ -n "$HITL_NON_STREAMING_EXPIRY" ]; then
        echo "  TRUVAG3_HITL_NON_STREAMING_EXPIRY=$HITL_NON_STREAMING_EXPIRY"
    else
        echo "  TRUVAG3_HITL_NON_STREAMING_EXPIRY=(not set, Go uses: apply_default)"
    fi
    echo "  TRUVAG3_HITL_ESCALATE_AFTER_RETRIES=$HITL_ESCALATE_AFTER_RETRIES"
    echo "  TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=$EXECUTION_DEBUG_STORE_ENABLED"

    # Create ConfigMap for HITL configuration
    # Note: Empty values are intentionally set to allow Go's fail-safe defaults to apply
    kubectl create configmap hitl-config \
        --from-literal=TRUVAG3_HITL_ENABLED="${HITL_ENABLED}" \
        --from-literal=TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL="${HITL_REQUIRE_PLAN_APPROVAL}" \
        --from-literal=TRUVAG3_HITL_SENSITIVE_CAPABILITIES="${HITL_SENSITIVE_CAPABILITIES}" \
        --from-literal=TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES="${HITL_STEP_SENSITIVE_CAPABILITIES}" \
        --from-literal=TRUVAG3_HITL_SENSITIVE_AGENTS="${HITL_SENSITIVE_AGENTS}" \
        --from-literal=TRUVAG3_HITL_STEP_SENSITIVE_AGENTS="${HITL_STEP_SENSITIVE_AGENTS}" \
        --from-literal=TRUVAG3_HITL_DEFAULT_TIMEOUT="${HITL_DEFAULT_TIMEOUT}" \
        --from-literal=TRUVAG3_HITL_DEFAULT_ACTION="${HITL_DEFAULT_ACTION}" \
        --from-literal=TRUVAG3_HITL_STREAMING_EXPIRY="${HITL_STREAMING_EXPIRY}" \
        --from-literal=TRUVAG3_HITL_NON_STREAMING_EXPIRY="${HITL_NON_STREAMING_EXPIRY}" \
        --from-literal=TRUVAG3_HITL_ESCALATE_AFTER_RETRIES="${HITL_ESCALATE_AFTER_RETRIES}" \
        --from-literal=TRUVAG3_HITL_REDIS_DB="${HITL_REDIS_DB}" \
        --from-literal=TRUVAG3_HITL_KEY_PREFIX="${HITL_KEY_PREFIX}" \
        --from-literal=TRUVAG3_HITL_WEBHOOK_URL="${HITL_WEBHOOK_URL}" \
        --from-literal=TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED="${EXECUTION_DEBUG_STORE_ENABLED}" \
        --from-literal=TRUVAG3_LLM_DEBUG_ENABLED="${LLM_DEBUG_ENABLED}" \
        --from-literal=TRUVAG3_ENABLE_OPENAPI="true" \
        --from-literal=TRUVAG3_OPENAI_MODEL_DEFAULT="${OPENAI_MODEL_DEFAULT}" \
        --from-literal=TRUVAG3_OPENAI_MODEL_SMART="${OPENAI_MODEL_SMART}" \
        --from-literal=TRUVAG3_ANTHROPIC_MODEL_DEFAULT="${ANTHROPIC_MODEL_DEFAULT}" \
        --from-literal=TRUVAG3_ANTHROPIC_MODEL_SMART="${ANTHROPIC_MODEL_SMART}" \
        --from-literal=TRUVAG3_GROQ_MODEL_SMART="${GROQ_MODEL_SMART}" \
        -n $NAMESPACE --dry-run=client -o yaml | kubectl apply -n $NAMESPACE -f -

    log_success "HITL configuration applied as K8s ConfigMap (hitl-config)"
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
    setup_hitl_config

    # Check if k8-deployment.yaml exists
    if [ ! -f "$SCRIPT_DIR/k8-deployment.yaml" ]; then
        log_warn "k8-deployment.yaml not found, creating basic deployment..."
        create_k8s_manifest
    fi

    # Deploy the HITL agent
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    log_success "agent-with-human-approval deployed"

    # Force rollout to pick up new images
    log_info "Rolling out new version..."
    kubectl rollout restart deployment/agent-with-human-approval -n $NAMESPACE
    kubectl rollout status deployment/agent-with-human-approval -n $NAMESPACE --timeout=120s

    log_info "Waiting for pods to be ready..."
    kubectl wait --for=condition=ready pod -l app=agent-with-human-approval -n $NAMESPACE --timeout=120s 2>/dev/null || true

    log_success "Deployment complete!"
    log_info "Run '$0 forward' to set up port forwards"
}

# Create basic k8s manifest if not exists
create_k8s_manifest() {
    cat > "$SCRIPT_DIR/k8-deployment.yaml" << 'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: agent-with-human-approval
  namespace: truvag3-examples
  labels:
    app: agent-with-human-approval
spec:
  replicas: 1
  selector:
    matchLabels:
      app: agent-with-human-approval
  template:
    metadata:
      labels:
        app: agent-with-human-approval
    spec:
      containers:
      - name: agent-with-human-approval
        image: agent-with-human-approval:latest
        imagePullPolicy: Never
        ports:
        - containerPort: 8352
        env:
        - name: PORT
          value: "8352"
        - name: REDIS_URL
          value: "redis://redis.truvag3-examples.svc.cluster.local:6379"
        - name: NAMESPACE
          value: "default"
        - name: APP_ENV
          value: "development"
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector.truvag3-examples.svc.cluster.local:4318"
        - name: TRUVAG3_LLM_DEBUG_ENABLED
          value: "true"
        envFrom:
        - secretRef:
            name: ai-provider-keys-hitl-agent
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /health
            port: 8352
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /health
            port: 8352
          initialDelaySeconds: 5
          periodSeconds: 10
---
apiVersion: v1
kind: Service
metadata:
  name: agent-with-human-approval-service
  namespace: truvag3-examples
spec:
  selector:
    app: agent-with-human-approval
  ports:
  - port: 80
    targetPort: 8352
  type: ClusterIP
EOF
    log_success "Created k8-deployment.yaml"
}





print_summary() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Setup Complete!                                 ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "Your HITL Agent is now running!"
    echo ""
    echo -e "${BLUE}Agent:${NC}"
    echo "  API:     http://hitl-agent.localhost/health"
    echo "  HITL UI: http://chat.localhost/hitl.html"
    echo ""
    echo -e "${BLUE}Monitoring:${NC}"
    echo "  Grafana:    http://grafana.localhost (admin/admin)"
    echo "  Prometheus: http://prometheus.localhost"
    echo "  Jaeger:     http://jaeger.localhost"
    echo ""
    echo -e "${BLUE}All services accessible via *.localhost (no port-forwarding needed)${NC}"
}

# Test the API
test_api() {
    local host="${1:-localhost:$AGENT_PORT}"

    log_info "Testing agent-with-human-approval at $host..."
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

        # Test streaming chat (will pause for HITL approval)
        log_info "Step 3: Test SSE chat stream (will pause for HITL approval)"
        echo "Sending: 'What is the weather in Tokyo?'"
        echo ""
        echo "NOTE: The stream will pause with a 'checkpoint' event waiting for approval."
        echo "      Use /hitl/command to approve, then /hitl/resume/{id} to continue."
        echo ""
        timeout 10 curl -N -X POST "http://$host/chat/stream" \
            -H "Content-Type: application/json" \
            -d "{\"session_id\": \"$SESSION_ID\", \"message\": \"What is the weather in Tokyo?\"}" 2>/dev/null || echo ""
        echo ""
    fi

    log_success "Test complete"
}

# Run the application locally
run_app() {
    log_info "Starting Agent with Human Approval..."
    echo ""
    echo "The agent will be available at: http://localhost:8352"
    echo ""
    echo "HITL is ALWAYS ENABLED - all plans require human approval"
    echo ""
    echo "Endpoints:"
    echo "  POST /chat/stream           - SSE streaming chat (pauses for approval)"
    echo "  POST /hitl/command          - Submit approval/rejection"
    echo "  POST /hitl/resume/{id}      - Resume after approval (SSE)"
    echo "  GET  /hitl/checkpoints      - List pending checkpoints"
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
    export PORT=${PORT:-8352}

    ./agent-with-human-approval
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

    # Step 1: Create Kind cluster
    truvag3_create_cluster

    # Step 2: Setup monitoring infrastructure
    truvag3_setup_infra

    # Step 3: Load environment for secrets
    load_env

    # Step 4: Build and deploy
    build_docker
    load_to_kind
    deploy_k8s

    # Step 5: Setup port forwards
    truvag3_verify_ingress "hitl-agent.localhost" "grafana.localhost" "jaeger.localhost" || true
    print_summary
}

# Rebuild with no-cache and redeploy
rebuild() {
    log_info "Rebuilding with Fresh Build"

    # Build Docker images with --no-cache
    log_info "Building Docker image with --no-cache..."
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

    # Load env and setup API keys + HITL config from .env file
    load_env
    setup_k8s_secrets
    setup_agent_config
    setup_hitl_config

    # Apply Kubernetes manifests
    log_info "Applying Kubernetes manifests..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    # Restart deployments
    log_info "Restarting deployment..."
    kubectl rollout restart deployment/agent-with-human-approval -n $NAMESPACE

    log_info "Waiting for deployment to be ready..."
    if kubectl rollout status deployment/agent-with-human-approval -n $NAMESPACE --timeout=120s; then
        log_success "agent-with-human-approval rebuilt and deployed!"
    else
        log_error "Deployment failed. Checking logs..."
        kubectl logs -n $NAMESPACE -l app=agent-with-human-approval --tail=20
        exit 1
    fi
}

# Show logs
logs() {
    log_info "Showing logs for agent-with-human-approval..."
    kubectl logs -n "$NAMESPACE" -l app=agent-with-human-approval -f
}

# Rollout - restart deployment to pick up new secrets/config
rollout() {
    print_header
    log_info "Rolling out deployment..."

    local rebuild=false

    # Check for --build flag
    if [ "$2" = "--build" ] || [ "$2" = "build" ]; then
        rebuild=true
    fi

    # Load env to update secrets
    load_env

    # Update secrets and config from .env
    log_info "Updating secrets and HITL config from .env..."
    setup_k8s_secrets
    setup_agent_config
    setup_hitl_config

    # Apply k8-deployment.yaml to pick up ConfigMap changes
    log_info "Applying k8-deployment.yaml..."
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

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

# Cleanup
cleanup() {
    log_info "Cleaning up..."

    # Stop port forwards for this agent only
    pkill -f "port-forward.*agent-with-human-approval" 2>/dev/null || true

    # Delete K8s resources
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true

    # Stop local Redis
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true

    # Remove local binary
    rm -f "$SCRIPT_DIR/agent-with-human-approval"

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
  build      Build the agent only (uses go.work for local modules)

Kubernetes Cluster Commands:
  cluster        Create a Kind cluster with port mappings
  infra          Setup monitoring infrastructure (Prometheus, Grafana, Jaeger, OTEL)
  full-deploy    Complete deployment: cluster + infra + agent (recommended)

Kubernetes Deployment Commands:
  docker         Build Docker image (uses Dockerfile.workspace with local modules)
  deploy         Build, load to Kind, and deploy to Kubernetes
  rebuild        Rebuild with --no-cache and redeploy
  rollout        Restart deployment to pick up new secrets/config
                 Use --build flag to rebuild Docker image first
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
  $0 docker           # Build Docker image
  $0 deploy           # Deploy to K8s
  $0 forward-all      # Port forward everything

  # Test the HITL flow
  $0 test             # Run API tests
  # Open examples/chat-ui/hitl.html in browser
  # Open Jaeger: http://localhost:16686
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
    forward)
        truvag3_forward "agent-with-human-approval-service" 8352 80
        ;;
    forward-all)
        truvag3_forward_all \
            "agent-with-human-approval-service:8352:80" \
            "grafana:3000:80" \
            "prometheus:9090:9090" \
            "jaeger-query:16686:80"
        ;;
    test)
        test_api "${2:-localhost:$AGENT_PORT}"
        ;;
    logs)
        logs
        ;;
    rollout)
        rollout "$@"
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
