#!/bin/bash

# Read-only end-to-end verification for a live Registry Viewer deployment.
# Producers must create the evidence through framework APIs; this script never
# writes Redis keys or mutates Viewer state.

set -euo pipefail

BASE_URL="${TRUVAG3_VIEWER_URL:-http://localhost:8361}"
BASE_URL="${BASE_URL%/}"
REQUEST_TIMEOUT="${TRUVAG3_VIEWER_VERIFY_TIMEOUT_SECONDS:-20}"
STRICT="${TRUVAG3_VIEWER_VERIFY_STRICT:-false}"
if [ "${1:-}" = "--strict" ]; then
    STRICT=true
fi

for command_name in curl jq; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "[ERROR] $command_name is required for Registry Viewer verification" >&2
        exit 1
    fi
done

SMOKE_TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/truvag3-viewer-smoke.XXXXXX")
cleanup() {
    rm -rf "$SMOKE_TMP_DIR"
}
trap cleanup EXIT

pass() { printf '[PASS] %s\n' "$1"; }
fail() {
    printf '[FAIL] %s\n' "$1" >&2
    exit 1
}

url_encode() {
    jq -rn --arg value "$1" '$value | @uri'
}

fetch() {
    local name="$1"
    local path="$2"
    local output="$SMOKE_TMP_DIR/$name.json"
    local status
    status=$(curl -sS \
        --max-time "$REQUEST_TIMEOUT" \
        --output "$output" \
        --write-out '%{http_code}' \
        "$BASE_URL$path") || fail "$name request could not reach $BASE_URL"
    case "$status" in
        2??) ;;
        *) fail "$name returned HTTP $status" ;;
    esac
    jq -e . "$output" >/dev/null || fail "$name did not return valid JSON"
}

assert_shape() {
    local name="$1"
    local expression="$2"
    jq -e "$expression" "$SMOKE_TMP_DIR/$name.json" >/dev/null || \
        fail "$name returned an unexpected response shape"
    pass "$name"
}

assert_expected_value() {
    local name="$1"
    local value="$2"
    local expression="$3"
    jq -e --arg expected "$value" "$expression" "$SMOKE_TMP_DIR/$name.json" >/dev/null || \
        fail "$name did not contain expected value $value"
    pass "$name contains expected data"
}

assert_trace_redirect() {
    local trace_id="$1"
    local headers="$SMOKE_TMP_DIR/trace_redirect.headers"
    local status
    status=$(curl -sS \
        --max-time "$REQUEST_TIMEOUT" \
        --output /dev/null \
        --dump-header "$headers" \
        --write-out '%{http_code}' \
        "$BASE_URL/api/traces/$(url_encode "$trace_id")") || \
        fail "trace link request could not reach $BASE_URL"
    [ "$status" = "307" ] || fail "trace link returned HTTP $status instead of 307"
    local location
    location=$(awk 'tolower($1) == "location:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print; exit }' "$headers")
    case "$location" in
        */trace/"$trace_id") pass "trace link" ;;
        *) fail "trace link did not target the expected trace" ;;
    esac
}

fetch_text() {
    local name="$1"
    local path="$2"
    local output="$SMOKE_TMP_DIR/$name.txt"
    local status
    status=$(curl -sS \
        --max-time "$REQUEST_TIMEOUT" \
        --output "$output" \
        --write-out '%{http_code}' \
        "$BASE_URL$path") || fail "$name request could not reach $BASE_URL"
    case "$status" in
        2??) ;;
        *) fail "$name returned HTTP $status" ;;
    esac
}

fetch_text dashboard "/"
grep -q "Registry" "$SMOKE_TMP_DIR/dashboard.txt" || fail "dashboard did not return the Viewer UI"
pass "dashboard"
fetch health "/api/health"
assert_shape health '.status == "ok"'

fetch readiness "/api/readiness"
assert_shape readiness \
    '.status == "ready" and .backend == "redis" and .redis_db == 0 and (.seed_count | type == "number" and . > 0) and (.redis_mode == "standalone" or .redis_mode == "sentinel" or .redis_mode == "cluster") and (.namespace | type == "string" and length > 0)'

