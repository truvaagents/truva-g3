#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(dirname "$SCRIPT_DIR")"
TRUVAG3_ROOT="$(dirname "$EXAMPLES_DIR")"

source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"
truvag3_load_env "$SCRIPT_DIR/.env"

CLUSTER_NAME="${TRUVAG3_CLUSTER_NAME:-${PORTABILITY_CLUSTER_NAME:-truvag3-portability-$(whoami)}}"
DEFAULT_CLUSTER_NAME="truvag3-demo-$(whoami)"
NAMESPACE="truvag3-examples"
HTTP_PORT="${PORTABILITY_HTTP_PORT:-18080}"
HTTPS_PORT="${PORTABILITY_HTTPS_PORT:-18443}"
IMAGE_NAME="truvag3-orchestration-portability:local"
LIVE_IMAGE_NAME="truvag3-portable-agent:local"
JOB_NAME="orchestration-backend-portability"
LIVE_API_DEPLOYMENT="portable-agent-api"
LIVE_WORKER_DEPLOYMENT="portable-agent-worker"
LIVE_SERVICE="portable-agent-api"
SCHEDULER_DEPLOYMENT="portable-scheduler-tool"
SCHEDULED_EXECUTOR_DEPLOYMENT="portable-scheduled-executor"
SCHEDULER_SERVICE="portable-scheduler-tool"
TARGET_DEPLOYMENT="portable-target-agent"
STATE_DIR="${PORTABILITY_STATE_DIR:-$SCRIPT_DIR/.state}"
KUBECONFIG_FILE="${PORTABILITY_KUBECONFIG:-$STATE_DIR/kubeconfig}"
RESOURCE_SELECTOR="app.kubernetes.io/part-of=orchestration-backend-portability"
POSTGRES_SECRET="portability-postgres"
MIGRATION_CONFIGMAP="portability-postgres-migrations"
MIGRATION_JOB="portability-postgres-migration"
WORKLOAD_MANIFESTS=(
    "$SCRIPT_DIR/k8-deployment.yaml"
    "$SCRIPT_DIR/k8-deployment-api.yaml"
    "$SCRIPT_DIR/k8-deployment-worker.yaml"
    "$SCRIPT_DIR/k8-deployment-scheduler.yaml"
    "$SCRIPT_DIR/k8-deployment-executor.yaml"
    "$SCRIPT_DIR/k8-deployment-target.yaml"
)

# Every kubectl/kind kubeconfig operation in this script is isolated from the
# user's default kubeconfig. Kind cluster discovery still uses the explicitly
# validated cluster name below.
export KUBECONFIG="$KUBECONFIG_FILE"

log_info() { printf '[INFO] %s\n' "$1"; }
log_success() { printf '[SUCCESS] %s\n' "$1"; }
log_warn() { printf '[WARN] %s\n' "$1"; }
log_error() { printf '[ERROR] %s\n' "$1" >&2; }

validate_scope() {
    if [ -z "$CLUSTER_NAME" ] || [ "$CLUSTER_NAME" = "$DEFAULT_CLUSTER_NAME" ]; then
        log_error "Refusing to target the normal examples cluster '$DEFAULT_CLUSTER_NAME'"
        exit 1
    fi
    case "$HTTP_PORT:$HTTPS_PORT" in
        *[!0-9:]*|:*|*:)
            log_error "PORTABILITY_HTTP_PORT and PORTABILITY_HTTPS_PORT must be numeric"
            exit 1
            ;;
    esac
    if [ "$HTTP_PORT" -lt 1 ] || [ "$HTTP_PORT" -gt 65535 ] ||
       [ "$HTTPS_PORT" -lt 1 ] || [ "$HTTPS_PORT" -gt 65535 ] ||
       [ "$HTTP_PORT" -eq "$HTTPS_PORT" ]; then
        log_error "Ingress host ports must be distinct integers between 1 and 65535"
        exit 1
    fi
}

