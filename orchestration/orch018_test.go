// ORCH-018: Unit tests for the clarification-checkpoint feature
// (BUG_TIERED_SELECTION_EMPTY_ON_CONTINUATION.md).
//
// This file centralises unit tests for the Layer 1 + Layer 2 + Layer 3 fix
// so each piece of the implementation has focused coverage. Integration-style
// tests that exercise the full phase loop end-to-end live alongside the
// existing tests in orchestrator_test.go and context_aware_selection_test.go.
//
// Test layout mirrors the Implementation Plan in the bug doc:
//
//	Layer 1 unit tests:
//	  - TestExtractUniqueToolIDs_ORCH018                    (Step 1e helper)
//	  - TestValidatePlan_AllowsEmptyStepsWithNeedsUserInput  (Step 1f gate)
//	  - TestBuildIterativePlanningInstructions_ClarificationEscapeValve (Step 2 prompt)
//	  - TestExecutePhaseLoop_ClarificationShortCircuit       (Step 3 short-circuit)
//	  - TestSynthesisSystemPromptFor_ORCH018                 (Step 5a helper)
//	  - TestBuildSynthesisPrompt_ClarificationSection        (Step 5b, non-streaming)
//	  - TestOrchestratorBuildSynthesisPrompt_ClarificationSection (Step 5e, streaming)
//	  - TestAISynthesizer_SynthesizeWithLLM_ClarificationModeSystemPrompt (Step 5b integration)
//
//	Layer 3 unit tests:
//	  - TestParseToolSelection_SentinelError_ORCH018         (Step 6 sentinel)
//	  - TestSelectRelevantTools_Layer3Recovery_ORCH018       (Step 6c case (a))
//	  - TestSelectRelevantTools_Layer3_Phase1ShortCircuitsOnSemanticEmpty   (Step 6c case (b))
//	  - TestSelectRelevantTools_Layer3_EmptyPriorToolIDs_ShortCircuitsLikePhase1 (Step 6c case (b) gate)

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// Step 1e — extractUniqueToolIDs helper (pure function)
// =============================================================================

func TestExtractUniqueToolIDs_ORCH018(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]*StepResult
		want    []string
	}{
		{
			name:    "empty map returns nil slice",
			results: map[string]*StepResult{},
			want:    nil,
		},
		{
			name: "single successful step returns one ID",
			results: map[string]*StepResult{
				"step-1": {AgentName: "country-info-tool", Capability: "get_country_info", Success: true},
			},
			want: []string{"country-info-tool/get_country_info"},
		},
		{
			name: "nil step result is skipped",
			results: map[string]*StepResult{
				"step-1": nil,
				"step-2": {AgentName: "travel-tool", Capability: "get_advisory", Success: true},
			},
			want: []string{"travel-tool/get_advisory"},
		},
		{
			name: "failed step is skipped (Success=false)",
			results: map[string]*StepResult{
				"step-1": {AgentName: "ok-tool", Capability: "foo", Success: true},
				"step-2": {AgentName: "failed-tool", Capability: "bar", Success: false},
			},
			want: []string{"ok-tool/foo"},
		},
		{
			name: "step with empty AgentName is skipped",
			results: map[string]*StepResult{
				"step-1": {AgentName: "", Capability: "foo", Success: true},
				"step-2": {AgentName: "ok-tool", Capability: "bar", Success: true},
			},
			want: []string{"ok-tool/bar"},
		},
		{
			name: "step with empty Capability is skipped",
			results: map[string]*StepResult{
				"step-1": {AgentName: "ok-tool", Capability: "", Success: true},
				"step-2": {AgentName: "ok-tool", Capability: "bar", Success: true},
			},
			want: []string{"ok-tool/bar"},
		},
		{
			name: "duplicate agent/capability pairs are deduplicated",
			results: map[string]*StepResult{
				"step-1": {AgentName: "tool-a", Capability: "cap", Success: true},
				"step-2": {AgentName: "tool-a", Capability: "cap", Success: true},
				"step-3": {AgentName: "tool-b", Capability: "cap", Success: true},
			},
			want: []string{"tool-a/cap", "tool-b/cap"},
		},
		{
			name: "output is sorted deterministically (map iteration order agnostic)",
			results: map[string]*StepResult{
				"step-1": {AgentName: "zebra-tool", Capability: "query", Success: true},
				"step-2": {AgentName: "alpha-tool", Capability: "query", Success: true},
				"step-3": {AgentName: "mango-tool", Capability: "query", Success: true},
			},
			want: []string{"alpha-tool/query", "mango-tool/query", "zebra-tool/query"},
		},
		{
			name: "mix of successful, failed, nil, and empty is filtered correctly",
			results: map[string]*StepResult{
				"step-1": {AgentName: "a", Capability: "x", Success: true},
				"step-2": nil,
				"step-3": {AgentName: "b", Capability: "", Success: true},
				"step-4": {AgentName: "", Capability: "z", Success: true},
				"step-5": {AgentName: "c", Capability: "y", Success: false},
				"step-6": {AgentName: "d", Capability: "w", Success: true},
			},
			want: []string{"a/x", "d/w"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUniqueToolIDs(tt.results)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractUniqueToolIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Step 1f — validatePlan allows empty steps when NeedsUserInput is set
// =============================================================================

// minimalValidPlanForValidation returns a plan with one minimal step that
// passes validatePlan's downstream agent/capability checks when paired with
// a discovery+catalog that knows about "test-agent/test_capability".
func minimalValidPlanForValidation() *RoutingPlan {
	return &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:      "step-1",
				AgentName:   "test-agent",
				Namespace:   "default",
				Instruction: "Do the thing",
				Metadata: map[string]interface{}{
					"capability": "test_capability",
				},
			},
		},
	}
}

