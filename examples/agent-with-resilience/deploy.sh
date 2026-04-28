#!/bin/bash

# deploy.sh - Deploy agent-with-resilience to Kind cluster
# This script can set up infrastructure from scratch or use existing components
# It NEVER deletes existing resources - only creates what's missing

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-truvag3-demo}"
NAMESPACE="${NAMESPACE:-truvag3-examples}"
APP_NAME="research-agent-resilience"
DOCKER_IMAGE="truvag3/${APP_NAME}"
DOCKER_TAG="${DOCKER_TAG:-latest}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INFRA_SCRIPT="$SCRIPT_DIR/../k8-deployment/setup-infrastructure.sh"

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

# Check if Kind cluster exists, create if not
ensure_cluster() {
    print_step "Checking Kind cluster..."

    if kind get clusters 2>/dev/null | grep -q "$CLUSTER_NAME"; then
        print_success "Cluster '$CLUSTER_NAME' exists"
    else
        print_warning "Cluster '$CLUSTER_NAME' does not exist"
        print_step "Creating Kind cluster..."

        cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30093
    hostPort: 8093
    protocol: TCP
EOF
        print_success "Cluster created"
    fi

    # Switch to the correct context
    kubectl config use-context "kind-$CLUSTER_NAME" >/dev/null 2>&1
    print_success "Using cluster: $CLUSTER_NAME"
}

# Check if namespace exists, create if not
ensure_namespace() {
    print_step "Checking namespace..."

    if kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
        print_success "Namespace '$NAMESPACE' exists"
    else
        print_step "Creating namespace '$NAMESPACE'..."
        kubectl create namespace "$NAMESPACE"
        print_success "Namespace created"
    fi
}

# Check if a service exists and is healthy
check_service_healthy() {
    local service_name=$1
    local namespace=$2

    if kubectl get service "$service_name" -n "$namespace" &>/dev/null; then
        local endpoints=$(kubectl get endpoints "$service_name" -n "$namespace" -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null)
        if [ -n "$endpoints" ]; then
            return 0  # Service exists and is healthy
        fi
    fi
    return 1
}

# Ensure infrastructure is set up (Redis, OTEL, etc.)
ensure_infrastructure() {
    print_step "Checking infrastructure..."

    # Check Redis in default namespace (shared infrastructure)
    if check_service_healthy "redis" "default"; then
        print_success "Redis: Available (default namespace)"
    else
        # Check if infrastructure setup script exists
        if [ -f "$INFRA_SCRIPT" ]; then
            print_warning "Redis not found, setting up infrastructure..."
            echo ""
            NAMESPACE="$NAMESPACE" "$INFRA_SCRIPT"
            echo ""
        else
            print_warning "Redis not available and no setup script found"
            print_warning "The agent may not function properly without Redis"
            echo ""
            echo "To set up Redis manually:"
            echo "  kubectl apply -f ../k8-deployment/redis.yaml"
            echo ""
        fi
    fi

    # Verify Redis is now available
    if check_service_healthy "redis" "default"; then
        print_success "Infrastructure ready"
    elif check_service_healthy "redis" "$NAMESPACE"; then
        print_success "Infrastructure ready (Redis in $NAMESPACE)"
    else
        print_warning "Redis still not available - continuing anyway"
    fi
}

build_image() {
    print_step "Building Docker image..."

    cd "$SCRIPT_DIR"
    if docker build -t "${DOCKER_IMAGE}:${DOCKER_TAG}" . >/dev/null 2>&1; then
        print_success "Built: ${DOCKER_IMAGE}:${DOCKER_TAG}"
    else
        print_error "Docker build failed"
        echo "Running with verbose output:"
        docker build -t "${DOCKER_IMAGE}:${DOCKER_TAG}" .
        exit 1
    fi
}

load_image() {
    print_step "Loading image into Kind cluster..."

    if kind load docker-image "${DOCKER_IMAGE}:${DOCKER_TAG}" --name "$CLUSTER_NAME" 2>/dev/null; then
        print_success "Image loaded into cluster"
    else
        print_error "Failed to load image"
        exit 1
    fi
}