check_prerequisites() {
    local missing=()
    local command_name
    for command_name in go kind kubectl curl jq openssl "${TRUVAG3_CONTAINER_RUNTIME:-docker}"; do
        if ! command -v "$command_name" >/dev/null 2>&1; then
            missing+=("$command_name")
        fi
    done
    if [ ${#missing[@]} -gt 0 ]; then
        log_error "Missing required commands: ${missing[*]}"
        exit 1
    fi
    mkdir -p "$STATE_DIR"
}

cluster_exists() {
    command kind get clusters 2>/dev/null | grep -Fxq "$CLUSTER_NAME"
}

port_is_listening() {
    local port="$1"
    if command -v lsof >/dev/null 2>&1; then
        lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
        return
    fi
    if command -v nc >/dev/null 2>&1; then
        nc -z 127.0.0.1 "$port" >/dev/null 2>&1
        return
    fi
    log_error "Cannot check host port $port because neither lsof nor nc is installed"
    exit 1
}

check_ingress_ports() {
    local port
    for port in "$HTTP_PORT" "$HTTPS_PORT"; do
        if port_is_listening "$port"; then
            log_error "Host port $port is already in use; set PORTABILITY_HTTP_PORT/PORTABILITY_HTTPS_PORT"
            exit 1
        fi
    done
}

create_cluster() {
    if cluster_exists; then
        log_info "Reusing dedicated Kind cluster '$CLUSTER_NAME'"
        command kind export kubeconfig --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG_FILE"
        return
    fi

    check_ingress_ports
    log_info "Creating isolated Kind cluster '$CLUSTER_NAME' on host ports $HTTP_PORT/$HTTPS_PORT"
    command kind create cluster \
        --name "$CLUSTER_NAME" \
        --kubeconfig "$KUBECONFIG_FILE" \
        --wait 120s \
        --config=- <<KIND_CONFIG
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: $HTTP_PORT
    protocol: TCP
  - containerPort: 443
    hostPort: $HTTPS_PORT
    protocol: TCP
KIND_CONFIG
    log_success "Dedicated Kind cluster is ready"
}

require_cluster() {
    if ! cluster_exists; then
        log_error "Dedicated cluster '$CLUSTER_NAME' does not exist; run ./setup.sh full-deploy"
        exit 1
    fi
    command kind export kubeconfig --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG_FILE"
    kubectl cluster-info >/dev/null
}

ensure_namespace() {
    kubectl apply -f "$EXAMPLES_DIR/k8-deployment/namespace.yaml" >/dev/null
}

ensure_postgres_secret() {
    local password
    if kubectl get secret -n "$NAMESPACE" "$POSTGRES_SECRET" >/dev/null 2>&1; then
        log_info "Reusing generated PostgreSQL Secret '$POSTGRES_SECRET'"
        return
    fi
    password="${PORTABILITY_POSTGRES_PASSWORD:-$(openssl rand -hex 24)}"
    kubectl create secret generic "$POSTGRES_SECRET" -n "$NAMESPACE" \
        --from-literal=POSTGRES_USER=portability \
        --from-literal=POSTGRES_PASSWORD="$password" \
        --from-literal=POSTGRES_DB=portability \
        --from-literal=POSTGRES_URL="postgres://portability:${password}@portability-postgres:5432/portability?sslmode=disable" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    kubectl label secret -n "$NAMESPACE" "$POSTGRES_SECRET" "$RESOURCE_SELECTOR" --overwrite >/dev/null
    log_success "Generated PostgreSQL credentials in Kubernetes Secret '$POSTGRES_SECRET'"
}

deploy_shared_infrastructure() {
    ensure_namespace
    log_info "Deploying the shared examples Redis and ingress controller"
    kubectl apply -f "$EXAMPLES_DIR/k8-deployment/redis.yaml" >/dev/null
    kubectl apply -f "$EXAMPLES_DIR/k8-deployment/ingress-nginx.yaml" >/dev/null
    kubectl rollout status -n "$NAMESPACE" deployment/redis --timeout=180s
    kubectl rollout status -n ingress-nginx deployment/ingress-nginx-controller --timeout=300s
    log_success "Shared Redis and ingress are ready"
}

deploy_reference_infrastructure() {
    ensure_postgres_secret
    log_info "Deploying reference-owned PostgreSQL and NATS JetStream"
    kubectl apply -f "$SCRIPT_DIR/k8-deployment-infra.yaml" >/dev/null
    kubectl rollout status -n "$NAMESPACE" deployment/portability-postgres --timeout=180s
    kubectl rollout status -n "$NAMESPACE" deployment/portability-nats --timeout=180s
    log_success "PostgreSQL and NATS JetStream are ready"
}

run_migration() {
    log_info "Applying explicit PostgreSQL migration before application rollout"
    kubectl create configmap "$MIGRATION_CONFIGMAP" -n "$NAMESPACE" \
        --from-file=001_create_orchestration_tables.sql="$SCRIPT_DIR/migrations/001_create_orchestration_tables.sql" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    kubectl label configmap -n "$NAMESPACE" "$MIGRATION_CONFIGMAP" "$RESOURCE_SELECTOR" --overwrite >/dev/null
    kubectl delete job -n "$NAMESPACE" "$MIGRATION_JOB" --ignore-not-found --wait=true >/dev/null
    kubectl apply -f "$SCRIPT_DIR/k8-deployment-migration.yaml" >/dev/null
    if ! kubectl wait -n "$NAMESPACE" --for=condition=complete "job/$MIGRATION_JOB" --timeout=180s; then
        kubectl logs -n "$NAMESPACE" "job/$MIGRATION_JOB" --all-containers=true || true
        return 1
    fi
    log_success "PostgreSQL migration completed"
}

build_image() {
    local no_cache="${1:-}"
    local node_arch
    node_arch="$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')"
    if [ -z "$node_arch" ]; then
        log_error "Could not determine the Kind node architecture"
        exit 1
    fi

    mkdir -p "$SCRIPT_DIR/.build"
    log_info "Compiling the Linux/$node_arch conformance test binary against local framework sources"
    (
        cd "$SCRIPT_DIR"
        GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$node_arch" go test -c -o .build/portability.test .
    )

    if [ "$no_cache" = "--no-cache" ]; then
        truvag3_build_docker "$IMAGE_NAME" "$SCRIPT_DIR/Dockerfile.test" "$SCRIPT_DIR" --no-cache
    else
        truvag3_build_docker "$IMAGE_NAME" "$SCRIPT_DIR/Dockerfile.test" "$SCRIPT_DIR"
    fi
    truvag3_load_to_kind "$IMAGE_NAME" "$CLUSTER_NAME"
}

build_live_image() {
    local no_cache="${1:-}"
    log_info "Building the portable application against local framework modules"
    if [ "$no_cache" = "--no-cache" ]; then
        truvag3_build_docker "$LIVE_IMAGE_NAME" "$SCRIPT_DIR/Dockerfile.workspace" "$TRUVAG3_ROOT" --no-cache
    else
        truvag3_build_docker "$LIVE_IMAGE_NAME" "$SCRIPT_DIR/Dockerfile.workspace" "$TRUVAG3_ROOT"
    fi
    truvag3_load_to_kind "$LIVE_IMAGE_NAME" "$CLUSTER_NAME"
}

deploy_workloads() {
    local restart="${1:-}"
    local manifest
    log_info "Deploying the complete self-contained portability reference"
    for manifest in "${WORKLOAD_MANIFESTS[@]}"; do
        kubectl apply -f "$manifest" >/dev/null
    done
    if [ "$restart" = "--restart" ]; then
        kubectl rollout restart -n "$NAMESPACE" \
            "deployment/$LIVE_API_DEPLOYMENT" \
            "deployment/$LIVE_WORKER_DEPLOYMENT" \
            "deployment/$SCHEDULER_DEPLOYMENT" \
            "deployment/$SCHEDULED_EXECUTOR_DEPLOYMENT" \
            "deployment/$TARGET_DEPLOYMENT" >/dev/null
    fi
    kubectl rollout status -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" --timeout=180s
    kubectl rollout status -n "$NAMESPACE" "deployment/$LIVE_API_DEPLOYMENT" --timeout=180s
    kubectl rollout status -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --timeout=180s
    kubectl rollout status -n "$NAMESPACE" "deployment/$SCHEDULER_DEPLOYMENT" --timeout=180s
    kubectl rollout status -n "$NAMESPACE" "deployment/$SCHEDULED_EXECUTOR_DEPLOYMENT" --timeout=180s
    log_success "All portability reference workloads are ready"
}

deploy_complete() {
    local no_cache="${1:-}"
    deploy_shared_infrastructure
    deploy_reference_infrastructure
    run_migration
    build_live_image "$no_cache"
    build_image "$no_cache"
    deploy_workloads --restart
}

verify_live() (
    local forward_log forward_pid response execution_id status deadline result
    forward_log="$(mktemp)"
    kubectl port-forward -n "$NAMESPACE" "service/$LIVE_SERVICE" 8394:80 >"$forward_log" 2>&1 &
    forward_pid=$!
    cleanup_forward() {
        trap - EXIT INT TERM
        kill "$forward_pid" >/dev/null 2>&1 || true
        wait "$forward_pid" >/dev/null 2>&1 || true
        rm -f "$forward_log"
    }
    trap cleanup_forward EXIT INT TERM

    deadline=$((SECONDS + 30))
    until curl -fsS http://127.0.0.1:8394/health >/dev/null 2>&1; do
        if [ "$SECONDS" -ge "$deadline" ]; then
            log_error "Portable API port-forward did not become ready"
            cat "$forward_log" >&2
            return 1
        fi
        sleep 1
    done

    log_info "Checking the inspectable provider selection"
    response="$(curl -fsS http://127.0.0.1:8394/backends)"
    if ! jq -e '
        .validated == true and
        .selected_backends.workflow_state.provider == "postgresql" and
        .selected_backends.task_dispatcher.provider == "nats-jetstream" and
        (.selected_backends | has("commands") | not) and
        (.selected_backends | has("task_consumer") | not) and
        (.selected_backends | has("discovery") | not)
    ' >/dev/null <<<"$response"; then
        log_error "Backend inspection did not report the expected provider mix"
        printf '%s\n' "$response"
        return 1
    fi

    log_info "Submitting deterministic work through API -> NATS JetStream"
    response="$(curl -fsS -X POST http://127.0.0.1:8394/tasks -H 'Content-Type: application/json' -d '{"location":"Chicago, IL"}')"
    execution_id="$(jq -r '.execution_id // empty' <<<"$response")"
    if [ -z "$execution_id" ]; then
        log_error "Task submission did not return an execution ID"
        printf '%s\n' "$response"
        return 1
    fi

    deadline=$((SECONDS + 90))
    while [ "$SECONDS" -lt "$deadline" ]; do
        result="$(curl -fsS "http://127.0.0.1:8394/tasks/$execution_id")"
        status="$(jq -r '.status // empty' <<<"$result")"
        case "$status" in
            completed)
                if ! jq -e '
                    (.outputs.summary | startswith("portable task ")) and
                    (.outputs | has("backend_proof") | not)
                ' >/dev/null <<<"$result"; then
                    log_error "Completed execution omitted the deterministic result or contained self-reported provider claims"
                    printf '%s\n' "$result"
                    return 1
                fi
                printf '%s\n' "$result" | jq .
                log_success "Live API -> NATS worker -> PostgreSQL proof passed"
                return 0
                ;;
            failed)
                log_error "Portable execution failed"
                printf '%s\n' "$result" | jq .
                return 1
                ;;
        esac
        sleep 2
    done
    log_error "Portable execution timed out"
    printf '%s\n' "$result" | jq .
    return 1
)