// newTestOrchestratorWithAgent returns an orchestrator configured with a
// discovery and catalog that know about a single agent/capability, sufficient
// for exercising validatePlan's happy paths without network I/O.
func newTestOrchestratorWithAgent(t *testing.T) *AIOrchestrator {
	t.Helper()
	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})
	o := NewAIOrchestrator(DefaultConfig(), discovery, NewMockAIClient())
	o.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID: "test-1", Name: "test-agent", Address: "localhost", Port: 8080,
			},
			Capabilities: []EnhancedCapability{{Name: "test_capability"}},
		},
	}
	return o
}

func TestValidatePlan_AllowsEmptyStepsWithNeedsUserInput(t *testing.T) {
	o := newTestOrchestratorWithAgent(t)

	t.Run("empty steps with NeedsUserInput set → passes", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "clarification-plan",
			Steps:  []RoutingStep{},
			NeedsUserInput: &ClarificationRequest{
				Question: "What are your travel dates?",
			},
		}
		if err := o.validatePlan(plan); err != nil {
			t.Errorf("expected clarification plan to pass validation, got error: %v", err)
		}
	})

	t.Run("empty steps with terminal=true (default nil) → passes (new exception)", func(t *testing.T) {
		// Real-world case observed in trace 81ef40f8842606be627fcb17f28a6695:
		// Phase 2 continuation planner returns `terminal: true, steps: []` to
		// mean "I have all the data I need from prior phases, just synthesize
		// from completed_steps". validatePlan must accept this — rejecting it
		// triggers a regeneration loop that hallucinates filler steps (the
		// same pattern ORCH-018 was meant to eliminate, in a different shape).
		//
		// Note: Terminal == nil → IsTerminal() returns true (backward compat
		// default per orchestrator.go:50), so this test exercises both the
		// "explicit terminal: true" and "terminal field omitted" code paths.
		plan := &RoutingPlan{
			PlanID: "terminal-empty-plan",
			Steps:  []RoutingStep{},
			// Terminal: nil (omitted) → IsTerminal() == true
		}
		if err := o.validatePlan(plan); err != nil {
			t.Errorf("expected terminal plan with empty steps to pass validation, got: %v", err)
		}
	})

	t.Run("empty steps with explicit terminal=true → passes", func(t *testing.T) {
		terminal := true
		plan := &RoutingPlan{
			PlanID:   "explicit-terminal-empty-plan",
			Steps:    []RoutingStep{},
			Terminal: &terminal,
		}
		if err := o.validatePlan(plan); err != nil {
			t.Errorf("expected explicit-terminal plan with empty steps to pass validation, got: %v", err)
		}
	})

	t.Run("empty steps with explicit terminal=false (pathological) → still errors", func(t *testing.T) {
		// The only remaining "no steps" rejection: planner says "more phases
		// needed" but proposes no work. This is the pathological case the
		// existing fail-fast guard was designed to catch — retrying or
		// continuing serves no purpose, so reject early.
		notTerminal := false
		plan := &RoutingPlan{
			PlanID:   "non-terminal-empty-plan",
			Steps:    []RoutingStep{},
			Terminal: &notTerminal,
			// NeedsUserInput nil
		}
		err := o.validatePlan(plan)
		if err == nil {
			t.Fatal("expected non-terminal empty plan without NeedsUserInput to fail validation")
		}
		if !strings.Contains(err.Error(), "no steps") {
			t.Errorf("expected 'no steps' error, got: %v", err)
		}
	})

	t.Run("non-empty steps → unaffected by the new exceptions", func(t *testing.T) {
		plan := minimalValidPlanForValidation()
		if err := o.validatePlan(plan); err != nil {
			t.Errorf("expected normal plan to pass validation, got error: %v", err)
		}
	})

	t.Run("non-empty steps with NeedsUserInput also set → passes step validation", func(t *testing.T) {
		// Pathological case: planner emits both steps and NeedsUserInput.
		// The doc's open-question resolution is to treat as a planner error
		// and prefer the clarification path, but validatePlan itself has no
		// way to express "drop steps" — it just passes because Steps is
		// non-empty. The short-circuit in the phase loop enforces the
		// "prefer clarification" resolution at a higher level.
		plan := minimalValidPlanForValidation()
		plan.NeedsUserInput = &ClarificationRequest{Question: "extra question"}
		if err := o.validatePlan(plan); err != nil {
			t.Errorf("expected plan with both steps and NeedsUserInput to pass step validation, got: %v", err)
		}
	})
}

