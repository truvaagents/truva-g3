package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// refinementMockAIClient is a mock AIClient for plan refinement tests.
// It returns pre-configured responses and tracks calls.
type refinementMockAIClient struct {
	responses []string // responses returned in order (first call -> responses[0], etc.)
	errors    []error  // errors returned in order (nil = no error)
	callIdx   int
	calls     []string          // prompts received
	callCtxs  []context.Context // contexts received per call (for deferral-marker assertions)
	lastOpts  *core.AIOptions
}

func (m *refinementMockAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	m.calls = append(m.calls, prompt)
	m.callCtxs = append(m.callCtxs, ctx)
	if options != nil {
		optsCopy := *options
		m.lastOpts = &optsCopy
	}
	idx := m.callIdx
	m.callIdx++

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}

	content := ""
	if idx < len(m.responses) {
		content = m.responses[idx]
	}

	return &core.AIResponse{
		Content: content,
		Usage: core.TokenUsage{
			PromptTokens:     50,
			CompletionTokens: 25,
			TotalTokens:      75,
		},
	}, nil
}

func TestPlanRefiner_SetAIOptionsOverride_PropagatesToGenerateResponse(t *testing.T) {
	ai := &refinementMockAIClient{
		responses: []string{`{"decisions": [{"step_id": "step-2", "action": "execute"}]}`},
	}
	refiner := NewPlanRefiner(ai, nil)
	refiner.SetAIOptionsOverride(&AIOptionsOverride{
		Model:           StringPtr("smart"),
		Temperature:     Float32Ptr(0),
		MaxTokens:       IntPtr(2100),
		ReasoningEffort: StringPtr("none"),
		ResponseFormat:  StringPtr("json"),
	})

	orchResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "devops-agent", Success: true, Response: `{"ok":true}`},
	}
	heldSteps := []RoutingStep{
		{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
	}

	_, err := refiner.Refine(context.Background(), orchResults, heldSteps)
	if err != nil {
		t.Fatalf("Refine failed: %v", err)
	}

	if ai.lastOpts == nil {
		t.Fatal("expected AI options to be captured")
	}
	if ai.lastOpts.Model != "smart" || ai.lastOpts.Temperature != 0 || ai.lastOpts.MaxTokens != 2100 || ai.lastOpts.ReasoningEffort != "none" || ai.lastOpts.ResponseFormat != "json" {
		t.Fatalf("unexpected override propagation: %#v", ai.lastOpts)
	}
}

func TestNewPlanRefiner(t *testing.T) {
	t.Run("nil AIClient returns nil", func(t *testing.T) {
		refiner := NewPlanRefiner(nil, &core.NoOpLogger{})
		if refiner != nil {
			t.Error("Expected nil refiner when AIClient is nil")
		}
	})

	t.Run("nil logger gets NoOpLogger", func(t *testing.T) {
		ai := &refinementMockAIClient{}
		refiner := NewPlanRefiner(ai, nil)
		if refiner == nil {
			t.Fatal("Expected non-nil refiner")
		}
		if refiner.logger == nil {
			t.Error("Expected logger to be set (NoOpLogger)")
		}
	})

	t.Run("valid AIClient and logger", func(t *testing.T) {
		ai := &refinementMockAIClient{}
		logger := &core.NoOpLogger{}
		refiner := NewPlanRefiner(ai, logger)
		if refiner == nil {
			t.Fatal("Expected non-nil refiner")
		}
		if refiner.aiClient != ai {
			t.Error("Expected AIClient to be set")
		}
		if refiner.maxTokens != 1500 {
			t.Errorf("Expected maxTokens=1500, got %d", refiner.maxTokens)
		}
	})
}