verify_scheduler() (
    local expected_attempt="${1:-1}"
    local forward_log forward_pid response schedule_id run_at task_id status deadline result materialized
    forward_log="$(mktemp)"
    kubectl port-forward -n "$NAMESPACE" "service/$SCHEDULER_SERVICE" 8395:80 >"$forward_log" 2>&1 &
    forward_pid=$!
    cleanup_forward() {
        trap - EXIT INT TERM
        kill "$forward_pid" >/dev/null 2>&1 || true
        wait "$forward_pid" >/dev/null 2>&1 || true
        rm -f "$forward_log"
    }
    trap cleanup_forward EXIT INT TERM

    deadline=$((SECONDS + 30))
    until curl -fsS http://127.0.0.1:8395/health >/dev/null 2>&1; do
        if [ "$SECONDS" -ge "$deadline" ]; then
            log_error "Portable scheduler port-forward did not become ready"
            cat "$forward_log" >&2
            return 1
        fi
        sleep 1
    done

    log_info "Checking the scheduler's inspectable provider selection"
    response="$(curl -fsS -X POST http://127.0.0.1:8395/api/capabilities/portability_backend_status -H 'Content-Type: application/json' -d '{}')"
    if ! jq -e '
        .success == true and
        .data.validated == true and
        .data.selected_backends.schedules.provider == "postgresql" and
        .data.selected_backends.tasks.provider == "postgresql" and
        .data.selected_backends.task_dispatcher.provider == "nats-jetstream" and
        .data.selected_backends.lock.provider == "redis" and
        (.data.selected_backends | has("task_consumer") | not) and
        (.data.selected_backends | has("discovery") | not)
    ' >/dev/null <<<"$response"; then
        log_error "Scheduler backend inspection did not report the expected provider mix"
        printf '%s\n' "$response"
        return 1
    fi

    log_info "Creating a one-shot deterministic target schedule in PostgreSQL"
    response="$(curl -fsS -X POST http://127.0.0.1:8395/api/capabilities/schedule_task \
        -H 'Content-Type: application/json' \
        -H 'X-TruvaG3-Agent-Name: portable-target-agent' \
        -d '{"delay":"2s","input":{"instruction":"Produce the deterministic portability response."}}')"
    schedule_id="$(jq -r '.data.schedule_id // empty' <<<"$response")"
    run_at="$(jq -r '.data.run_at // empty' <<<"$response")"
    if [ -z "$schedule_id" ] || [ -z "$run_at" ]; then
        log_error "Schedule creation did not return an ID and run time"
        printf '%s\n' "$response"
        return 1
    fi
    task_id="$(jq -nr --arg schedule_id "$schedule_id" --arg run_at "$run_at" \
        '$schedule_id + ":" + (($run_at | fromdateiso8601) | tostring)')"

    log_info "Waiting for PostgreSQL scheduler -> NATS -> Redis-discovered deterministic target"
    deadline=$((SECONDS + 180))
    result='{}'
    while [ "$SECONDS" -lt "$deadline" ]; do
        response="$(curl -sS -X POST http://127.0.0.1:8395/api/capabilities/portability_task_status \
            -H 'Content-Type: application/json' \
            -d "{\"task_id\":\"$task_id\"}")"
        if jq -e '.success == true' >/dev/null 2>&1 <<<"$response"; then
            result="$response"
            status="$(jq -r '.data.task.status // empty' <<<"$result")"
            case "$status" in
                completed)
                    if ! jq -e '
                        .data.task.result.response != null and
                        (.data | has("backend_proof") | not) and
                        (.data.task.result | has("backend_proof") | not)
                    ' >/dev/null <<<"$result"; then
                        log_error "Completed scheduled task omitted the target response or contained self-reported provider claims"
                        printf '%s\n' "$result" | jq .
                        return 1
                    fi
                    if ! jq -e --argjson expected_attempt "$expected_attempt" '
                        .data.task.result.response.data.response.metadata.attempt == $expected_attempt
                    ' >/dev/null <<<"$result"; then
                        log_error "Target attempt count did not prove the expected request behavior"
                        printf '%s\n' "$result" | jq .
                        return 1
                    fi
                    materialized="$(kubectl exec -n "$NAMESPACE" deployment/portability-postgres -- \
                        psql -U portability -d portability -Atc \
                        "SELECT count(*) FROM portability_tasks WHERE task_id = '$task_id'")"
                    if [ "$materialized" != "1" ]; then
                        log_error "Expected exactly one PostgreSQL task for two scheduler replicas; found $materialized"
                        return 1
                    fi
                    printf '%s\n' "$result" | jq .
                    log_success "Scheduler proof passed with exactly one PostgreSQL task and target attempt $expected_attempt"
                    return 0
                    ;;
                failed|cancelled)
                    log_error "Portable scheduled execution reached terminal status '$status'"
                    printf '%s\n' "$result" | jq .
                    return 1
                    ;;
            esac
        fi
        sleep 2
    done
    log_error "Portable scheduled execution timed out"
    printf '%s\n' "$result" | jq .
    return 1
)

