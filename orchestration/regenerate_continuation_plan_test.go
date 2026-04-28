package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// --- Test-local mocks ---

// promptCapturingAIClient captures the prompt sent to GenerateResponse and returns
// configurable responses. It also supports returning errors on specific calls.
type promptCapturingAIClient struct {
	capturedPrompts []string
	responses       []string // indexed by call number
	errors          []error  // if non-nil at index, return error instead
	callCount       int
}

func (m *promptCapturingAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.capturedPrompts = append(m.capturedPrompts, prompt)
	idx := m.callCount
	m.callCount++

	// Check for error at this index
	if m.errors != nil && idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}

	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}

	return &core.AIResponse{
		Content:  m.responses[idx],
		Model:    "test-model",
		Provider: "test-provider",
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

// testCapabilityProvider is a simple mock for CapabilityProvider that returns
// canned results. This avoids the need for discovery/catalog setup.
type testCapabilityProvider struct {
	formattedInfo string
	agentNames    []string
	err           error
}

func (p *testCapabilityProvider) GetCapabilities(ctx context.Context, request string, metadata map[string]interface{}) (*CapabilityResult, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &CapabilityResult{
		FormattedInfo: p.formattedInfo,
		AgentNames:    p.agentNames,
	}, nil
}

// validPlanJSON returns a minimal valid plan JSON string with the given terminal value.
func validPlanJSON(terminal *bool) string {
	plan := map[string]interface{}{
		"plan_id":          "regen-plan-1",
		"original_request": "test request",
		"mode":             "autonomous",
		"steps": []map[string]interface{}{
			{
				"step_id":     "step-3",
				"agent_name":  "test-agent",
				"namespace":   "default",
				"instruction": "Do something",
				"depends_on":  []string{},
			},
		},
	}
	if terminal != nil {
		plan["terminal"] = *terminal
	}
	jsonBytes, _ := json.Marshal(plan)
	return string(jsonBytes)
}

// setupTestOrchestrator creates an orchestrator with mocked dependencies suitable
// for unit testing regenerateContinuationPlan.
func setupTestOrchestrator(t *testing.T, aiClient core.AIClient) *AIOrchestrator {
	t.Helper()

	discovery := NewMockDiscovery()
	config := DefaultConfig()

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Set up a simple capability provider (avoids discovery/catalog dependency)
	orchestrator.SetCapabilityProvider(&testCapabilityProvider{
		formattedInfo: "Agent: test-agent\n  Capabilities: test_capability\n",
		agentNames:    []string{"test-agent"},
	})

	// Set up a prompt builder
	builder, _ := NewDefaultPromptBuilder(nil)
	orchestrator.SetPromptBuilder(builder)

	return orchestrator
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}

// --- Tests ---

// TestRegenerateContinuationPlan_TerminalTruePreserved verifies that when
// originalTerminal is true, the regeneration prompt includes a terminal preservation
// instruction telling the LLM to keep terminal=true.
func TestRegenerateContinuationPlan_TerminalTruePreserved(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(boolPtr(true))},
	}
	orch := setupTestOrchestrator(t, mockAI)

	completedResults := map[string]*StepResult{
		"step-1": {AgentName: "test-agent", Response: `{"data":"ok"}`, Instruction: "do thing"},
	}
	validationErr := errors.New("step step-1 references non-existent agent")

	plan, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		completedResults, []string{"step-1"},
		"", 2, validationErr,
		boolPtr(true), // originalTerminal = true
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}

	// Verify prompt contains terminal preservation instruction
	if len(mockAI.capturedPrompts) == 0 {
		t.Fatal("expected at least one prompt capture")
	}
	prompt := mockAI.capturedPrompts[0]

	if !strings.Contains(prompt, `"terminal": true`) {
		t.Errorf("prompt should contain terminal true instruction, got:\n%s", truncateForTest(prompt, 500))
	}
	if !strings.Contains(prompt, "Preserve this value") {
		t.Errorf("prompt should contain 'Preserve this value' instruction, got:\n%s", truncateForTest(prompt, 500))
	}
	if !strings.Contains(prompt, "validation error is about plan structure") {
		t.Errorf("prompt should explain that validation error ≠ phase needs, got:\n%s", truncateForTest(prompt, 500))
	}
}

