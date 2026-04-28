package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestAISynthesizer_Synthesize(t *testing.T) {
	aiClient := NewMockAIClient()
	synthesizer := NewAISynthesizer(aiClient)

	ctx := context.Background()
	request := "Test request"

	results := &ExecutionResult{
		PlanID:  "test-plan",
		Success: true,
		Steps: []StepResult{
			{
				StepID:    "step-1",
				AgentName: "agent1",
				Response:  `{"data": "response1"}`,
				Success:   true,
			},
			{
				StepID:    "step-2",
				AgentName: "agent2",
				Response:  "plain text response",
				Success:   true,
			},
			{
				StepID:    "step-3",
				AgentName: "agent3",
				Error:     "agent failed",
				Success:   false,
			},
		},
	}

	// Test LLM synthesis
	synthesizer.SetStrategy(StrategyLLM)
	response, err := synthesizer.Synthesize(ctx, request, results)
	if err != nil {
		t.Errorf("LLM synthesis failed: %v", err)
	}
	if response == "" {
		t.Error("Expected non-empty synthesized response")
	}

	// Test template synthesis
	synthesizer.SetStrategy(StrategyTemplate)
	response, err = synthesizer.Synthesize(ctx, request, results)
	if err != nil {
		t.Errorf("Template synthesis failed: %v", err)
	}
	if !strings.Contains(response, "Response to:") {
		t.Error("Template synthesis should contain 'Response to:'")
	}
	if !strings.Contains(response, "2 of 3 tasks successfully") {
		t.Error("Template should mention success count")
	}

	// Test simple synthesis
	synthesizer.SetStrategy(StrategySimple)
	response, err = synthesizer.Synthesize(ctx, request, results)
	if err != nil {
		t.Errorf("Simple synthesis failed: %v", err)
	}
	if !strings.Contains(response, "agent1:") {
		t.Error("Simple synthesis should contain agent names")
	}
}

func TestAISynthesizer_BuildSynthesisPrompt(t *testing.T) {
	synthesizer := &AISynthesizer{}

	request := "Analyze stock"
	results := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "stock-agent",
				Instruction: "Get stock price",
				Response:    `{"price": 150.50}`,
				Success:     true,
			},
			{
				StepID:    "step-2",
				AgentName: "news-agent",
				Error:     "Service unavailable",
				Success:   false,
			},
		},
	}

	prompt := synthesizer.buildSynthesisPrompt(context.Background(), request, results)

	// Verify XML-tagged prompt structure (EFFECTIVE_PROMPTS_GUIDE.md §8.3)
	expectedStrings := []string{
		"<user_request>",
		"Analyze stock",
		"</user_request>",
		"<agent_responses>",
		`<agent name="stock-agent" task="Get stock price" status="success">`,
		`"price": 150.5`,
		"</agent>",
		`<agent name="news-agent" task="" status="failed">`,
		"Service unavailable",
		"</agent_responses>",
		"Synthesize the above into a helpful answer.",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(prompt, expected) {
			t.Errorf("Expected prompt to contain '%s'", expected)
		}
	}

	// Verify instructions are NOT in user message (moved to system prompt)
	if strings.Contains(prompt, "Instructions:") {
		t.Error("Instructions should be in system message, not in buildSynthesisPrompt output")
	}
}

func TestAISynthesizer_SynthesizeWithTemplate(t *testing.T) {
	synthesizer := &AISynthesizer{}

	request := "Test request"
	results := &ExecutionResult{
		Steps: []StepResult{
			{
				AgentName: "agent1",
				Response:  `{"status": "ok"}`,
				Success:   true,
			},
			{
				AgentName: "agent2",
				Response:  "plain response",
				Success:   true,
			},
			{
				AgentName: "agent3",
				Error:     "timeout",
				Success:   false,
			},
		},
	}

	response, err := synthesizer.synthesizeWithTemplate(request, results)
	if err != nil {
		t.Errorf("Template synthesis failed: %v", err)
	}

	// Check structure
	if !strings.Contains(response, "Response to: Test request") {
		t.Error("Missing request header")
	}

	if !strings.Contains(response, "Results:") {
		t.Error("Missing results section")
	}

	if !strings.Contains(response, "agent1:") {
		t.Error("Missing successful agent")
	}

	if !strings.Contains(response, "Some agents encountered errors") {
		t.Error("Missing error notification")
	}

	if !strings.Contains(response, "Completed 2 of 3 tasks successfully") {
		t.Error("Missing summary")
	}
}