verify_worker_restart() (
    local forward_log forward_pid response execution_id status deadline result
    forward_log="$(mktemp)"
    forward_pid=""
    restore_workers() {
        trap - EXIT INT TERM
        if [ -n "$forward_pid" ]; then
            kill "$forward_pid" >/dev/null 2>&1 || true
            wait "$forward_pid" >/dev/null 2>&1 || true
        fi
        kubectl scale -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --replicas=2 >/dev/null 2>&1 || true
        kubectl rollout status -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --timeout=180s >/dev/null 2>&1 || true
        rm -f "$forward_log"
    }
    trap restore_workers EXIT INT TERM

    log_info "Failure check: holding work in NATS while all workers are stopped"
    kubectl scale -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --replicas=0 >/dev/null
    kubectl wait -n "$NAMESPACE" --for=delete pod -l app="$LIVE_WORKER_DEPLOYMENT" --timeout=90s >/dev/null
    kubectl port-forward -n "$NAMESPACE" "service/$LIVE_SERVICE" 8396:80 >"$forward_log" 2>&1 &
    forward_pid=$!
    deadline=$((SECONDS + 30))
    until curl -fsS http://127.0.0.1:8396/health >/dev/null 2>&1; do
        if [ "$SECONDS" -ge "$deadline" ]; then
            cat "$forward_log" >&2
            return 1
        fi
        sleep 1
    done
    response="$(curl -fsS -X POST http://127.0.0.1:8396/tasks \
        -H 'Content-Type: application/json' -d '{"location":"restart-recovery"}')"
    execution_id="$(jq -r '.execution_id // empty' <<<"$response")"
    result="$(curl -fsS "http://127.0.0.1:8396/tasks/$execution_id")"
    if [ "$(jq -r '.status' <<<"$result")" != "pending" ]; then
        log_error "Execution should remain pending with no workers"
        printf '%s\n' "$result" | jq .
        return 1
    fi

    log_info "Restarting workers and waiting for the retained NATS task"
    kubectl scale -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --replicas=2 >/dev/null
    kubectl rollout status -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --timeout=180s >/dev/null
    deadline=$((SECONDS + 90))
    while [ "$SECONDS" -lt "$deadline" ]; do
        result="$(curl -fsS "http://127.0.0.1:8396/tasks/$execution_id")"
        status="$(jq -r '.status // empty' <<<"$result")"
        if [ "$status" = "completed" ]; then
            log_success "Pending NATS work survived complete worker shutdown and restart"
            return 0
        fi
        if [ "$status" = "failed" ]; then
            printf '%s\n' "$result" | jq .
            return 1
        fi
        sleep 2
    done
    log_error "Worker restart recovery timed out"
    return 1
)

