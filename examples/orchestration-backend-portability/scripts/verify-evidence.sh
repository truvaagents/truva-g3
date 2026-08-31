#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
NAMESPACE="${PORTABILITY_NAMESPACE:-truvag3-examples}"
export KUBECONFIG="${PORTABILITY_KUBECONFIG:-${KUBECONFIG:-$EXAMPLE_DIR/.state/kubeconfig}}"

fail() {
    printf '[ERROR] %s\n' "$1" >&2
    exit 1
}

postgres_scalar() {
    kubectl exec -n "$NAMESPACE" deployment/portability-postgres -- \
        psql -U portability -d portability -Atc "$1"
}

redis_count="$(kubectl get deployment -n "$NAMESPACE" -o json |
    jq '[.items[] | select(.metadata.name == "redis")] | length')"
[ "$redis_count" = "1" ] || fail "expected exactly one Redis deployment, found $redis_count"

kubectl get configmap -n "$NAMESPACE" portability-backend-config -o json |
    jq -e '.data | (has("POSTGRES_URL") | not) and (has("POSTGRES_PASSWORD") | not)' >/dev/null ||
    fail "PostgreSQL credentials leaked into the ConfigMap"

kubectl get job -n "$NAMESPACE" portability-postgres-migration -o json |
    jq -e '.status.succeeded == 1' >/dev/null || fail "migration Job is not complete"

workflow_count="$(postgres_scalar "SELECT count(*) FROM portability_workflow_executions WHERE payload->>'status' = 'completed'")"
[ "$workflow_count" -ge 1 ] || fail "PostgreSQL has no completed workflow execution"
task_count="$(postgres_scalar "SELECT count(*) FROM portability_tasks WHERE status = 'completed'")"
[ "$task_count" -ge 1 ] || fail "PostgreSQL has no completed scheduled task"

nats_json="$(kubectl exec -n "$NAMESPACE" deployment/portability-nats -- \
    wget -qO- 'http://127.0.0.1:8222/jsz?streams=true&consumers=true&config=true')"
jq -e '
    [.account_details[].stream_detail[]?] as $streams |
    ($streams | length) >= 1 and
    any($streams[];
        .config.retention == "workqueue" and
        .config.storage == "file" and
        any(.config.subjects[]; startswith("truvag3.portability.") and contains(".tasks."))
    ) and
    ([ $streams[].consumer_detail[]? ] | length) >= 2 and
    all($streams[].consumer_detail[]?; .num_ack_pending == 0 and .num_pending == 0)
' >/dev/null <<<"$nats_json" || fail "NATS stream/consumer state does not prove settled work-queue delivery"

target_json="$(kubectl exec -n "$NAMESPACE" deployment/redis -- \
    redis-cli GET truvag3:services:portable-target-agent)"
jq -e '
    .name == "portable-target-agent" and
    .type == "agent" and
    .health == "healthy" and
    (.address | contains("truvag3-examples.svc.cluster.local"))
' >/dev/null <<<"$target_json" || fail "Redis does not contain the healthy included target registration"

lock_ttl=-2
for _ in 1 2 3 4 5 6 7 8 9 10; do
    lock_ttl="$(kubectl exec -n "$NAMESPACE" deployment/redis -- \
        redis-cli TTL truvag3:lock:orchestration-portability:truvag3:scheduler)"
    if [ "$lock_ttl" -gt 0 ]; then
        break
    fi
    sleep 1
done
[ "$lock_ttl" -gt 0 ] || fail "Redis scheduler lock was not observed with a positive TTL"

printf '[SUCCESS] Independent PostgreSQL, NATS JetStream, Redis, migration, secret, and single-Redis checks passed\n'
