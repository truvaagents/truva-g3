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
#   Skill packages:
#     truvag3_check_skill_tools    - Check local tools needed for skill management
#     truvag3_check_skill_package  - Compare one Git package with the published revision
#     truvag3_sync_skill_package   - Create/update and verify one published package
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

_truvag3_log_warn() {
    if type print_warn &>/dev/null; then
        print_warn "$1"
    elif type log_warn &>/dev/null; then
        log_warn "$1"
    else
        echo "[WARN] $1"
    fi
}

_truvag3_log_error() {
    if type print_error &>/dev/null; then
        print_error "$1"
    elif type log_error &>/dev/null; then
        log_error "$1"
    else
        echo "[ERROR] $1" >&2
    fi
}

# ─── FUNCTIONS ───────────────────────────────────────────────────────────────

# truvag3_check_skill_tools
#
# Checks only the local tools needed by the provider-neutral Skills HTTP API
# workflow. It intentionally does not require Kind, kubectl, Go, or a container
# runtime so the same command can target a remote management host.
truvag3_check_skill_tools() {
    local missing=()
    local tool
    for tool in curl jq cksum; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            missing+=("$tool")
        fi
    done
    if [ ${#missing[@]} -gt 0 ]; then
        _truvag3_log_error "Skill management requires: ${missing[*]}"
        return 1
    fi
}

# Validate through the same server policy used for publication and write the
# normalized authoring representation to the requested temporary file.
_truvag3_validate_skill_source() {
    local api_base="${1%/}"
    local skill_namespace="$2"
    local skill_name="$3"
    local package_file="$4"
    local normalized_file="$5"
    local response_file status

    if [ ! -f "$package_file" ]; then
        _truvag3_log_error "Skill package not found: $package_file"
        return 1
    fi

    response_file=$(mktemp)
    if ! status=$(curl -sS --connect-timeout 5 --max-time 20 \
        -o "$response_file" -w '%{http_code}' -X POST \
        "${api_base}/${skill_namespace}/${skill_name}/validate" \
        -H 'Content-Type: application/json' \
        --data-binary "@$package_file"); then
        rm -f "$response_file"
        _truvag3_log_error "The configured Skills API is unavailable"
        return 1
    fi
    if [ "$status" != "200" ]; then
        _truvag3_log_error "Validating $skill_namespace/$skill_name failed (HTTP $status)"
        sed -n '1,8p' "$response_file" >&2
        rm -f "$response_file"
        return 1
    fi
    if ! jq -e '.validation.valid == true and (.normalized | type == "object")' \
        "$response_file" >/dev/null 2>&1; then
        _truvag3_log_error "Skill package $skill_namespace/$skill_name is invalid"
        jq -c '{errors: (.validation.errors // []), warnings: (.validation.warnings // [])}' \
            "$response_file" >&2 2>/dev/null || sed -n '1,8p' "$response_file" >&2
        rm -f "$response_file"
        return 1
    fi
    if ! jq '.normalized' "$response_file" > "$normalized_file"; then
        rm -f "$response_file"
        _truvag3_log_error "Skills API returned invalid validation data for $skill_namespace/$skill_name"
        return 1
    fi
    rm -f "$response_file"
}

# Produce the stable JSON compared by the framework when deciding whether a
# package needs a new immutable revision. change_reason is audit metadata, not
# versioned behavior. Activation examples and resources are order-independent.
_truvag3_skill_versioned_content() {
    jq -cS '
        del(.change_reason)
        | if (.activation_examples? | type) == "object" then
            .activation_examples.should_activate = ((.activation_examples.should_activate // []) | sort)
            | .activation_examples.should_not_activate = ((.activation_examples.should_not_activate // []) | sort)
          else . end
        | if (.resources? | type) == "array" then
            .resources |= sort_by(.name)
          else . end
    ' "$1"
}

_truvag3_skill_packages_equal() {
    local desired_file="$1"
    local published_file="$2"
    local desired published

    desired=$(_truvag3_skill_versioned_content "$desired_file") || return 1
    published=$(_truvag3_skill_versioned_content "$published_file") || return 1
    [ "$desired" = "$published" ]
}

# Read the published skill and print its HTTP status. Callers can therefore
# distinguish a missing package from an unavailable or integrity-failing store.
_truvag3_read_published_skill() {
    local api_base="${1%/}"
    local skill_namespace="$2"
    local skill_name="$3"
    local headers_file="$4"
    local response_file="$5"

    curl -sS --connect-timeout 5 --max-time 15 \
        -D "$headers_file" -o "$response_file" -w '%{http_code}' \
        "${api_base}/${skill_namespace}/${skill_name}"
}

# Validate the provider-neutral published representation, require its ETag, and
# extract the resubmittable package for semantic comparison.
_truvag3_extract_published_skill() {
    local skill_namespace="$1"
    local skill_name="$2"
    local headers_file="$3"
    local response_file="$4"
    local published_file="$5"
    local etag

    if ! jq -e --arg namespace "$skill_namespace" --arg name "$skill_name" '
        (.revision.ref.ref.namespace == $namespace)
        and (.revision.ref.ref.name == $name)
        and (.revision.ref.version | type == "number" and . > 0)
        and (.revision.ref.manifest_hash | type == "string" and test("^sha256:[0-9a-f]{64}$"))
        and (.revision.metadata.ref.namespace == $namespace)
        and (.revision.metadata.ref.name == $name)
        and (.revision.metadata.published_version == .revision.ref.version)
        and (.revision.metadata.status == "published")
        and (.package | type == "object")
        and (.manifest | type == "object")
        and (.manifest.ref.ref.namespace == $namespace)
        and (.manifest.ref.ref.name == $name)
        and (.manifest.ref.version == .revision.ref.version)
        and (.manifest.ref.manifest_hash == .revision.ref.manifest_hash)
    ' "$response_file" >/dev/null 2>&1; then
        return 1
    fi
    etag=$(awk 'tolower($1) == "etag:" {print $2}' "$headers_file" | tr -d '\r' | tail -1)
    if [ -z "$etag" ]; then
        return 1
    fi
    jq '.package' "$response_file" > "$published_file"
}

# truvag3_check_skill_package <api_base> <namespace> <name> <package_file>
#
# Read-only validation and comparison of one Git-authored package. A successful
# GET also exercises the backend's published-pointer and revision checks.
truvag3_check_skill_package() {
    local api_base="${1%/}"
    local skill_namespace="$2"
    local skill_name="$3"
    local package_file="$4"
    local normalized_file headers_file response_file published_file status version

    normalized_file=$(mktemp)
    headers_file=$(mktemp)
    response_file=$(mktemp)
    published_file=$(mktemp)

    if ! _truvag3_validate_skill_source "$api_base" "$skill_namespace" "$skill_name" \
        "$package_file" "$normalized_file"; then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        return 1
    fi
    if ! status=$(_truvag3_read_published_skill "$api_base" "$skill_namespace" "$skill_name" \
        "$headers_file" "$response_file"); then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_error "The configured Skills API is unavailable"
        return 1
    fi
    if [ "$status" = "404" ]; then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_warn "Skill $skill_namespace/$skill_name is missing"
        return 1
    fi
    if [ "$status" != "200" ]; then
        _truvag3_log_error "Reading $skill_namespace/$skill_name failed (HTTP $status)"
        sed -n '1,8p' "$response_file" >&2
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        return 1
    fi
    if ! _truvag3_extract_published_skill "$skill_namespace" "$skill_name" \
        "$headers_file" "$response_file" "$published_file"; then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_error "Skills API returned an invalid published representation for $skill_namespace/$skill_name"
        return 1
    fi
    if ! _truvag3_skill_packages_equal "$normalized_file" "$published_file"; then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_warn "Skill $skill_namespace/$skill_name differs from its Git package"
        return 1
    fi

    version=$(jq -r '.revision.ref.version // "unknown"' "$response_file")
    rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
    _truvag3_log_success "Skill $skill_namespace/$skill_name matches Git (published version $version)"
}

# truvag3_sync_skill_package <api_base> <namespace> <name> <package_file>
#
# Reconciles one Git-authored package without rebuilding or restarting an
# agent. Missing content is created, changed behavior rolls forward, and equal
# content is skipped. The published representation is verified before return.
truvag3_sync_skill_package() {
    local api_base="${1%/}"
    local skill_namespace="$2"
    local skill_name="$3"
    local package_file="$4"
    local skill_url="${api_base}/${skill_namespace}/${skill_name}"
    local normalized_file headers_file response_file published_file status etag
    local precondition idempotency_key package_checksum state_checksum outcome version

    normalized_file=$(mktemp)
    headers_file=$(mktemp)
    response_file=$(mktemp)
    published_file=$(mktemp)

    if ! _truvag3_validate_skill_source "$api_base" "$skill_namespace" "$skill_name" \
        "$package_file" "$normalized_file"; then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        return 1
    fi
    if ! status=$(_truvag3_read_published_skill "$api_base" "$skill_namespace" "$skill_name" \
        "$headers_file" "$response_file"); then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_error "The configured Skills API is unavailable"
        return 1
    fi

    if [ "$status" = "200" ]; then
        if ! _truvag3_extract_published_skill "$skill_namespace" "$skill_name" \
            "$headers_file" "$response_file" "$published_file"; then
            rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
            _truvag3_log_error "Skills API returned an invalid published representation for $skill_namespace/$skill_name"
            return 1
        fi
        if _truvag3_skill_packages_equal "$normalized_file" "$published_file"; then
            version=$(jq -r '.revision.ref.version // "unknown"' "$response_file")
            rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
            _truvag3_log_success "Skill $skill_namespace/$skill_name already matches Git (published version $version)"
            return 0
        fi
        etag=$(awk 'tolower($1) == "etag:" {print $2}' "$headers_file" | tr -d '\r' | tail -1)
        if [ -z "$etag" ]; then
            rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
            _truvag3_log_error "Skills API did not return an ETag for $skill_namespace/$skill_name"
            return 1
        fi
        precondition="If-Match: $etag"
    elif [ "$status" = "404" ]; then
        precondition="If-None-Match: *"
    else
        _truvag3_log_error "Reading $skill_namespace/$skill_name failed (HTTP $status)"
        sed -n '1,8p' "$response_file" >&2
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        return 1
    fi

    package_checksum=$(cksum "$package_file" | awk '{print $1 "-" $2}')
    state_checksum=$(printf '%s' "$precondition" | cksum | awk '{print $1 "-" $2}')
    idempotency_key="example-${skill_namespace}-${skill_name}-${package_checksum}-${state_checksum}"
    if ! status=$(curl -sS --connect-timeout 5 --max-time 20 \
        -o "$response_file" -w '%{http_code}' -X PUT "$skill_url" \
        -H 'Content-Type: application/json' \
        -H "$precondition" \
        -H "Idempotency-Key: $idempotency_key" \
        --data-binary "@$package_file"); then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_error "The configured Skills API is unavailable"
        return 1
    fi
    if [ "$status" != "200" ] && [ "$status" != "201" ]; then
        _truvag3_log_error "Publishing $skill_namespace/$skill_name failed (HTTP $status)"
        sed -n '1,8p' "$response_file" >&2
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        return 1
    fi
    outcome=$(jq -r '.result.outcome // "published"' "$response_file" 2>/dev/null || echo "published")
    case "$outcome" in
        created|updated|same_content_noop|idempotent_replay) ;;
        *) outcome="published" ;;
    esac

    : > "$headers_file"
    : > "$response_file"
    if ! status=$(_truvag3_read_published_skill "$api_base" "$skill_namespace" "$skill_name" \
        "$headers_file" "$response_file") || [ "$status" != "200" ] ||
       ! _truvag3_extract_published_skill "$skill_namespace" "$skill_name" \
            "$headers_file" "$response_file" "$published_file" ||
       ! _truvag3_skill_packages_equal "$normalized_file" "$published_file"; then
        rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
        _truvag3_log_error "Published skill $skill_namespace/$skill_name did not verify against Git"
        return 1
    fi

    version=$(jq -r '.revision.ref.version // "unknown"' "$response_file")
    rm -f "$normalized_file" "$headers_file" "$response_file" "$published_file"
    _truvag3_log_success "Synchronized skill $skill_namespace/$skill_name ($outcome, published version $version)"
}

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

# truvag3_detect_container_runtime
#
# Resolves which container runtime to use (docker or podman) and exports it as
# TRUVAG3_CONTAINER_RUNTIME so the rest of the scripts are engine-agnostic. Also
# sets DOCKER_AVAILABLE for backward compatibility.
#
# Resolution order:
#   1. An explicit TRUVAG3_CONTAINER_RUNTIME set by the caller is honored as-is.
#   2. docker, but only if its daemon actually responds (`docker info`). A docker
#      CLI with no running daemon (e.g. Docker Desktop/OrbStack stopped) is skipped.
#   3. podman, if installed.
#
# When the resolved runtime is podman, KIND_EXPERIMENTAL_PROVIDER=podman is
# exported so `kind` uses podman as well.
truvag3_detect_container_runtime() {
    # 1. Respect an explicit choice — but validate it so a typo or a missing
    #    binary surfaces here with a clear message, not as a cryptic build error.
    if [ -n "${TRUVAG3_CONTAINER_RUNTIME:-}" ]; then
        case "$TRUVAG3_CONTAINER_RUNTIME" in
            docker|podman) ;;
            *)
                echo "ERROR: TRUVAG3_CONTAINER_RUNTIME='$TRUVAG3_CONTAINER_RUNTIME' is not supported (use 'docker' or 'podman')"
                exit 1 ;;
        esac
        if ! command -v "$TRUVAG3_CONTAINER_RUNTIME" &> /dev/null; then
            # Binary not installed — mark unavailable (like the "no runtime found"
            # branch) so build/Redis gates report it cleanly instead of trying to
            # exec a missing binary. Not a hard exit: detection runs at source time
            # for every subcommand, and status/logs/help shouldn't be blocked.
            _truvag3_log_warn "Pinned runtime '$TRUVAG3_CONTAINER_RUNTIME' is not installed / not on PATH — build and deploy steps will fail until it is"
            export DOCKER_AVAILABLE=false
            return
        elif ! "$TRUVAG3_CONTAINER_RUNTIME" info &> /dev/null; then
            # Binary present but its daemon/machine is down. Warn (symmetric with
            # the auto-detect ladder below) but still honor the choice and proceed:
            # the build step will surface the real engine error.
            if [ "$TRUVAG3_CONTAINER_RUNTIME" = "podman" ]; then
                _truvag3_log_warn "Pinned runtime 'podman' is installed but its machine is not running; run 'podman machine start'"
            else
                _truvag3_log_warn "Pinned runtime 'docker' is installed but its daemon is not responding; start Docker"
            fi
        else
            _truvag3_log_success "Container runtime (pinned): $TRUVAG3_CONTAINER_RUNTIME"
        fi
    # 2. Prefer docker if its daemon is reachable.
    elif command -v docker &> /dev/null && docker info &> /dev/null; then
        export TRUVAG3_CONTAINER_RUNTIME=docker
        _truvag3_log_success "Container runtime: docker"
    # 3. Otherwise podman, but only if its machine is actually running
    #    (symmetric with the docker daemon check above — a stopped podman machine
    #    would otherwise be picked silently and then fail at build time).
    elif command -v podman &> /dev/null && podman info &> /dev/null; then
        export TRUVAG3_CONTAINER_RUNTIME=podman
        _truvag3_log_success "Container runtime: podman"
    # 4. A CLI is installed but its daemon/machine isn't up. Pick it and warn so
    #    the next step fails with a clear "start your runtime" message rather than
    #    a silent wrong choice. Docker is preferred when both are down.
    elif command -v docker &> /dev/null; then
        export TRUVAG3_CONTAINER_RUNTIME=docker
        _truvag3_log_warn "docker found but its daemon is not responding; start Docker (or start/install podman)"
    elif command -v podman &> /dev/null; then
        export TRUVAG3_CONTAINER_RUNTIME=podman
        _truvag3_log_warn "podman found but its machine is not running; run 'podman machine start' (or start/install Docker)"
    else
        export TRUVAG3_CONTAINER_RUNTIME=docker
        _truvag3_log_info "No container runtime found (docker or podman required for K8s deployment)"
        export DOCKER_AVAILABLE=false
        return
    fi

    export DOCKER_AVAILABLE=true

    # kind needs to be told to use podman explicitly; docker is its default.
    if [ "$TRUVAG3_CONTAINER_RUNTIME" = "podman" ]; then
        export KIND_EXPERIMENTAL_PROVIDER=podman
    fi
}

# truvag3_check_prerequisites
#
# Checks that required tools are installed: go, a container runtime
# (docker or podman), kind, kubectl. Marks optional tools as available via
# DOCKER_AVAILABLE, KIND_AVAILABLE, etc. Exits with error if Go is not installed.
truvag3_check_prerequisites() {
    _truvag3_log_info "Checking prerequisites..."

    if ! command -v go &> /dev/null; then
        echo "ERROR: Go is not installed. Please install Go 1.26+ from https://golang.org/dl/"
        exit 1
    fi
    _truvag3_log_success "Go installed: $(go version)"

    truvag3_detect_container_runtime

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

    "${TRUVAG3_CONTAINER_RUNTIME:-docker}" build $no_cache_flag -f "$dockerfile" -t "$image_name" "$context"
    _truvag3_log_success "$image_name built"
}

# _truvag3_podman_load_image <image_name> <cluster_name>
#
# Loads a podman-built image into a Kind cluster via a saved archive.
#
# Why not `kind load docker-image`? With the podman provider that path is
# unreliable, and podman tags local builds as `localhost/<name>` — which
# containerd on the node will NOT match against a manifest's bare
# `image: <name>:tag` (containerd normalizes that to `docker.io/library/<name>:tag`).
# So we retag to the normalized ref, then load it as an image archive.
_truvag3_podman_load_image() {
    local image_name="$1"
    local cluster_name="$2"

    local node_ref="$image_name"
    case "$image_name" in
        */*) : ;;                                   # already namespaced/registry-qualified
        *)   node_ref="docker.io/library/$image_name" ;;
    esac
    if [ "$node_ref" != "$image_name" ]; then
        podman tag "$image_name" "$node_ref"
    fi

    local archive
    archive="$(mktemp -t kind-img-XXXXXX).tar"
    podman save -o "$archive" "$node_ref"
    command kind load image-archive "$archive" --name "$cluster_name"
    rm -f "$archive"
}

# kind() — transparent wrapper around the real `kind` binary.
#
# When the resolved runtime is podman, `kind load docker-image <imgs...> --name <c>`
# is rewritten to the archive-based load (see _truvag3_podman_load_image), since
# `kind load docker-image` does not work reliably with podman. Every other kind
# invocation (create/get/load image-archive/etc.) passes straight through.
#
# This lets the many example scripts that call `kind load docker-image` inline
# work under podman without each one being modified.
kind() {
    if [ "${TRUVAG3_CONTAINER_RUNTIME:-docker}" = "podman" ] && \
       [ "${1:-}" = "load" ] && [ "${2:-}" = "docker-image" ]; then
        shift 2
        local images=() cluster=""
        while [ $# -gt 0 ]; do
            case "$1" in
                --name)   cluster="$2"; shift 2 ;;
                --name=*) cluster="${1#--name=}"; shift ;;
                *)        images+=("$1"); shift ;;
            esac
        done
        # Fall back to the current kind context if --name was omitted.
        if [ -z "$cluster" ]; then
            local ctx
            ctx=$(kubectl config current-context 2>/dev/null)
            [[ "$ctx" == kind-* ]] && cluster="${ctx#kind-}"
            [ -z "$cluster" ] && cluster="${CLUSTER_NAME:-}"
        fi
        local img
        for img in "${images[@]}"; do
            _truvag3_podman_load_image "$img" "$cluster"
        done
        return $?
    fi
    command kind "$@"
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

    if [ "${TRUVAG3_CONTAINER_RUNTIME:-docker}" = "podman" ]; then
        _truvag3_podman_load_image "$image_name" "$cluster_name"
    else
        command kind load docker-image --name "$cluster_name" "$image_name"
    fi

    _truvag3_log_success "$image_name loaded to Kind"
}

# ─── SOURCE-TIME RUNTIME DETECTION ───────────────────────────────────────────
# Resolve the container runtime as soon as the library is sourced so that
# TRUVAG3_CONTAINER_RUNTIME (and KIND_EXPERIMENTAL_PROVIDER for podman) are set
# for every script — including the many tool setup.sh scripts that build/load
# images without first calling truvag3_check_prerequisites.
#
# Always call this, even when TRUVAG3_CONTAINER_RUNTIME is already pinned: the
# function honors the pinned value but ALSO exports KIND_EXPERIMENTAL_PROVIDER
# and DOCKER_AVAILABLE for it. Guarding on the pinned var would skip those
# exports, leaving kind without its podman provider on the source-time path.
truvag3_detect_container_runtime
