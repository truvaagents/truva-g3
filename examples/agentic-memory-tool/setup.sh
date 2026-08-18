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

# Keep local-only endpoints from the tool's environment out of shared
# infrastructure configuration. The caller's environment is restored when the
# subshell exits.
setup_shared_infra() (
    unset REDIS_URL
    unset OTEL_EXPORTER_OTLP_ENDPOINT
    truvag3_setup_infra "$NAMESPACE"
)

setup_config() {
    truvag3_create_configmap "${APP_NAME}-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
}

ensure_namespace() {
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
}

apply_manifest() {
    kubectl apply -f "$SCRIPT_DIR/k8-deployment.yaml"
}

wait_for_rollout() {
    if kubectl rollout status "deployment/$APP_NAME" -n "$NAMESPACE" --timeout=120s; then
        echo "$APP_NAME rollout completed successfully!"
        return
    fi

    echo "ERROR: $APP_NAME rollout failed" >&2
    kubectl logs -n "$NAMESPACE" -l "app=$APP_NAME" --tail=20 || true
    return 1
}

cmd_cluster() {
    truvag3_create_cluster "$CLUSTER_NAME"
}

cmd_infra() {
    setup_shared_infra
}

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

    ensure_namespace
    setup_config
    apply_manifest
    wait_for_rollout

    echo "$APP_NAME deployed successfully!"
}

# Full deploy: cluster + shared infrastructure + tool deployment.
cmd_full_deploy() {
    cmd_cluster
    cmd_infra
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
    local pf_pid
    cleanup_test_forward() {
        kill "$pf_pid" 2>/dev/null || true
        wait "$pf_pid" 2>/dev/null || true
    }

    kubectl port-forward -n "$NAMESPACE" "svc/${APP_NAME}-service" "$PORT:80" >/dev/null 2>&1 &
    pf_pid=$!
    trap cleanup_test_forward EXIT INT TERM
    sleep 3

    echo "--- Health ---"
    local health
    health=$(curl -fsS "http://localhost:$PORT/health")
    echo "$health" | jq .
    echo "$health" | jq -e '.status == "healthy"' >/dev/null
    echo ""

    echo "--- Capabilities ---"
    local capabilities
    capabilities=$(curl -fsS "http://localhost:$PORT/api/capabilities")
    echo "$capabilities" | jq .
    echo "$capabilities" | jq -e '
        map(.name) as $names |
        all(["query_events", "query_knowledge", "query_investigations"][]; . as $name | $names | index($name))
    ' >/dev/null
    echo ""

    echo "--- query_events (last 24h) ---"
    local events
    events=$(curl -fsS -X POST "http://localhost:$PORT/api/capabilities/query_events" \
        -H "Content-Type: application/json" \
        -d '{"since_hours": 24, "limit": 5}')
    echo "$events" | jq .
    echo "$events" | jq -e '.success == true and (.data.events | type == "array")' >/dev/null
    echo ""

    echo "--- query_investigations ---"
    local investigations
    investigations=$(curl -fsS -X POST "http://localhost:$PORT/api/capabilities/query_investigations" \
        -H "Content-Type: application/json" \
        -d '{}')
    echo "$investigations" | jq .
    echo "$investigations" | jq -e '.success == true and (.data.investigations | type == "array")' >/dev/null

    cleanup_test_forward
    trap - EXIT INT TERM
    echo "$APP_NAME smoke tests passed!"
}

# Rollout configuration changes without rebuilding the image.
cmd_rollout() {
    if [ "$#" -gt 0 ]; then
        echo "ERROR: rollout does not accept build flags; use './setup.sh rebuild' after code changes" >&2
        return 2
    fi

    load_env
    ensure_namespace
    setup_config
    apply_manifest
    kubectl rollout restart "deployment/$APP_NAME" -n "$NAMESPACE"
    wait_for_rollout
}

# Rebuild the image without cache and guarantee that the running pod uses it.
cmd_rebuild() {
    load_env
    DOCKER_NO_CACHE=true cmd_docker_build
    truvag3_load_to_kind "$APP_NAME:latest" "$CLUSTER_NAME"

    ensure_namespace
    setup_config
    apply_manifest
    kubectl rollout restart "deployment/$APP_NAME" -n "$NAMESPACE"
    wait_for_rollout
}

cmd_logs() {
    kubectl logs -n "$NAMESPACE" -l "app=$APP_NAME" -f --tail=100
}

cmd_status() {
    kubectl get deployment,pods,svc -n "$NAMESPACE" -l "app=$APP_NAME"
}

# Clean — remove deployment only (keep cluster/infra)
cmd_clean() {
    kubectl delete -f "$SCRIPT_DIR/k8-deployment.yaml" --ignore-not-found
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

cmd_help() {
    echo "Usage: ./setup.sh <command>"
    echo ""
    echo "Quick Start:"
    echo "  full-deploy  One-click: cluster + infra + deploy"
    echo "  cluster      Create the Kind cluster if it does not exist"
    echo "  infra        Deploy or reconcile shared infrastructure"
    echo ""
    echo "Build & Deploy:"
    echo "  build        Build Go binary locally"
    echo "  run          Build and run locally"
    echo "  docker-build Build Docker image (workspace mode)"
    echo "  deploy       Build, load, and deploy to K8s"
    echo "  rebuild      Rebuild with --no-cache and replace the running pod"
    echo ""
    echo "Testing & Access:"
    echo "  test         Run API tests against the deployed tool"
    echo "  forward      Port forward the service only"
    echo "  forward-all  Port forward service + monitoring"
    echo "  logs         View tool logs"
    echo "  status       Check deployment status"
    echo ""
    echo "Operational:"
    echo "  rollout      Refresh .env-backed config and restart the deployment"
    echo "  clean        Remove tool deployment only"
    echo "  clean-all    Delete the entire Kind cluster"
}

# Main entry point
case "${1:-help}" in
    cluster)     cmd_cluster ;;
    infra)       cmd_infra ;;
    build)       cmd_build ;;
    run)         cmd_run ;;
    docker-build) cmd_docker_build ;;
    deploy)      cmd_deploy ;;
    full-deploy) cmd_full_deploy ;;
    rebuild)     cmd_rebuild ;;
    test)        cmd_test ;;
    forward)     cmd_forward ;;
    forward-all) cmd_forward_all ;;
    logs)        cmd_logs ;;
    status)      cmd_status ;;
    rollout)     shift; cmd_rollout "$@" ;;
    clean)       cmd_clean ;;
    clean-all)   cmd_clean_all ;;
    help|--help|-h) cmd_help ;;
    *)
        echo "ERROR: unknown command '$1'" >&2
        cmd_help
        exit 1
        ;;
esac
