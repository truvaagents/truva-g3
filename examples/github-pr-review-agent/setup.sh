#!/bin/bash

# setup.sh - One-click setup for github-pr-review-agent
# Modeled after examples/travel-chat-agent/setup.sh (gold standard).

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"

# Shared environment library
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="github-pr-review-agent"
DEFAULT_PORT=8382
HOST="github-pr-review-agent.localhost"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

print_header() {
    echo -e "${BLUE}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║        TruvaG3 GitHub PR Review Agent                  ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

check_prerequisites() { truvag3_check_prerequisites; }

setup_redis() {
    log_info "Setting up Redis..."
    if command -v redis-cli &> /dev/null && redis-cli ping &> /dev/null; then
        log_success "Redis already running"
        return 0
    fi
    if [ "${DOCKER_AVAILABLE:-false}" = true ]; then
        log_info "Starting Redis via Docker..."
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true
        "${TRUVAG3_CONTAINER_RUNTIME:-docker}" run -d --name truvag3-redis -p 6379:6379 redis:8.2.8-alpine
        log_success "Redis started on port 6379"
    else
        log_error "Redis not available. Install Redis or Docker."
        exit 1
    fi
}

check_api_keys() {
    local found=""
    for var in OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY GROQ_API_KEY; do
        if [ -n "${!var}" ]; then
            [ -n "$found" ] && found="$found, "
            found="${found}${var%_API_KEY} (env)"
        elif [ -f .env ] && grep -q "^${var}=." .env; then
            [ -n "$found" ] && found="$found, "
            found="${found}${var%_API_KEY} (.env)"
        fi
    done
    if [ -n "$found" ]; then
        log_success "AI provider key(s) found: $found"
        return 0
    fi
    log_warn "No AI provider API keys configured — set at least one in .env"
    return 1
}

check_webhook_secret() {
    if [ -n "$GITHUB_WEBHOOK_SECRET" ]; then
        log_success "GITHUB_WEBHOOK_SECRET found in env"
        return 0
    fi
    if [ -f .env ] && grep -qE "^GITHUB_WEBHOOK_SECRET=.+" .env; then
        log_success "GITHUB_WEBHOOK_SECRET found in .env"
        return 0
    fi
    log_warn "GITHUB_WEBHOOK_SECRET not set — webhook endpoint will reject all deliveries with 401."
    log_warn "Generate one with: openssl rand -hex 32"
    return 1
}

setup_env() {
    log_info "Setting up environment..."
    if [ ! -f .env ]; then
        cp .env.example .env
        log_success "Created .env from .env.example — edit it with your keys"
    else
        log_success ".env already exists"
    fi
    check_api_keys || true
    check_webhook_secret || true
    echo ""
}

load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

build_app() {
    log_info "Building github-pr-review-agent..."
    cd "$SCRIPT_DIR"
    GOWORK=off go mod download
    GOWORK=off go mod tidy
    GOWORK=off go build -o github-pr-review-agent .
    log_success "Built ./github-pr-review-agent"
}

build_docker() {
    log_info "Building Docker image (using local workspace modules)..."
    local no_cache=""
    [ "${DOCKER_NO_CACHE:-}" = "true" ] && no_cache="--no-cache"
    truvag3_build_docker "github-pr-review-agent:latest" \
        "$SCRIPT_DIR/Dockerfile.workspace" "$TRUVAG3_ROOT" $no_cache
}

load_to_kind() { truvag3_load_to_kind "github-pr-review-agent:latest"; }

setup_k8s_secrets() {
    truvag3_create_secret "ai-provider-keys-pr-review-agent" "$NAMESPACE"
    # Webhook secret lives in its own Secret so it can be rotated independently.
    if [ -n "${GITHUB_WEBHOOK_SECRET:-}" ]; then
        kubectl create secret generic github-pr-review-agent-webhook \
            --namespace="$NAMESPACE" \
            --from-literal=GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
            --dry-run=client -o yaml | kubectl apply -f -
        log_success "Webhook secret applied"
    else
        log_warn "GITHUB_WEBHOOK_SECRET not set — webhook secret not created"
    fi
}

setup_agent_config() {
    truvag3_create_configmap "github-pr-review-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

# Deploy embedded mode (single pod: api + worker)
deploy_embedded() {
    log_info "Deploying in embedded mode (api + worker in one pod)..."
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
    load_env
    setup_k8s_secrets
    setup_agent_config
    # Remove split-mode deployments if present, then apply embedded
    kubectl delete -f "$SCRIPT_DIR/k8-deployment-api.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$SCRIPT_DIR/k8-deployment-worker.yaml" --ignore-not-found 2>/dev/null || true
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
    kubectl rollout restart deployment/github-pr-review-agent -n "$NAMESPACE"
    kubectl rollout status deployment/github-pr-review-agent -n "$NAMESPACE" --timeout=120s
    log_success "Embedded deployment ready"
}

# Deploy split mode (api + worker in separate deployments)
deploy_split() {
    log_info "Deploying in split mode (api and worker as separate pods)..."
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
    load_env
    setup_k8s_secrets
    setup_agent_config
    # Remove embedded deployment if present, then apply split
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl apply -f "$SCRIPT_DIR/k8-deployment-api.yaml"
    kubectl apply -f "$SCRIPT_DIR/k8-deployment-worker.yaml"
    kubectl rollout restart deployment/github-pr-review-agent-api -n "$NAMESPACE"
    kubectl rollout restart deployment/github-pr-review-agent-worker -n "$NAMESPACE"
    kubectl rollout status deployment/github-pr-review-agent-api -n "$NAMESPACE" --timeout=120s
    kubectl rollout status deployment/github-pr-review-agent-worker -n "$NAMESPACE" --timeout=120s
    log_success "Split deployment ready"
}

deploy_k8s() {
    # Default deploy = embedded mode (matches travel-chat-agent ergonomics)
    deploy_embedded
}

verify_ingress() {
    truvag3_verify_ingress "$HOST" \
        "grafana.localhost" "prometheus.localhost" "jaeger.localhost" || true
}

print_summary() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Setup Complete!                                 ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${BLUE}Agent Endpoints:${NC}"
    echo "  Health:    http://$HOST/health"
    echo "  Tasks:     http://$HOST/api/v1/tasks"
    echo "  Webhook:   http://$HOST/webhook/github   (POST with X-Hub-Signature-256)"
    echo ""
    echo -e "${BLUE}Monitoring:${NC}"
    echo "  Grafana:    http://grafana.localhost (admin/admin)"
    echo "  Prometheus: http://prometheus.localhost"
    echo "  Jaeger:     http://jaeger.localhost"
    echo ""
    echo -e "${BLUE}Smoke test:${NC}"
    echo "  $0 mock-webhook   - send a signed fake pull_request webhook"
    echo "  $0 logs           - tail the agent logs"
    echo "  $0 cleanup        - delete deployed resources"
    echo ""
}

# mock-webhook posts a signed synthetic pull_request payload to the agent.
# Useful smoke test that exercises HMAC verification, dedup, and enqueue
# without needing a real GitHub round-trip.
mock_webhook() {
    local host="${1:-$HOST}"
    local secret="${GITHUB_WEBHOOK_SECRET:-}"
    if [ -z "$secret" ] && [ -f .env ]; then
        secret=$(grep -E "^GITHUB_WEBHOOK_SECRET=" .env | head -1 | cut -d'=' -f2-)
    fi
    if [ -z "$secret" ]; then
        log_error "GITHUB_WEBHOOK_SECRET not set in env or .env — cannot sign payload"
        exit 1
    fi

    local delivery_id="mock-$(date +%s)-$RANDOM"
    # Generate a fresh head SHA each invocation so the (owner, repo, head SHA)
    # dedup TTL doesn't make repeated smoke tests look broken.
    local head_sha
    head_sha=$(openssl rand -hex 20)

    local payload
    payload=$(cat <<EOF
{
  "action": "opened",
  "number": 42,
  "pull_request": {
    "number": 42,
    "draft": false,
    "head": { "sha": "$head_sha" }
  },
  "repository": {
    "full_name": "acme/payments",
    "name": "payments",
    "owner": { "login": "acme" }
  }
}
EOF
)
    local sig
    sig="sha256=$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$secret" -hex | awk '{print $2}')"

    log_info "POST http://$host/webhook/github  (delivery=$delivery_id, head=$head_sha)"
    # --data-binary preserves bytes exactly so the server's HMAC re-computation
    # matches; --data can transform inputs in surprising ways.
    curl -sS -i -X POST "http://$host/webhook/github" \
        -H "Content-Type: application/json" \
        -H "X-GitHub-Event: pull_request" \
        -H "X-GitHub-Delivery: $delivery_id" \
        -H "X-Hub-Signature-256: $sig" \
        --data-binary "$payload"
    echo ""
}