verify_failures() (
    restore_target() {
        trap - EXIT INT TERM
        kubectl set env -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" PORTABILITY_TARGET_FAIL_FIRST- >/dev/null 2>&1 || true
        kubectl rollout restart -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" >/dev/null 2>&1 || true
        kubectl rollout status -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" --timeout=180s >/dev/null 2>&1 || true
    }
    trap restore_target EXIT INT TERM

    run_proof
    verify_worker_restart
    log_info "Failure check: target returns one HTTP 500 before succeeding on retry"
    kubectl set env -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" PORTABILITY_TARGET_FAIL_FIRST=1 >/dev/null
    kubectl rollout restart -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" >/dev/null
    kubectl rollout status -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" --timeout=180s >/dev/null
    configured_failures="$(kubectl get deployment -n "$NAMESPACE" "$TARGET_DEPLOYMENT" -o \
        jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="PORTABILITY_TARGET_FAIL_FIRST")].value}')"
    if [ "$configured_failures" != "1" ]; then
        log_error "Target failure injection was not present on the rolled-out pod template"
        return 1
    fi
    verify_scheduler 2
    log_success "Failure and recovery checks passed"
)

run_proof() {
    local succeeded failed deadline
    ensure_namespace
    kubectl delete job -n "$NAMESPACE" "$JOB_NAME" --ignore-not-found --wait=true >/dev/null
    log_info "Running provider conformance and mixed-composition proof"
    kubectl apply -f "$SCRIPT_DIR/k8-deployment-test.yaml" >/dev/null
    deadline=$((SECONDS + 180))
    while [ "$SECONDS" -lt "$deadline" ]; do
        read -r succeeded failed <<< "$(kubectl get job -n "$NAMESPACE" "$JOB_NAME" -o jsonpath='{.status.succeeded} {.status.failed}')"
        if [ "${succeeded:-0}" -ge 1 ]; then
            kubectl logs -n "$NAMESPACE" "job/$JOB_NAME" --all-containers=true
            log_success "Provider conformance and mixed-composition proof passed"
            return 0
        fi
        if [ "${failed:-0}" -ge 1 ]; then
            log_error "Portability proof failed"
            kubectl logs -n "$NAMESPACE" "job/$JOB_NAME" --all-containers=true || true
            kubectl describe job -n "$NAMESPACE" "$JOB_NAME" || true
            return 1
        fi
        sleep 2
    done
    log_error "Portability proof timed out"
    kubectl logs -n "$NAMESPACE" "job/$JOB_NAME" --all-containers=true || true
    kubectl describe job -n "$NAMESPACE" "$JOB_NAME" || true
    return 1
}

