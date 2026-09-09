package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveVerificationScriptStrictContract(t *testing.T) {
	for _, command := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s is unavailable", command)
		}
	}

	var includeSwagger atomic.Bool
	includeSwagger.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		write := func(value any) { _ = json.NewEncoder(writer).Encode(value) }
		switch {
		case request.URL.Path == "/":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = writer.Write([]byte("<title>Registry Viewer</title>"))
		case request.URL.Path == "/api/health":
			write(map[string]any{"status": "ok"})
		case request.URL.Path == "/api/readiness":
			write(map[string]any{
				"status": "ready", "backend": "redis", "redis_mode": "cluster",
				"redis_db": 0, "namespace": "verification", "seed_count": 3,
			})
		case request.URL.Path == "/api/services":
			write(map[string]any{"services": []any{map[string]any{
				"id": "service-1", "name": "travel-chat-agent",
				"capabilities": []any{map[string]any{"name": "chat"}},
			}}})
		case request.URL.Path == "/swagger-urls.json":
			if includeSwagger.Load() {
				write([]any{map[string]any{
					"name": "travel-chat-agent", "url": "/svc/travel-chat-agent-service/openapi.json",
				}})
			} else {
				write([]any{})
			}
		case request.URL.Path == "/api/llm-debug/orch-123":
			write(map[string]any{"request_id": "orch-123", "interactions": []any{map[string]any{"type": "plan_generation"}}})
		case request.URL.Path == "/api/llm-debug":
			write(map[string]any{"records": []any{map[string]any{"request_id": "orch-123"}}})
		case request.URL.Path == "/api/hitl/checkpoints/checkpoint-123":
			write(map[string]any{"checkpoint_id": "checkpoint-123", "status": "pending"})
		case request.URL.Path == "/api/hitl/checkpoints":
			write(map[string]any{"checkpoints": []any{map[string]any{"checkpoint_id": "checkpoint-123"}}})
		case request.URL.Path == "/api/executions/orch-123/unified":
			write(map[string]any{
				"request_id": "orch-123", "trace_id": "0123456789abcdef",
				"pipeline_hooks": []any{map[string]any{
					"hook_name": "memory-record", "phase": "after_execution",
					"status": "succeeded", "sequence": 1,
					"started_at": "2026-09-03T12:00:00Z", "duration": 1_000_000,
				}},
				"dag": map[string]any{"nodes": []any{}},
			})
		case request.URL.Path == "/api/executions/orch-123/dag":
			write(map[string]any{"nodes": []any{}, "edges": []any{}})
		case request.URL.Path == "/api/executions/search":
			write(map[string]any{"executions": []any{}, "query": request.URL.Query().Get("q")})
		case request.URL.Path == "/api/executions" && request.URL.Query().Get("group_conversations") == "true":
			write(map[string]any{
				"groups":            []any{map[string]any{"group_key": "conversation-123", "conversation_id": "conversation-123"}},
				"query_fingerprint": "query-123", "partial": false, "llm_enrichment_incomplete": false,
			})
		case request.URL.Path == "/api/executions":
			write(map[string]any{"executions": []any{map[string]any{"request_id": "orch-123"}}})
		case request.URL.Path == "/api/conversations":
			write(map[string]any{
				"conversation_id": "conversation-123", "turns": []any{map[string]any{"execution": map[string]any{"request_id": "orch-123"}}},
				"orphans": []any{}, "partial": false, "index_incomplete": false, "llm_enrichment_incomplete": false,
			})
		case request.URL.Path == "/api/analytics/resolution":
			write(map[string]any{"records": []any{}})
		case request.URL.Path == "/api/v1/skills/travel/trip-planning/versions":
			write(map[string]any{"versions": []any{map[string]any{
				"ref": map[string]any{"version": 1}, "status": "retained",
			}}})
		case request.URL.Path == "/api/v1/skills/travel/trip-planning":
			write(map[string]any{
				"revision": map[string]any{"ref": map[string]any{"ref": map[string]any{"namespace": "travel", "name": "trip-planning"}}},
				"manifest": map[string]any{"planning_instructions": []any{"Plan the trip"}},
			})
		case request.URL.Path == "/api/v1/skills":
			write(map[string]any{"skills": []any{map[string]any{
				"ref": map[string]any{"namespace": "travel", "name": "trip-planning"},
			}}})
		case request.URL.Path == "/api/v1/skills/schema":
			write(map[string]any{"type": "object", "properties": map[string]any{}})
		case request.URL.Path == "/api/traces/0123456789abcdef":
			http.Redirect(writer, request, "http://jaeger.example/trace/0123456789abcdef", http.StatusTemporaryRedirect)
		case request.URL.Path == "/api/memory/domains":
			write(map[string]any{"domains": []any{"infrastructure"}})
		case request.URL.Path == "/api/memory/events" || request.URL.Path == "/api/memory/events/recent":
			write(map[string]any{"events": []any{map[string]any{"entity_id": "pod-1"}}})
		case request.URL.Path == "/api/memory/investigations":
			write(map[string]any{"investigations": []any{}})
		case request.URL.Path == "/api/memory/digest":
			write(map[string]any{"available": true, "digest": "healthy"})
		case request.URL.Path == "/api/memory/activities":
			write(map[string]any{"activities": []any{}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	runStrictVerification := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "bash", "scripts/verify-live-data.sh", "--strict")
		command.Env = append(os.Environ(),
			"TRUVAG3_VIEWER_URL="+server.URL,
			"TRUVAG3_VIEWER_EXPECT_REDIS_MODE=cluster",
			"TRUVAG3_VIEWER_EXPECT_NAMESPACE=verification",
			"TRUVAG3_VIEWER_EXPECT_SERVICE=travel-chat-agent",
			"TRUVAG3_VIEWER_EXPECT_REQUEST_ID=orch-123",
			"TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID=orch-123",
			"TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID=conversation-123",
			"TRUVAG3_VIEWER_EXPECT_SKILL=travel/trip-planning",
			"TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID=checkpoint-123",
			"TRUVAG3_VIEWER_EXPECT_MEMORY_DOMAIN=infrastructure",
			"TRUVAG3_VIEWER_REQUIRE_PIPELINE_HOOKS=true",
			"TRUVAG3_VIEWER_REQUIRE_TRACE_LINK=true",
		)
		return command.CombinedOutput()
	}

	output, err := runStrictVerification()
	if err != nil {
		t.Fatalf("strict live verification failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "[PASS] Registry Viewer live-data verification completed") ||
		!strings.Contains(string(output), "topology: cluster, DB 0, namespace verification") {
		t.Fatalf("verification output did not contain completion evidence:\n%s", output)
	}

	includeSwagger.Store(false)
	output, err = runStrictVerification()
	if err == nil {
		t.Fatalf("strict live verification accepted missing Swagger discovery:\n%s", output)
	}
	if !strings.Contains(string(output), "swagger did not contain expected service travel-chat-agent") {
		t.Fatalf("strict live verification failed for the wrong reason:\n%s", output)
	}
}