func TestPlanRefiner_Refine_ParseResponse(t *testing.T) {
	// Tests for parseResponse via the Refine method (parseResponse is unexported).

	heldSteps := []RoutingStep{
		{
			StepID:    "step-2",
			AgentName: "jira-tool",
			Metadata:  map[string]interface{}{"capability": "create_issue"},
		},
		{
			StepID:    "step-3",
			AgentName: "slack-tool",
			Metadata:  map[string]interface{}{"capability": "send_message"},
		},
	}

	orchResults := map[string]*StepResult{
		"step-1": {
			StepID:    "step-1",
			AgentName: "devops-agent",
			Success:   true,
			Response:  `{"steps": []}`,
		},
	}

	t.Run("valid JSON with skip and execute", func(t *testing.T) {
		respJSON := `{"decisions": [{"step_id": "step-2", "action": "skip", "reason": "orchestrator already created the ticket"}, {"step_id": "step-3", "action": "execute"}]}`
		ai := &refinementMockAIClient{responses: []string{respJSON}}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(decisions) != 2 {
			t.Fatalf("Expected 2 decisions, got %d", len(decisions))
		}

		var skipFound, execFound bool
		for _, d := range decisions {
			if d.StepID == "step-2" && d.Action == RefinementSkip {
				skipFound = true
				if d.Reason == "" {
					t.Error("Expected skip reason to be set")
				}
			}
			if d.StepID == "step-3" && d.Action == RefinementExecute {
				execFound = true
			}
		}
		if !skipFound {
			t.Error("Expected skip decision for step-2")
		}
		if !execFound {
			t.Error("Expected execute decision for step-3")
		}
	})

	t.Run("code block wrapping stripped", func(t *testing.T) {
		respJSON := "```json\n{\"decisions\": [{\"step_id\": \"step-2\", \"action\": \"execute\"}, {\"step_id\": \"step-3\", \"action\": \"execute\"}]}\n```"
		ai := &refinementMockAIClient{responses: []string{respJSON}}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(decisions) != 2 {
			t.Fatalf("Expected 2 decisions, got %d", len(decisions))
		}
	})

	t.Run("invalid JSON triggers retry", func(t *testing.T) {
		validResp := `{"decisions": [{"step_id": "step-2", "action": "execute"}, {"step_id": "step-3", "action": "execute"}]}`
		ai := &refinementMockAIClient{
			responses: []string{"not valid json", validResp},
		}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Expected success after retry, got: %v", err)
		}
		if len(decisions) != 2 {
			t.Fatalf("Expected 2 decisions, got %d", len(decisions))
		}
		if len(ai.calls) != 2 {
			t.Errorf("Expected 2 AI calls (initial + retry), got %d", len(ai.calls))
		}
	})

	t.Run("invalid JSON both attempts fails", func(t *testing.T) {
		ai := &refinementMockAIClient{
			responses: []string{"bad json 1", "bad json 2"},
		}
		refiner := NewPlanRefiner(ai, nil)

		_, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err == nil {
			t.Fatal("Expected error for double parse failure")
		}
		if !strings.Contains(err.Error(), "parse failed") {
			t.Errorf("Expected 'parse failed' in error, got: %v", err)
		}
	})

	t.Run("unknown action triggers retry", func(t *testing.T) {
		badAction := `{"decisions": [{"step_id": "step-2", "action": "delete"}]}`
		validResp := `{"decisions": [{"step_id": "step-2", "action": "skip", "reason": "done"}, {"step_id": "step-3", "action": "execute"}]}`
		ai := &refinementMockAIClient{
			responses: []string{badAction, validResp},
		}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Expected success after retry, got: %v", err)
		}
		if len(decisions) != 2 {
			t.Fatalf("Expected 2 decisions, got %d", len(decisions))
		}
	})

	t.Run("missing decisions default to execute", func(t *testing.T) {
		// Only decision for step-2, step-3 should be auto-added as execute
		respJSON := `{"decisions": [{"step_id": "step-2", "action": "skip", "reason": "done"}]}`
		ai := &refinementMockAIClient{responses: []string{respJSON}}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(decisions) != 2 {
			t.Fatalf("Expected 2 decisions (1 explicit + 1 default), got %d", len(decisions))
		}

		var defaultFound bool
		for _, d := range decisions {
			if d.StepID == "step-3" {
				defaultFound = true
				if d.Action != RefinementExecute {
					t.Errorf("Expected default action=execute, got %q", d.Action)
				}
				if !strings.Contains(d.Reason, "defaulting to execute") {
					t.Errorf("Expected default reason, got %q", d.Reason)
				}
			}
		}
		if !defaultFound {
			t.Error("Expected default decision for step-3")
		}
	})

	t.Run("empty decisions defaults all to execute", func(t *testing.T) {
		respJSON := `{"decisions": []}`
		ai := &refinementMockAIClient{responses: []string{respJSON}}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(decisions) != 2 {
			t.Fatalf("Expected 2 default decisions, got %d", len(decisions))
		}
		for _, d := range decisions {
			if d.Action != RefinementExecute {
				t.Errorf("Expected all defaults to be execute, got %q for %s", d.Action, d.StepID)
			}
		}
	})

	t.Run("modify decision with new capability and parameters", func(t *testing.T) {
		respJSON := `{"decisions": [{"step_id": "step-2", "action": "modify", "reason": "ticket exists", "new_capability": "update_issue", "new_parameters": {"issue_key": "PROJ-123"}}, {"step_id": "step-3", "action": "execute"}]}`
		ai := &refinementMockAIClient{responses: []string{respJSON}}
		refiner := NewPlanRefiner(ai, nil)

		decisions, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		var modifyFound bool
		for _, d := range decisions {
			if d.StepID == "step-2" && d.Action == RefinementModify {
				modifyFound = true
				if d.NewCapability != "update_issue" {
					t.Errorf("Expected new_capability=update_issue, got %q", d.NewCapability)
				}
				if d.NewParameters == nil {
					t.Error("Expected new_parameters to be set")
				} else if d.NewParameters["issue_key"] != "PROJ-123" {
					t.Errorf("Expected issue_key=PROJ-123, got %v", d.NewParameters["issue_key"])
				}
			}
		}
		if !modifyFound {
			t.Error("Expected modify decision for step-2")
		}
	})

	t.Run("LLM failure returns error", func(t *testing.T) {
		ai := &refinementMockAIClient{
			errors: []error{fmt.Errorf("LLM unavailable")},
		}
		refiner := NewPlanRefiner(ai, nil)

		_, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err == nil {
			t.Fatal("Expected error for LLM failure")
		}
		if !strings.Contains(err.Error(), "LLM call failed") {
			t.Errorf("Expected 'LLM call failed' in error, got: %v", err)
		}
	})
}

