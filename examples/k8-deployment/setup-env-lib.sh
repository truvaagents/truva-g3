#!/bin/bash
# examples/k8-deployment/setup-env-lib.sh
# Shared library for TruvaG3 Kubernetes deployments.
#
# Usage: source this file from individual setup.sh scripts.
#   source "$EXAMPLES_DIR/k8-deployment/setup-env-lib.sh"
#
# Provides:
#   Cluster & Infrastructure:
#     truvag3_create_cluster       - Create Kind cluster with NGINX Ingress Controller
#     truvag3_setup_infra          - Deploy shared infrastructure (Redis, Prometheus, etc.)
#     truvag3_verify_ingress       - Verify ingress routes are reachable
#     truvag3_check_prerequisites  - Check required tools (go, docker, kind, kubectl)
#
#   Secrets & Config:
#     truvag3_create_secret        - Create K8s Secret with AI provider + AWS keys from env
#     truvag3_create_configmap     - Create K8s ConfigMap with TRUVAG3_* + non-TRUVAG3 config from .env file
#     truvag3_create_tool_secret   - Create K8s Secret with only tool-specific keys
#
# Expected variables (set by the caller before sourcing):
#   CLUSTER_NAME              - Kind cluster name (default: truvag3-demo-$(whoami))
#   NAMESPACE                 - K8s namespace (default: truvag3-examples)
#   EXAMPLES_DIR              - Path to examples/ directory (auto-detected if not set)
#   TRUVAG3_DEV_DISABLE_OPENAPI - Optional: set to "true" to suppress the dev-only
#                               default of TRUVAG3_ENABLE_OPENAPI=true injected
#                               into every tool/agent ConfigMap. See docs/operations/DEV_TOOLS_GUIDE.md.
#
# Dev-only defaults injected by this library:
#   TRUVAG3_ENABLE_OPENAPI=true - Enables the /openapi.json endpoint on every
#                                tool/agent so the example kind-cluster Swagger
#                                UI can discover and render specs. This is NOT
#                                shipped with the framework — it is a dev-only
#                                convenience of the example scripts. Production
#                                deployments managed by SRE pipelines never
#                                source this library and must opt in explicitly.
#                                A .env file that sets TRUVAG3_ENABLE_OPENAPI
#                                (to any value) always wins over this default.
#
# Variable registry maintained here is the single source of truth.
# When adding a new AI provider, update ONLY this file.
# See docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md for full reference.

# ─── MASTER VARIABLE REGISTRY ───────────────────────────────────────────────

# AI provider API keys (go into K8s Secrets)
# Source: ai/providers/*/factory.go
TRUVAG3_AI_PROVIDER_KEYS="OPENAI_API_KEY ANTHROPIC_API_KEY GROQ_API_KEY DEEPSEEK_API_KEY XAI_API_KEY MISTRAL_API_KEY QWEN_API_KEY TOGETHER_API_KEY GEMINI_API_KEY GOOGLE_API_KEY"

# AWS credential keys (go into K8s Secrets)
# Source: ai/providers/bedrock/factory.go
TRUVAG3_AWS_KEYS="AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_REGION"

# Combined: all standard secret keys
TRUVAG3_ALL_SECRET_KEYS="$TRUVAG3_AI_PROVIDER_KEYS $TRUVAG3_AWS_KEYS"

# Non-TRUVAG3 configuration variables (go into K8s ConfigMaps)
# These are NOT secrets. API keys are handled separately.
# Source: core/config.go, telemetry/logger.go, orchestration/capability_provider.go, examples
#
# Core/Example:   APP_ENV, DEV_MODE
# Telemetry:      OTEL_SERVICE_NAME, TELEMETRY_DEBUG
# Orchestration:  CAPABILITY_SERVICE_URL
# Provider URLs:  OPENAI_BASE_URL, ANTHROPIC_BASE_URL, etc.
# Async:          WORKER_COUNT
#
# NOTE: PORT, NAMESPACE, REDIS_URL, and OTEL_EXPORTER_OTLP_ENDPOINT are
# intentionally excluded. These are infrastructure addresses that differ
# between local dev (localhost) and K8s (cluster-internal DNS). The .env
# values (e.g., localhost:4318, localhost:6379) would override the correct
# K8s values from the static ConfigMap or explicit env entries in the
# deployment spec. Pass them as extra_vars if explicitly needed.
TRUVAG3_CONFIG_INCLUDE_VARS="APP_ENV DEV_MODE OTEL_SERVICE_NAME TELEMETRY_DEBUG CAPABILITY_SERVICE_URL OPENAI_BASE_URL ANTHROPIC_BASE_URL GROQ_BASE_URL DEEPSEEK_BASE_URL XAI_BASE_URL MISTRAL_BASE_URL QWEN_BASE_URL TOGETHER_BASE_URL GEMINI_BASE_URL OLLAMA_BASE_URL WORKER_COUNT"

