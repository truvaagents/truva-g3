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

func newSSEAgentFixture(t *testing.T, client *sseFixtureAI) (*TravelChatAgent, string, *sseFixtureLogger) {
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
	agent := &TravelChatAgent{BaseAgent: base, orchestrator: orch, sessionStore: store}
	return agent, store.Create("user-1", nil).ID, logger
}

func TestTravelSSEPartialNativeResultIsNotSuccess(t *testing.T) {
	client := &sseFixtureAI{partial: true}
	agent, sessionID, logger := newSSEAgentFixture(t, client)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat/stream", strings.NewReader(`{"session_id":"`+sessionID+`","message":"fixture"}`))
	NewSSEHandler(agent).ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.HasPrefix(body, ": heartbeat\n\n") || !strings.Contains(body, `"code":"stream_incomplete"`) || !strings.Contains(body, `"retryable":false`) || strings.Contains(body, "event: done") {
		t.Fatalf("partial result emitted success or retry permission: %s", body)
	}
	history := agent.sessionStore.GetHistory(sessionID)
	if len(history) != 2 || history[1].Content != "retained answer" || history[1].Metadata["partial_content"] != true || history[1].Metadata["stream_completed"] != false {
		t.Fatalf("partial history lost: %#v", history)
	}
	if client.streamCalls != 1 || logger.contains("Chat request completed") || logger.contains("SSE stream completed") {
		t.Fatal("partial result retried or logged success")
	}
}

func TestTravelSSEChunkFailureCancelsAndRetainsPartialHistory(t *testing.T) {
	client := &sseFixtureAI{}
	agent, sessionID, logger := newSSEAgentFixture(t, client)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := newSSETestResponseWriter()
	writer.failEvent = "chunk"
	callback := newManagedSSECallback(writer, writer, ctx, cancel)
	err := agent.ProcessWithStreaming(ctx, sessionID, "fixture", callback)
	if !errors.Is(err, core.ErrStreamPartiallyCompleted) || !errors.Is(err, errTestSSEWrite) || ctx.Err() == nil {
		t.Fatalf("delivery error = %v, context = %v", err, ctx.Err())
	}
	history := agent.sessionStore.GetHistory(sessionID)
	if len(history) != 1 || history[0].Content != "retained answer" || history[0].Metadata["partial_content"] != true {
		t.Fatalf("partial history = %#v", history)
	}
	if logger.contains("Chat request completed") || client.streamCalls != 1 {
		t.Fatal("failed delivery reported success or retried")
	}
}

func TestTravelSSEFinalFailureRetainsExistingHistorySchema(t *testing.T) {
	agent, sessionID, logger := newSSEAgentFixture(t, &sseFixtureAI{})
	writer := newSSETestResponseWriter()
	writer.failEvent = "done"
	callback := NewSSECallback(writer, writer)
	err := agent.ProcessWithStreaming(t.Context(), sessionID, "fixture", callback)
	if !errors.Is(err, core.ErrStreamPartiallyCompleted) || !errors.Is(err, errTestSSEWrite) {
		t.Fatalf("final delivery = %v", err)
	}
	history := agent.sessionStore.GetHistory(sessionID)
	if len(history) != 1 || history[0].Content != "retained answer" || history[0].Metadata["total_tokens"] != float64(5) {
		t.Fatalf("final history = %#v", history)
	}
	if _, exists := history[0].Metadata["delivery_status"]; exists {
		t.Fatal("unexpected DevOps history accounting")
	}
	if logger.contains("Chat request completed") {
		t.Fatal("failed terminal delivery logged success")
	}
}