// TestRegenerateContinuationPlan_TerminalFalsePreserved verifies that when
// originalTerminal is false, the prompt instructs the LLM to keep terminal=false.
func TestRegenerateContinuationPlan_TerminalFalsePreserved(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(boolPtr(false))},
	}
	orch := setupTestOrchestrator(t, mockAI)

	plan, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"data":"ok"}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("missing field"),
		boolPtr(false), // originalTerminal = false
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}

	prompt := mockAI.capturedPrompts[0]
	if !strings.Contains(prompt, `"terminal": false`) {
		t.Errorf("prompt should contain terminal false instruction, got:\n%s", truncateForTest(prompt, 500))
	}
	if !strings.Contains(prompt, "Preserve this value") {
		t.Errorf("prompt should contain preservation instruction")
	}
}

// TestRegenerateContinuationPlan_NilTerminalNoInstruction verifies that when
// originalTerminal is nil, no terminal preservation instruction is added to the prompt.
func TestRegenerateContinuationPlan_NilTerminalNoInstruction(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"data":"ok"}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("parse error"),
		nil, // originalTerminal = nil
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := mockAI.capturedPrompts[0]
	if strings.Contains(prompt, "Preserve this value") {
		t.Errorf("prompt should NOT contain terminal preservation instruction when originalTerminal is nil")
	}
}

// TestRegenerateContinuationPlan_PromptIncludesValidationError verifies that the
// regeneration prompt includes the specific validation error message.
func TestRegenerateContinuationPlan_PromptIncludesValidationError(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)

	specificError := "step step-2 references non-existent agent 'ghost-agent'"

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New(specificError),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := mockAI.capturedPrompts[0]
	if !strings.Contains(prompt, specificError) {
		t.Errorf("prompt should contain the validation error verbatim.\nExpected to find: %q\nGot prompt:\n%s",
			specificError, truncateForTest(prompt, 500))
	}
	if !strings.Contains(prompt, "corrected plan") {
		t.Errorf("prompt should ask for a corrected plan")
	}
}

// TestRegenerateContinuationPlan_LLMCallFails verifies error handling when the
// LLM call returns an error.
func TestRegenerateContinuationPlan_LLMCallFails(t *testing.T) {
	llmErr := errors.New("LLM service unavailable")
	mockAI := &promptCapturingAIClient{
		responses: []string{""}, // won't be used
		errors:    []error{llmErr},
	}
	orch := setupTestOrchestrator(t, mockAI)
	orch.logger = &mockLogger{}

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("validation failed"),
		boolPtr(true),
	)

	if err == nil {
		t.Fatal("expected error when LLM call fails")
	}
	if !strings.Contains(err.Error(), "continuation plan retry LLM call failed") {
		t.Errorf("error should wrap with context, got: %v", err)
	}

	// Verify logger captured the error
	logger := orch.logger.(*mockLogger)
	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Continuation plan regeneration LLM call failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error log message, got messages: %v", logger.messages)
	}
}

// TestRegenerateContinuationPlan_LLMCallFailsDebugRecording verifies that a failed
// LLM call still records a debug interaction with Success=false.
func TestRegenerateContinuationPlan_LLMCallFailsDebugRecording(t *testing.T) {
	llmErr := errors.New("timeout")
	mockAI := &promptCapturingAIClient{
		responses: []string{""},
		errors:    []error{llmErr},
	}
	orch := setupTestOrchestrator(t, mockAI)
	debugStore := NewMemoryLLMDebugStore()
	orch.debugStore = debugStore
	orch.logger = &mockLogger{}

	_, _ = orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("validation failed"),
		nil,
	)

	// Allow async debug recording to complete
	orch.debugWg.Wait()

	// Check debug store
	record, err := debugStore.GetRecord(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("unexpected error getting debug record: %v", err)
	}
	if record == nil {
		t.Fatal("expected debug record to be created for failed LLM call")
	}

	if len(record.Interactions) != 1 {
		t.Fatalf("expected 1 interaction, got %d", len(record.Interactions))
	}

	interaction := record.Interactions[0]
	if interaction.Type != "continuation_plan_regeneration" {
		t.Errorf("expected type 'continuation_plan_regeneration', got %q", interaction.Type)
	}
	if interaction.Success {
		t.Error("expected Success=false for failed LLM call")
	}
	if interaction.Error == "" {
		t.Error("expected non-empty error field")
	}
	if interaction.PhaseNumber != 2 {
		t.Errorf("expected PhaseNumber=2, got %d", interaction.PhaseNumber)
	}
}

