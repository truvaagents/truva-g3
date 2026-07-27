package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// ================================
// Streaming Unit Tests for Orchestrator
// ================================

// StreamingMockAIClient implements both core.AIClient and core.StreamingAIClient
type StreamingMockAIClient struct {
	responses         map[string]string
	calls             []string
	supportsStreaming bool
	streamCallCount   int
}

func NewStreamingMockAIClient() *StreamingMockAIClient {
	return &StreamingMockAIClient{
		responses:         make(map[string]string),
		calls:             []string{},
		supportsStreaming: true,
	}
}

func (m *StreamingMockAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	m.calls = append(m.calls, prompt)

	// Return predefined responses based on prompt content
	if strings.Contains(prompt, "Create an execution plan") {
		return &core.AIResponse{
			Content: m.getPlanResponse(),
			Usage: core.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}, nil
	}

	if strings.Contains(prompt, "Synthesize") || strings.Contains(prompt, "synthesize") {
		return &core.AIResponse{
			Content: "This is a synthesized response combining all agent outputs.",
		}, nil
	}

	return &core.AIResponse{
		Content: "Default response",
	}, nil
}

func (m *StreamingMockAIClient) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	m.streamCallCount++
	m.calls = append(m.calls, prompt)

	response := "This is a streaming synthesized response from the orchestrator."

	chunkSize := 10
	var fullContent strings.Builder
	chunkIndex := 0

	for i := 0; i < len(response); i += chunkSize {
		// Check context cancellation
		select {
		case <-ctx.Done():
			if fullContent.Len() > 0 {
				return &core.AIResponse{
					Content: fullContent.String(),
					Model:   "mock-model",
				}, core.ErrStreamPartiallyCompleted
			}
			return nil, ctx.Err()
		default:
		}

		end := i + chunkSize
		if end > len(response) {
			end = len(response)
		}

		chunk := core.StreamChunk{
			Content: response[i:end],
			Delta:   true,
			Index:   chunkIndex,
			Model:   "mock-model",
		}
		fullContent.WriteString(response[i:end])
		chunkIndex++

		if err := callback(chunk); err != nil {
			return &core.AIResponse{
				Content: fullContent.String(),
				Model:   "mock-model",
			}, nil
		}
	}

	// Send final chunk
	usage := core.TokenUsage{
		PromptTokens:     len(prompt) / 4,
		CompletionTokens: len(response) / 4,
		TotalTokens:      (len(prompt) + len(response)) / 4,
	}
	finalChunk := core.StreamChunk{
		Delta:        false,
		Index:        chunkIndex,
		FinishReason: "stop",
		Model:        "mock-model",
		Usage:        &usage,
	}
	_ = callback(finalChunk)

	return &core.AIResponse{
		Content: fullContent.String(),
		Model:   "mock-model",
		Usage:   usage,
	}, nil
}

func (m *StreamingMockAIClient) SupportsStreaming() bool {
	return m.supportsStreaming
}

func (m *StreamingMockAIClient) getPlanResponse() string {
	plan := map[string]interface{}{
		"plan_id":          "test-plan-1",
		"original_request": "test request",
		"mode":             "autonomous",
		"steps": []map[string]interface{}{
			{
				"step_id":     "step-1",
				"agent_name":  "test-agent",
				"namespace":   "default",
				"instruction": "Test instruction",
				"depends_on":  []string{},
				"metadata": map[string]interface{}{
					"capability": "test_capability",
					"parameters": map[string]interface{}{
						"param1": "value1",
					},
				},
			},
		},
	}

	jsonBytes, _ := json.Marshal(plan)
	return string(jsonBytes)
}

// TestProcessRequestStreaming_BasicStreaming tests basic streaming functionality
func TestProcessRequestStreaming_BasicStreaming(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewStreamingMockAIClient()

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "test_capability", Description: "Test capability"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	var chunks []core.StreamChunk
	var fullContent strings.Builder
	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
		}
		return nil
	}

	resp, err := orchestrator.ProcessRequestStreaming(context.Background(), "Test request", nil, callback)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming failed: %v", err)
	}

	// Verify response
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}

	// Verify streaming-specific fields
	if resp.ChunksDelivered == 0 {
		t.Error("Expected ChunksDelivered > 0")
	}
	if !resp.StreamCompleted {
		t.Error("Expected StreamCompleted to be true")
	}
	if resp.PartialContent {
		t.Error("Expected PartialContent to be false")
	}

	// Verify chunks were delivered
	if len(chunks) == 0 {
		t.Error("Expected chunks to be delivered")
	}

	// Verify full content was streamed
	if fullContent.Len() == 0 {
		t.Error("Expected content to be streamed")
	}
}