# ─── LOGGING SHIM ───────────────────────────────────────────────────────────
# Delegates to the caller's logging functions if available.

_truvag3_log_info() {
    if type print_info &>/dev/null; then
        print_info "$1"
    elif type log_info &>/dev/null; then
        log_info "$1"
    else
        echo "[INFO] $1"
    fi
}

_truvag3_log_success() {
    if type print_success &>/dev/null; then
        print_success "$1"
    elif type log_success &>/dev/null; then
        log_success "$1"
    else
        echo "[SUCCESS] $1"
    fi
}

# ─── FUNCTIONS ───────────────────────────────────────────────────────────────

# truvag3_create_secret <secret_name> <namespace> [extra_keys...]
#
# Creates a K8s Secret containing AI provider keys + AWS keys.
# Only includes keys where the env variable is non-empty.
# Extra tool-specific keys can be appended as additional arguments.
#
# Arguments:
#   secret_name   - Name for the K8s secret (e.g., "ai-provider-keys-telemetry-agent")
#   namespace     - K8s namespace
#   extra_keys... - Optional additional key names (e.g., "FINNHUB_API_KEY")
#
# Examples:
#   truvag3_create_secret "ai-provider-keys-telemetry-agent" "$NAMESPACE"
#   truvag3_create_secret "ai-provider-keys-chat-agent" "$NAMESPACE" "GNEWS_API_KEY"
truvag3_create_secret() {
    local secret_name="$1"
    local namespace="$2"
    shift 2
    local extra_keys=("$@")

    # Combine standard keys with any extras
    local all_keys="$TRUVAG3_ALL_SECRET_KEYS"
    for key in "${extra_keys[@]}"; do
        all_keys="$all_keys $key"
    done

    local kubectl_args=""
    local key_count=0

    _truvag3_log_info "Setting up K8s Secret: $secret_name"

    for key in $all_keys; do
        local value="${!key}"
        if [ -n "$value" ]; then
            echo "  $key=***"
            kubectl_args="$kubectl_args --from-literal=$key=$value"
            ((++key_count))
        fi
    done

    if [ $key_count -eq 0 ]; then
        _truvag3_log_info "No keys found in environment"
        kubectl create secret generic "$secret_name" \
            -n "$namespace" --dry-run=client -o yaml | kubectl apply -n "$namespace" -f -
    else
        _truvag3_log_info "Found $key_count keys"
        eval "kubectl create secret generic \"$secret_name\" $kubectl_args \
            -n \"$namespace\" --dry-run=client -o yaml" | kubectl apply -n "$namespace" -f -
    fi

    _truvag3_log_success "Secret '$secret_name' configured"
}

