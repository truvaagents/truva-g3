package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// gauntletOrchestrator wires an orchestrator with a real "good-agent" in both discovery
// (validatePlan source) and catalog (capability check + executor), so a well-formed plan can pass
// the full validation gauntlet.
func gauntletOrchestrator(t *testing.T) *AIOrchestrator {
	t.Helper()
	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "g1",
		Name:         "good-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "do"}},
	})
	o := NewAIOrchestrator(DefaultConfig(), discovery, NewMockAIClient())
	o.catalog.agents = map[string]*AgentInfo{
		"g1": {
			Registration: &core.ServiceRegistration{ID: "g1", Name: "good-agent", Address: "localhost", Port: 8080},
			Capabilities: []EnhancedCapability{{Name: "do"}},
		},
	}
	o.executor = NewSmartExecutor(o.catalog)
	return o
}

func planWith(agent, capability string, params map[string]interface{}) *RoutingPlan {
	return &RoutingPlan{
		PlanID:   "p",
		Terminal: boolPtr(true),
		Steps: []RoutingStep{{
			StepID:    "step-1",
			AgentName: agent,
			Metadata:  map[string]interface{}{"capability": capability, "parameters": params},
		}},
	}
}

// TestRunPlanValidationGauntlet exercises the relocated validator helper directly: a valid plan
// passes; an unknown agent is caught by validatePlan; a malformed macro is caught by RC2 only
// after validatePlan passes (confirms ordering).
func TestRunPlanValidationGauntlet(t *testing.T) {
	o := gauntletOrchestrator(t)
	ctx := context.Background()

	if err := o.runPlanValidationGauntlet(ctx, planWith("good-agent", "do", map[string]interface{}{}), nil, nil, 1, "t"); err != nil {
		t.Fatalf("valid plan should pass the gauntlet, got: %v", err)
	}

	if err := o.runPlanValidationGauntlet(ctx, planWith("ghost-agent", "do", map[string]interface{}{}), nil, nil, 1, "t"); err == nil {
		t.Fatal("plan with an unregistered agent should fail the gauntlet (validatePlan)")
	}

	// Valid agent but a malformed framework macro → validatePlan passes, RC2 (validateNoUnknownMacros) fails.
	macroBad := planWith("good-agent", "do", map[string]interface{}{"x": "{{step-1}}"})
	if err := o.runPlanValidationGauntlet(ctx, macroBad, nil, nil, 1, "t"); err == nil {
		t.Fatal("plan with a malformed {{step-1}} macro should fail the gauntlet (RC2)")
	}
}