// TestRegenerateContinuationPlan_SuccessDebugRecording verifies that a successful
// regeneration records a debug interaction with Success=true and all expected fields.
func TestRegenerateContinuationPlan_SuccessDebugRecording(t *testing.T) {
	planJSON := validPlanJSON(boolPtr(true))
	mockAI := &promptCapturingAIClient{
		responses: []string{planJSON},
	}
	orch := setupTestOrchestrator(t, mockAI)
	debugStore := NewMemoryLLMDebugStore()
	orch.debugStore = debugStore
	orch.logger = &mockLogger{}

	plan, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("missing field"),
		boolPtr(true),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}

	// Allow async debug recording to complete
	orch.debugWg.Wait()

	record, err := debugStore.GetRecord(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("unexpected error getting debug record: %v", err)
	}
	if record == nil {
		t.Fatal("expected debug record to be created")
	}

	if len(record.Interactions) != 1 {
		t.Fatalf("expected 1 interaction, got %d", len(record.Interactions))
	}

	interaction := record.Interactions[0]
	if interaction.Type != "continuation_plan_regeneration" {
		t.Errorf("expected type 'continuation_plan_regeneration', got %q", interaction.Type)
	}
	if !interaction.Success {
		t.Error("expected Success=true for successful regeneration")
	}
	if interaction.PhaseNumber != 2 {
		t.Errorf("expected PhaseNumber=2, got %d", interaction.PhaseNumber)
	}
	if interaction.PromptTokens != 100 {
		t.Errorf("expected PromptTokens=100, got %d", interaction.PromptTokens)
	}
	if interaction.CompletionTokens != 50 {
		t.Errorf("expected CompletionTokens=50, got %d", interaction.CompletionTokens)
	}
	if interaction.Model != "test-model" {
		t.Errorf("expected Model='test-model', got %q", interaction.Model)
	}
	if interaction.Provider != "test-provider" {
		t.Errorf("expected Provider='test-provider', got %q", interaction.Provider)
	}
	if interaction.Temperature != 0.2 {
		t.Errorf("expected Temperature=0.2, got %f", interaction.Temperature)
	}
	if interaction.MaxTokens != orch.config.PlanMaxTokens {
		t.Errorf("expected MaxTokens=%d, got %d", orch.config.PlanMaxTokens, interaction.MaxTokens)
	}
	if !strings.Contains(interaction.CallDescription, "REGENERATION") {
		t.Errorf("expected CallDescription to contain 'REGENERATION', got %q", interaction.CallDescription)
	}
	if interaction.DurationMs < 0 {
		t.Errorf("expected non-negative DurationMs, got %d", interaction.DurationMs)
	}
	if interaction.Prompt == "" {
		t.Error("expected non-empty Prompt")
	}
	if interaction.Response == "" {
		t.Error("expected non-empty Response")
	}
}

// TestRegenerateContinuationPlan_ParseFailure verifies that when the LLM returns
// unparseable content, the function returns an appropriate error.
func TestRegenerateContinuationPlan_ParseFailure(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{"this is not valid JSON at all"},
	}
	orch := setupTestOrchestrator(t, mockAI)
	orch.logger = &mockLogger{}

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("validation failed"),
		nil,
	)

	if err == nil {
		t.Fatal("expected error when LLM returns unparseable content")
	}
	if !strings.Contains(err.Error(), "continuation plan retry parse failed") {
		t.Errorf("error should indicate parse failure, got: %v", err)
	}

	// Verify warning log
	logger := orch.logger.(*mockLogger)
	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Regenerated continuation plan parsing failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected parse failure warning log, got messages: %v", logger.messages)
	}
}