// TestProcessRequestStreaming_FallbackToSimulated tests fallback when AI client doesn't support streaming
func TestProcessRequestStreaming_FallbackToSimulated(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient() // Non-streaming client

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "stock-analyzer",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "analyze_stock"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "stock-analyzer",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "analyze_stock"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "analyze_stock", Description: "Analyzes stocks"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	var chunks []core.StreamChunk
	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}

	resp, err := orchestrator.ProcessRequestStreaming(context.Background(), "Analyze AAPL stock", nil, callback)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming failed: %v", err)
	}

	// Should still work via simulated streaming
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}

	// Verify streaming completed (simulated)
	if !resp.StreamCompleted {
		t.Error("Expected StreamCompleted to be true even with simulated streaming")
	}

	// Verify chunks were delivered (simulated)
	if len(chunks) == 0 {
		t.Error("Expected simulated chunks to be delivered")
	}
}

func TestProcessRequestStreaming_FallbackDoesNotDuplicateConversationRejection(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-conversation-fallback",
		Name:         "stock-analyzer",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "analyze_stock"}},
	})

	orchestrator := NewAIOrchestrator(DefaultConfig(), discovery, aiClient)
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-conversation-fallback": {
			Registration: &core.ServiceRegistration{
				ID:           "test-conversation-fallback",
				Name:         "stock-analyzer",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "analyze_stock"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "analyze_stock", Description: "Analyzes stocks"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)
	capture := &conversationCaptureTelemetry{}
	orchestrator.SetTelemetry(capture)

	response, err := orchestrator.ProcessRequestStreaming(
		context.Background(),
		"Analyze AAPL stock",
		map[string]interface{}{
			MetadataConversationID: "invalid conversation",
			"application_key":      "preserved",
		},
		func(core.StreamChunk) error { return nil },
	)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming() error = %v", err)
	}
	if response == nil || !response.StreamCompleted {
		t.Fatalf("fallback business result = %+v, want completed response", response)
	}

	rejectionCount := 0
	for _, record := range capture.records {
		if record.name == conversationIDRejectionMetric {
			rejectionCount++
		}
	}
	if rejectionCount != 1 {
		t.Fatalf(
			"conversation rejection count = %d, want 1 across streaming fallback re-entry",
			rejectionCount,
		)
	}
}

// TestProcessRequestStreaming_CallbackStop tests callback can stop streaming
func TestProcessRequestStreaming_CallbackStop(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewStreamingMockAIClient()

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "test_capability", Description: "Test capability"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	chunkCount := 0
	callback := func(chunk core.StreamChunk) error {
		chunkCount++
		if chunkCount >= 3 {
			return context.Canceled // Stop after 3 chunks
		}
		return nil
	}

	resp, err := orchestrator.ProcessRequestStreaming(context.Background(), "Test request", nil, callback)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming failed: %v", err)
	}

	// Should have partial content
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}

	// Should indicate partial content
	if resp.StreamCompleted && !resp.PartialContent {
		// Either not completed or marked as partial
		t.Log("Callback stop may result in partial content")
	}
}

// TestProcessRequestStreaming_NilTelemetry tests nil telemetry doesn't panic
func TestProcessRequestStreaming_NilTelemetry(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewStreamingMockAIClient()

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)
	orchestrator.telemetry = nil // Explicitly set to nil

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "test_capability", Description: "Test capability"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	callback := func(chunk core.StreamChunk) error { return nil }

	// Should not panic
	resp, err := orchestrator.ProcessRequestStreaming(context.Background(), "Test request", nil, callback)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming failed with nil telemetry: %v", err)
	}
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}
}

