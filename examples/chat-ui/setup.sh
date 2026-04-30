#!/bin/bash

# setup.sh - One-click setup for TruvaG3 Chat UI
# A static frontend serving multiple chat interfaces:
#   - /           - Standard chat UI (connects to travel-chat-agent on port 8356)
#   - /hitl.html  - HITL chat UI (connects to agent-with-human-approval on port 8352)
#
# Usage:
#   ./setup.sh          - Full deployment (build, load, deploy)
#   ./setup.sh build    - Build Docker image only
#   ./setup.sh deploy   - Deploy to existing cluster
#   ./setup.sh rebuild  - Rebuild and redeploy
#   ./setup.sh logs     - View pod logs
#   ./setup.sh status   - Check deployment status
#   ./setup.sh forward  - Start port forwarding
#   ./setup.sh clean    - Remove deployment

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="chat-ui"
LOCAL_PORT=8360

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
    echo -e "${BLUE}║     TruvaG3 Chat UI - Standard & HITL Frontends        ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check Docker
    if ! command -v docker &> /dev/null; then
        log_error "Docker is not installed"
        echo "Please install Docker from https://www.docker.com/"
        exit 1
    fi
    log_success "Docker installed"

    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed"
        echo "Please install kubectl from https://kubernetes.io/docs/tasks/tools/"
        exit 1
    fi
    log_success "kubectl installed"

    # Check kind
    if ! command -v kind &> /dev/null; then
        log_error "Kind is not installed"
        echo "Please install Kind from https://kind.sigs.k8s.io/"
        exit 1
    fi
    log_success "Kind installed"

    echo ""
}

# Detect Kind cluster
detect_cluster() {
    # Try to find an existing truvag3 cluster
    EXISTING_CLUSTER=$(kind get clusters 2>/dev/null | grep "truvag3-demo" | head -1 || true)
    if [ -n "$EXISTING_CLUSTER" ]; then
        CLUSTER_NAME="$EXISTING_CLUSTER"
        log_info "Detected Kind cluster: $CLUSTER_NAME"
    else
        log_error "No truvag3-demo cluster found"
        echo "Please run setup.sh from travel-chat-agent first to create the cluster"
        exit 1
    fi
}

# Build Docker image
build_image() {
    local no_cache=""
    if [ "$1" == "--no-cache" ]; then
        no_cache="--no-cache"
        log_info "Building with --no-cache"
    fi

    log_info "Building Docker image..."
    docker build $no_cache -t ${APP_NAME}:latest .
    log_success "${APP_NAME}:latest built"
}

# Load image into Kind
load_image() {
    log_info "Loading image into Kind cluster..."
    kind load docker-image ${APP_NAME}:latest --name "$CLUSTER_NAME"
    log_success "Image loaded to Kind"
}

# Deploy to Kubernetes
deploy() {
    log_info "Creating namespace if needed..."
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    log_info "Applying Kubernetes manifests..."
    kubectl apply -f k8-deployment.yaml -n $NAMESPACE

    log_info "Waiting for deployment to be ready..."
    kubectl rollout status deployment/${APP_NAME} -n $NAMESPACE --timeout=60s

    log_success "${APP_NAME} deployed successfully!"
}

# Start port forwarding
port_forward() {
    # Kill any existing port-forward for this service
    pkill -f "port-forward.*${APP_NAME}-service.*${LOCAL_PORT}" 2>/dev/null || true
    sleep 1

    log_info "Starting port forward on localhost:${LOCAL_PORT}..."
    log_info "Press Ctrl+C to stop"
    echo ""
    kubectl port-forward svc/${APP_NAME}-service ${LOCAL_PORT}:80 -n $NAMESPACE
}

# Stop port forwarding
stop_forward() {
    log_info "Stopping any existing port forwards for ${APP_NAME}..."
    pkill -f "port-forward.*${APP_NAME}-service.*${LOCAL_PORT}" 2>/dev/null && \
        log_success "Port forward stopped" || \
        log_info "No active port forward found"
}