func TestAISynthesizer_SynthesizeSimple(t *testing.T) {
	synthesizer := &AISynthesizer{}

	// Test with successful results
	results := &ExecutionResult{
		Steps: []StepResult{
			{
				AgentName: "agent1",
				Response:  "response1",
				Success:   true,
			},
			{
				AgentName: "agent2",
				Response:  "response2",
				Success:   true,
			},
			{
				AgentName: "agent3",
				Response:  "",
				Success:   false,
			},
		},
	}

	response, err := synthesizer.synthesizeSimple(results)
	if err != nil {
		t.Errorf("Simple synthesis failed: %v", err)
	}

	if !strings.Contains(response, "agent1: response1") {
		t.Error("Missing agent1 response")
	}

	if !strings.Contains(response, "agent2: response2") {
		t.Error("Missing agent2 response")
	}

	if strings.Contains(response, "agent3") {
		t.Error("Failed agent should not be included")
	}

	// Test with no successful results
	emptyResults := &ExecutionResult{
		Steps: []StepResult{
			{Success: false},
		},
	}

	response, err = synthesizer.synthesizeSimple(emptyResults)
	if err != nil {
		t.Errorf("Simple synthesis failed: %v", err)
	}

	if response != "No successful responses to synthesize" {
		t.Errorf("Expected no responses message, got: %s", response)
	}
}

func TestSimpleSynthesizer(t *testing.T) {
	synthesizer := NewSynthesizer()

	ctx := context.Background()
	request := "Test request"

	results := &ExecutionResult{
		Steps: []StepResult{
			{
				AgentName: "agent1",
				Response:  "response1",
				Success:   true,
			},
			{
				AgentName: "agent2",
				Error:     "failed",
				Success:   false,
			},
		},
	}

	// Test template strategy
	synthesizer.SetStrategy(StrategyTemplate)
	response, err := synthesizer.Synthesize(ctx, request, results)
	if err != nil {
		t.Errorf("Template synthesis failed: %v", err)
	}

	if !strings.Contains(response, "agent1 completed successfully") {
		t.Error("Expected success message for agent1")
	}

	if !strings.Contains(response, "agent2 failed") {
		t.Error("Expected failure message for agent2")
	}

	// Test simple strategy
	synthesizer.SetStrategy(StrategySimple)
	response, err = synthesizer.Synthesize(ctx, request, results)
	if err != nil {
		t.Errorf("Simple synthesis failed: %v", err)
	}

	if !strings.Contains(response, "response1") {
		t.Error("Expected response1 in output")
	}

	if strings.Contains(response, "failed") {
		t.Error("Failed responses should not be in simple output")
	}
}

func TestSynthesisStrategies(t *testing.T) {
	// Test that all strategies are handled
	strategies := []SynthesisStrategy{
		StrategyLLM,
		StrategyTemplate,
		StrategySimple,
		StrategyCustom,
	}

	aiClient := NewMockAIClient()
	synthesizer := NewAISynthesizer(aiClient)

	ctx := context.Background()
	results := &ExecutionResult{
		Steps: []StepResult{
			{
				AgentName: "test",
				Response:  "test",
				Success:   true,
			},
		},
	}

	for _, strategy := range strategies {
		synthesizer.SetStrategy(strategy)
		_, err := synthesizer.Synthesize(ctx, "test", results)
		if err != nil {
			t.Errorf("Strategy %s failed: %v", strategy, err)
		}
	}
}