func TestPlanRefiner_BuildPrompt(t *testing.T) {
	// buildPrompt is unexported, but we can test it indirectly by inspecting
	// the prompt passed to the mock AI client via Refine.

	t.Run("filters sub-steps by held capabilities", func(t *testing.T) {
		orchResults := map[string]*StepResult{
			"step-1": {
				StepID:    "step-1",
				AgentName: "devops-agent",
				Success:   true,
				Response: `{"steps": [
					{"agent_name": "jira-tool", "capability": "create_issue", "success": true, "response": "PROJ-123 created"},
					{"agent_name": "unrelated-tool", "capability": "do_something", "success": true, "response": "done"}
				]}`,
			},
		}

		heldSteps := []RoutingStep{
			{
				StepID:    "step-2",
				AgentName: "jira-tool",
				Metadata:  map[string]interface{}{"capability": "create_issue"},
			},
		}

		validResp := `{"decisions": [{"step_id": "step-2", "action": "execute"}]}`
		ai := &refinementMockAIClient{responses: []string{validResp}}
		refiner := NewPlanRefiner(ai, nil)

		_, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		prompt := ai.calls[0]
		// Should include jira-tool sub-step (matches held step)
		if !strings.Contains(prompt, "jira-tool.create_issue") {
			t.Error("Expected prompt to include jira-tool.create_issue sub-step")
		}
		// Should NOT include unrelated-tool sub-step
		if strings.Contains(prompt, "unrelated-tool.do_something") {
			t.Error("Expected prompt to exclude unrelated-tool sub-step")
		}
	})

	t.Run("truncates long responses to 500 chars", func(t *testing.T) {
		longResp := strings.Repeat("x", 600)
		orchResults := map[string]*StepResult{
			"step-1": {
				StepID:    "step-1",
				AgentName: "devops-agent",
				Success:   true,
				Response:  fmt.Sprintf(`{"steps": [{"agent_name": "jira-tool", "capability": "create_issue", "success": true, "response": "%s"}]}`, longResp),
			},
		}

		heldSteps := []RoutingStep{
			{
				StepID:    "step-2",
				AgentName: "jira-tool",
				Metadata:  map[string]interface{}{"capability": "create_issue"},
			},
		}

		validResp := `{"decisions": [{"step_id": "step-2", "action": "execute"}]}`
		ai := &refinementMockAIClient{responses: []string{validResp}}
		refiner := NewPlanRefiner(ai, nil)

		_, _ = refiner.Refine(context.Background(), orchResults, heldSteps)

		prompt := ai.calls[0]
		// The sub-step response should be truncated with "..."
		if !strings.Contains(prompt, "...") {
			t.Error("Expected prompt to contain truncation indicator '...'")
		}
		// Should not contain the full 600-char response
		if strings.Contains(prompt, longResp) {
			t.Error("Expected long response to be truncated")
		}
	})

	t.Run("handles empty orchestrator response JSON", func(t *testing.T) {
		orchResults := map[string]*StepResult{
			"step-1": {
				StepID:    "step-1",
				AgentName: "devops-agent",
				Success:   true,
				Response:  "not json at all",
			},
		}

		heldSteps := []RoutingStep{
			{
				StepID:    "step-2",
				AgentName: "jira-tool",
				Metadata:  map[string]interface{}{"capability": "create_issue"},
			},
		}

		validResp := `{"decisions": [{"step_id": "step-2", "action": "execute"}]}`
		ai := &refinementMockAIClient{responses: []string{validResp}}
		refiner := NewPlanRefiner(ai, nil)

		_, err := refiner.Refine(context.Background(), orchResults, heldSteps)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		prompt := ai.calls[0]
		// Should still contain the step header even without parseable sub-steps
		if !strings.Contains(prompt, "step-1") {
			t.Error("Expected prompt to include orchestrator step ID")
		}
		// Should include remaining steps section
		if !strings.Contains(prompt, "step-2") {
			t.Error("Expected prompt to include held step ID")
		}
	})

	t.Run("includes held step instructions in remaining_steps", func(t *testing.T) {
		orchResults := map[string]*StepResult{
			"step-1": {
				StepID:    "step-1",
				AgentName: "devops-agent",
				Success:   true,
				Response:  `{"steps": []}`,
			},
		}

		heldSteps := []RoutingStep{
			{
				StepID:      "step-2",
				AgentName:   "jira-tool",
				Instruction: "Create a bug ticket for the deployment failure",
				Metadata:    map[string]interface{}{"capability": "create_issue"},
			},
		}

		validResp := `{"decisions": [{"step_id": "step-2", "action": "execute"}]}`
		ai := &refinementMockAIClient{responses: []string{validResp}}
		refiner := NewPlanRefiner(ai, nil)

		_, _ = refiner.Refine(context.Background(), orchResults, heldSteps)

		prompt := ai.calls[0]
		if !strings.Contains(prompt, "Create a bug ticket for the deployment failure") {
			t.Error("Expected prompt to include held step instruction")
		}
		if !strings.Contains(prompt, "jira-tool.create_issue") {
			t.Error("Expected prompt to include agent.capability format")
		}
	})
}