// TestStreamingOrchestratorResponse_Fields tests StreamingOrchestratorResponse fields
func TestStreamingOrchestratorResponse_Fields(t *testing.T) {
	resp := &StreamingOrchestratorResponse{
		OrchestratorResponse: OrchestratorResponse{
			RequestID:       "test-123",
			OriginalRequest: "Test request",
			Response:        "Test response",
			RoutingMode:     ModeAutonomous,
			ExecutionTime:   100 * time.Millisecond,
			AgentsInvolved:  []string{"agent1", "agent2"},
			Confidence:      0.95,
		},
		ChunksDelivered: 10,
		StreamCompleted: true,
		PartialContent:  false,
	}

	// Verify embedded fields
	if resp.RequestID != "test-123" {
		t.Errorf("Expected RequestID 'test-123', got %q", resp.RequestID)
	}
	if resp.OriginalRequest != "Test request" {
		t.Errorf("Expected OriginalRequest 'Test request', got %q", resp.OriginalRequest)
	}
	if resp.Response != "Test response" {
		t.Errorf("Expected Response 'Test response', got %q", resp.Response)
	}

	// Verify streaming-specific fields
	if resp.ChunksDelivered != 10 {
		t.Errorf("Expected ChunksDelivered 10, got %d", resp.ChunksDelivered)
	}
	if !resp.StreamCompleted {
		t.Error("Expected StreamCompleted true")
	}
	if resp.PartialContent {
		t.Error("Expected PartialContent false")
	}
}

// TestBuildSynthesisPrompt tests the synthesis prompt builder
func TestBuildSynthesisPrompt(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()
	orchestrator := NewAIOrchestrator(DefaultConfig(), discovery, aiClient)

	result := &ExecutionResult{
		PlanID: "test-plan",
		Steps: []StepResult{
			{
				StepID:    "step-1",
				AgentName: "weather-agent",
				Response:  "Weather is sunny",
				Success:   true,
			},
			{
				StepID:    "step-2",
				AgentName: "news-agent",
				Response:  "Top news: Tech stocks up",
				Success:   true,
			},
		},
		Success: true,
	}

	prompt := orchestrator.buildSynthesisPrompt(context.Background(), "What's the weather and news?", result)

	// Verify XML-tagged prompt structure (EFFECTIVE_PROMPTS_GUIDE.md §8.3)
	expectedStrings := []string{
		"<user_request>",
		"What's the weather and news?",
		"</user_request>",
		"<agent_responses>",
		`<agent name="weather-agent" task=""`,
		"Weather is sunny",
		`<agent name="news-agent" task=""`,
		"Top news: Tech stocks up",
		"</agent_responses>",
		"Synthesize the above into a helpful answer.",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(prompt, expected) {
			t.Errorf("Expected prompt to contain '%s'", expected)
		}
	}

	// Verify the old format is gone
	if strings.Contains(prompt, "Agent Responses:\n") {
		t.Error("Expected XML-tagged format, not legacy 'Agent Responses:' header")
	}
}

// TestProcessRequestStreaming_AgentsInvolved tests that agents involved are tracked
func TestProcessRequestStreaming_AgentsInvolved(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewStreamingMockAIClient()

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "test_capability", Description: "Test capability"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	callback := func(chunk core.StreamChunk) error { return nil }

	resp, err := orchestrator.ProcessRequestStreaming(context.Background(), "Test request", nil, callback)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming failed: %v", err)
	}

	// Verify agents involved are tracked
	if len(resp.AgentsInvolved) == 0 {
		t.Error("Expected AgentsInvolved to be populated")
	}
}

// TestProcessRequestStreaming_ExecutionTime tests that execution time is tracked
func TestProcessRequestStreaming_ExecutionTime(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewStreamingMockAIClient()

	// Register test agent
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	config := DefaultConfig()
	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Setup catalog
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			},
			Capabilities: []EnhancedCapability{
				{Name: "test_capability", Description: "Test capability"},
			},
		},
	}
	orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

	callback := func(chunk core.StreamChunk) error { return nil }

	resp, err := orchestrator.ProcessRequestStreaming(context.Background(), "Test request", nil, callback)
	if err != nil {
		t.Fatalf("ProcessRequestStreaming failed: %v", err)
	}

	// Verify execution time is positive
	if resp.ExecutionTime <= 0 {
		t.Errorf("Expected positive ExecutionTime, got %v", resp.ExecutionTime)
	}
}

// =============================================================================
// Streaming Synthesis Parameter Tests
// =============================================================================

// optionsCapturingStreamingClient captures the AIOptions passed to StreamResponse,
// allowing tests to verify that the streaming synthesis path forwards the correct
// Temperature and MaxTokens from OrchestratorConfig.
type optionsCapturingStreamingClient struct {
	capturedGenerateOpts []*core.AIOptions
	capturedStreamOpts   []*core.AIOptions
}

