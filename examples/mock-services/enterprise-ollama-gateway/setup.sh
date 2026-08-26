#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

load_env() {
    if [[ -f "$SCRIPT_DIR/.env" ]]; then
        set -a
        # shellcheck disable=SC1091
        source "$SCRIPT_DIR/.env"
        set +a
    fi
}

show_help() {
    cat <<'EOF'
Usage: ./setup.sh <command>

Commands:
  run      Start the enterprise gateway simulator and connect it to Ollama
  test     Run the gateway's contract tests using an in-process fake Ollama
  smoke    Exercise a running simulator with the configured token + chat flow
  help     Show this help

First run:
  cp .env.example .env
  ollama pull llama3.2
  ./setup.sh run

In another terminal:
  ./setup.sh smoke
EOF
}

run_server() {
    load_env
    local ollama_base_url="${OLLAMA_BASE_URL:-http://127.0.0.1:11434}"
    if ! curl -fsS --max-time 2 "${ollama_base_url%/}/api/tags" >/dev/null; then
        echo "Ollama is not reachable at $ollama_base_url" >&2
        echo "Start Ollama and pull OLLAMA_MODEL before running the simulator." >&2
        exit 1
    fi
    cd "$SCRIPT_DIR"
    exec go run .
}

run_tests() {
    cd "$SCRIPT_DIR"
    GOWORK=off go test ./...
}

smoke_test() {
    load_env
    local base_url="${ENTERPRISE_BASE_HOST:-http://127.0.0.1:18080}"
    local token_url="${ENTERPRISE_TOKEN_URL:-$base_url/oauth2/token}"
    local client_id="${ENTERPRISE_CLIENT_ID:-travel-chat-agent}"
    local client_secret="${ENTERPRISE_CLIENT_SECRET:-local-enterprise-secret}"
    local app_key="${ENTERPRISE_APP_KEY:-travel-chat-app}"
    local deployment="${ENTERPRISE_DEPLOYMENT:-gpt-4o-mini}"
    local query=""
    if [[ -n "${ENTERPRISE_API_VERSION:-}" ]]; then
        query="?api-version=${ENTERPRISE_API_VERSION}"
    fi

    local token_response
    token_response="$(curl -fsS -u "$client_id:$client_secret" \
        -H 'Content-Type: application/x-www-form-urlencoded' \
        --data 'grant_type=client_credentials' \
        "$token_url")"
    local token
    token="$(printf '%s' "$token_response" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
    if [[ -z "$token" ]]; then
        echo "Token response did not contain access_token: $token_response" >&2
        exit 1
    fi

    local request_body
    request_body="$(printf '{\"messages\":[{\"role\":\"user\",\"content\":\"What is the Model Context Protocol (MCP), and why is it useful when building AI agents? Answer in three concise sentences.\"}],\"user\":\"{\\\"appkey\\\":\\\"%s\\\"}\",\"stop\":[\"<|im_end|>\"],\"stream\":false}' "$app_key")"

    curl -fsS \
        -H 'Content-Type: application/json' \
        -H 'Accept: application/json' \
        -H "api-key: $token" \
        --data "$request_body" \
        "$base_url/openai/deployments/$deployment/chat/completions$query"
    echo
}

case "${1:-help}" in
    run)
        run_server
        ;;
    test)
        run_tests
        ;;
    smoke)
        smoke_test
        ;;
    help|-h|--help)
        show_help
        ;;
    *)
        echo "Unknown command: $1" >&2
        show_help >&2
        exit 1
        ;;
esac