// TestPlanRefiner_Setters tests trivial setters for coverage.
func TestPlanRefiner_Setters(t *testing.T) {
	ai := &refinementMockAIClient{responses: []string{`{"decisions":[]}`}}
	refiner := NewPlanRefiner(ai, nil)
	if refiner == nil {
		t.Fatal("Expected non-nil refiner")
	}

	t.Run("SetModel", func(t *testing.T) {
		refiner.SetModel("test-model")
		if refiner.model != "test-model" {
			t.Errorf("Expected model 'test-model', got %q", refiner.model)
		}
	})

	t.Run("SetDebugStore", func(t *testing.T) {
		store := &mockDebugStoreForRefinement{}
		refiner.SetDebugStore(store)
		if refiner.debugStore == nil {
			t.Error("Expected debugStore to be set")
		}
	})

	t.Run("SetTelemetry", func(t *testing.T) {
		refiner.SetTelemetry(nil) // Just verify no panic
	})

	t.Run("SetLogger_nil", func(t *testing.T) {
		refiner.SetLogger(nil) // Should not panic or change logger
	})

	t.Run("SetLogger_non_nil", func(t *testing.T) {
		refiner.SetLogger(&core.NoOpLogger{})
		if refiner.logger == nil {
			t.Error("Expected logger to be set")
		}
	})
}