// TestRunPlanValidationGauntlet_FailurePaths drives each validator to fail (with earlier ones
// passing) and asserts the gauntlet returns the error AND emits the validator's WARN log with the
// expected operation/error_type. A recording logger is attached so the nil-guarded log blocks
// actually execute — this is the coverage that was previously missing and that guards the
// relocated telemetry strings against silent renames.
func TestRunPlanValidationGauntlet_FailurePaths(t *testing.T) {
	o := gauntletOrchestrator(t)
	rl := &recordingLogger{}
	o.SetLogger(rl)
	ctx := context.Background()

	step := func(agent string, params map[string]interface{}, implicit []string) RoutingStep {
		return RoutingStep{
			StepID:       "step-1",
			AgentName:    agent,
			ImplicitDeps: implicit,
			Metadata:     map[string]interface{}{"capability": "do", "parameters": params},
		}
	}
	plan := func(s RoutingStep) *RoutingPlan {
		return &RoutingPlan{PlanID: "p", Terminal: boolPtr(true), Steps: []RoutingStep{s}}
	}

	cases := []struct {
		name          string
		plan          *RoutingPlan
		execStepIDs   []string
		phaseCount    int
		wantOperation string
		wantErrorType string // "" if the log carries no error_type field
	}{
		{
			name:          "validatePlan: unregistered agent",
			plan:          plan(step("ghost-agent", map[string]interface{}{}, nil)),
			phaseCount:    1,
			wantOperation: "phase_validation_regeneration_trigger",
		},
		{
			name:          "RC2: malformed framework macro",
			plan:          plan(step("good-agent", map[string]interface{}{"x": "{{step-1}}"}, nil)),
			phaseCount:    1,
			wantOperation: "unknown_macro_validation",
			wantErrorType: "unknown_macro",
		},
		{
			name:          "RC3: prior-phase ref not declared in implicit_deps",
			plan:          plan(step("good-agent", map[string]interface{}{"x": "{{step-2.response.data.f}}"}, nil)),
			phaseCount:    2,
			wantOperation: "missing_dependency_validation",
			wantErrorType: "missing_dependency",
		},
		{
			name:          "RC1: declared but non-existent prior step",
			plan:          plan(step("good-agent", map[string]interface{}{"x": "{{step-2.response.data.f}}"}, []string{"step-2"})),
			phaseCount:    2,
			wantOperation: "cross_phase_missing_step_validation",
			wantErrorType: "cross_phase_missing_step",
		},
		{
			name:          "step-ID conflict (continuation phase)",
			plan:          plan(step("good-agent", map[string]interface{}{}, nil)),
			execStepIDs:   []string{"step-1"},
			phaseCount:    2,
			wantOperation: "step_id_conflict_regeneration_trigger",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rl.warns = nil
			err := o.runPlanValidationGauntlet(ctx, tc.plan, nil, tc.execStepIDs, tc.phaseCount, "req-1")
			if err == nil {
				t.Fatalf("expected the gauntlet to return an error")
			}
			if len(rl.warns) == 0 {
				t.Fatalf("expected a WARN log to be emitted for the failing validator")
			}
			f := rl.warns[len(rl.warns)-1].fields
			if f["operation"] != tc.wantOperation {
				t.Errorf("log operation = %v, want %q", f["operation"], tc.wantOperation)
			}
			if tc.wantErrorType != "" && f["error_type"] != tc.wantErrorType {
				t.Errorf("log error_type = %v, want %q", f["error_type"], tc.wantErrorType)
			}
		})
	}
}

// scriptedAIClient returns scripted plan JSON for planning/regeneration prompts (advancing through
// the list, repeating the last) and a canned reply for synthesis. Lets a test drive the fixpoint
// loop deterministically across regenerations.
type scriptedAIClient struct {
	plans     []string
	synth     string
	planCalls int
}

func (m *scriptedAIClient) GenerateResponse(_ context.Context, prompt string, _ *core.AIOptions) (*core.AIResponse, error) {
	switch {
	case strings.Contains(prompt, "Synthesize"):
		return &core.AIResponse{Content: m.synth}, nil
	case strings.Contains(prompt, "Create an execution plan"):
		i := m.planCalls
		if i >= len(m.plans) {
			i = len(m.plans) - 1
		}
		m.planCalls++
		return &core.AIResponse{Content: m.plans[i]}, nil
	default:
		return &core.AIResponse{Content: "{}"}, nil // benign for any stray prompt
	}
}

const loopValidPlanJSON = `{"plan_id":"valid-1","original_request":"t","mode":"autonomous","terminal":true,"steps":[{"step_id":"step-1","agent_name":"good-agent","namespace":"default","instruction":"do","depends_on":[],"metadata":{"capability":"do","parameters":{}}}]}`

// loopInvalidPlanJSON routes to an agent that is NOT in discovery → validatePlan fails every round.
const loopInvalidPlanJSON = `{"plan_id":"invalid-1","original_request":"t","mode":"autonomous","terminal":true,"steps":[{"step_id":"step-1","agent_name":"ghost-agent","namespace":"default","instruction":"do","depends_on":[],"metadata":{"capability":"do","parameters":{}}}]}`

func loopConfig() *OrchestratorConfig {
	cfg := DefaultConfig()
	// Make AI-call sequencing deterministic: no tiered-selection call, no hallucination retries.
	cfg.HallucinationValidationEnabled = false
	cfg.EnableTieredResolution = false
	return cfg
}