# truvag3_create_configmap <configmap_name> <namespace> <env_file_path> [extra_vars...]
#
# Creates a K8s ConfigMap from a .env file containing:
#   - All TRUVAG3_* variables
#   - Curated non-TRUVAG3 config variables (from TRUVAG3_CONFIG_INCLUDE_VARS)
#   - Any additional variable names passed as extra arguments
#
# Only includes variables that are present and non-empty in the .env file.
# API keys are excluded (those go in Secrets via truvag3_create_secret).
#
# Arguments:
#   configmap_name - Name for the K8s ConfigMap
#   namespace      - K8s namespace
#   env_file_path  - Absolute path to the .env file
#   extra_vars...  - Optional additional variable names (e.g., "GROCERY_API_URL")
#
# Examples:
#   truvag3_create_configmap "my-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env"
#   truvag3_create_configmap "my-agent-env-config" "$NAMESPACE" "$SCRIPT_DIR/.env" "GROCERY_API_URL"
truvag3_create_configmap() {
    local configmap_name="$1"
    local namespace="$2"
    local env_file="$3"
    shift 3
    local extra_vars=("$@")

    _truvag3_log_info "Setting up K8s ConfigMap: $configmap_name"

    if [ ! -f "$env_file" ]; then
        _truvag3_log_info "No .env file found at $env_file, applying dev defaults only"
        local dev_defaults=""
        if [[ "${TRUVAG3_DEV_DISABLE_OPENAPI:-false}" != "true" ]]; then
            dev_defaults="--from-literal=TRUVAG3_ENABLE_OPENAPI=true"
        fi
        eval "kubectl create configmap \"$configmap_name\" $dev_defaults \
            -n \"$namespace\" --dry-run=client -o yaml" | kubectl apply -n "$namespace" -f -
        return
    fi

    # Build the combined include list
    local include_vars="$TRUVAG3_CONFIG_INCLUDE_VARS"
    for var in "${extra_vars[@]}"; do
        include_vars="$include_vars $var"
    done

    local kubectl_args=""
    local var_count=0

    _truvag3_log_info "Configuration (from .env):"

    while IFS= read -r line || [[ -n "$line" ]]; do
        # Skip comments and empty lines
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        [[ -z "$line" ]] && continue

        local key="${line%%=*}"
        local value="${line#*=}"

        key=$(echo "$key" | xargs)
        [[ -z "$key" ]] && continue

        # Include: TRUVAG3_* or in include_vars list
        local should_include=false
        if [[ "$key" == TRUVAG3_* ]]; then
            should_include=true
        else
            for var in $include_vars; do
                if [[ "$key" == "$var" ]]; then
                    should_include=true
                    break
                fi
            done
        fi

        [[ "$should_include" != "true" ]] && continue

        # Clean up value
        value="${value#\"}"
        value="${value%\"}"
        value=$(echo "$value" | xargs)

        [[ -z "$value" ]] && continue

        # Track whether .env already sets TRUVAG3_ENABLE_OPENAPI so we don't
        # clobber an explicit developer choice (including an explicit =false).
        if [[ "$key" == "TRUVAG3_ENABLE_OPENAPI" ]]; then
            _openapi_set_from_env=true
        fi

        echo "  $key=$value"
        kubectl_args="$kubectl_args --from-literal=$key=$value"
        ((++var_count))
    done < "$env_file"

    # Dev default: enable the /openapi.json endpoint for every tool/agent
    # deployed via the example setup scripts. This is intentionally a dev-only
    # default — example setup.sh scripts exist for the local kind workflow,
    # not for production. Enterprise/SRE deployments do not use this library
    # and must opt in explicitly via their own config management.
    #
    # Developers can override by setting TRUVAG3_ENABLE_OPENAPI=false in the
    # tool's .env file. They can also disable the dev default entirely by
    # exporting TRUVAG3_DEV_DISABLE_OPENAPI=true before running setup.sh.
    if [[ "${_openapi_set_from_env:-false}" != "true" ]] && [[ "${TRUVAG3_DEV_DISABLE_OPENAPI:-false}" != "true" ]]; then
        echo "  TRUVAG3_ENABLE_OPENAPI=true  (dev default — see setup-env-lib.sh)"
        kubectl_args="$kubectl_args --from-literal=TRUVAG3_ENABLE_OPENAPI=true"
        ((++var_count))
    fi
    unset _openapi_set_from_env

    if [ $var_count -eq 0 ]; then
        _truvag3_log_info "No configuration variables found"
        kubectl create configmap "$configmap_name" \
            -n "$namespace" --dry-run=client -o yaml | kubectl apply -n "$namespace" -f -
    else
        _truvag3_log_info "Found $var_count configuration variables"
        eval "kubectl create configmap \"$configmap_name\" $kubectl_args \
            -n \"$namespace\" --dry-run=client -o yaml" | kubectl apply -n "$namespace" -f -
    fi

    _truvag3_log_success "ConfigMap '$configmap_name' configured"
}

# truvag3_create_tool_secret <secret_name> <namespace> <key_names...>
#
# Creates a K8s Secret with ONLY the specified tool-specific keys.
# Does NOT include AI provider or AWS keys.
# Useful for tools that only need their own API key (e.g., FINNHUB_API_KEY).
#
# Arguments:
#   secret_name  - Name for the K8s secret
#   namespace    - K8s namespace
#   key_names... - Variable names to include
#
# Examples:
#   truvag3_create_tool_secret "stock-tool-secrets" "$NAMESPACE" "FINNHUB_API_KEY"
#   truvag3_create_tool_secret "external-api-keys" "$NAMESPACE" "GNEWS_API_KEY"
truvag3_create_tool_secret() {
    local secret_name="$1"
    local namespace="$2"
    shift 2
    local key_names=("$@")

    local kubectl_args=""
    local key_count=0

    _truvag3_log_info "Setting up tool secret: $secret_name"

    for key in "${key_names[@]}"; do
        local value="${!key}"
        if [ -n "$value" ]; then
            echo "  $key=***"
            kubectl_args="$kubectl_args --from-literal=$key=$value"
            ((++key_count))
        fi
    done

    if [ $key_count -eq 0 ]; then
        _truvag3_log_info "No tool-specific keys found"
        kubectl create secret generic "$secret_name" \
            -n "$namespace" --dry-run=client -o yaml | kubectl apply -n "$namespace" -f -
    else
        _truvag3_log_info "Found $key_count tool keys"
        eval "kubectl create secret generic \"$secret_name\" $kubectl_args \
            -n \"$namespace\" --dry-run=client -o yaml" | kubectl apply -n "$namespace" -f -
    fi

    _truvag3_log_success "Tool secret '$secret_name' configured"
}