// =============================================================================
// Step 2 — BuildIterativePlanningInstructions clarification escape valve
// =============================================================================

func TestBuildIterativePlanningInstructions_ClarificationEscapeValve(t *testing.T) {
	t.Run("escape valve content present when iterative planning enabled", func(t *testing.T) {
		config := &IterativePlanConfig{Enabled: true, MaxPhases: 3, MaxTotalSteps: 20}
		out := BuildIterativePlanningInstructions(config)

		wantSubstrings := []string{
			"CLARIFICATION ESCAPE VALVE:",
			`Use "needs_user_input" when`,
			`Populate "question"`,
			`Set "steps" to an empty array`,
			`Set "terminal": true`,
			`"missing_fields"`,
			`"partial_progress"`,
			"Example clarification plan:",
			`"needs_user_input":`,
			`"travel_dates"`,
		}
		for _, s := range wantSubstrings {
			if !strings.Contains(out, s) {
				t.Errorf("expected output to contain %q; got:\n%s", s, out)
			}
		}
	})

	t.Run("escape valve example JSON is parseable and has the expected shape", func(t *testing.T) {
		// Extract and parse the example JSON block to prove it's valid.
		// This guards against future edits that accidentally break the JSON.
		config := &IterativePlanConfig{Enabled: true, MaxPhases: 3, MaxTotalSteps: 20}
		out := BuildIterativePlanningInstructions(config)

		// Find the opening brace that follows "Example clarification plan:"
		start := strings.Index(out, "Example clarification plan:")
		if start == -1 {
			t.Fatal("example block header missing")
		}
		braceStart := strings.Index(out[start:], "{")
		if braceStart == -1 {
			t.Fatal("example block opening brace missing")
		}
		// Find the matching closing brace by naive counting
		segment := out[start+braceStart:]
		depth := 0
		end := -1
		for i, r := range segment {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i + 1
				}
			}
			if end != -1 {
				break
			}
		}
		if end == -1 {
			t.Fatal("example block has unbalanced braces")
		}
		jsonBlock := segment[:end]

		var plan struct {
			PlanID         string `json:"plan_id"`
			Terminal       bool   `json:"terminal"`
			Steps          []any  `json:"steps"`
			NeedsUserInput struct {
				Question        string   `json:"question"`
				MissingFields   []string `json:"missing_fields"`
				PartialProgress string   `json:"partial_progress"`
			} `json:"needs_user_input"`
		}
		if err := json.Unmarshal([]byte(jsonBlock), &plan); err != nil {
			t.Fatalf("example JSON should parse as a RoutingPlan-shaped struct: %v\nJSON was:\n%s", err, jsonBlock)
		}
		if plan.PlanID == "" {
			t.Error("example plan_id is empty")
		}
		if !plan.Terminal {
			t.Error("example terminal should be true for clarification plans")
		}
		if len(plan.Steps) != 0 {
			t.Errorf("example steps should be empty, got %d items", len(plan.Steps))
		}
		if plan.NeedsUserInput.Question == "" {
			t.Error("example question is empty")
		}
		if len(plan.NeedsUserInput.MissingFields) == 0 {
			t.Error("example missing_fields is empty")
		}
	})

	t.Run("empty output when config is nil (backward compat)", func(t *testing.T) {
		if out := BuildIterativePlanningInstructions(nil); out != "" {
			t.Errorf("expected empty string for nil config, got: %q", out)
		}
	})

	t.Run("empty output when iterative planning disabled (backward compat)", func(t *testing.T) {
		config := &IterativePlanConfig{Enabled: false}
		if out := BuildIterativePlanningInstructions(config); out != "" {
			t.Errorf("expected empty string when disabled, got: %q", out)
		}
	})

	t.Run("escape valve text does not contain negative 'Do NOT' instructions", func(t *testing.T) {
		// Regression guard for EFFECTIVE_PROMPTS_GUIDE.md §2.4 compliance.
		// All directives in the clarification escape valve must be positive.
		config := &IterativePlanConfig{Enabled: true, MaxPhases: 3, MaxTotalSteps: 20}
		out := BuildIterativePlanningInstructions(config)

		// Extract only the clarification section
		start := strings.Index(out, "CLARIFICATION ESCAPE VALVE:")
		end := strings.Index(out, "</iterative_planning>")
		if start == -1 || end == -1 {
			t.Fatal("clarification section boundaries not found")
		}
		clarificationSection := out[start:end]

		forbidden := []string{"Do NOT", "do not", "NEVER", "WRONG", "avoid"}
		for _, f := range forbidden {
			if strings.Contains(clarificationSection, f) {
				t.Errorf("clarification section must not contain negative instruction %q (§2.4 Pink Elephant)", f)
			}
		}
	})
}

