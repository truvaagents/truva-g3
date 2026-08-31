#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
NAMESPACE="${PORTABILITY_NAMESPACE:-truvag3-examples}"
export KUBECONFIG="${PORTABILITY_KUBECONFIG:-${KUBECONFIG:-$EXAMPLE_DIR/.state/kubeconfig}}"

printf '=== Kubernetes workloads ===\n'
kubectl get deployment,pod,service,job,ingress -n "$NAMESPACE" \
    -l app.kubernetes.io/part-of=orchestration-backend-portability -o wide

for report in workflows schedules tasks; do
    printf '\n=== PostgreSQL %s ===\n' "$report"
    kubectl exec -i -n "$NAMESPACE" deployment/portability-postgres -- \
        psql -U portability -d portability -v ON_ERROR_STOP=1 \
        <"$SCRIPT_DIR/sql/inspect-${report}.sql"
done

printf '\n=== NATS JetStream ===\n'
kubectl exec -n "$NAMESPACE" deployment/portability-nats -- \
    wget -qO- 'http://127.0.0.1:8222/jsz?streams=true&consumers=true&config=true' |
    jq '{streams: [.account_details[].stream_detail[]? | {
        name,
        subjects: .config.subjects,
        retention: .config.retention,
        storage: .config.storage,
        state,
        consumers: [.consumer_detail[]? | {
            name, num_ack_pending, num_redelivered, num_pending, delivered, ack_floor
        }]
    }]}'

printf '\n=== Redis discovery and scheduler lock ===\n'
kubectl exec -n "$NAMESPACE" deployment/redis -- \
    redis-cli GET truvag3:services:portable-target-agent |
    jq '{id, name, type, address, port, health}'
printf 'scheduler_lock_ttl_seconds='
kubectl exec -n "$NAMESPACE" deployment/redis -- \
    redis-cli TTL truvag3:lock:orchestration-portability:truvag3:scheduler