// TestNewPlanRefiner_ComponentAwareLogger tests the ComponentAwareLogger wrapping path.
func TestNewPlanRefiner_ComponentAwareLogger(t *testing.T) {
	ai := &refinementMockAIClient{responses: []string{`{"decisions":[]}`}}
	cal := &mockComponentAwareLogger{component: ""}
	refiner := NewPlanRefiner(ai, cal)
	if refiner == nil {
		t.Fatal("Expected non-nil refiner")
	}
	// The logger should have been wrapped with WithComponent
	if cal.component != "framework/orchestration" {
		t.Errorf("Expected component 'framework/orchestration', got %q", cal.component)
	}
}

// TestPlanRefiner_Refine_DebugStoreRecording tests that debug store records the interaction.
func TestPlanRefiner_Refine_DebugStoreRecording(t *testing.T) {
	ai := &refinementMockAIClient{
		responses: []string{`{"decisions":[{"step_id":"step-5","action":"skip","reason":"already done"}]}`},
	}
	store := &mockDebugStoreForRefinement{}
	refiner := NewPlanRefiner(ai, nil)
	refiner.SetDebugStore(store)

	orchResults := map[string]*StepResult{
		"step-4": {StepID: "step-4", AgentName: "devops-chat-agent", Success: true, Response: `{"steps":[]}`},
	}
	heldSteps := []RoutingStep{
		{StepID: "step-5", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
	}

	_, err := refiner.Refine(context.Background(), orchResults, heldSteps)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if store.recordCount != 1 {
		t.Errorf("Expected 1 debug store recording, got %d", store.recordCount)
	}
	if store.lastType != "plan_refinement" {
		t.Errorf("Expected type 'plan_refinement', got %q", store.lastType)
	}
	if !store.lastSuccess {
		t.Error("Expected success=true in debug store recording")
	}
}

// TestPlanRefiner_BuildPrompt_CapabilityFieldFallback tests that buildPrompt
// uses result.Capability first, then falls back to Metadata["capability"].
func TestPlanRefiner_BuildPrompt_CapabilityFieldFallback(t *testing.T) {
	ai := &refinementMockAIClient{responses: []string{`{"decisions":[]}`}}
	refiner := NewPlanRefiner(ai, nil)

	t.Run("uses_Capability_field", func(t *testing.T) {
		orchResults := map[string]*StepResult{
			"step-4": {
				StepID:     "step-4",
				AgentName:  "devops-chat-agent",
				Capability: "devops_operations",
				Success:    true,
				Response:   `{"steps":[]}`,
			},
		}
		heldSteps := []RoutingStep{
			{StepID: "step-5", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
		}
		prompt := refiner.buildPrompt(orchResults, heldSteps)
		if !strings.Contains(prompt, "devops_operations") {
			t.Error("Expected prompt to contain Capability field value 'devops_operations'")
		}
	})

	t.Run("falls_back_to_metadata_capability", func(t *testing.T) {
		orchResults := map[string]*StepResult{
			"step-4": {
				StepID:    "step-4",
				AgentName: "devops-chat-agent",
				// Capability field empty — should fall back to Metadata
				Metadata: map[string]interface{}{"capability": "devops_operations_legacy"},
				Success:  true,
				Response: `{"steps":[]}`,
			},
		}
		heldSteps := []RoutingStep{
			{StepID: "step-5", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
		}
		prompt := refiner.buildPrompt(orchResults, heldSteps)
		if !strings.Contains(prompt, "devops_operations_legacy") {
			t.Error("Expected prompt to contain Metadata capability fallback value")
		}
	})
}

// --- Mock types for additional tests ---

// mockComponentAwareLogger tracks WithComponent calls.
type mockComponentAwareLogger struct {
	core.NoOpLogger
	component string
}

func (m *mockComponentAwareLogger) WithComponent(component string) core.Logger {
	m.component = component
	return &core.NoOpLogger{}
}

// mockDebugStoreForRefinement tracks RecordInteraction calls.
type mockDebugStoreForRefinement struct {
	recordCount int
	lastType    string
	lastSuccess bool
}

func (m *mockDebugStoreForRefinement) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	m.recordCount++
	m.lastType = interaction.Type
	m.lastSuccess = interaction.Success
	return nil
}

func (m *mockDebugStoreForRefinement) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (m *mockDebugStoreForRefinement) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

func (m *mockDebugStoreForRefinement) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

func (m *mockDebugStoreForRefinement) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

// TestPlanRefiner_Refine_MarksContextForWrapperDeferral proves the
// end-to-end plumbing: when Refine() is invoked with debugStore set,
// every AI call sees a context that carries the deferral marker. The
// retry path at plan_refinement.go:171 is covered by forcing a parse
// failure on the first response.
func TestPlanRefiner_Refine_MarksContextForWrapperDeferral(t *testing.T) {
	ai := &refinementMockAIClient{
		responses: []string{
			"not-json",
			`{"decisions":[{"step_id":"step-2","action":"execute"}]}`,
		},
	}
	refiner := NewPlanRefiner(ai, nil)
	refiner.SetDebugStore(&mockDebugStoreForRefinement{})

	orchResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "devops-agent", Success: true, Response: `{"ok":true}`},
	}
	heldSteps := []RoutingStep{
		{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
	}
	if _, err := refiner.Refine(context.Background(), orchResults, heldSteps); err != nil {
		t.Fatalf("Refine failed: %v", err)
	}

	if len(ai.callCtxs) != 2 {
		t.Fatalf("expected 2 AI calls (initial + retry), got %d", len(ai.callCtxs))
	}
	for i, ctx := range ai.callCtxs {
		if !telemetry.IsLLMCallRecordingDeferred(ctx) {
			t.Fatalf("call %d: context missing deferral marker — wrapper would double-record as agent_llm_call", i)
		}
	}
}

// TestPlanRefiner_Refine_NoMarker_WhenDebugStoreNil is the
// graceful-fallback regression guard: with debugStore=nil, Refine()
// must NOT mark ctx. Without this guard the wrapper would skip its own
// recording AND PlanRefiner cannot record, so the call would vanish.
func TestPlanRefiner_Refine_NoMarker_WhenDebugStoreNil(t *testing.T) {
	ai := &refinementMockAIClient{
		responses: []string{`{"decisions":[{"step_id":"step-2","action":"execute"}]}`},
	}
	refiner := NewPlanRefiner(ai, nil)
	// Intentionally do NOT call SetDebugStore — simulates a
	// debug-disabled or misconfigured orchestrator.

	orchResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "devops-agent", Success: true, Response: `{"ok":true}`},
	}
	heldSteps := []RoutingStep{
		{StepID: "step-2", AgentName: "jira-tool", Metadata: map[string]interface{}{"capability": "create_issue"}},
	}
	if _, err := refiner.Refine(context.Background(), orchResults, heldSteps); err != nil {
		t.Fatalf("Refine failed: %v", err)
	}

	if len(ai.callCtxs) != 1 {
		t.Fatalf("expected 1 AI call, got %d", len(ai.callCtxs))
	}
	if telemetry.IsLLMCallRecordingDeferred(ai.callCtxs[0]) {
		t.Fatal("with nil debugStore the marker must be absent so InstrumentedAIClient records agent_llm_call")
	}
}