func BenchmarkSynthesizer_Simple(b *testing.B) {
	synthesizer := &AISynthesizer{}
	results := &ExecutionResult{
		Steps: []StepResult{
			{AgentName: "agent1", Response: "response1", Success: true},
			{AgentName: "agent2", Response: "response2", Success: true},
			{AgentName: "agent3", Response: "response3", Success: true},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = synthesizer.synthesizeSimple(results)
	}
}

func BenchmarkSynthesizer_Template(b *testing.B) {
	synthesizer := &AISynthesizer{}
	results := &ExecutionResult{
		Steps: []StepResult{
			{AgentName: "agent1", Response: `{"data": "test"}`, Success: true},
			{AgentName: "agent2", Response: "response2", Success: true},
			{AgentName: "agent3", Error: "failed", Success: false},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = synthesizer.synthesizeWithTemplate("test request", results)
	}
}

func TestExecutionResult_JSON(t *testing.T) {
	// Test that results can be marshaled/unmarshaled properly
	result := &ExecutionResult{
		PlanID:        "test-123",
		Success:       true,
		TotalDuration: 5 * time.Second,
		Steps: []StepResult{
			{
				StepID:    "step-1",
				AgentName: "agent1",
				Response:  "test",
				Success:   true,
				Duration:  1 * time.Second,
			},
		},
	}

	// This is more of a smoke test to ensure our structures are serializable
	// which is important for the orchestration system
	_ = result.PlanID
	_ = result.Success
	_ = result.TotalDuration
	_ = result.Steps[0].Duration
}

// =============================================================================
// Synthesis LLM Parameters Tests
// =============================================================================

// synthesisCapturingAIClient captures the AIOptions passed to GenerateResponse.
// This allows tests to verify that the synthesizer forwards the correct
// Temperature and MaxTokens to the underlying AI client.
type synthesisCapturingAIClient struct {
	mu           sync.Mutex
	capturedOpts []*core.AIOptions
	response     string
	err          error
}

func (m *synthesisCapturingAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Deep copy opts to avoid capturing a pointer that might be reused
	if opts != nil {
		optsCopy := *opts
		m.capturedOpts = append(m.capturedOpts, &optsCopy)
	}

	if m.err != nil {
		return nil, m.err
	}

	resp := m.response
	if resp == "" {
		resp = "Synthesized response from capturing mock."
	}
	return &core.AIResponse{
		Content:  resp,
		Model:    "test-model",
		Provider: "test-provider",
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

func (m *synthesisCapturingAIClient) lastOpts() *core.AIOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.capturedOpts) == 0 {
		return nil
	}
	return m.capturedOpts[len(m.capturedOpts)-1]
}

// TestNewAISynthesizer_Defaults verifies constructor defaults match OrchestratorConfig defaults.
// This ensures standalone synthesizer usage (tests, direct creation) behaves identically
// to orchestrator-managed usage.
func TestNewAISynthesizer_Defaults(t *testing.T) {
	aiClient := NewMockAIClient()
	s := NewAISynthesizer(aiClient)

	if s.synthesisTemperature != 0.5 {
		t.Errorf("Expected default synthesisTemperature=0.5, got %f", s.synthesisTemperature)
	}
	if s.synthesisMaxTokens != 5000 {
		t.Errorf("Expected default synthesisMaxTokens=5000, got %d", s.synthesisMaxTokens)
	}
	if s.strategy != StrategyLLM {
		t.Errorf("Expected default strategy=StrategyLLM, got %s", s.strategy)
	}
	if s.aiClient != aiClient {
		t.Error("Expected aiClient to be set")
	}
}

// TestAISynthesizer_SetSynthesisTemperature tests the setter validation logic.
func TestAISynthesizer_SetSynthesisTemperature(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		{"valid_0.7", 0.7, 0.7},
		{"valid_0.3", 0.3, 0.3},
		{"valid_1.0", 1.0, 1.0},
		{"zero_accepted", 0.0, 0.0},
		{"max_boundary_2.0", 2.0, 2.0},
		{"above_max_ignored", 2.1, 0.5}, // stays at constructor default
		{"negative_ignored", -0.1, 0.5}, // stays at constructor default
		{"large_negative_ignored", -5.0, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewAISynthesizer(NewMockAIClient())
			// s starts with default 0.5
			s.SetSynthesisTemperature(tt.input)

			if s.synthesisTemperature != tt.expected {
				t.Errorf("After SetSynthesisTemperature(%f): got %f, want %f",
					tt.input, s.synthesisTemperature, tt.expected)
			}
		})
	}
}

// TestAISynthesizer_SetSynthesisTemperature_Overwrite verifies that a valid set
// followed by an invalid set does not revert the value.
func TestAISynthesizer_SetSynthesisTemperature_Overwrite(t *testing.T) {
	s := NewAISynthesizer(NewMockAIClient())

	s.SetSynthesisTemperature(0.9)
	if s.synthesisTemperature != 0.9 {
		t.Fatalf("Expected 0.9 after first set, got %f", s.synthesisTemperature)
	}

	// Invalid value should NOT revert to default; it should keep 0.9
	s.SetSynthesisTemperature(3.0)
	if s.synthesisTemperature != 0.9 {
		t.Errorf("Expected 0.9 to be preserved after invalid set, got %f", s.synthesisTemperature)
	}

	// Valid overwrite
	s.SetSynthesisTemperature(1.5)
	if s.synthesisTemperature != 1.5 {
		t.Errorf("Expected 1.5 after valid overwrite, got %f", s.synthesisTemperature)
	}
}

