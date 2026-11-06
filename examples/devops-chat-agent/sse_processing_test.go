package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

type sseFixtureAI struct {
	partial       bool
	clarification bool
	streamCalls   int
}

func (c *sseFixtureAI) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	plan := `{"terminal":true,"steps":[]}`
	if c.clarification {
		plan = `{"steps":[],"needs_user_input":{"question":"Which region?","missing_fields":["region"]}}`
	}
	return &core.AIResponse{Content: plan}, nil
}
func (*sseFixtureAI) SupportsStreaming() bool { return true }
func (c *sseFixtureAI) StreamResponse(_ context.Context, _ string, _ *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	c.streamCalls++
	response := &core.AIResponse{Content: "retained answer", Usage: core.TokenUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}, FinishReason: "stop"}
	if err := callback(core.StreamChunk{Content: response.Content}); err != nil {
		return response, errors.Join(core.ErrStreamPartiallyCompleted, err)
	}
	if c.partial {
		return response, errors.Join(core.ErrStreamPartiallyCompleted, errors.New("fixture interrupted"))
	}
	if err := callback(core.StreamChunk{FinishReason: "stop", Usage: &response.Usage}); err != nil {
		return response, errors.Join(core.ErrStreamPartiallyCompleted, err)
	}
	return response, nil
}

type sseCapturedLog struct {
	message string
	fields  map[string]interface{}
}
type sseFixtureLogger struct {
	core.NoOpLogger
	mu      sync.Mutex
	entries []sseCapturedLog
}

func (l *sseFixtureLogger) capture(message string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, sseCapturedLog{message, fields})
}
func (l *sseFixtureLogger) InfoWithContext(_ context.Context, message string, fields map[string]interface{}) {
	l.capture(message, fields)
}
func (l *sseFixtureLogger) ErrorWithContext(_ context.Context, message string, fields map[string]interface{}) {
	l.capture(message, fields)
}
func (l *sseFixtureLogger) contains(message string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.message == message {
			return true
		}
	}
	return false
}

func newSSEAgentFixture(t *testing.T, client *sseFixtureAI) (*DevOpsChatAgent, string, *sseFixtureLogger) {
	t.Helper()
	server := miniredis.RunT(t)
	connection := core.DefaultRedisConnectionConfig()
	connection.Addrs = []string{server.Addr()}
	keyspace, err := core.NewRedisKeyspace("sse-fixture")
	if err != nil {
		t.Fatal(err)
	}
	logger := &sseFixtureLogger{}
	store, err := NewSessionStore(connection, keyspace, time.Minute, 20, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config := orchestration.NewDefaultOrchestratorConfig()
	config.EnableTelemetry = false
	config.MetricsEnabled = false
	config.CacheEnabled = false
	orch, err := orchestration.CreateResolvedOrchestrator(config, orchestration.OrchestratorDependencies{Discovery: core.NewMockDiscovery(), AIClient: client, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := orch.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	base := core.NewBaseAgent("sse-fixture")
	base.Logger = logger
	agent := &DevOpsChatAgent{BaseAgent: base, orchestrator: orch, sessionStore: store}
	return agent, store.Create("user-1", nil).ID, logger
}

func TestDevOpsSSEFinalFailurePreservesAccounting(t *testing.T) {
	agent, sessionID, logger := newSSEAgentFixture(t, &sseFixtureAI{})
	writer := newSSETestResponseWriter()
	writer.failEvent = "done"
	request := httptest.NewRequest(http.MethodPost, "/chat/stream", strings.NewReader(`{"session_id":"`+sessionID+`","message":"fixture"}`))
	NewSSEHandler(agent).ServeHTTP(writer, request)
	history := agent.sessionStore.GetHistory(sessionID)
	if len(history) != 2 || history[1].Metadata["delivery_status"] != "failed" || history[1].Content != "retained answer" {
		t.Fatalf("final history = %#v", history)
	}
	if !logger.contains("Final SSE delivery failed") || logger.contains("Chat request completed") || logger.contains("SSE stream completed") {
		t.Fatalf("incorrect final delivery logs: %#v", logger.entries)
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	for _, entry := range logger.entries {
		if entry.message == "Final SSE delivery failed" && (entry.fields["error_type"] != "callback" || entry.fields["delivery_status"] != "failed" || entry.fields["operation"] != "chat_stream" || entry.fields["request_id"] == "") {
			t.Fatalf("lost delivery classification: %#v", entry.fields)
		}
	}
}

func TestDevOpsSSEClarificationSurvivesDone(t *testing.T) {
	agent, sessionID, _ := newSSEAgentFixture(t, &sseFixtureAI{clarification: true})
	response := httptest.NewRecorder()
	if err := agent.ProcessWithStreaming(t.Context(), sessionID, "fixture", NewSSECallback(response, response)); err != nil {
		t.Fatal(err)
	}
	if body := response.Body.String(); !strings.Contains(body, `"clarification":{"question":"Which region?"`) || !strings.Contains(body, "event: done") {
		t.Fatalf("missing clarification: %s", body)
	}
	history := agent.sessionStore.GetHistory(sessionID)
	if len(history) != 1 || history[0].Metadata["clarification"] == nil {
		t.Fatalf("missing history clarification: %#v", history)
	}
}
