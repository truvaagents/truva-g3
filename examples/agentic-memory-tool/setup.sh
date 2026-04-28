#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Configuration
CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
APP_NAME="agentic-memory-tool"
PORT=${PORT:-8377}

# Source shared setup library (provides truvag3_build_docker, truvag3_create_configmap, etc.)
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"

# Load .env. truvag3_load_env auto-bootstraps from .env.example on fresh checkouts.
load_env() { truvag3_load_env "$SCRIPT_DIR/.env"; }

# Build Go binary locally (GOWORK=off for replace directives)
cmd_build() {
    GOWORK=off go mod tidy
    GOWORK=off go build -o $APP_NAME .
}

# Build and run locally
cmd_run() {
    load_env
    cmd_build
    ./$APP_NAME
}

# Build Docker image — auto-selects Dockerfile.workspace vs Dockerfile
cmd_docker_build() {
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

# Deploy to Kubernetes
cmd_deploy() {
    load_env
    cmd_docker_build
    truvag3_load_to_kind "$APP_NAME:latest" "$CLUSTER_NAME"

    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    # Create ConfigMap from .env (no secrets needed — this tool has no API keys)
    truvag3_create_configmap "${APP_NAME}-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"

    kubectl apply -f k8-deployment.yaml
    kubectl wait --for=condition=available deployment/$APP_NAME -n $NAMESPACE --timeout=120s

    echo "$APP_NAME deployed successfully!"
}

# Full deploy: cluster + infra + deploy (non-blocking per feedback_setup_scripts.md)
cmd_full_deploy() {
    truvag3_create_cluster "$CLUSTER_NAME"
    truvag3_setup_infra "$NAMESPACE"
    cmd_deploy
    echo ""
    echo "Full deployment complete!"
    echo "Run './setup.sh forward-all' to access services."
}

# Port forward — foreground with auto-reconnect via shared helper
cmd_forward() {
    truvag3_forward "${APP_NAME}-service" "$PORT" "80" "$NAMESPACE"
}

# Port forward service + monitoring — first service foreground, rest background
cmd_forward_all() {
    truvag3_forward_all \
        "${APP_NAME}-service:${PORT}:80" \
        "grafana:3000:80" \
        "prometheus:9090:9090" \
        "jaeger-query:16686:80"
}

# Test — spawn temporary port-forward, run tests, clean up
cmd_test() {
    kubectl port-forward -n $NAMESPACE svc/${APP_NAME}-service $PORT:80 &
    PF_PID=$!
    sleep 3

    echo "--- Health ---"
    curl -s http://localhost:$PORT/health | jq .
    echo ""
    echo "--- Capabilities ---"
    curl -s http://localhost:$PORT/api/capabilities | jq .
    echo ""
    echo "--- query_events (last 24h) ---"
    curl -s -X POST http://localhost:$PORT/api/capabilities/query_events \
        -H "Content-Type: application/json" \
        -d '{"since_hours": 24, "limit": 5}' | jq .
    echo ""
    echo "--- query_investigations ---"
    curl -s -X POST http://localhost:$PORT/api/capabilities/query_investigations \
        -H "Content-Type: application/json" \
        -d '{}' | jq .

    kill $PF_PID 2>/dev/null || true
}

# Rollout — optional --build flag to rebuild image first
cmd_rollout() {
    if [ "${2:-}" = "--build" ]; then
        cmd_docker_build
        truvag3_load_to_kind "$APP_NAME:latest" "$CLUSTER_NAME"
    fi
    kubectl rollout restart deployment/$APP_NAME -n $NAMESPACE
}

# Clean — remove deployment only (keep cluster/infra)
cmd_clean() {
    kubectl delete -f k8-deployment.yaml --ignore-not-found
}

# Clean all — delete entire Kind cluster with confirmation
cmd_clean_all() {
    read -p "Delete entire Kind cluster '$CLUSTER_NAME'? [y/N] " confirm
    if [ "$confirm" = "y" ] || [ "$confirm" = "Y" ]; then
        kind delete cluster --name "$CLUSTER_NAME"
        echo "Cluster deleted."
    else
        echo "Cancelled."
    fi
}

# Main entry point
case "${1:-help}" in
    build)       cmd_build ;;
    run)         cmd_run ;;
    docker-build) cmd_docker_build ;;
    deploy)      cmd_deploy ;;
    full-deploy) cmd_full_deploy ;;
    rebuild)     DOCKER_NO_CACHE=true cmd_deploy ;;
    test)        cmd_test ;;
    forward)     cmd_forward ;;
    forward-all) cmd_forward_all ;;
    logs)        kubectl logs -n $NAMESPACE -l app=$APP_NAME -f --tail=100 ;;
    status)      kubectl get pods,svc -n $NAMESPACE -l app=$APP_NAME ;;
    rollout)     cmd_rollout "$@" ;;
    clean)       cmd_clean ;;
    clean-all)   cmd_clean_all ;;
    *)
        echo "Usage: ./setup.sh <command>"
        echo ""
        echo "Quick Start:"
        echo "  full-deploy  One-click: cluster + infra + deploy"
        echo ""
        echo "Build & Deploy:"
        echo "  build        Build Go binary locally"
        echo "  run          Build and run locally"
        echo "  docker-build Build Docker image (workspace mode)"
        echo "  deploy       Build, load to Kind, deploy to K8s"
        echo "  rebuild      Rebuild with --no-cache and redeploy"
        echo ""
        echo "Testing & Access:"
        echo "  test         Run API tests against deployed tool"
        echo "  forward      Port forward the service only"
        echo "  forward-all  Port forward service + monitoring"
        echo "  logs         View tool logs"
        echo "  status       Check deployment status"
        echo ""
        echo "Operational:"
        echo "  rollout      Restart deployment (--build to rebuild first)"
        echo "  clean        Remove tool deployment only"
        echo "  clean-all    Delete entire Kind cluster"
        ;;
esac