# Show logs
show_logs() {
    log_info "Showing ${APP_NAME} logs..."
    kubectl logs -l app=${APP_NAME} -n $NAMESPACE --tail=50 -f
}

# Show status
show_status() {
    log_info "Deployment Status:"
    kubectl get pods -l app=${APP_NAME} -n $NAMESPACE
    echo ""
    log_info "Service Status:"
    kubectl get svc -l app=${APP_NAME} -n $NAMESPACE
}

# Clean up deployment
clean() {
    log_info "Removing ${APP_NAME} deployment..."
    kubectl delete -f k8-deployment.yaml -n $NAMESPACE --ignore-not-found
    log_success "Cleanup complete"
}

# Main execution
main() {
    print_header

    case "${1:-}" in
        build)
            check_prerequisites
            build_image "${2:-}"
            ;;
        deploy)
            check_prerequisites
            detect_cluster
            deploy
            ;;
        rebuild)
            check_prerequisites
            detect_cluster
            log_info "Rebuilding ${APP_NAME}..."
            build_image --no-cache
            load_image
            deploy
            kubectl rollout restart deployment/${APP_NAME} -n $NAMESPACE
            kubectl rollout status deployment/${APP_NAME} -n $NAMESPACE --timeout=60s
            log_success "${APP_NAME} rebuilt and deployed!"
            ;;
        logs)
            show_logs
            ;;
        status)
            show_status
            ;;
        forward)
            port_forward
            ;;
        stop-forward)
            stop_forward
            ;;
        clean)
            clean
            ;;
        help|--help|-h)
            echo "Usage: ./setup.sh [command]"
            echo ""
            echo "Serves two chat frontends:"
            echo "  /           - Standard Chat UI (for travel-chat-agent)"
            echo "  /hitl.html  - HITL Chat UI (for agent-with-human-approval)"
            echo ""
            echo "Commands:"
            echo "  (none)    Full deployment (build, load, deploy)"
            echo "  build     Build Docker image only"
            echo "  deploy    Deploy to existing cluster"
            echo "  rebuild   Rebuild and redeploy with fresh image"
            echo "  logs      View pod logs"
            echo "  status    Check deployment status"
            echo "  forward      Start port forwarding (localhost:${LOCAL_PORT})"
            echo "  stop-forward Stop port forwarding"
            echo "  clean        Remove deployment"
            echo "  help      Show this help message"
            echo ""
            echo "Prerequisites:"
            echo "  - Docker"
            echo "  - kubectl"
            echo "  - Kind cluster (created by agent setup scripts)"
            ;;
        *)
            # Default: full deployment
            check_prerequisites
            detect_cluster
            build_image
            load_image
            deploy
            echo ""
            log_success "Chat UI is ready!"
            echo ""
            echo "Access the UIs:"
            echo "  Option 1: kubectl port-forward svc/${APP_NAME}-service ${LOCAL_PORT}:80 -n $NAMESPACE"
            echo "            Standard Chat: http://localhost:${LOCAL_PORT}/"
            echo "            HITL Chat:     http://localhost:${LOCAL_PORT}/hitl.html"
            echo ""
            echo "  Option 2: Use NodePort (if cluster has port mapping)"
            echo "            Standard Chat: http://localhost:30096/"
            echo "            HITL Chat:     http://localhost:30096/hitl.html"
            echo ""
            echo "Backend agents (via Ingress):"
            echo "  travel-chat-agent:         http://travel-chat-agent.localhost"
            echo "  devops-chat-agent:         http://devops-chat-agent.localhost"
            echo "  agent-with-human-approval: http://hitl-agent.localhost"
            echo ""
            echo "Commands:"
            echo "  ./setup.sh logs     - View logs"
            echo "  ./setup.sh status   - Check status"
            echo "  ./setup.sh forward  - Start port forwarding"
            echo "  ./setup.sh rebuild  - Rebuild and redeploy"
            ;;
    esac
}

main "$@"