show_status() {
    require_cluster
    kubectl get pods,services,jobs,ingress -n "$NAMESPACE" -o wide
    printf '\nReference URLs:\n'
    printf '  API:       http://portability-agent.localhost:%s\n' "$HTTP_PORT"
    printf '  Scheduler: http://portability-scheduler.localhost:%s\n' "$HTTP_PORT"
    printf '  Target:    http://portability-target.localhost:%s\n' "$HTTP_PORT"
}

show_live_logs() {
    require_cluster
    log_info "Portable API logs"
    kubectl logs -n "$NAMESPACE" "deployment/$LIVE_API_DEPLOYMENT" --all-pods=true --tail=100
    log_info "Portable worker logs"
    kubectl logs -n "$NAMESPACE" "deployment/$LIVE_WORKER_DEPLOYMENT" --all-pods=true --tail=100
}

show_scheduler_logs() {
    require_cluster
    log_info "Portable scheduler logs"
    kubectl logs -n "$NAMESPACE" "deployment/$SCHEDULER_DEPLOYMENT" --all-pods=true --tail=150
    log_info "Portable scheduled executor logs"
    kubectl logs -n "$NAMESPACE" "deployment/$SCHEDULED_EXECUTOR_DEPLOYMENT" --all-pods=true --tail=150
    log_info "Portable deterministic target logs"
    kubectl logs -n "$NAMESPACE" "deployment/$TARGET_DEPLOYMENT" --all-pods=true --tail=100
}