// =============================================================================
// Step 5a — synthesisSystemPromptFor helper (pure function)
// =============================================================================

func TestSynthesisSystemPromptFor_ORCH018(t *testing.T) {
	t.Run("nil results returns base prompt", func(t *testing.T) {
		got := synthesisSystemPromptFor(nil)
		if got != synthesisSystemPrompt {
			t.Error("expected base prompt for nil results")
		}
		if strings.Contains(got, "<clarification_mode>") {
			t.Error("base prompt should not contain <clarification_mode>")
		}
	})

	t.Run("non-nil results with ClarificationNeeded nil returns base prompt", func(t *testing.T) {
		results := &ExecutionResult{Steps: []StepResult{{StepID: "s1"}}}
		got := synthesisSystemPromptFor(results)
		if got != synthesisSystemPrompt {
			t.Error("expected base prompt when ClarificationNeeded is nil")
		}
		if strings.Contains(got, "<clarification_mode>") {
			t.Error("base prompt should not contain <clarification_mode>")
		}
	})

	t.Run("ClarificationNeeded set returns base + addendum", func(t *testing.T) {
		results := &ExecutionResult{
			ClarificationNeeded: &ClarificationRequest{Question: "When?"},
		}
		got := synthesisSystemPromptFor(results)
		if got == synthesisSystemPrompt {
			t.Error("expected augmented prompt when ClarificationNeeded is set")
		}
		if !strings.Contains(got, "<clarification_mode>") {
			t.Error("augmented prompt should contain <clarification_mode> tag")
		}
		if !strings.Contains(got, "</clarification_mode>") {
			t.Error("augmented prompt should contain closing </clarification_mode> tag")
		}
		// Base content still present — addendum does not replace, it extends.
		if !strings.Contains(got, "You are an AI synthesis engine") {
			t.Error("augmented prompt should still contain the base identity")
		}
	})

	t.Run("addendum content — all positive directives (§2.4 compliance)", func(t *testing.T) {
		results := &ExecutionResult{
			ClarificationNeeded: &ClarificationRequest{Question: "When?"},
		}
		got := synthesisSystemPromptFor(results)
		forbidden := []string{"Do NOT", "do not", "NEVER", "WRONG"}
		for _, f := range forbidden {
			if strings.Contains(got, f) {
				t.Errorf("clarification addendum must not contain negative instruction %q", f)
			}
		}
	})
}

// =============================================================================
// Step 5b — buildSynthesisPrompt <clarification_needed> section (non-streaming)
// =============================================================================

func TestBuildSynthesisPrompt_ClarificationSection(t *testing.T) {
	synth := &AISynthesizer{}
	results := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "country-info-tool", Instruction: "Fetch country info", Response: `{"country":"JP"}`, Success: true},
		},
		ClarificationNeeded: &ClarificationRequest{
			Question:        "What are your travel dates?",
			MissingFields:   []string{"travel_dates", "trip_duration"},
			PartialProgress: "Country information gathered for Japan and Korea.",
		},
	}

	prompt := synth.buildSynthesisPrompt(context.Background(), "original request", results)

	// <clarification_needed> block must be present with all fields
	wantSubstrings := []string{
		"<clarification_needed>",
		"Question to ask the user: What are your travel dates?",
		"Missing fields: travel_dates, trip_duration",
		"Partial progress to mention: Country information gathered for Japan and Korea.",
		"</clarification_needed>",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(prompt, s) {
			t.Errorf("expected clarification section to contain %q", s)
		}
	}

	// Block must come after <agent_responses> and before the trailing
	// synthesis directive (ordering matters for U-curve positioning).
	agentEnd := strings.Index(prompt, "</agent_responses>")
	clarStart := strings.Index(prompt, "<clarification_needed>")
	trailingDirective := strings.Index(prompt, "Synthesize the above into a helpful answer.")
	if agentEnd == -1 || clarStart == -1 || trailingDirective == -1 {
		t.Fatal("expected all three anchor strings to be present")
	}
	if agentEnd >= clarStart || clarStart >= trailingDirective {
		t.Errorf("ordering violation: <agent_responses> end=%d, <clarification_needed>=%d, directive=%d",
			agentEnd, clarStart, trailingDirective)
	}
}