# ─── CLUSTER & INFRASTRUCTURE ────────────────────────────────────────────────

# Auto-detect K8S_DEPLOYMENT_DIR (directory containing this file)
TRUVAG3_K8S_DEPLOYMENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# truvag3_check_prerequisites
#
# Checks that required tools are installed: go, docker, kind, kubectl.
# Marks optional tools as available via DOCKER_AVAILABLE, KIND_AVAILABLE, etc.
# Exits with error if Go is not installed.
truvag3_check_prerequisites() {
    _truvag3_log_info "Checking prerequisites..."

    if ! command -v go &> /dev/null; then
        echo "ERROR: Go is not installed. Please install Go 1.25+ from https://golang.org/dl/"
        exit 1
    fi
    _truvag3_log_success "Go installed: $(go version)"

    if command -v docker &> /dev/null; then
        _truvag3_log_success "Docker installed"
        export DOCKER_AVAILABLE=true
    else
        _truvag3_log_info "Docker not found (required for K8s deployment)"
        export DOCKER_AVAILABLE=false
    fi

    if command -v kubectl &> /dev/null; then
        _truvag3_log_success "kubectl installed"
        export KUBECTL_AVAILABLE=true
    else
        _truvag3_log_info "kubectl not found (required for K8s deployment)"
        export KUBECTL_AVAILABLE=false
    fi

    if command -v kind &> /dev/null; then
        _truvag3_log_success "Kind installed"
        export KIND_AVAILABLE=true
    else
        _truvag3_log_info "Kind not found (required for local K8s deployment)"
        export KIND_AVAILABLE=false
    fi

    echo ""
}

# truvag3_create_cluster [cluster_name]
#
# Creates a Kind cluster configured for NGINX Ingress Controller.
# Only exposes ports 80 and 443 — all services are accessed via *.localhost ingress.
# Reuses existing cluster if one with the same name already exists.
#
# Arguments:
#   cluster_name - Optional. Defaults to $CLUSTER_NAME or "truvag3-demo-$(whoami)"
#
# Examples:
#   truvag3_create_cluster
#   truvag3_create_cluster "my-custom-cluster"
truvag3_create_cluster() {
    local cluster="${1:-${CLUSTER_NAME:-truvag3-demo-$(whoami)}}"

    _truvag3_log_info "Setting up Kind cluster ($cluster)..."

    if kind get clusters 2>/dev/null | grep -q "^${cluster}$"; then
        _truvag3_log_success "Cluster $cluster already exists, reusing it"
    else
        cat <<EOF | kind create cluster --name "$cluster" --config=-
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
  # NGINX Ingress Controller — single entrypoint for all services via *.localhost
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
EOF
        _truvag3_log_success "Kind cluster created with ingress port mappings"
    fi

    kubectl config use-context "kind-$cluster"
    echo ""
}

# truvag3_setup_infra [namespace]
#
# Deploys shared infrastructure: NGINX Ingress Controller, Redis, Prometheus,
# Grafana, Jaeger, Loki, OTEL Collector, Metrics Server, and Ingress routes.
# Delegates to setup-infrastructure.sh which is idempotent (checks before deploying).
#
# Arguments:
#   namespace - Optional. Defaults to $NAMESPACE or "truvag3-examples"
truvag3_setup_infra() {
    local ns="${1:-${NAMESPACE:-truvag3-examples}}"

    _truvag3_log_info "Setting up monitoring infrastructure..."

    local infra_script="$TRUVAG3_K8S_DEPLOYMENT_DIR/setup-infrastructure.sh"
    if [ -f "$infra_script" ]; then
        _truvag3_log_success "Found infrastructure setup script"
        echo ""
        NAMESPACE="$ns" "$infra_script"
        echo ""
        _truvag3_log_success "Monitoring infrastructure ready"
    else
        _truvag3_log_info "Infrastructure setup script not found at $infra_script"
        echo "  Monitoring will not be available"
    fi
    echo ""
}