show_logs() {
    require_cluster
    show_live_logs
    show_scheduler_logs
    if kubectl get job -n "$NAMESPACE" "$JOB_NAME" >/dev/null 2>&1; then
        log_info "Latest conformance logs"
        kubectl logs -n "$NAMESPACE" "job/$JOB_NAME" --all-containers=true
    fi
}

show_evidence() {
    require_cluster
    "$SCRIPT_DIR/scripts/collect-evidence.sh"
    "$SCRIPT_DIR/scripts/verify-evidence.sh"
}

forward_services() {
    require_cluster
    log_info "Fallback forwards: API 18081, scheduler 18082, target 18083"
    truvag3_forward_all \
        "$LIVE_SERVICE:18081:80" \
        "$SCHEDULER_SERVICE:18082:80" \
        "$TARGET_DEPLOYMENT:18083:80"
}

cleanup_reference() {
    require_cluster
    log_info "Deleting only portability-owned resources from '$NAMESPACE'"
    kubectl delete all,configmap,secret,ingress -n "$NAMESPACE" \
        -l "$RESOURCE_SELECTOR" --ignore-not-found --wait=true
    log_success "Reference resources removed; shared namespace, Redis, ingress, and cluster retained"
}

delete_cluster() {
    if ! cluster_exists; then
        log_info "Dedicated cluster '$CLUSTER_NAME' is already absent"
        return
    fi
    log_info "Deleting only dedicated Kind cluster '$CLUSTER_NAME'"
    command kind delete cluster --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG_FILE"
    log_success "Dedicated portability cluster removed"
}