if [ -n "${TRUVAG3_VIEWER_EXPECT_REDIS_MODE:-}" ]; then
    assert_expected_value readiness "$TRUVAG3_VIEWER_EXPECT_REDIS_MODE" '.redis_mode == $expected'
fi
if [ -n "${TRUVAG3_VIEWER_EXPECT_NAMESPACE:-}" ]; then
    assert_expected_value readiness "$TRUVAG3_VIEWER_EXPECT_NAMESPACE" '.namespace == $expected'
fi

fetch services "/api/services"
assert_shape services '.services | type == "array"'
fetch swagger "/swagger-urls.json"
assert_shape swagger 'type == "array" and all(.[]; (.name | type == "string" and length > 0) and (.url | type == "string" and length > 0))'
fetch llm "/api/llm-debug?limit=50"
assert_shape llm '.records | type == "array"'
fetch hitl "/api/hitl/checkpoints"
assert_shape hitl '.checkpoints | type == "array"'
fetch executions "/api/executions?limit=50"
assert_shape executions '.executions | type == "array"'
fetch execution_search "/api/executions/search?q=__viewer_verification_no_match__&limit=1"
assert_shape execution_search '.executions | type == "array"'
fetch groups "/api/executions?group_conversations=true&limit=50"
assert_shape groups \
    '(.groups | type == "array") and (.query_fingerprint | type == "string") and (.partial | type == "boolean") and (.llm_enrichment_incomplete | type == "boolean")'
fetch analytics "/api/analytics/resolution"
assert_shape analytics '.records | type == "array"'
fetch skills "/api/v1/skills?limit=50"
assert_shape skills '.skills | type == "array"'
fetch skill_schema "/api/v1/skills/schema"
assert_shape skill_schema 'type == "object" and (.type == "object")'
fetch memory_domains "/api/memory/domains"
assert_shape memory_domains '.domains | type == "array"'

memory_domain="${TRUVAG3_VIEWER_EXPECT_MEMORY_DOMAIN:-}"
if [ -z "$memory_domain" ]; then
    memory_domain=$(jq -r '.domains[0] // empty' "$SMOKE_TMP_DIR/memory_domains.json")