# truvag3_verify_ingress <host1> [host2] [host3] ...
#
# Verifies that ingress routes are reachable via HTTP.
# Each argument is a hostname (e.g., "travel-chat-agent.localhost").
# Returns 0 if all routes are reachable, 1 if any failed.
#
# NOTE: Only agents and infra UIs should have Ingress routes. Tools are
# internal cluster services (ClusterIP only) — do not add Ingress for tools.
#
# Examples:
#   truvag3_verify_ingress "travel-chat-agent.localhost" "chat.localhost"
#   truvag3_verify_ingress "grafana.localhost" "jaeger.localhost" "prometheus.localhost"
truvag3_verify_ingress() {
    _truvag3_log_info "Verifying ingress routes..."

    local all_ok=true
    for host in "$@"; do
        local status
        status=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "http://$host" 2>/dev/null)
        if [ "$status" -ge 200 ] && [ "$status" -lt 500 ]; then
            _truvag3_log_success "$host (HTTP $status)"
        else
            _truvag3_log_info "$host (HTTP $status — may still be starting)"
            all_ok=false
        fi
    done

    if [ "$all_ok" = true ]; then
        _truvag3_log_success "All ingress routes verified"
        return 0
    else
        _truvag3_log_info "Some routes not ready yet — ingress controller may need a few more seconds"
        return 1
    fi
}

# truvag3_delete_cluster [cluster_name]
#
# Deletes a Kind cluster.
#
# Arguments:
#   cluster_name - Optional. Defaults to $CLUSTER_NAME or "truvag3-demo-$(whoami)"
truvag3_delete_cluster() {
    local cluster="${1:-${CLUSTER_NAME:-truvag3-demo-$(whoami)}}"

    if kind get clusters 2>/dev/null | grep -q "^${cluster}$"; then
        _truvag3_log_info "Deleting Kind cluster $cluster..."
        kind delete cluster --name "$cluster"
        _truvag3_log_success "Kind cluster deleted"
    else
        _truvag3_log_info "Cluster $cluster not found, nothing to delete"
    fi
}

# truvag3_forward <service_name> <local_port> <service_port> [namespace]
#
# Port-forwards a Kubernetes service to localhost. Runs in the foreground with
# auto-reconnect. Use as a fallback when Ingress is not available.
# Press Ctrl+C to stop.
#
# Arguments:
#   service_name - K8s service name (e.g., "travel-chat-agent-service")
#   local_port   - Local port to bind (e.g., 8356)
#   service_port - Service port to forward to (e.g., 80)
#   namespace    - Optional. Defaults to $NAMESPACE or "truvag3-examples"
#
# Examples:
#   truvag3_forward "travel-chat-agent-service" 8356 80
#   truvag3_forward "grafana" 3000 80
truvag3_forward() {
    local svc="$1"
    local local_port="$2"
    local svc_port="$3"
    local ns="${4:-${NAMESPACE:-truvag3-examples}}"

    _truvag3_log_info "Port-forwarding $svc → localhost:$local_port (Ctrl+C to stop)"

    while true; do
        kubectl port-forward -n "$ns" "svc/$svc" "$local_port:$svc_port" 2>/dev/null
        local exit_code=$?
        if [ $exit_code -eq 130 ] || [ $exit_code -eq 143 ]; then
            _truvag3_log_info "Port forward stopped by user"
            break
        fi
        _truvag3_log_info "Disconnected (exit $exit_code), reconnecting in 3s..."
        sleep 3
    done
}