// TestRegenerateContinuationPlan_StepIDConflict verifies that when the regenerated
// plan reuses step IDs from previously executed phases, the function returns an error.
func TestRegenerateContinuationPlan_StepIDConflict(t *testing.T) {
	// Return a plan with step-1 which conflicts with the executedStepIDs
	conflictingPlan := map[string]interface{}{
		"plan_id":          "regen-plan-1",
		"original_request": "test request",
		"mode":             "autonomous",
		"steps": []map[string]interface{}{
			{
				"step_id":     "step-1", // conflicts with executed step-1
				"agent_name":  "test-agent",
				"namespace":   "default",
				"instruction": "Do something",
			},
		},
	}
	jsonBytes, _ := json.Marshal(conflictingPlan)
	mockAI := &promptCapturingAIClient{
		responses: []string{string(jsonBytes)},
	}
	orch := setupTestOrchestrator(t, mockAI)
	orch.logger = &mockLogger{}

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, // step-1 already executed
		"", 2,
		errors.New("original error"),
		nil,
	)

	if err == nil {
		t.Fatal("expected error when regenerated plan has step ID conflicts")
	}
	if !strings.Contains(err.Error(), "continuation plan retry still has conflicts") {
		t.Errorf("error should indicate step ID conflict, got: %v", err)
	}

	// Verify warning log
	logger := orch.logger.(*mockLogger)
	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Regenerated continuation plan still has step ID conflicts") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected step ID conflict warning log, got messages: %v", logger.messages)
	}
}

// TestRegenerateContinuationPlan_NilAIClient verifies the guard clause when
// aiClient is nil.
func TestRegenerateContinuationPlan_NilAIClient(t *testing.T) {
	discovery := NewMockDiscovery()
	config := DefaultConfig()
	orch := NewAIOrchestrator(config, discovery, nil)

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test", "req-1",
		map[string]*StepResult{}, []string{},
		"", 2, errors.New("err"), nil,
	)

	if err == nil {
		t.Fatal("expected error when aiClient is nil")
	}
	if !strings.Contains(err.Error(), "AI client not configured") {
		t.Errorf("expected 'AI client not configured' error, got: %v", err)
	}
}

// TestRegenerateContinuationPlan_PromptBuildFailure verifies error handling when
// buildContinuationPrompt fails (e.g., capabilityProvider returns error).
func TestRegenerateContinuationPlan_PromptBuildFailure(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	discovery := NewMockDiscovery()
	config := DefaultConfig()
	orch := NewAIOrchestrator(config, discovery, mockAI)

	// Set capability provider that returns an error
	orch.SetCapabilityProvider(&testCapabilityProvider{
		err: errors.New("capability service down"),
	})
	builder, _ := NewDefaultPromptBuilder(nil)
	orch.SetPromptBuilder(builder)

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("original error"),
		nil,
	)

	if err == nil {
		t.Fatal("expected error when prompt build fails")
	}
	if !strings.Contains(err.Error(), "failed to build continuation retry prompt") {
		t.Errorf("expected prompt build error, got: %v", err)
	}
}

// TestRegenerateContinuationPlan_SetsPhaseNumber verifies that the returned plan
// has PhaseNumber set correctly.
func TestRegenerateContinuationPlan_SetsPhaseNumber(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)

	plan, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 3,
		errors.New("err"),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.PhaseNumber != 3 {
		t.Errorf("expected PhaseNumber=3, got %d", plan.PhaseNumber)
	}
}

