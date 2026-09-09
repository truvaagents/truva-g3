#!/bin/bash

# Deploys an isolated three-primary Redis/Valkey Cluster for lightweight local
# application verification in the existing Kind cluster. It intentionally
# leaves the normal single-instance `redis` service untouched. Replica failover
# remains covered by the separate Linux integration fixture.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST="$SCRIPT_DIR/redis-cluster-validation.yaml"
NAMESPACE="${NAMESPACE:-truvag3-examples}"
STATEFULSET="redis-cluster-validation"
BOOTSTRAP_JOB="redis-cluster-validation-bootstrap"
CLUSTER_SEEDS="${STATEFULSET}-0.${STATEFULSET}.${NAMESPACE}.svc.cluster.local:6379,${STATEFULSET}-1.${STATEFULSET}.${NAMESPACE}.svc.cluster.local:6379,${STATEFULSET}-2.${STATEFULSET}.${NAMESPACE}.svc.cluster.local:6379"

distribution_image() {
    case "$1" in
        redis-oss) printf '%s' 'redis:8.8.0-alpine' ;;
        valkey-8)  printf '%s' 'valkey/valkey:8.1.9-alpine' ;;
        valkey-9)  printf '%s' 'valkey/valkey:9.1.1-alpine' ;;
        *)
            echo "unsupported distribution '$1'; use redis-oss, valkey-8, or valkey-9" >&2
            return 1
            ;;
    esac
}

require_tools() {
    command -v kubectl >/dev/null 2>&1 || {
        echo "kubectl is required" >&2
        exit 1
    }
    kubectl cluster-info >/dev/null 2>&1 || {
        echo "the current kubectl context is not reachable" >&2
        exit 1
    }
}

current_distribution() {
    kubectl get statefulset "$STATEFULSET" -n "$NAMESPACE" \
        -o jsonpath='{.metadata.labels.truvag3\.io/redis-distribution}' 2>/dev/null || true
}

render_manifest() {
    local distribution=$1
    local image=$2
    sed \
        -e "s|__TRUVAG3_REDIS_DISTRIBUTION__|${distribution}|g" \
        -e "s|__TRUVAG3_REDIS_IMAGE__|${image}|g" \
        "$MANIFEST"
}

cluster_cli() {
    kubectl exec -n "$NAMESPACE" "${STATEFULSET}-0" -- /bin/sh -ec '
        if command -v redis-cli >/dev/null 2>&1; then
          exec redis-cli "$@"
        fi
        exec valkey-cli "$@"
    ' -- "$@"
}

verify_cluster() {
    local info
    info=$(cluster_cli CLUSTER INFO)
    printf '%s\n' "$info"
    printf '%s\n' "$info" | grep -q '^cluster_state:ok'
    printf '%s\n' "$info" | grep -q '^cluster_slots_assigned:16384'
    printf '%s\n' "$info" | grep -q '^cluster_slots_ok:16384'

    local nodes
    nodes=$(cluster_cli CLUSTER NODES)
    local primaries
    local replicas
    primaries=$(printf '%s\n' "$nodes" | awk '$3 ~ /master/ && $3 !~ /fail/ {count++} END {print count+0}')
    replicas=$(printf '%s\n' "$nodes" | awk '$3 ~ /slave|replica/ && $3 !~ /fail/ {count++} END {print count+0}')
    if [ "$primaries" -ne 3 ] || [ "$replicas" -ne 0 ]; then
        echo "expected 3 healthy primaries and no replicas; found ${primaries} and ${replicas}" >&2
        return 1
    fi
    echo "Verified 16,384 slots across 3 primaries."
}

deploy_cluster() {
    local distribution=${1:-redis-oss}
    local image
    image=$(distribution_image "$distribution")

    local installed
    installed=$(current_distribution)
    if [ -n "$installed" ] && [ "$installed" != "$distribution" ]; then
        echo "the ${installed} validation cluster already exists; run '$0 cleanup' before switching distributions" >&2
        exit 1
    fi

    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

    # A completed Job is immutable. Recreate only this disposable bootstrap Job
    # so an interrupted cluster initialization can be retried deterministically.
    kubectl delete job "$BOOTSTRAP_JOB" -n "$NAMESPACE" --ignore-not-found --wait=true >/dev/null
    render_manifest "$distribution" "$image" | kubectl apply -f -

    kubectl rollout status statefulset/"$STATEFULSET" -n "$NAMESPACE" --timeout=180s
    if ! kubectl wait --for=condition=complete job/"$BOOTSTRAP_JOB" -n "$NAMESPACE" --timeout=180s; then
        kubectl logs -n "$NAMESPACE" job/"$BOOTSTRAP_JOB" --all-containers=true || true
        exit 1
    fi
    verify_cluster

    echo "Distribution: $distribution"
    echo "Seed addresses:"
    echo "  ${STATEFULSET}-0.${STATEFULSET}.${NAMESPACE}.svc.cluster.local:6379"
    echo "  ${STATEFULSET}-1.${STATEFULSET}.${NAMESPACE}.svc.cluster.local:6379"
    echo "  ${STATEFULSET}-2.${STATEFULSET}.${NAMESPACE}.svc.cluster.local:6379"
}

show_status() {
    local installed
    installed=$(current_distribution)
    if [ -z "$installed" ]; then
        echo "Redis/Valkey validation cluster is not deployed."
        return 1
    fi
    echo "Distribution: $installed"
    kubectl get pods -n "$NAMESPACE" -l app="$STATEFULSET" -o wide
    verify_cluster
}