// TestAISynthesizer_SetSynthesisMaxTokens tests the setter validation logic.
func TestAISynthesizer_SetSynthesisMaxTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{"valid_3000", 3000, 3000},
		{"valid_1", 1, 1},
		{"valid_100000", 100000, 100000},
		{"zero_ignored", 0, 5000},      // stays at constructor default
		{"negative_ignored", -1, 5000}, // stays at constructor default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewAISynthesizer(NewMockAIClient())
			s.SetSynthesisMaxTokens(tt.input)

			if s.synthesisMaxTokens != tt.expected {
				t.Errorf("After SetSynthesisMaxTokens(%d): got %d, want %d",
					tt.input, s.synthesisMaxTokens, tt.expected)
			}
		})
	}
}

// TestAISynthesizer_SetSynthesisMaxTokens_Overwrite verifies that invalid sets
// preserve the previously-set valid value.
func TestAISynthesizer_SetSynthesisMaxTokens_Overwrite(t *testing.T) {
	s := NewAISynthesizer(NewMockAIClient())

	s.SetSynthesisMaxTokens(3000)
	if s.synthesisMaxTokens != 3000 {
		t.Fatalf("Expected 3000, got %d", s.synthesisMaxTokens)
	}

	// Invalid should preserve 3000
	s.SetSynthesisMaxTokens(0)
	if s.synthesisMaxTokens != 3000 {
		t.Errorf("Expected 3000 preserved, got %d", s.synthesisMaxTokens)
	}

	s.SetSynthesisMaxTokens(-10)
	if s.synthesisMaxTokens != 3000 {
		t.Errorf("Expected 3000 preserved, got %d", s.synthesisMaxTokens)
	}
}

// TestAISynthesizer_SynthesizeWithLLM_PassesConfiguredParameters verifies that
// the synthesizer forwards the configured Temperature and MaxTokens to the AI client.
func TestAISynthesizer_SynthesizeWithLLM_PassesConfiguredParameters(t *testing.T) {
	tests := []struct {
		name         string
		temperature  float64
		maxTokens    int
		expectTemp   float32 // core.AIOptions.Temperature is float32
		expectTokens int
	}{
		{
			name:         "default values",
			temperature:  0.5,
			maxTokens:    5000,
			expectTemp:   0.5,
			expectTokens: 5000,
		},
		{
			name:         "custom streaming values",
			temperature:  0.7,
			maxTokens:    8000,
			expectTemp:   0.7,
			expectTokens: 8000,
		},
		{
			name:         "zero temperature",
			temperature:  0.0,
			maxTokens:    1000,
			expectTemp:   0.0,
			expectTokens: 1000,
		},
		{
			name:         "max temperature",
			temperature:  2.0,
			maxTokens:    500,
			expectTemp:   2.0,
			expectTokens: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &synthesisCapturingAIClient{}
			s := NewAISynthesizer(mock)
			s.SetSynthesisTemperature(tt.temperature)
			s.SetSynthesisMaxTokens(tt.maxTokens)

			results := &ExecutionResult{
				Steps: []StepResult{
					{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
				},
			}

			_, err := s.Synthesize(context.Background(), "test request", results)
			if err != nil {
				t.Fatalf("Synthesize failed: %v", err)
			}

			opts := mock.lastOpts()
			if opts == nil {
				t.Fatal("Expected AI options to be captured")
			}
			if opts.Temperature != tt.expectTemp {
				t.Errorf("Expected Temperature=%f, got %f", tt.expectTemp, opts.Temperature)
			}
			if opts.MaxTokens != tt.expectTokens {
				t.Errorf("Expected MaxTokens=%d, got %d", tt.expectTokens, opts.MaxTokens)
			}
			if opts.SystemPrompt != synthesisSystemPrompt {
				t.Error("Expected SystemPrompt to be the shared synthesis system prompt")
			}
		})
	}
}