fi
if [ -n "$memory_domain" ]; then
    encoded_domain=$(url_encode "$memory_domain")
    fetch memory_events "/api/memory/events?domain=$encoded_domain&limit=20"
    assert_shape memory_events '.events | type == "array"'
    fetch memory_recent "/api/memory/events/recent?domain=$encoded_domain&limit=20"
    assert_shape memory_recent '.events | type == "array"'
    fetch memory_investigations "/api/memory/investigations?domain=$encoded_domain"
    assert_shape memory_investigations '.investigations | type == "array"'
    fetch memory_digest "/api/memory/digest?domain=$encoded_domain"
    assert_shape memory_digest '.available | type == "boolean"'
    fetch memory_activities "/api/memory/activities?domain=$encoded_domain"
    assert_shape memory_activities '.activities | type == "array"'
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_SERVICE:-}" ]; then
    assert_expected_value services "$TRUVAG3_VIEWER_EXPECT_SERVICE" \
        'any(.services[]; (.id == $expected or .name == $expected) and (.capabilities | type == "array" and length > 0))'
    expected_service_name=$(jq -r --arg expected "$TRUVAG3_VIEWER_EXPECT_SERVICE" \
        '[.services[] | select(.id == $expected or .name == $expected) | .name][0] // empty' \
        "$SMOKE_TMP_DIR/services.json")
    [ -n "$expected_service_name" ] || \
        fail "services did not resolve the expected service name"
    jq -e --arg expected "$expected_service_name" \
        'any(.[]; .name == $expected and (.url | type == "string" and startswith("/svc/") and endswith("/openapi.json")))' \
        "$SMOKE_TMP_DIR/swagger.json" >/dev/null || \
        fail "swagger did not contain expected service $expected_service_name"
    pass "swagger contains expected service"
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_REQUEST_ID:-}" ]; then
    assert_expected_value executions "$TRUVAG3_VIEWER_EXPECT_REQUEST_ID" \
        'any(.executions[]; .request_id == $expected)'
    encoded_request=$(url_encode "$TRUVAG3_VIEWER_EXPECT_REQUEST_ID")
    fetch execution_unified "/api/executions/$encoded_request/unified"
    assert_expected_value execution_unified "$TRUVAG3_VIEWER_EXPECT_REQUEST_ID" \
        '.request_id == $expected and (.dag | type == "object")'
    if [ "${TRUVAG3_VIEWER_REQUIRE_PIPELINE_HOOKS:-false}" = true ]; then
        assert_shape execution_unified \
            '(.pipeline_hooks | type == "array" and length > 0) and all(.pipeline_hooks[]; (.hook_name | type == "string" and length > 0) and (.phase | type == "string" and length > 0) and (.status == "succeeded" or .status == "failed" or .status == "skipped") and (.sequence | type == "number") and (.started_at | type == "string") and (.duration | type == "number"))'
    fi
    if [ "${TRUVAG3_VIEWER_REQUIRE_TRACE_LINK:-false}" = true ]; then
        assert_shape execution_unified '(.trace_id | type == "string" and length > 0)'
        trace_id=$(jq -r '.trace_id' "$SMOKE_TMP_DIR/execution_unified.json")
        assert_trace_redirect "$trace_id"
    fi
    fetch execution_dag "/api/executions/$encoded_request/dag"
    assert_shape execution_dag '.nodes | type == "array"'
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID:-}" ]; then
    assert_expected_value llm "$TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID" \
        'any(.records[]; .request_id == $expected)'
    encoded_llm_request=$(url_encode "$TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID")
    fetch llm_detail "/api/llm-debug/$encoded_llm_request"
    assert_expected_value llm_detail "$TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID" \
        '.request_id == $expected and (.interactions | type == "array" and length > 0)'
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID:-}" ]; then
    assert_expected_value groups "$TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID" \
        '(.partial == false) and (.llm_enrichment_incomplete == false) and any(.groups[]; .conversation_id == $expected)'
    encoded_conversation=$(url_encode "$TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID")
    fetch conversation "/api/conversations?conversation_id=$encoded_conversation"
    assert_expected_value conversation "$TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID" \
        '.conversation_id == $expected and (.turns | type == "array" and length > 0) and (.orphans | type == "array") and (.partial == false) and (.index_incomplete == false) and (.llm_enrichment_incomplete == false)'
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_SKILL:-}" ]; then
    skill_namespace="${TRUVAG3_VIEWER_EXPECT_SKILL%%/*}"
    skill_name="${TRUVAG3_VIEWER_EXPECT_SKILL#*/}"
    if [ -z "$skill_namespace" ] || [ -z "$skill_name" ] || [ "$skill_name" = "$TRUVAG3_VIEWER_EXPECT_SKILL" ] || [[ "$skill_name" == */* ]]; then
        fail "TRUVAG3_VIEWER_EXPECT_SKILL must use namespace/name"
    fi
    fetch skill_detail "/api/v1/skills/$(url_encode "$skill_namespace")/$(url_encode "$skill_name")"
    jq -e --arg namespace "$skill_namespace" --arg name "$skill_name" \
        '.revision.ref.ref.namespace == $namespace and .revision.ref.ref.name == $name and (.manifest | type == "object")' \
        "$SMOKE_TMP_DIR/skill_detail.json" >/dev/null || \
        fail "skill_detail did not contain expected skill $TRUVAG3_VIEWER_EXPECT_SKILL"
    pass "skill_detail contains expected data"
    jq -e --arg namespace "$skill_namespace" --arg name "$skill_name" \
        'any(.skills[]; .ref.namespace == $namespace and .ref.name == $name)' \
        "$SMOKE_TMP_DIR/skills.json" >/dev/null || \
        fail "skills did not list expected skill $TRUVAG3_VIEWER_EXPECT_SKILL"
    pass "skills contains expected data"
    fetch skill_versions "/api/v1/skills/$(url_encode "$skill_namespace")/$(url_encode "$skill_name")/versions"
    assert_shape skill_versions \
        '(.versions | type == "array" and length > 0) and all(.versions[]; (.ref.version | type == "number") and (.status | type == "string"))'
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID:-}" ]; then
    assert_expected_value hitl "$TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID" \
        'any(.checkpoints[]; .checkpoint_id == $expected)'
    encoded_checkpoint=$(url_encode "$TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID")
    fetch hitl_detail "/api/hitl/checkpoints/$encoded_checkpoint"
    assert_expected_value hitl_detail "$TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID" \
        '.checkpoint_id == $expected'
fi

if [ -n "${TRUVAG3_VIEWER_EXPECT_MEMORY_DOMAIN:-}" ]; then
    assert_expected_value memory_domains "$TRUVAG3_VIEWER_EXPECT_MEMORY_DOMAIN" \
        'any(.domains[]; . == $expected)'
fi

if [ "$STRICT" = true ]; then
    required_names=(
        TRUVAG3_VIEWER_EXPECT_SERVICE
        TRUVAG3_VIEWER_EXPECT_REQUEST_ID
        TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID
        TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID
        TRUVAG3_VIEWER_EXPECT_SKILL
        TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID
        TRUVAG3_VIEWER_EXPECT_MEMORY_DOMAIN
        TRUVAG3_VIEWER_REQUIRE_PIPELINE_HOOKS
    )
    for required_name in "${required_names[@]}"; do
        required_value=$(printenv "$required_name" 2>/dev/null || true)
        if [ -z "$required_value" ]; then
            fail "$required_name is required by strict verification"
        fi
    done
    if [ "$TRUVAG3_VIEWER_REQUIRE_PIPELINE_HOOKS" != true ]; then
        fail "TRUVAG3_VIEWER_REQUIRE_PIPELINE_HOOKS must be true for strict verification"
    fi
    memory_evidence=$(jq -n \
        --slurpfile events "$SMOKE_TMP_DIR/memory_events.json" \
        --slurpfile investigations "$SMOKE_TMP_DIR/memory_investigations.json" \
        --slurpfile digest "$SMOKE_TMP_DIR/memory_digest.json" \
        --slurpfile activities "$SMOKE_TMP_DIR/memory_activities.json" \
        '($events[0].events | length) + ($investigations[0].investigations | length) + ($activities[0].activities | length) + (if $digest[0].available then 1 else 0 end)')
    if [ "$memory_evidence" -lt 1 ]; then
        fail "strict verification found no memory evidence in the expected domain"
    fi
fi

printf '\nLive data summary:\n'
printf '  topology: %s, DB %s, namespace %s\n' \
    "$(jq -r '.redis_mode' "$SMOKE_TMP_DIR/readiness.json")" \
    "$(jq -r '.redis_db' "$SMOKE_TMP_DIR/readiness.json")" \
    "$(jq -r '.namespace' "$SMOKE_TMP_DIR/readiness.json")"
printf '  services: %s\n' "$(jq '.services | length' "$SMOKE_TMP_DIR/services.json")"
printf '  executions: %s\n' "$(jq '.executions | length' "$SMOKE_TMP_DIR/executions.json")"
printf '  conversation groups: %s\n' "$(jq '.groups | length' "$SMOKE_TMP_DIR/groups.json")"
printf '  LLM records: %s\n' "$(jq '.records | length' "$SMOKE_TMP_DIR/llm.json")"
printf '  HITL checkpoints: %s\n' "$(jq '.checkpoints | length' "$SMOKE_TMP_DIR/hitl.json")"
printf '  skills: %s\n' "$(jq '.skills | length' "$SMOKE_TMP_DIR/skills.json")"
printf '\n[PASS] Registry Viewer live-data verification completed\n'