cleanup_cluster() {
    kubectl delete job "$BOOTSTRAP_JOB" -n "$NAMESPACE" --ignore-not-found --wait=true
    kubectl delete statefulset "$STATEFULSET" -n "$NAMESPACE" --ignore-not-found --wait=true
    kubectl delete service "$STATEFULSET" -n "$NAMESPACE" --ignore-not-found
    kubectl delete configmap redis-cluster-validation-bootstrap -n "$NAMESPACE" --ignore-not-found
    kubectl wait --for=delete pod -l app="$STATEFULSET" -n "$NAMESPACE" --timeout=120s 2>/dev/null || true
}

configure_deployments() {
    local mode=$1
    local redis_namespace=$2
    shift 2
    if [ "$#" -eq 0 ]; then
        echo "at least one deployment name is required" >&2
        exit 1
    fi
    case "$redis_namespace" in
        ""|*[!A-Za-z0-9._-]*)
            echo "Redis namespace must contain only letters, numbers, '.', '_', or '-'" >&2
            exit 1
            ;;
    esac

    local redis_url=""
    local redis_mode=""
    local redis_addrs=""
    if [ "$mode" = "cluster" ]; then
        verify_cluster >/dev/null
        redis_mode=cluster
        redis_addrs=$CLUSTER_SEEDS
    elif [ "$mode" = "standalone" ]; then
        redis_url="redis://redis.${NAMESPACE}.svc.cluster.local:6379/0"
    else
        echo "unsupported deployment connection mode '$mode'" >&2
        exit 1
    fi

    local deployment
    local deployment_config
    for deployment in "$@"; do
        case "$deployment" in
            ""|*[!a-z0-9.-]*)
                echo "invalid Kubernetes deployment name '$deployment'" >&2
                exit 1
                ;;
        esac
        kubectl get deployment "$deployment" -n "$NAMESPACE" >/dev/null
        # Explicit values override any example-specific envFrom entries. Empty
        # values are intentionally treated as absent by the shared resolver.
        # Apply the topology override so it becomes the deployment's recorded
        # configuration. A later example setup.sh apply can then remove these
        # cluster-only fields cleanly and restore its portable standalone
        # manifest instead of preserving an out-of-band, mixed topology.
        deployment_config=$(kubectl set env deployment/"$deployment" -n "$NAMESPACE" --containers='*' \
            --dry-run=client -o yaml \
            REDIS_URL="$redis_url" \
            TRUVAG3_REDIS_MODE="$redis_mode" \
            TRUVAG3_REDIS_ADDRS="$redis_addrs" \
            TRUVAG3_REDIS_MASTER_NAME= \
            TRUVAG3_REDIS_USERNAME= \
            TRUVAG3_REDIS_PASSWORD= \
            TRUVAG3_REDIS_SENTINEL_USERNAME= \
            TRUVAG3_REDIS_SENTINEL_PASSWORD= \
            TRUVAG3_REDIS_TLS_ENABLED= \
            TRUVAG3_REDIS_TLS_SERVER_NAME= \
            TRUVAG3_REDIS_CA_FILE= \
            TRUVAG3_REDIS_DB="${redis_mode:+0}" \
            TRUVAG3_REDIS_NAMESPACE="$redis_namespace")
        # kubectl set env emits no object when every value already matches.
        # Still record the current configuration: older helpers may have set
        # the same values without updating the last-applied annotation.
        if [ -z "$deployment_config" ]; then
            deployment_config=$(kubectl get deployment "$deployment" -n "$NAMESPACE" -o yaml)
        fi
        printf '%s\n' "$deployment_config" | kubectl apply -f -
        kubectl rollout status deployment/"$deployment" -n "$NAMESPACE" --timeout=180s
    done

    echo "Configured $# deployment(s) for ${mode}, DB 0, namespace ${redis_namespace}."
    if [ "$mode" = "cluster" ]; then
        echo "Re-run this configure-cluster command after any selected example's setup.sh deploy/rebuild; its portable manifest restores the standalone REDIS_URL default."
    fi
}

show_help() {
    cat <<'EOF'
Usage: ./setup-redis-cluster-validation.sh <command> [distribution]

Commands:
  deploy [redis-oss|valkey-8|valkey-9]  Deploy and verify the three-primary cluster.
  status                               Verify the current cluster.
  configure-cluster <namespace> <deployments...>
                                       Point live test deployments at the cluster.
  configure-standalone <namespace> <deployments...>
                                       Point them back at standalone Redis DB 0.
  cleanup                              Remove only the validation cluster.
  help                                 Show this help.

This lightweight topology is for local live application verification. It does
not test replica promotion and does not replace or delete the normal standalone
`redis` deployment.

Example manifests intentionally default to standalone REDIS_URL. Re-run
configure-cluster after setup.sh deploy/rebuild for every selected deployment.
EOF
}

case "${1:-help}" in
    deploy)  require_tools; deploy_cluster "${2:-redis-oss}" ;;
    status)  require_tools; show_status ;;
    configure-cluster)
        require_tools
        configure_deployments cluster "${2:-}" "${@:3}"
        ;;
    configure-standalone)
        require_tools
        configure_deployments standalone "${2:-}" "${@:3}"
        ;;
    cleanup) require_tools; cleanup_cluster ;;
    help|-h|--help) show_help ;;
    *)
        show_help >&2
        exit 1
        ;;
esac