func TestBuildSynthesisPrompt_NoClarificationSection_WhenNil(t *testing.T) {
	synth := &AISynthesizer{}
	results := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "a", Instruction: "", Response: "ok", Success: true},
		},
		// ClarificationNeeded intentionally nil
	}

	prompt := synth.buildSynthesisPrompt(context.Background(), "req", results)

	if strings.Contains(prompt, "<clarification_needed>") {
		t.Error("clarification section must be absent when ClarificationNeeded is nil (regression check)")
	}
}

func TestBuildSynthesisPrompt_ClarificationSection_OptionalFieldsOmitted(t *testing.T) {
	synth := &AISynthesizer{}
	results := &ExecutionResult{
		Steps: []StepResult{{StepID: "s1", AgentName: "a", Response: "r", Success: true}},
		ClarificationNeeded: &ClarificationRequest{
			Question: "What?", // only the required field
		},
	}

	prompt := synth.buildSynthesisPrompt(context.Background(), "req", results)

	if !strings.Contains(prompt, "Question to ask the user: What?") {
		t.Error("expected question line")
	}
	if strings.Contains(prompt, "Missing fields:") {
		t.Error("expected missing_fields line to be omitted when empty")
	}
	if strings.Contains(prompt, "Partial progress to mention:") {
		t.Error("expected partial_progress line to be omitted when empty")
	}
}

// =============================================================================
// Step 5b — synthesisSystemPromptFor integration into synthesizeWithLLM
// (verifies the system prompt actually reaches the AI client)
// =============================================================================

// mockAIClientCapturingSystemPrompt records the SystemPrompt field of every
// AIOptions the synthesizer passes to the AI client, so tests can verify the
// correct system prompt (base vs clarification-mode) was selected.
type mockAIClientCapturingSystemPrompt struct {
	calls []string
}

func (m *mockAIClientCapturingSystemPrompt) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if opts != nil {
		m.calls = append(m.calls, opts.SystemPrompt)
	}
	return &core.AIResponse{Content: "ok"}, nil
}

func TestAISynthesizer_SynthesizeWithLLM_ClarificationModeSystemPrompt(t *testing.T) {
	t.Run("normal synthesis uses base system prompt", func(t *testing.T) {
		mock := &mockAIClientCapturingSystemPrompt{}
		synth := NewAISynthesizer(mock)

		_, err := synth.Synthesize(context.Background(), "req", &ExecutionResult{
			Steps: []StepResult{{StepID: "s1", AgentName: "a", Response: "ok", Success: true}},
		})
		if err != nil {
			t.Fatalf("Synthesize failed: %v", err)
		}
		if len(mock.calls) != 1 {
			t.Fatalf("expected 1 LLM call, got %d", len(mock.calls))
		}
		if strings.Contains(mock.calls[0], "<clarification_mode>") {
			t.Error("normal synthesis system prompt should NOT contain <clarification_mode>")
		}
	})

	t.Run("clarification-mode synthesis uses augmented system prompt", func(t *testing.T) {
		mock := &mockAIClientCapturingSystemPrompt{}
		synth := NewAISynthesizer(mock)

		_, err := synth.Synthesize(context.Background(), "req", &ExecutionResult{
			Steps:               []StepResult{{StepID: "s1", AgentName: "a", Response: "ok", Success: true}},
			ClarificationNeeded: &ClarificationRequest{Question: "What dates?"},
		})
		if err != nil {
			t.Fatalf("Synthesize failed: %v", err)
		}
		if len(mock.calls) != 1 {
			t.Fatalf("expected 1 LLM call, got %d", len(mock.calls))
		}
		if !strings.Contains(mock.calls[0], "<clarification_mode>") {
			t.Error("clarification synthesis system prompt MUST contain <clarification_mode>")
		}
	})
}

// =============================================================================
// Step 5e — streaming buildSynthesisPrompt <clarification_needed> section
// =============================================================================