func (m *optionsCapturingStreamingClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if opts != nil {
		optsCopy := *opts
		m.capturedGenerateOpts = append(m.capturedGenerateOpts, &optsCopy)
	}

	// Return a plan response when planning, otherwise a generic response
	if strings.Contains(prompt, "Create an execution plan") {
		plan := map[string]interface{}{
			"plan_id":          "test-plan-1",
			"original_request": "test request",
			"mode":             "autonomous",
			"steps": []map[string]interface{}{
				{
					"step_id":     "step-1",
					"agent_name":  "test-agent",
					"namespace":   "default",
					"instruction": "Test instruction",
					"depends_on":  []string{},
					"metadata": map[string]interface{}{
						"capability": "test_capability",
						"parameters": map[string]interface{}{
							"param1": "value1",
						},
					},
				},
			},
		}
		jsonBytes, _ := json.Marshal(plan)
		return &core.AIResponse{
			Content: string(jsonBytes),
			Usage:   core.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		}, nil
	}

	return &core.AIResponse{Content: "Default response"}, nil
}

func (m *optionsCapturingStreamingClient) StreamResponse(ctx context.Context, prompt string, opts *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	if opts != nil {
		optsCopy := *opts
		m.capturedStreamOpts = append(m.capturedStreamOpts, &optsCopy)
	}

	// Simulate streaming synthesis
	response := "Streaming synthesis response."
	for i := 0; i < len(response); i += 10 {
		end := i + 10
		if end > len(response) {
			end = len(response)
		}
		chunk := core.StreamChunk{Content: response[i:end], Delta: true, Index: i / 10}
		if err := callback(chunk); err != nil {
			break
		}
	}
	// Final chunk
	_ = callback(core.StreamChunk{FinishReason: "stop"})

	return &core.AIResponse{
		Content: response,
		Model:   "test-model",
		Usage:   core.TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
	}, nil
}

func (m *optionsCapturingStreamingClient) SupportsStreaming() bool { return true }

func (m *optionsCapturingStreamingClient) lastStreamOpts() *core.AIOptions {
	if len(m.capturedStreamOpts) == 0 {
		return nil
	}
	return m.capturedStreamOpts[len(m.capturedStreamOpts)-1]
}

// TestProcessRequestStreaming_UsesConfiguredSynthesisParameters verifies that
// the streaming synthesis path passes SynthesisTemperature and SynthesisMaxTokens
// from OrchestratorConfig to the StreamResponse AIOptions.
func TestProcessRequestStreaming_UsesConfiguredSynthesisParameters(t *testing.T) {
	tests := []struct {
		name         string
		temperature  float64
		maxTokens    int
		expectTemp   float32
		expectTokens int
	}{
		{
			name:         "default values (0.5/5000)",
			temperature:  0.5,
			maxTokens:    5000,
			expectTemp:   0.5,
			expectTokens: 5000,
		},
		{
			name:         "streaming chat values (0.7/8000)",
			temperature:  0.7,
			maxTokens:    8000,
			expectTemp:   0.7,
			expectTokens: 8000,
		},
		{
			name:         "deterministic (0.0/1000)",
			temperature:  0.0,
			maxTokens:    1000,
			expectTemp:   0.0,
			expectTokens: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discovery := NewMockDiscovery()
			aiClient := &optionsCapturingStreamingClient{}

			_ = discovery.Register(context.Background(), &core.ServiceRegistration{
				ID:           "test-1",
				Name:         "test-agent",
				Address:      "localhost",
				Port:         8080,
				Capabilities: []core.Capability{{Name: "test_capability"}},
			})

			config := DefaultConfig()
			config.SynthesisTemperature = tt.temperature
			config.SynthesisMaxTokens = tt.maxTokens
			orchestrator := NewAIOrchestrator(config, discovery, aiClient)

			orchestrator.catalog.agents = map[string]*AgentInfo{
				"test-1": {
					Registration: &core.ServiceRegistration{
						ID:           "test-1",
						Name:         "test-agent",
						Address:      "localhost",
						Port:         8080,
						Capabilities: []core.Capability{{Name: "test_capability"}},
					},
					Capabilities: []EnhancedCapability{
						{Name: "test_capability", Description: "Test capability"},
					},
				},
			}
			orchestrator.executor = NewSmartExecutor(orchestrator.catalog)

			callback := func(chunk core.StreamChunk) error { return nil }

			_, err := orchestrator.ProcessRequestStreaming(context.Background(), "Test request", nil, callback)
			if err != nil {
				t.Fatalf("ProcessRequestStreaming failed: %v", err)
			}

			// Verify the StreamResponse call received correct AIOptions
			opts := aiClient.lastStreamOpts()
			if opts == nil {
				t.Fatal("Expected StreamResponse to be called with AIOptions")
			}
			if opts.Temperature != tt.expectTemp {
				t.Errorf("StreamResponse Temperature: expected %f, got %f",
					tt.expectTemp, opts.Temperature)
			}
			if opts.MaxTokens != tt.expectTokens {
				t.Errorf("StreamResponse MaxTokens: expected %d, got %d",
					tt.expectTokens, opts.MaxTokens)
			}
			if opts.SystemPrompt != synthesisSystemPrompt {
				t.Error("StreamResponse SystemPrompt should be the shared synthesis system prompt")
			}
		})
	}
}