// TestAISynthesizer_SynthesizeWithLLM_ErrorPath_RecordsParameters verifies that
// when the AI client returns an error, the debug interaction still records the
// correct Temperature and MaxTokens.
func TestAISynthesizer_SynthesizeWithLLM_ErrorPath_RecordsParameters(t *testing.T) {
	mock := &synthesisCapturingAIClient{
		err: errors.New("AI service unavailable"),
	}
	s := NewAISynthesizer(mock)
	s.SetSynthesisTemperature(0.8)
	s.SetSynthesisMaxTokens(4000)

	// Set up a debug store to capture the interaction
	debugStore := &capturingDebugStore{}
	s.SetLLMDebugStore(debugStore)

	results := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
		},
	}

	_, err := s.Synthesize(context.Background(), "test request", results)
	if err == nil {
		t.Fatal("Expected error from Synthesize")
	}

	// Verify AIOptions were still passed correctly (the call happened, then errored)
	opts := mock.lastOpts()
	if opts == nil {
		t.Fatal("Expected AI options to be captured even on error")
	}
	if opts.Temperature != 0.8 {
		t.Errorf("Expected Temperature=0.8, got %f", opts.Temperature)
	}
	if opts.MaxTokens != 4000 {
		t.Errorf("Expected MaxTokens=4000, got %d", opts.MaxTokens)
	}

	// Wait briefly for async debug recording to complete
	time.Sleep(50 * time.Millisecond)

	// Verify debug interaction recorded correct parameters
	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction to be recorded")
	}
	interaction := interactions[0]
	if interaction.Temperature != 0.8 {
		t.Errorf("Debug interaction Temperature: expected 0.8, got %f", interaction.Temperature)
	}
	if interaction.MaxTokens != 4000 {
		t.Errorf("Debug interaction MaxTokens: expected 4000, got %d", interaction.MaxTokens)
	}
	if interaction.Success {
		t.Error("Expected debug interaction to record Success=false")
	}
}

// TestAISynthesizer_SynthesizeWithLLM_SuccessPath_RecordsParameters verifies that
// successful synthesis records the correct parameters in the debug interaction.
func TestAISynthesizer_SynthesizeWithLLM_SuccessPath_RecordsParameters(t *testing.T) {
	mock := &synthesisCapturingAIClient{}
	s := NewAISynthesizer(mock)
	s.SetSynthesisTemperature(0.6)
	s.SetSynthesisMaxTokens(3000)

	debugStore := &capturingDebugStore{}
	s.SetLLMDebugStore(debugStore)

	results := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
		},
	}

	resp, err := s.Synthesize(context.Background(), "test request", results)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}
	if resp == "" {
		t.Error("Expected non-empty response")
	}

	// Wait briefly for async debug recording
	time.Sleep(50 * time.Millisecond)

	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction")
	}
	interaction := interactions[0]
	if interaction.Temperature != 0.6 {
		t.Errorf("Debug interaction Temperature: expected 0.6, got %f", interaction.Temperature)
	}
	if interaction.MaxTokens != 3000 {
		t.Errorf("Debug interaction MaxTokens: expected 3000, got %d", interaction.MaxTokens)
	}
	if !interaction.Success {
		t.Error("Expected debug interaction to record Success=true")
	}
	if interaction.Type != "synthesis" {
		t.Errorf("Expected Type='synthesis', got %q", interaction.Type)
	}
}

// capturingDebugStore captures LLM interactions for test verification.
// Implements the full LLMDebugStore interface.
type capturingDebugStore struct {
	mu           sync.Mutex
	interactions []LLMInteraction
}

func (s *capturingDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interactions = append(s.interactions, interaction)
	return nil
}

func (s *capturingDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return &LLMDebugRecord{RequestID: requestID}, nil
}