func TestOrchestratorBuildSynthesisPrompt_ClarificationSection(t *testing.T) {
	o := &AIOrchestrator{config: DefaultConfig()}
	result := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "tool-a", Instruction: "do thing", Response: `{"ok":true}`, Success: true},
		},
		ClarificationNeeded: &ClarificationRequest{
			Question:        "Which cities?",
			MissingFields:   []string{"korea_cities"},
			PartialProgress: "Advisories gathered.",
		},
	}

	prompt := o.buildSynthesisPrompt(context.Background(), "req", result)

	wantSubstrings := []string{
		"<clarification_needed>",
		"Question to ask the user: Which cities?",
		"Missing fields: korea_cities",
		"Partial progress to mention: Advisories gathered.",
		"</clarification_needed>",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(prompt, s) {
			t.Errorf("streaming prompt missing expected substring %q", s)
		}
	}

	// Same ordering invariant as the non-streaming version
	agentEnd := strings.Index(prompt, "</agent_responses>")
	clarStart := strings.Index(prompt, "<clarification_needed>")
	trailingDirective := strings.Index(prompt, "Synthesize the above into a helpful answer.")
	if agentEnd >= clarStart || clarStart >= trailingDirective {
		t.Errorf("ordering violation in streaming prompt: agentEnd=%d, clarStart=%d, directive=%d",
			agentEnd, clarStart, trailingDirective)
	}
}

func TestOrchestratorBuildSynthesisPrompt_NoClarificationSection_WhenNil(t *testing.T) {
	o := &AIOrchestrator{config: DefaultConfig()}
	result := &ExecutionResult{
		Steps: []StepResult{{StepID: "s1", AgentName: "a", Response: "ok", Success: true}},
		// ClarificationNeeded intentionally nil
	}

	prompt := o.buildSynthesisPrompt(context.Background(), "req", result)
	if strings.Contains(prompt, "<clarification_needed>") {
		t.Error("streaming prompt must not contain clarification section when ClarificationNeeded is nil")
	}
}

// =============================================================================
// Step 6 — parseToolSelection sentinel error identity
// =============================================================================

func TestParseToolSelection_SentinelError_ORCH018(t *testing.T) {
	provider := &TieredCapabilityProvider{}

	t.Run("empty array returns errNoToolsSelected sentinel", func(t *testing.T) {
		_, err := provider.parseToolSelection("[]")
		if err == nil {
			t.Fatal("expected error for empty array")
		}
		if !errors.Is(err, errNoToolsSelected) {
			t.Errorf("expected errors.Is(err, errNoToolsSelected) to be true; got err=%v", err)
		}
	})

	t.Run("empty array with markdown fences returns the sentinel", func(t *testing.T) {
		_, err := provider.parseToolSelection("```json\n[]\n```")
		if !errors.Is(err, errNoToolsSelected) {
			t.Errorf("expected sentinel; got %v", err)
		}
	})

	t.Run("valid non-empty array returns tools, nil error", func(t *testing.T) {
		tools, err := provider.parseToolSelection(`["agent/cap1", "agent/cap2"]`)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if len(tools) != 2 {
			t.Errorf("expected 2 tools, got %d", len(tools))
		}
	})

	t.Run("malformed JSON returns a non-sentinel error", func(t *testing.T) {
		_, err := provider.parseToolSelection("not valid json")
		if err == nil {
			t.Fatal("expected error for malformed JSON")
		}
		if errors.Is(err, errNoToolsSelected) {
			t.Error("malformed JSON error must NOT be confused with the empty-selection sentinel")
		}
	})
}

// =============================================================================
// Step 6c — Layer 3 defensive recovery in selectRelevantTools
// =============================================================================