# truvag3_forward_all <pair1> [pair2] ...
#
# Port-forwards multiple services in the background, then blocks on the first
# one with auto-reconnect. Each pair is "service:local_port:svc_port".
# Press Ctrl+C to stop all forwards.
#
# Examples:
#   truvag3_forward_all "travel-chat-agent-service:8356:80" "chat-ui-service:8360:80"
#   truvag3_forward_all "grafana:3000:80" "prometheus:9090:9090" "jaeger-query:16686:80"
truvag3_forward_all() {
    local ns="${NAMESPACE:-truvag3-examples}"
    local pairs=("$@")

    if [ ${#pairs[@]} -eq 0 ]; then
        _truvag3_log_info "No services specified"
        return
    fi

    # Start all but the first in background
    for i in $(seq 1 $((${#pairs[@]} - 1))); do
        local pair="${pairs[$i]}"
        local svc="${pair%%:*}"
        local rest="${pair#*:}"
        local local_port="${rest%%:*}"
        local svc_port="${rest#*:}"
        kubectl port-forward -n "$ns" "svc/$svc" "$local_port:$svc_port" >/dev/null 2>&1 &
        _truvag3_log_success "$svc → localhost:$local_port (background)"
    done

    # Run the first one in foreground with auto-reconnect
    local first="${pairs[0]}"
    local svc="${first%%:*}"
    local rest="${first#*:}"
    local local_port="${rest%%:*}"
    local svc_port="${rest#*:}"
    truvag3_forward "$svc" "$local_port" "$svc_port" "$ns"
}

# truvag3_load_env <env_file_path>
#
# Sources a .env file, exporting all variables. If the .env file is missing
# but a sibling .env.example exists, the example is auto-copied first so that
# fresh checkouts can run `./setup.sh full-deploy` without a manual
# bootstrap step. The example file is the documented "sensible defaults"
# starting point — secrets in it are placeholders that the user is expected
# to edit afterwards, but the resulting .env is always loadable.
#
# This convention was already implemented inline by ~11 example scripts.
# Hoisting it into the shared helper makes the behaviour consistent across
# all tools with no change required at the call sites. The inline copies
# in those 11 scripts become harmless — the file already exists by the
# time they run.
#
# Arguments:
#   env_file_path - Path to the .env file
truvag3_load_env() {
    local env_file="$1"
    local example_file="${env_file}.example"

    _truvag3_log_info "Loading environment variables..."

    # Auto-bootstrap from .env.example on fresh checkouts.
    if [ ! -f "$env_file" ] && [ -f "$example_file" ]; then
        cp "$example_file" "$env_file"
        _truvag3_log_info "Created .env from .env.example (edit to customize secrets/config)"
    fi

    if [ -f "$env_file" ]; then
        set -a
        source "$env_file"
        set +a
        _truvag3_log_success "Loaded .env file"
    else
        _truvag3_log_info "No .env file found at $env_file"
    fi
}

# truvag3_build_docker <image_name> <dockerfile_path> <build_context> [--no-cache]
#
# Builds a Docker image. Supports --no-cache for fresh builds.
#
# Arguments:
#   image_name     - Docker image tag (e.g., "travel-chat-agent:latest")
#   dockerfile_path - Path to Dockerfile
#   build_context   - Docker build context directory
#   --no-cache      - Optional flag for fresh build
truvag3_build_docker() {
    local image_name="$1"
    local dockerfile="$2"
    local context="$3"
    local no_cache_flag=""

    if [ "${4:-}" = "--no-cache" ] || [ "${DOCKER_NO_CACHE:-}" = "true" ]; then
        _truvag3_log_info "Building $image_name with --no-cache (fresh build)"
        no_cache_flag="--no-cache"
    else
        _truvag3_log_info "Building $image_name..."
    fi

    docker build $no_cache_flag -f "$dockerfile" -t "$image_name" "$context"
    _truvag3_log_success "$image_name built"
}

# truvag3_load_to_kind <image_name> [cluster_name]
#
# Loads a Docker image into a Kind cluster.
#
# Arguments:
#   image_name   - Docker image to load (e.g., "travel-chat-agent:latest")
#   cluster_name - Optional. Auto-detected from kubectl context or $CLUSTER_NAME
truvag3_load_to_kind() {
    local image_name="$1"
    local cluster_name="${2:-}"

    if ! command -v kind >/dev/null 2>&1; then
        _truvag3_log_info "Kind not found, skipping image load"
        return
    fi

    # Auto-detect cluster name from kubectl context
    if [ -z "$cluster_name" ]; then
        local context
        context=$(kubectl config current-context 2>/dev/null)
        if [[ "$context" == kind-* ]]; then
            cluster_name="${context#kind-}"
        elif [ -n "${CLUSTER_NAME:-}" ] && kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
            cluster_name="$CLUSTER_NAME"
        else
            cluster_name=$(kind get clusters 2>/dev/null | head -1)
            if [ -z "$cluster_name" ]; then
                _truvag3_log_info "No Kind clusters found. Please create one first."
                return 1
            fi
        fi
    fi

    _truvag3_log_info "Loading $image_name to Kind cluster '$cluster_name'..."
    kind load docker-image --name "$cluster_name" "$image_name"
    _truvag3_log_success "$image_name loaded to Kind"
}