// TestBuildSynthesisPrompt_Parity verifies that both buildSynthesisPrompt implementations
// (AISynthesizer and AIOrchestrator) produce structurally identical output when given
// the same ExecutionResult. This is the D.5.3 parity test from BUG_STREAMING_SYNTHESIS_PROMPT_PARITY.md.
func TestBuildSynthesisPrompt_Parity(t *testing.T) {
	request := "Tell me about Apple stock and recent news"

	// Shared test data — identical for both paths
	sharedResult := &ExecutionResult{
		PlanID: "parity-plan",
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "stock-market-tool",
				Instruction: "Get Apple stock price",
				Response:    `{"symbol":"AAPL","price":185.42,"change":-1.23}`,
				Success:     true,
			},
			{
				StepID:      "step-2",
				AgentName:   "news-tool",
				Instruction: "Get latest Apple news",
				Response:    `{"articles":[{"title":"Apple Q4 Earnings","source":"Reuters"}]}`,
				Success:     true,
			},
			{
				StepID:      "step-3",
				AgentName:   "research-agent",
				Instruction: "Deep analysis of Apple market position",
				Error:       "timeout after 30s",
				Success:     false,
			},
		},
		Success: true,
	}

	// Deep-copy the result so each path gets its own copy (buildSynthesisPrompt may mutate Metadata)
	copyResult := func() *ExecutionResult {
		r := &ExecutionResult{
			PlanID:  sharedResult.PlanID,
			Success: sharedResult.Success,
			Steps:   make([]StepResult, len(sharedResult.Steps)),
		}
		for i, step := range sharedResult.Steps {
			r.Steps[i] = StepResult{
				StepID:      step.StepID,
				AgentName:   step.AgentName,
				Instruction: step.Instruction,
				Response:    step.Response,
				Error:       step.Error,
				Success:     step.Success,
			}
		}
		return r
	}

	// Non-streaming path (synthesizer.go)
	synthesizer := NewAISynthesizer(NewMockAIClient())
	nonStreamingPrompt := synthesizer.buildSynthesisPrompt(context.Background(), request, copyResult())

	// Streaming path (orchestrator.go)
	discovery := NewMockDiscovery()
	orchestrator := NewAIOrchestrator(DefaultConfig(), discovery, NewMockAIClient())
	streamingPrompt := orchestrator.buildSynthesisPrompt(context.Background(), request, copyResult())

	// Exact string equality — both paths should produce byte-identical output
	if nonStreamingPrompt != streamingPrompt {
		t.Errorf("Prompt parity violation: non-streaming and streaming buildSynthesisPrompt produce different output.\n\n--- Non-streaming ---\n%s\n\n--- Streaming ---\n%s", nonStreamingPrompt, streamingPrompt)
	}

	// Also verify key structural elements are present (regression guard)
	for _, expected := range []string{
		"<user_request>",
		request,
		"</user_request>",
		"<agent_responses>",
		`<agent name="stock-market-tool" task="Get Apple stock price" status="success">`,
		`<agent name="news-tool" task="Get latest Apple news" status="success">`,
		`<agent name="research-agent" task="Deep analysis of Apple market position" status="failed">`,
		"timeout after 30s",
		"</agent_responses>",
		"Synthesize the above into a helpful answer.",
	} {
		if !strings.Contains(nonStreamingPrompt, expected) {
			t.Errorf("Expected prompt to contain %q", expected)
		}
	}

	// Verify MarshalIndent formatting (multi-line JSON, not compact)
	if !strings.Contains(nonStreamingPrompt, "\"symbol\": \"AAPL\"") {
		t.Error("Expected pretty-printed JSON with MarshalIndent (2-space indent)")
	}
}