// TestSelectRelevantTools_Layer3Recovery_ORCH018 verifies that when the
// tiered selector LLM returns an empty array on a continuation phase AND
// PhaseContextKeyPriorToolIDs is populated, the defensive recovery fires:
// selectedTools is replaced with the prior tool IDs, no retries fire, and
// the call completes successfully. Preserves tiered selection's token-saving
// purpose by avoiding all-agents fallback.
func TestSelectRelevantTools_Layer3Recovery_ORCH018(t *testing.T) {
	catalog := setupTestCatalog(25) // 25 tools → triggers tiered selection
	aiClient := NewTieredTestAIClient()
	// Simulate LLM disobeying Layer 2's "return prior tools" instruction
	aiClient.SetResponse(`[]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, &TieredCapabilityConfig{
		MinToolsForTiering: 20,
		RetryEnabled:       true,
		MaxRetries:         2, // would normally retry 3 times on empty; recovery must short-circuit
	})

	// Populate phaseContext with prior tool IDs that exist in the catalog.
	// setupTestCatalog creates "test-agent/capability_N" pairs, so use those.
	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
		PhaseContextKeyPriorToolIDs: []string{
			"test-agent/capability_0",
			"test-agent/capability_1",
		},
	}

	result, err := provider.GetCapabilities(context.Background(), "test request", phaseContext)
	if err != nil {
		t.Fatalf("expected successful recovery, got error: %v", err)
	}

	// The selector LLM should have been called exactly once (no retries).
	if len(aiClient.GetCalls()) != 1 {
		t.Errorf("expected exactly 1 LLM call (no retries on semantic empty), got %d", len(aiClient.GetCalls()))
	}

	// The capability result should contain the prior tools, NOT the full
	// catalog. Verify by checking that capability_0 and capability_1 are
	// present but capability_20+ (not in prior_tool_ids) are absent.
	if !strings.Contains(result.FormattedInfo, "capability_0") {
		t.Error("expected recovered capabilities to include prior tool capability_0")
	}
	if !strings.Contains(result.FormattedInfo, "capability_1") {
		t.Error("expected recovered capabilities to include prior tool capability_1")
	}
	// capability_20 is in the full catalog but NOT in prior_tool_ids.
	// If recovery used all-agents fallback instead of prior-tools fallback,
	// it would be present — regression guard.
	if strings.Contains(result.FormattedInfo, "capability_20") {
		t.Error("recovery should return only prior tools, not the full catalog (capability_20 leaked)")
	}
}

// TestSelectRelevantTools_Layer3_Phase1ShortCircuitsOnSemanticEmpty verifies
// that on Phase 1 (no phaseContext / no prior tools), a semantic-empty
// selector response short-circuits retries and immediately falls back to
// all-agents — the prior-tools recovery is gated on PriorToolIDs being
// non-empty, but case (b) of Layer 3 still surfaces the sentinel after one
// LLM call to avoid wasting retries on a deterministic empty answer.
//
// This guards against the regression observed in trace
// orch-1775528657240968209 where Phase 1 empty selector responses caused 6
// wasted LLM calls (2 trips × 3 attempts) before the orchestrator gave up.
func TestSelectRelevantTools_Layer3_Phase1ShortCircuitsOnSemanticEmpty(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`[]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, &TieredCapabilityConfig{
		MinToolsForTiering: 20,
		RetryEnabled:       true,
		MaxRetries:         2,
	})

	// Phase 1: nil phaseContext (no prior_tool_ids to fall back to).
	// Expected: the selector LLM is called exactly ONCE — the semantic-empty
	// short-circuit surfaces the sentinel error immediately, the existing
	// graceful-degradation path in selectTools (line 373) catches it and
	// falls back to all-agents.
	result, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("expected graceful degradation via all-agents fallback, got error: %v", err)
	}

	if len(aiClient.GetCalls()) != 1 {
		t.Errorf("expected exactly 1 LLM call on Phase 1 semantic empty (no retries), got %d calls", len(aiClient.GetCalls()))
	}
	// All-agents fallback must include the full catalog.
	if !strings.Contains(result.FormattedInfo, "capability_0") {
		t.Error("expected all-agents fallback to include capability_0")
	}
	if !strings.Contains(result.FormattedInfo, "capability_20") {
		t.Error("expected all-agents fallback to include capability_20 (full catalog, not a filtered subset)")
	}
}