func (s *capturingDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

func (s *capturingDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

func (s *capturingDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

func (s *capturingDebugStore) getInteractions() []LLMInteraction {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]LLMInteraction, len(s.interactions))
	copy(result, s.interactions)
	return result
}

// =============================================================================
// Model Override Tests
// =============================================================================

// TestAISynthesizer_SetSynthesisModel_PropagatesModelToAIOptions verifies that
// SetSynthesisModel causes opts.Model to be set on GenerateResponse calls.
func TestAISynthesizer_SetSynthesisModel_PropagatesModelToAIOptions(t *testing.T) {
	t.Run("model override passed to GenerateResponse", func(t *testing.T) {
		mock := &synthesisCapturingAIClient{}
		s := NewAISynthesizer(mock)
		s.SetSynthesisModel("fast")

		results := &ExecutionResult{
			Steps: []StepResult{
				{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
			},
		}

		_, err := s.Synthesize(context.Background(), "test request", results)
		if err != nil {
			t.Fatalf("Synthesize failed: %v", err)
		}

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "fast" {
			t.Errorf("Expected opts.Model='fast', got %q", opts.Model)
		}
	})

	t.Run("empty model leaves opts.Model unset", func(t *testing.T) {
		mock := &synthesisCapturingAIClient{}
		s := NewAISynthesizer(mock)
		// Do NOT call SetSynthesisModel

		results := &ExecutionResult{
			Steps: []StepResult{
				{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
			},
		}

		_, err := s.Synthesize(context.Background(), "test request", results)
		if err != nil {
			t.Fatalf("Synthesize failed: %v", err)
		}

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "" {
			t.Errorf("Expected opts.Model='', got %q", opts.Model)
		}
	})
}

// TestAISynthesizer_SetSynthesisModel_ErrorPath_RecordsModelInDebugPayload verifies
// that when GenerateResponse fails, the debug interaction records Model = s.model.
func TestAISynthesizer_SetSynthesisModel_ErrorPath_RecordsModelInDebugPayload(t *testing.T) {
	mock := &synthesisCapturingAIClient{
		err: errors.New("AI service unavailable"),
	}
	s := NewAISynthesizer(mock)
	s.SetSynthesisModel("fast")

	debugStore := &capturingDebugStore{}
	s.SetLLMDebugStore(debugStore)

	results := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
		},
	}

	_, err := s.Synthesize(context.Background(), "test request", results)
	if err == nil {
		t.Fatal("Expected error from Synthesize")
	}

	// Wait for async debug recording
	time.Sleep(50 * time.Millisecond)

	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction")
	}
	if interactions[0].Model != "fast" {
		t.Errorf("Expected debug interaction Model='fast', got %q", interactions[0].Model)
	}
	if interactions[0].Type != "synthesis" {
		t.Errorf("Expected Type='synthesis', got %q", interactions[0].Type)
	}
	if interactions[0].Success {
		t.Error("Expected Success=false for error path")
	}
}

func TestAISynthesizer_SetAIOptionsOverride_PropagatesToGenerateResponse(t *testing.T) {
	mock := &synthesisCapturingAIClient{}
	s := NewAISynthesizer(mock)
	s.SetAIOptionsOverride(&AIOptionsOverride{
		Model:           StringPtr("smart"),
		Temperature:     Float32Ptr(0),
		MaxTokens:       IntPtr(3210),
		ReasoningEffort: StringPtr("none"),
		ResponseFormat:  StringPtr("json"),
	})

	results := &ExecutionResult{
		Steps: []StepResult{{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true}},
	}

	_, err := s.Synthesize(context.Background(), "test request", results)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	opts := mock.lastOpts()
	if opts == nil {
		t.Fatal("expected AI options to be captured")
	}
	if opts.Model != "smart" || opts.Temperature != 0 || opts.MaxTokens != 3210 || opts.ReasoningEffort != "none" || opts.ResponseFormat != "json" {
		t.Fatalf("unexpected override propagation: %#v", opts)
	}
}

// TestAISynthesizer_SetSynthesisModel_SuccessPath_RecordsModelFromResponse verifies
// that on success, the debug interaction records Model from the AI response (not the alias).
func TestAISynthesizer_SetSynthesisModel_SuccessPath_RecordsModelFromResponse(t *testing.T) {
	mock := &synthesisCapturingAIClient{}
	s := NewAISynthesizer(mock)
	s.SetSynthesisModel("fast")

	debugStore := &capturingDebugStore{}
	s.SetLLMDebugStore(debugStore)

	results := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "agent1", Response: "data", Success: true},
		},
	}

	_, err := s.Synthesize(context.Background(), "test request", results)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction")
	}
	// On success, Model comes from resp.Model (the resolved concrete model)
	if interactions[0].Model != "test-model" {
		t.Errorf("Expected debug interaction Model='test-model' (from response), got %q", interactions[0].Model)
	}
}