print_help() {
    cat <<EOF
Usage: ./setup.sh <command>

Commands:
  full-deploy  Create the parallel cluster and deploy/test the complete reference
  deploy       Build and deploy/test the complete reference in the existing cluster
  rebuild      Force no-cache image builds, redeploy, and rerun all default tests
  rollout      Reapply configuration/migration and restart workloads without building
  conformance-test  Run provider conformance and mixed-composition tests
  live-test    Verify API -> NATS -> deterministic worker -> PostgreSQL end to end
  scheduler-test    Verify PostgreSQL -> NATS -> Redis-discovered included target
  failure-test Verify redelivery, worker restart, target retry, and scheduler idempotency
  evidence     Print and independently verify PostgreSQL, NATS, and Redis state
  status       Show workloads, infrastructure, jobs, ingress, and URLs
  logs         Show application and latest conformance logs
  forward      Forward API/scheduler/target to 18081/18082/18083 as a fallback
  cleanup      Delete only portability-owned resources; retain shared infrastructure
  cleanup-all  Delete only the dedicated portability Kind cluster
  help         Show this help

Isolation:
  Cluster:    $CLUSTER_NAME
  Namespace:  $NAMESPACE
  Kubeconfig: $KUBECONFIG_FILE
  HTTP:       $HTTP_PORT
  HTTPS:      $HTTPS_PORT
EOF
}

main() {
    validate_scope
    case "${1:-help}" in
        full-deploy)
            check_prerequisites
            create_cluster
            deploy_complete
            run_proof
            verify_live
            verify_scheduler
            ;;
        deploy)
            check_prerequisites
            require_cluster
            deploy_complete
            run_proof
            verify_live
            verify_scheduler
            ;;
        rebuild)
            check_prerequisites
            require_cluster
            deploy_complete --no-cache
            run_proof
            verify_live
            verify_scheduler
            ;;
        rollout)
            check_prerequisites
            require_cluster
            deploy_shared_infrastructure
            deploy_reference_infrastructure
            run_migration
            deploy_workloads --restart
            ;;
        conformance-test)
            check_prerequisites
            require_cluster
            run_proof
            ;;
        live-test)
            check_prerequisites
            require_cluster
            verify_live
            ;;
        scheduler-test)
            check_prerequisites
            require_cluster
            verify_scheduler
            ;;
        failure-test)
            check_prerequisites
            require_cluster
            verify_failures
            ;;
        evidence)
            check_prerequisites
            show_evidence
            ;;
        status)
            check_prerequisites
            show_status
            ;;
        logs)
            check_prerequisites
            show_logs
            ;;
        forward)
            check_prerequisites
            forward_services
            ;;
        cleanup)
            check_prerequisites
            cleanup_reference
            ;;
        cleanup-all)
            check_prerequisites
            delete_cluster
            ;;
        help|-h|--help)
            print_help
            ;;
        *)
            log_error "Unknown command: $1"
            print_help
            exit 1
            ;;
    esac
}

main "$@"