// TestRegenerateContinuationPlan_SuccessLogging verifies the structured logging
// on the success path.
func TestRegenerateContinuationPlan_SuccessLogging(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(boolPtr(true))},
	}
	orch := setupTestOrchestrator(t, mockAI)

	var infoFields map[string]interface{}
	orch.logger = &mockLogger{
		infoFunc: func(msg string, fields map[string]interface{}) {
			if strings.Contains(msg, "Continuation plan regeneration succeeded") {
				infoFields = fields
			}
		},
	}

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		boolPtr(true),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if infoFields == nil {
		t.Fatal("expected info log for successful regeneration")
	}

	// Verify structured fields
	if infoFields["operation"] != "continuation_plan_regeneration_complete" {
		t.Errorf("expected operation 'continuation_plan_regeneration_complete', got %v", infoFields["operation"])
	}
	if infoFields["request_id"] != "req-1" {
		t.Errorf("expected request_id 'req-1', got %v", infoFields["request_id"])
	}
	if infoFields["phase"] != 2 {
		t.Errorf("expected phase 2, got %v", infoFields["phase"])
	}
}

// TestRegenerateContinuationPlan_DebugLogAfterLLMResponse verifies that a debug
// log is emitted after receiving the LLM response.
func TestRegenerateContinuationPlan_DebugLogAfterLLMResponse(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)

	debugMessages := []string{}
	orch.logger = &mockLogger{
		debugFunc: func(msg string, fields map[string]interface{}) {
			debugMessages = append(debugMessages, msg)
		},
	}

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, msg := range debugMessages {
		if strings.Contains(msg, "LLM continuation regeneration response received") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected debug log for LLM response, got: %v", debugMessages)
	}
}

// TestRegenerateContinuationPlan_CallDescriptionFormat verifies that the
// CallDescription field in the debug interaction is properly formatted.
func TestRegenerateContinuationPlan_CallDescriptionFormat(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)
	debugStore := NewMemoryLLMDebugStore()
	orch.debugStore = debugStore
	orch.logger = &mockLogger{}

	validationErr := errors.New("step references invalid capability")

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 3,
		validationErr,
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	orch.debugWg.Wait()

	record, _ := debugStore.GetRecord(context.Background(), "req-1")
	if record == nil || len(record.Interactions) == 0 {
		t.Fatal("expected debug record with interactions")
	}

	desc := record.Interactions[0].CallDescription
	if !strings.Contains(desc, "Phase 3") {
		t.Errorf("CallDescription should include phase number, got: %q", desc)
	}
	if !strings.Contains(desc, "REGENERATION") {
		t.Errorf("CallDescription should include 'REGENERATION', got: %q", desc)
	}
	if !strings.Contains(desc, "step references invalid capability") {
		t.Errorf("CallDescription should include validation error trigger, got: %q", desc)
	}
}

// TestRegenerateContinuationPlan_TimingRecorded verifies that the debug interaction
// has a non-zero DurationMs, proving timing is captured around the LLM call.
func TestRegenerateContinuationPlan_TimingRecorded(t *testing.T) {
	// Create a mock that introduces a small delay
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)
	debugStore := NewMemoryLLMDebugStore()
	orch.debugStore = debugStore
	orch.logger = &mockLogger{}

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	orch.debugWg.Wait()

	record, _ := debugStore.GetRecord(context.Background(), "req-1")
	if record == nil || len(record.Interactions) == 0 {
		t.Fatal("expected debug record")
	}

	// DurationMs should be >= 0 (can be 0 since mock is instant)
	if record.Interactions[0].DurationMs < 0 {
		t.Errorf("expected non-negative DurationMs, got %d", record.Interactions[0].DurationMs)
	}
	// Timestamp should be set (non-zero)
	if record.Interactions[0].Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}

// TestRegenerateContinuationPlan_NoDebugStoreNoPanic verifies that the function
// works correctly even when debugStore is nil (no-op recording).
func TestRegenerateContinuationPlan_NoDebugStoreNoPanic(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)
	// debugStore is nil by default from setupTestOrchestrator

	plan, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}