// TestExecutePhaseLoop_ValidationExhausted: the planner keeps emitting an invalid plan; the
// fixpoint loop must re-validate every round and fail the phase explicitly after MaxValidationRounds
// rather than dispatching the bad plan.
func TestExecutePhaseLoop_ValidationExhausted(t *testing.T) {
	discovery := NewMockDiscovery() // empty — ghost-agent is never registered
	ai := &scriptedAIClient{plans: []string{loopInvalidPlanJSON}, synth: "synthesized"}
	o := NewAIOrchestrator(loopConfig(), discovery, ai)
	o.executor = NewSmartExecutor(o.catalog)

	_, err := o.ProcessRequest(context.Background(), "do something", nil)
	if err == nil {
		t.Fatal("expected ProcessRequest to fail when the plan never validates")
	}
	if !strings.Contains(err.Error(), "failed validation after") {
		t.Fatalf("expected a validation-exhausted error, got: %v", err)
	}
	// 1 initial generation + MaxValidationRounds (default 4) regenerations = 5 planning calls.
	if ai.planCalls < 2 {
		t.Fatalf("expected the loop to regenerate (>=2 planning calls), got %d", ai.planCalls)
	}
}

// TestExecutePhaseLoop_RegeneratesThenSucceeds: the first plan fails validation (unregistered
// agent); the regenerated plan is valid. The loop must re-validate the regenerated plan and
// converge — proving regeneration output is re-checked (the core fix).
func TestExecutePhaseLoop_RegeneratesThenSucceeds(t *testing.T) {
	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "g1",
		Name:         "good-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "do"}},
	})
	ai := &scriptedAIClient{plans: []string{loopInvalidPlanJSON, loopValidPlanJSON}, synth: "synthesized"}
	o := NewAIOrchestrator(loopConfig(), discovery, ai)
	o.catalog.agents = map[string]*AgentInfo{
		"g1": {
			Registration: &core.ServiceRegistration{ID: "g1", Name: "good-agent", Address: "localhost", Port: 8080},
			Capabilities: []EnhancedCapability{{Name: "do"}},
		},
	}
	o.executor = NewSmartExecutor(o.catalog)

	resp, err := o.ProcessRequest(context.Background(), "do something", nil)
	if err != nil {
		t.Fatalf("loop should converge after one regeneration, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil response")
	}
	if ai.planCalls < 2 {
		t.Fatalf("expected initial generation + regeneration (>=2 planning calls), got %d", ai.planCalls)
	}
}

// synthPseudoContinuationJSON is a terminal continuation plan whose single step is a synthesis
// pseudo-step (unregistered "default" agent, unregistered capability, params that are only
// step-output templates referencing completed prior steps).
const synthPseudoContinuationJSON = `{"plan_id":"synth-1","original_request":"t","mode":"autonomous","terminal":true,"steps":[{"step_id":"step-3","agent_name":"default","namespace":"default","instruction":"synthesize","depends_on":[],"implicit_deps":["step-1","step-2"],"metadata":{"capability":"synthesize_x","parameters":{"a":"{{step-1.response.data}}","b":"{{step-2.response.data}}"}}}]}`

// TestGenerateContinuationPlan_NormalizesSynthesisPseudoStepFirstPass proves the Finding-1 fix:
// the parse-time normalizer inside generateContinuationPlan collapses a terminal synthesis
// pseudo-step BEFORE the internal hallucination check, so the plan is accepted on the FIRST
// generation (zero-step terminal) with NO hallucination-triggered regeneration. With
// HallucinationValidationEnabled left ON, a regeneration would have meant a second planning call;
// asserting exactly one call proves the normalize ran first.
func TestGenerateContinuationPlan_NormalizesSynthesisPseudoStepFirstPass(t *testing.T) {
	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "g1",
		Name:         "good-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "do"}},
	})
	cfg := DefaultConfig()
	cfg.EnableTieredResolution = false // avoid a tiered-selection AI call; keep the hallucination check ON
	ai := &scriptedAIClient{plans: []string{synthPseudoContinuationJSON}, synth: "synthesized"}
	o := NewAIOrchestrator(cfg, discovery, ai)
	o.catalog.agents = map[string]*AgentInfo{
		"g1": {
			Registration: &core.ServiceRegistration{ID: "g1", Name: "good-agent", Address: "localhost", Port: 8080},
			Capabilities: []EnhancedCapability{{Name: "do"}},
		},
	}
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "good-agent", Success: true, Response: `{"data":"x"}`},
		"step-2": {StepID: "step-2", AgentName: "good-agent", Success: true, Response: `{"data":"y"}`},
	}

	plan, err := o.generateContinuationPlan(context.Background(), "do something", "req-1",
		completed, []string{"step-1", "step-2"}, "note", 2)
	if err != nil {
		t.Fatalf("generateContinuationPlan returned error: %v", err)
	}
	if !plan.IsTerminal() || len(plan.Steps) != 0 {
		t.Fatalf("expected synthesis pseudo-step normalized to a zero-step terminal plan; got terminal=%v steps=%d", plan.IsTerminal(), len(plan.Steps))
	}
	if ai.planCalls != 1 {
		t.Fatalf("expected exactly 1 planning call (normalized first-pass, no hallucination regeneration); got %d", ai.planCalls)
	}
}