run_app() {
    log_info "Starting github-pr-review-agent locally..."
    if [ -f .env ]; then
        set -a
        # shellcheck disable=SC1091
        source .env
        set +a
    fi
    export REDIS_URL=${REDIS_URL:-redis://localhost:6379}
    export PORT=${PORT:-$DEFAULT_PORT}
    ./github-pr-review-agent
}

run_all() {
    if ! redis-cli ping 2>/dev/null | grep -q PONG; then
        setup_redis
    fi
    setup_env
    build_app
    run_app
}

full_deploy() {
    print_header
    log_info "Starting full deployment (non-blocking)..."
    truvag3_create_cluster
    truvag3_setup_infra
    load_env
    build_docker
    load_to_kind
    deploy_embedded
    verify_ingress
    print_summary
}

rebuild() {
    log_info "Rebuilding with --no-cache and redeploying..."
    DOCKER_NO_CACHE=true build_docker
    if command -v kind &> /dev/null; then
        local cluster_name
        cluster_name=$(kubectl config current-context 2>/dev/null | sed 's/kind-//')
        if kind get clusters 2>/dev/null | grep -q "^${cluster_name}$"; then
            load_to_kind
        fi
    fi
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
    load_env
    setup_k8s_secrets
    setup_agent_config

    # Restart whichever deployment(s) currently exist
    if kubectl get deployment github-pr-review-agent -n "$NAMESPACE" &>/dev/null; then
        kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
        kubectl rollout restart deployment/github-pr-review-agent -n "$NAMESPACE"
        kubectl rollout status deployment/github-pr-review-agent -n "$NAMESPACE" --timeout=120s
    fi
    if kubectl get deployment github-pr-review-agent-api -n "$NAMESPACE" &>/dev/null; then
        kubectl apply -f "$SCRIPT_DIR/k8-deployment-api.yaml"
        kubectl rollout restart deployment/github-pr-review-agent-api -n "$NAMESPACE"
        kubectl rollout status deployment/github-pr-review-agent-api -n "$NAMESPACE" --timeout=120s
    fi
    if kubectl get deployment github-pr-review-agent-worker -n "$NAMESPACE" &>/dev/null; then
        kubectl apply -f "$SCRIPT_DIR/k8-deployment-worker.yaml"
        kubectl rollout restart deployment/github-pr-review-agent-worker -n "$NAMESPACE"
        kubectl rollout status deployment/github-pr-review-agent-worker -n "$NAMESPACE" --timeout=120s
    fi
    log_success "Rebuild complete"
}

rollout() {
    print_header
    load_env
    setup_k8s_secrets
    setup_agent_config
    if kubectl get deployment github-pr-review-agent -n "$NAMESPACE" &>/dev/null; then
        kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
        kubectl rollout restart deployment/github-pr-review-agent -n "$NAMESPACE"
    fi
    if kubectl get deployment github-pr-review-agent-api -n "$NAMESPACE" &>/dev/null; then
        kubectl apply -f "$SCRIPT_DIR/k8-deployment-api.yaml"
        kubectl rollout restart deployment/github-pr-review-agent-api -n "$NAMESPACE"
    fi
    if kubectl get deployment github-pr-review-agent-worker -n "$NAMESPACE" &>/dev/null; then
        kubectl apply -f "$SCRIPT_DIR/k8-deployment-worker.yaml"
        kubectl rollout restart deployment/github-pr-review-agent-worker -n "$NAMESPACE"
    fi
    log_success "Rollout triggered"
}

logs() {
    log_info "Tailing logs for github-pr-review-agent (all components)..."
    kubectl logs -n "$NAMESPACE" \
        -l "app in (github-pr-review-agent,github-pr-review-agent-api,github-pr-review-agent-worker)" \
        --max-log-requests 10 --tail 50 -f
}

cleanup() {
    log_info "Removing deployed resources..."
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$SCRIPT_DIR/k8-deployment-api.yaml" --ignore-not-found 2>/dev/null || true
    kubectl delete -f "$SCRIPT_DIR/k8-deployment-worker.yaml" --ignore-not-found 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" stop truvag3-redis 2>/dev/null || true
    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" rm truvag3-redis 2>/dev/null || true
    rm -f "$SCRIPT_DIR/github-pr-review-agent"
    log_success "Cleanup complete"
}

cleanup_all() {
    cleanup
    truvag3_delete_cluster
    log_success "Full cleanup complete"
}

show_help() {
    print_header
    cat <<EOF
Usage: $0 <command>

Local development:
  setup           Setup the local environment (.env, deps)
  run             Build and run the agent locally
  run-all         Setup Redis, build, and run (recommended for local dev)
  build           Build the agent binary only
  redis           Start a local Redis container

Kubernetes cluster:
  cluster         Create a Kind cluster with NGINX Ingress
  infra           Deploy monitoring (Prometheus, Grafana, Jaeger, OTEL)
  full-deploy     Cluster + infra + embedded deployment (one-shot, non-blocking)

Kubernetes deployment:
  docker          Build the Docker image
  deploy          Build, load, and deploy in EMBEDDED mode (default)
  deploy-embedded Same as 'deploy' (single-pod api+worker)
  deploy-split    Deploy api and worker as separate pods
  rebuild         Rebuild with --no-cache and redeploy
  rollout         Re-apply manifests + restart (pick up new env/secrets)
  verify          Verify the agent ingress is reachable
  logs            Tail logs across all agent components
  cleanup         Remove deployed resources
  cleanup-all     Remove resources + delete Kind cluster

Smoke testing:
  mock-webhook [host]   Send a signed synthetic pull_request webhook
                        Default host: $HOST

Access (via NGINX Ingress):
  Agent:        http://$HOST
  Grafana:      http://grafana.localhost
  Jaeger:       http://jaeger.localhost
  Prometheus:   http://prometheus.localhost

Examples:
  $0 run-all                    # local dev
  $0 full-deploy                # one-shot K8s deploy (embedded)
  $0 deploy-split               # production-style split deployment
  $0 mock-webhook               # smoke-test the webhook end-to-end
EOF
}

case "${1:-help}" in
    setup)            check_prerequisites; setup_env; build_app ;;
    run)              check_prerequisites; build_app; run_app ;;
    run-all)          check_prerequisites; run_all ;;
    build)            check_prerequisites; build_app ;;
    redis)            check_prerequisites; setup_redis ;;
    cluster)          check_prerequisites; print_header; truvag3_create_cluster ;;
    infra)            check_prerequisites; print_header; truvag3_setup_infra ;;
    docker)           check_prerequisites; build_docker ;;
    deploy)           check_prerequisites; build_docker; load_to_kind; deploy_embedded ;;
    deploy-embedded)  check_prerequisites; build_docker; load_to_kind; deploy_embedded ;;
    deploy-split)     check_prerequisites; build_docker; load_to_kind; deploy_split ;;
    rebuild)          check_prerequisites; rebuild ;;
    rollout)          check_prerequisites; rollout ;;
    full-deploy)      check_prerequisites; full_deploy ;;
    verify)           verify_ingress ;;
    forward)          truvag3_forward "github-pr-review-agent-service" "$DEFAULT_PORT" 80 ;;
    forward-all)
        truvag3_forward_all \
            "github-pr-review-agent-service:$DEFAULT_PORT:80" \
            "grafana:3000:80" "prometheus:9090:9090" "jaeger-query:16686:80"
        ;;
    mock-webhook)     mock_webhook "${2:-$HOST}" ;;
    logs)             logs ;;
    cleanup)          cleanup ;;
    cleanup-all)      cleanup_all ;;
    help|--help|-h)   show_help ;;
    *) echo "Unknown command: $1"; echo ""; show_help; exit 1 ;;
esac