# Deploy agent - uses kubectl apply (idempotent, never deletes)
deploy_agent() {
    print_step "Deploying $APP_NAME..."

    # Check if already deployed and healthy
    if check_service_healthy "$APP_NAME" "$NAMESPACE"; then
        local current_image=$(kubectl get deployment "$APP_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)
        print_success "$APP_NAME already running (image: $current_image)"
        print_step "Updating deployment with new image..."
    fi

    # Apply deployment (idempotent - creates or updates)
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"

    print_step "Waiting for deployment to be ready..."
    if kubectl wait --for=condition=available --timeout=120s deployment/$APP_NAME -n "$NAMESPACE" 2>/dev/null; then
        print_success "Deployment successful!"
    else
        print_error "Deployment failed or timed out"
        echo ""
        echo "Checking pod status:"
        kubectl get pods -n "$NAMESPACE" -l app=$APP_NAME
        echo ""
        echo "Recent logs:"
        kubectl logs -n "$NAMESPACE" -l app=$APP_NAME --tail=20 2>/dev/null || true
        exit 1
    fi
}

print_summary() {
    echo ""
    echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  $APP_NAME deployed successfully!${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
    echo ""
    echo -e "${BLUE}Deployment Status:${NC}"
    kubectl get deployment $APP_NAME -n "$NAMESPACE" --no-headers
    echo ""
    echo -e "${BLUE}Pods:${NC}"
    kubectl get pods -n "$NAMESPACE" -l app=$APP_NAME --no-headers
    echo ""
    echo -e "${BLUE}Service:${NC}"
    kubectl get svc $APP_NAME -n "$NAMESPACE" --no-headers
    echo ""
    echo -e "${BLUE}Test the agent:${NC}"
    echo "  # Port forward first:"
    echo "  kubectl port-forward -n $NAMESPACE svc/$APP_NAME 8093:8093"
    echo ""
    echo "  # Then test:"
    echo "  curl http://localhost:8093/health"
    echo ""
    echo "  curl -X POST http://localhost:8093/api/capabilities/research_topic \\"
    echo "    -H \"Content-Type: application/json\" \\"
    echo "    -d '{\"topic\": \"weather in New York\", \"ai_synthesis\": false}'"
    echo ""
}

# Cleanup function - only removes this agent, not infrastructure
cleanup() {
    print_step "Removing $APP_NAME deployment..."

    # Delete only the agent resources, not shared infrastructure
    kubectl delete deployment $APP_NAME -n "$NAMESPACE" --ignore-not-found
    kubectl delete service $APP_NAME -n "$NAMESPACE" --ignore-not-found
    kubectl delete configmap ${APP_NAME}-config -n "$NAMESPACE" --ignore-not-found
    kubectl delete hpa ${APP_NAME}-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
    kubectl delete pdb ${APP_NAME}-pdb -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    # Clean up Redis registration for this agent only
    print_step "Cleaning up Redis registration..."
    kubectl exec -n default deploy/redis -- redis-cli DEL \
        "truvag3:services:research-assistant-resilience" \
        "truvag3:names:research-assistant-resilience" 2>/dev/null || true

    print_success "Cleanup complete (infrastructure preserved)"
}

# Show status
status() {
    echo -e "${BLUE}═══════════════════════════════════════════════${NC}"
    echo -e "${BLUE}  $APP_NAME Status${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════${NC}"
    echo ""

    echo -e "${BLUE}Deployment:${NC}"
    kubectl get deployment $APP_NAME -n "$NAMESPACE" 2>/dev/null || echo "Not deployed"
    echo ""

    echo -e "${BLUE}Pods:${NC}"
    kubectl get pods -n "$NAMESPACE" -l app=$APP_NAME 2>/dev/null || echo "No pods"
    echo ""

    echo -e "${BLUE}Service:${NC}"
    kubectl get svc $APP_NAME -n "$NAMESPACE" 2>/dev/null || echo "No service"
    echo ""

    echo -e "${BLUE}Redis Registration:${NC}"
    kubectl exec -n default deploy/redis -- redis-cli GET "truvag3:services:research-assistant-resilience" 2>/dev/null | head -c 200 || echo "Not registered"
    echo ""
}

# Show logs
logs() {
    kubectl logs -n "$NAMESPACE" -l app=$APP_NAME -f
}

# Main deployment
main() {
    echo ""
    echo -e "${BLUE}╔═══════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║  Agent with Resilience - Deployment           ║${NC}"
    echo -e "${BLUE}║  (Creates missing infrastructure, preserves   ║${NC}"
    echo -e "${BLUE}║   existing components)                        ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════════╝${NC}"
    echo ""

    ensure_cluster
    ensure_namespace
    ensure_infrastructure
    echo ""
    build_image
    load_image
    deploy_agent
    print_summary
}

# Show help
show_help() {
    echo "Agent with Resilience - Deployment Script"
    echo ""
    echo "This script deploys the agent and sets up infrastructure if needed."
    echo "It NEVER deletes existing resources - only creates what's missing."
    echo ""
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  deploy   - Build and deploy (creates infra if needed) - default"
    echo "  cleanup  - Remove agent only (preserves infrastructure)"
    echo "  status   - Show deployment status"
    echo "  logs     - Stream pod logs"
    echo "  build    - Build Docker image only"
    echo "  help     - Show this help"
    echo ""
    echo "Environment Variables:"
    echo "  CLUSTER_NAME  - Kind cluster name (default: truvag3-demo)"
    echo "  NAMESPACE     - Kubernetes namespace (default: truvag3-examples)"
    echo "  DOCKER_TAG    - Docker image tag (default: latest)"
    echo ""
    echo "Safety Features:"
    echo "  ✓ Creates Kind cluster if it doesn't exist"
    echo "  ✓ Creates namespace if it doesn't exist"
    echo "  ✓ Sets up infrastructure if not available"
    echo "  ✓ Never deletes existing resources"
    echo "  ✓ Uses kubectl apply (idempotent)"
    echo "  ✓ Cleanup only removes this agent, not shared infra"
}

# Handle arguments
case "${1:-deploy}" in
    deploy)
        main
        ;;
    cleanup|delete|remove)
        ensure_cluster
        cleanup
        ;;
    status)
        ensure_cluster
        status
        ;;
    logs)
        ensure_cluster
        logs
        ;;
    build)
        build_image
        ;;
    help|-h|--help)
        show_help
        ;;
    *)
        echo "Unknown command: $1"
        echo ""
        show_help
        exit 1
        ;;
esac