// synthPseudoInitialJSON is an INITIAL (phase-1) terminal plan: two real gather steps plus a
// synthesis pseudo-step (unregistered "default" agent, unregistered capability, params that are
// only templates referencing the two gather steps in the same plan).
const synthPseudoInitialJSON = `{"plan_id":"p1-synth","original_request":"t","mode":"autonomous","terminal":true,"steps":[` +
	`{"step_id":"step-1","agent_name":"good-agent","namespace":"default","instruction":"get x","depends_on":[],"metadata":{"capability":"do","parameters":{"q":"x"}}},` +
	`{"step_id":"step-2","agent_name":"good-agent","namespace":"default","instruction":"get y","depends_on":[],"metadata":{"capability":"do","parameters":{"q":"y"}}},` +
	`{"step_id":"step-3","agent_name":"default","namespace":"default","instruction":"synthesize","depends_on":[],"implicit_deps":[],"metadata":{"capability":"synthesize_xy","parameters":{"a":"{{step-1.response.data}}","b":"{{step-2.response.data}}"}}}` +
	`]}`

// TestGenerateExecutionPlan_NormalizesSynthesisPseudoStepFirstPass proves the same Finding-1 fix
// on the INITIAL plan path (generateExecutionPlan): the parse-time normalizer strips the terminal
// synthesis pseudo-step BEFORE the hallucination check, so the phase-1 plan is accepted on the
// first generation (the two real gather steps retained, the "default"-agent synth step dropped)
// with NO hallucination-triggered regeneration. Mirrors the continuation-path test above.
func TestGenerateExecutionPlan_NormalizesSynthesisPseudoStepFirstPass(t *testing.T) {
	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "g1",
		Name:         "good-agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "do"}},
	})
	cfg := DefaultConfig()
	cfg.EnableTieredResolution = false // avoid a tiered-selection AI call; keep the hallucination check ON
	ai := &scriptedAIClient{plans: []string{synthPseudoInitialJSON}, synth: "synthesized"}
	o := NewAIOrchestrator(cfg, discovery, ai)
	o.catalog.agents = map[string]*AgentInfo{
		"g1": {
			Registration: &core.ServiceRegistration{ID: "g1", Name: "good-agent", Address: "localhost", Port: 8080},
			Capabilities: []EnhancedCapability{{Name: "do"}},
		},
	}

	plan, err := o.generateExecutionPlan(context.Background(), "get x and y then summarize", "req-1")
	if err != nil {
		t.Fatalf("generateExecutionPlan returned error: %v", err)
	}
	// The synthesis pseudo-step (step-3, agent=default) must be stripped; the two real gather steps remain.
	if len(plan.Steps) != 2 {
		t.Fatalf("expected the synthesis pseudo-step stripped (2 steps remaining); got %d", len(plan.Steps))
	}
	for _, s := range plan.Steps {
		if s.AgentName == "default" {
			t.Fatalf("synthesis pseudo-step (agent=default) was not stripped: %+v", s)
		}
	}
	// Normalized before the hallucination check ⇒ no hallucination-triggered regeneration.
	if ai.planCalls != 1 {
		t.Fatalf("expected exactly 1 planning call (normalized first-pass); got %d", ai.planCalls)
	}
}