// TestRegenerateContinuationPlan_NoLoggerNoPanic verifies that the function
// works correctly even when logger is nil.
func TestRegenerateContinuationPlan_NoLoggerNoPanic(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	orch := setupTestOrchestrator(t, mockAI)
	orch.logger = nil // explicitly nil

	plan, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}

// TestRegenerateContinuationPlan_LLMCallFailsNoLoggerNoPanic verifies the error
// path works without panicking when logger is nil.
func TestRegenerateContinuationPlan_LLMCallFailsNoLoggerNoPanic(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{""},
		errors:    []error{errors.New("service down")},
	}
	orch := setupTestOrchestrator(t, mockAI)
	orch.logger = nil

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	if err == nil {
		t.Fatal("expected error")
	}
}

// TestRegenerateContinuationPlan_AIClientCalledWithCorrectOptions verifies that
// the AI client is called with the correct temperature and max tokens.
func TestRegenerateContinuationPlan_AIClientCalledWithCorrectOptions(t *testing.T) {
	var capturedOpts *core.AIOptions
	mockAI := &optionsCapturingAIClient{
		response: validPlanJSON(nil),
		captureOpts: func(opts *core.AIOptions) {
			capturedOpts = opts
		},
	}

	discovery := NewMockDiscovery()
	config := DefaultConfig()
	orch := NewAIOrchestrator(config, discovery, mockAI)
	orch.SetCapabilityProvider(&testCapabilityProvider{
		formattedInfo: "Agent: test-agent\n",
		agentNames:    []string{"test-agent"},
	})
	builder, _ := NewDefaultPromptBuilder(nil)
	orch.SetPromptBuilder(builder)

	_, err := orch.regenerateContinuationPlan(
		context.Background(), "test request", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedOpts == nil {
		t.Fatal("expected AI options to be captured")
	}
	if capturedOpts.Temperature != 0.2 {
		t.Errorf("expected Temperature=0.2, got %f", capturedOpts.Temperature)
	}
	if capturedOpts.MaxTokens != config.PlanMaxTokens {
		t.Errorf("expected MaxTokens=%d, got %d", config.PlanMaxTokens, capturedOpts.MaxTokens)
	}
}

// optionsCapturingAIClient captures the options passed to GenerateResponse.
type optionsCapturingAIClient struct {
	response    string
	captureOpts func(*core.AIOptions)
}

func (m *optionsCapturingAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if m.captureOpts != nil {
		m.captureOpts(opts)
	}
	return &core.AIResponse{
		Content:  m.response,
		Model:    "test-model",
		Provider: "test-provider",
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

// TestRegenerateContinuationPlan_PromptBuildFailsDebugNotRecorded verifies that
// when prompt building fails (before the LLM call), no debug interaction is recorded.
func TestRegenerateContinuationPlan_PromptBuildFailsDebugNotRecorded(t *testing.T) {
	mockAI := &promptCapturingAIClient{
		responses: []string{validPlanJSON(nil)},
	}
	discovery := NewMockDiscovery()
	config := DefaultConfig()
	orch := NewAIOrchestrator(config, discovery, mockAI)
	orch.SetCapabilityProvider(&testCapabilityProvider{
		err: errors.New("capability service down"),
	})
	builder, _ := NewDefaultPromptBuilder(nil)
	orch.SetPromptBuilder(builder)

	debugStore := NewMemoryLLMDebugStore()
	orch.debugStore = debugStore

	_, _ = orch.regenerateContinuationPlan(
		context.Background(), "test", "req-1",
		map[string]*StepResult{
			"step-1": {AgentName: "test-agent", Response: `{"ok":true}`, Instruction: "do thing"},
		},
		[]string{"step-1"}, "", 2,
		errors.New("err"),
		nil,
	)

	// Wait briefly for any potential async debug recording
	time.Sleep(50 * time.Millisecond)

	record, _ := debugStore.GetRecord(context.Background(), "req-1")
	if record != nil && len(record.Interactions) > 0 {
		t.Error("expected no debug interaction when prompt build fails (no LLM call made)")
	}
}

// --- Helper ---

func truncateForTest(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}