// TestSelectRelevantTools_Layer3_EmptyPriorToolIDs_ShortCircuitsLikePhase1
// verifies that a continuation phase with PhaseContextKeyPriorToolIDs present
// but empty is treated like Phase 1: case (a) recovery is gated out, case (b)
// short-circuits after one call. Same anti-regression as the Phase 1 test.
func TestSelectRelevantTools_Layer3_EmptyPriorToolIDs_ShortCircuitsLikePhase1(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`[]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, &TieredCapabilityConfig{
		MinToolsForTiering: 20,
		RetryEnabled:       true,
		MaxRetries:         2,
	})

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:  2,
		PhaseContextKeyPriorToolIDs: []string{}, // empty slice — case (a) gate fails, case (b) fires
	}

	result, err := provider.GetCapabilities(context.Background(), "test request", phaseContext)
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}

	if len(aiClient.GetCalls()) != 1 {
		t.Errorf("expected exactly 1 LLM call when priorIDs is empty (case (b) short-circuit), got %d", len(aiClient.GetCalls()))
	}
	if !strings.Contains(result.FormattedInfo, "capability_0") {
		t.Error("expected all-agents fallback to include capability_0")
	}
}

// =============================================================================
// Step 3 — executePhaseLoop clarification short-circuit (integration-style)
// =============================================================================

// mockAIClientReturnsClarification is a mock AI client that returns a plan
// containing NeedsUserInput when called for plan generation. It distinguishes
// planning calls from other calls via prompt content heuristics matching the
// framework's planning prompt structure.
type mockAIClientReturnsClarification struct {
	planningCalls int
	synthCalls    int
}

func (m *mockAIClientReturnsClarification) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	// Planning prompts contain <available_agents> or plan_id schema hints
	isPlanning := strings.Contains(prompt, "<available_agents>") ||
		strings.Contains(prompt, "plan_id") ||
		strings.Contains(prompt, "execution plan")
	isSynthesis := strings.Contains(prompt, "<agent_responses>") ||
		strings.Contains(prompt, "Synthesize the above")

	if isPlanning && !isSynthesis {
		m.planningCalls++
		clarificationPlan := map[string]any{
			"plan_id":          "clarification-plan-001",
			"original_request": "test",
			"mode":             "autonomous",
			"terminal":         true,
			"steps":            []any{},
			"needs_user_input": map[string]any{
				"question":       "What dates are you planning to travel?",
				"missing_fields": []string{"travel_dates", "destination"},
			},
		}
		b, _ := json.Marshal(clarificationPlan)
		return &core.AIResponse{Content: string(b)}, nil
	}
	if isSynthesis {
		m.synthCalls++
		return &core.AIResponse{Content: "I'd love to help plan your trip. Could you let me know your preferred travel dates and destinations?"}, nil
	}
	// Fallback: return a plan-shaped response (safety net — shouldn't normally hit)
	return &core.AIResponse{Content: `{"plan_id":"x","steps":[]}`}, nil
}

func TestExecutePhaseLoop_ClarificationShortCircuit(t *testing.T) {
	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "test-1",
		Name:         "test-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "test_capability"}},
	})

	mockAI := &mockAIClientReturnsClarification{}
	o := NewAIOrchestrator(DefaultConfig(), discovery, mockAI)
	o.catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID: "test-1", Name: "test-agent", Address: "localhost", Port: 8080,
			},
			Capabilities: []EnhancedCapability{{Name: "test_capability"}},
		},
	}

	response, err := o.ProcessRequest(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}
	if response == nil {
		t.Fatal("expected non-nil response")
	}

	// Clarification field MUST be populated on the response — this is the
	// key user-visible behaviour of Layer 1 Step 4.
	if response.Clarification == nil {
		t.Fatal("response.Clarification must be set when planner emits NeedsUserInput")
	}
	if response.Clarification.Question != "What dates are you planning to travel?" {
		t.Errorf("unexpected clarification question: %q", response.Clarification.Question)
	}
	if !reflect.DeepEqual(response.Clarification.MissingFields, []string{"travel_dates", "destination"}) {
		t.Errorf("unexpected missing fields: %v", response.Clarification.MissingFields)
	}

	// Exactly ONE planning call must have fired — the short-circuit prevents
	// any continuation plan generation after the first clarification emission.
	if mockAI.planningCalls != 1 {
		t.Errorf("expected exactly 1 planning call, got %d (short-circuit failed to prevent continuation planning)",
			mockAI.planningCalls)
	}

	// Synthesis must have been called (so the user sees the question in prose).
	if mockAI.synthCalls != 1 {
		t.Errorf("expected exactly 1 synthesis call, got %d", mockAI.synthCalls)
	}

	// The response body must be the synthesized text, not the raw plan JSON.
	if response.Response == "" {
		t.Error("response body is empty")
	}
	if strings.Contains(response.Response, "plan_id") {
		t.Error("response body leaked the raw plan JSON instead of using the synthesized clarification text")
	}
}

// TestExecutePhaseLoop_NormalPlan_ClarificationNil is a regression check: a
// normal (non-clarification) plan must NOT populate response.Clarification.
func TestExecutePhaseLoop_NormalPlan_ClarificationNil(t *testing.T) {
	discovery := NewMockDiscovery()
	// Register the agent the MockAIClient's canned plan actually references
	// (stock-analyzer / analyze_stock). Validation now re-runs to a fixpoint, so the
	// registered agent must match the plan or the phase fails.
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "stock-1",
		Name:         "stock-analyzer",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "analyze_stock"}},
	})

	// Use the existing MockAIClient which returns a normal (non-clarification) plan
	mockAI := NewMockAIClient()
	o := NewAIOrchestrator(DefaultConfig(), discovery, mockAI)
	o.catalog.agents = map[string]*AgentInfo{
		"stock-1": {
			Registration: &core.ServiceRegistration{
				ID: "stock-1", Name: "stock-analyzer", Address: "localhost", Port: 8080,
			},
			Capabilities: []EnhancedCapability{{Name: "analyze_stock"}},
		},
	}
	o.executor = NewSmartExecutor(o.catalog)

	response, err := o.ProcessRequest(context.Background(), "normal request", nil)
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}
	if response.Clarification != nil {
		t.Errorf("response.Clarification must be nil for normal plans, got %+v", response.Clarification)
	}
}
