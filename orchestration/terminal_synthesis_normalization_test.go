package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// boolPtr is defined in regenerate_continuation_plan_test.go (same package).

// aggregationParams is the exact trace-shape parameter set: every leaf is a step-output template.
func aggregationParams() map[string]interface{} {
	return map[string]interface{}{
		"weather":     "{{step-8.response.data}}",
		"flights":     "{{step-11.response.data}}",
		"budget_eur":  "{{step-20.response.data.result}}",
		"attractions": "{{step-17.response.data}}",
	}
}

func TestOnlyAggregatesPriorOutputs(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]interface{}
		want   bool
	}{
		{"all step-output templates", aggregationParams(), true},
		{"single template", map[string]interface{}{"x": "{{step-1.response.data}}"}, true},
		{"nested map all templates", map[string]interface{}{
			"outer": map[string]interface{}{"inner": "{{step-1.response.data.lat}}"},
		}, true},
		{"slice of templates", map[string]interface{}{
			"list": []interface{}{"{{step-1.response.data}}", "{{step-2.response.data}}"},
		}, true},
		{"plain literal string", map[string]interface{}{"origin": "JFK"}, false},
		{"mixed literal+template string", map[string]interface{}{
			"msg": "send {{step-1.response.data}} to Bob",
		}, false},
		{"numeric literal leaf", map[string]interface{}{
			"amount": 3, "data": "{{step-1.response.data}}",
		}, false},
		{"bool literal leaf", map[string]interface{}{
			"flag": true, "data": "{{step-1.response.data}}",
		}, false},
		{"empty params", map[string]interface{}{}, false},
		{"empty slice leaf", map[string]interface{}{"list": []interface{}{}}, false},
		{"slice with a literal element", map[string]interface{}{
			"list": []interface{}{"{{step-1.response.data}}", "JFK"},
		}, false},
		{"template with surrounding whitespace", map[string]interface{}{
			"x": "  {{step-1.response.data}}  ",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlyAggregatesPriorOutputs(tc.params); got != tc.want {
				t.Fatalf("onlyAggregatesPriorOutputs(%v) = %v, want %v", tc.params, got, tc.want)
			}
		})
	}
}

func TestKnownStepIDSet(t *testing.T) {
	plan := &RoutingPlan{Steps: []RoutingStep{{StepID: "step-21"}}}
	known := knownStepIDSet([]string{"step-1", "step-8"}, plan)
	for _, id := range []string{"step-1", "step-8", "step-21"} {
		if _, ok := known[id]; !ok {
			t.Fatalf("expected %s in known set", id)
		}
	}
	if _, ok := known["step-99"]; ok {
		t.Fatalf("did not expect step-99 in known set")
	}
}

// catalogWithRealAgents returns a catalog that has real tools but NOT a "default" agent
// and NOT a "synthesize_*" capability.
func catalogWithRealAgents() *AgentCatalog {
	return NewAgentCatalogWithFixtures(map[string]*AgentInfo{
		"flight-tool": {
			Registration: &core.ServiceRegistration{Name: "flight-tool"},
			Capabilities: []EnhancedCapability{{Name: "search_flights"}},
		},
		"weather-tool-v2": {
			Registration: &core.ServiceRegistration{Name: "weather-tool-v2"},
			Capabilities: []EnhancedCapability{{Name: "get_weather_forecast"}},
		},
	})
}

func synthesisPseudoStep() RoutingStep {
	return RoutingStep{
		StepID:    "step-21",
		AgentName: "default",
		Metadata: map[string]interface{}{
			"capability": "synthesize_itinerary_and_affordability",
			"parameters": aggregationParams(),
		},
	}
}

func realActionStep() RoutingStep {
	return RoutingStep{
		StepID:    "step-30",
		AgentName: "flight-tool",
		Metadata: map[string]interface{}{
			"capability": "search_flights",
			"parameters": map[string]interface{}{"origin": "JFK", "destination": "AMS"},
		},
	}
}

func TestDetectTerminalSynthesisPseudoSteps(t *testing.T) {
	// knownStepIDs covers every step referenced by the synthesis pseudo-step.
	known := map[string]struct{}{
		"step-8": {}, "step-11": {}, "step-17": {}, "step-20": {},
	}

	tests := []struct {
		name        string
		orch        *AIOrchestrator
		plan        *RoutingPlan
		known       map[string]struct{}
		wantDropped int
	}{
		{
			name:        "exact trace shape → detected",
			orch:        &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan:        &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{synthesisPseudoStep()}},
			known:       known,
			wantDropped: 1,
		},
		{
			name:        "nil catalog → never detected (fail open)",
			orch:        &AIOrchestrator{catalog: nil},
			plan:        &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{synthesisPseudoStep()}},
			known:       known,
			wantDropped: 0,
		},
		{
			name: "agent in catalog → never detected",
			orch: &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan: &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{func() RoutingStep {
				s := synthesisPseudoStep()
				s.AgentName = "flight-tool" // resolvable agent
				return s
			}()}},
			known:       known,
			wantDropped: 0,
		},
		{
			name: "registered capability → never detected",
			orch: &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan: &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{func() RoutingStep {
				s := synthesisPseudoStep()
				s.Metadata["capability"] = "search_flights" // registered capability
				return s
			}()}},
			known:       known,
			wantDropped: 0,
		},
		{
			name:        "non-terminal plan → never detected",
			orch:        &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan:        &RoutingPlan{Terminal: boolPtr(false), Steps: []RoutingStep{synthesisPseudoStep()}},
			known:       known,
			wantDropped: 0,
		},
		{
			name:        "unsatisfiable reference (step-999) → never detected",
			orch:        &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan:        &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{synthesisPseudoStep()}},
			known:       map[string]struct{}{"step-8": {}}, // missing step-11/17/20
			wantDropped: 0,
		},
		{
			name: "literal external input → never detected",
			orch: &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan: &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{func() RoutingStep {
				s := synthesisPseudoStep()
				p := aggregationParams()
				p["recipient"] = "ops@example.com" // literal
				s.Metadata["parameters"] = p
				return s
			}()}},
			known:       known,
			wantDropped: 0,
		},
		{
			name: "mixed plan → only the pseudo-step detected",
			orch: &AIOrchestrator{catalog: catalogWithRealAgents()},
			plan: &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{
				realActionStep(),
				synthesisPseudoStep(),
			}},
			known:       known,
			wantDropped: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.orch.detectTerminalSynthesisPseudoSteps(tc.plan, tc.known)
			if len(got) != tc.wantDropped {
				t.Fatalf("detected %d pseudo-steps, want %d", len(got), tc.wantDropped)
			}
		})
	}
}

func TestNormalizeTerminalSynthesisPlan(t *testing.T) {
	known := map[string]struct{}{"step-8": {}, "step-11": {}, "step-17": {}, "step-20": {}}
	o := &AIOrchestrator{catalog: catalogWithRealAgents()}

	t.Run("only synthesis step → collapses to zero-step terminal", func(t *testing.T) {
		plan := &RoutingPlan{PlanID: "p1", Terminal: boolPtr(true), Steps: []RoutingStep{synthesisPseudoStep()}}
		changed := o.normalizeTerminalSynthesisPlan(context.Background(), plan, known, "req-1")
		if !changed {
			t.Fatal("expected plan to be changed")
		}
		if len(plan.Steps) != 0 {
			t.Fatalf("expected 0 steps after normalization, got %d", len(plan.Steps))
		}
		if !plan.IsTerminal() {
			t.Fatal("expected plan to remain terminal")
		}
	})

	t.Run("mixed plan → keeps the real step", func(t *testing.T) {
		plan := &RoutingPlan{PlanID: "p2", Terminal: boolPtr(true), Steps: []RoutingStep{
			realActionStep(),
			synthesisPseudoStep(),
		}}
		changed := o.normalizeTerminalSynthesisPlan(context.Background(), plan, known, "req-2")
		if !changed {
			t.Fatal("expected plan to be changed")
		}
		if len(plan.Steps) != 1 || plan.Steps[0].StepID != "step-30" {
			t.Fatalf("expected only the real step-30 to remain, got %+v", plan.Steps)
		}
	})

	t.Run("no pseudo-step → no change", func(t *testing.T) {
		plan := &RoutingPlan{PlanID: "p3", Terminal: boolPtr(true), Steps: []RoutingStep{realActionStep()}}
		if o.normalizeTerminalSynthesisPlan(context.Background(), plan, known, "req-3") {
			t.Fatal("did not expect a change")
		}
		if len(plan.Steps) != 1 {
			t.Fatalf("expected the real step to be preserved, got %d", len(plan.Steps))
		}
	})
}

func TestDefaultConfigMaxValidationRounds(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.IterativePlanning.MaxValidationRounds != 4 {
		t.Fatalf("expected default MaxValidationRounds=4, got %d", cfg.IterativePlanning.MaxValidationRounds)
	}
}

func TestDefaultConfig_MaxValidationRoundsEnv(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want int
	}{
		{"valid override", "7", 7},
		{"zero rejected (guard n>0)", "0", 4},
		{"negative rejected", "-3", 4},
		{"non-numeric rejected", "abc", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRUVAG3_ITERATIVE_MAX_VALIDATION_ROUNDS", tc.set)
			cfg := DefaultConfig()
			if cfg.IterativePlanning.MaxValidationRounds != tc.want {
				t.Fatalf("env=%q → MaxValidationRounds=%d, want %d", tc.set, cfg.IterativePlanning.MaxValidationRounds, tc.want)
			}
		})
	}
}

func TestAgentInCatalog(t *testing.T) {
	o := &AIOrchestrator{catalog: catalogWithRealAgents()}
	if !o.agentInCatalog("flight-tool") {
		t.Fatal("flight-tool should be in catalog")
	}
	if o.agentInCatalog("Flight-Tool") {
		t.Fatal("agentInCatalog should be case-sensitive (matches executor/validatePlan)")
	}
	if o.agentInCatalog("nope") {
		t.Fatal("unregistered agent should not be in catalog")
	}
	if o.agentInCatalog("") {
		t.Fatal("empty name should not be in catalog")
	}
	if (&AIOrchestrator{}).agentInCatalog("flight-tool") {
		t.Fatal("nil catalog must report not-in-catalog (fail open)")
	}
}

func TestCapabilityRegistered(t *testing.T) {
	o := &AIOrchestrator{catalog: catalogWithRealAgents()}
	if !o.capabilityRegistered("search_flights") {
		t.Fatal("search_flights should be registered")
	}
	if o.capabilityRegistered("synthesize_itinerary_and_affordability") {
		t.Fatal("invented capability should not be registered")
	}
	if o.capabilityRegistered("") {
		t.Fatal("empty capability should not be registered")
	}
	if (&AIOrchestrator{}).capabilityRegistered("search_flights") {
		t.Fatal("nil catalog must report not-registered (fail open)")
	}
}

// TestDetectTerminalSynthesisPseudoSteps_ReferencedStepNotDropped covers review-fix #1: a step
// that another step references (depends_on/templates) is never dropped, to avoid creating a
// dangling reference that would force a needless regeneration round.
func TestDetectTerminalSynthesisPseudoSteps_ReferencedStepNotDropped(t *testing.T) {
	o := &AIOrchestrator{catalog: catalogWithRealAgents()}
	known := map[string]struct{}{"step-8": {}, "step-11": {}, "step-17": {}, "step-20": {}}

	// step-21 is a synthesis pseudo-step, but a sibling step-40 depends_on it.
	pseudo := synthesisPseudoStep() // step-21
	dependent := RoutingStep{
		StepID:    "step-40",
		AgentName: "flight-tool", // real agent, so itself never a drop candidate
		DependsOn: []string{"step-21"},
		Metadata: map[string]interface{}{
			"capability": "search_flights",
			"parameters": map[string]interface{}{"origin": "JFK"},
		},
	}
	plan := &RoutingPlan{Terminal: boolPtr(true), Steps: []RoutingStep{pseudo, dependent}}

	if got := o.detectTerminalSynthesisPseudoSteps(plan, known); len(got) != 0 {
		t.Fatalf("step-21 is referenced by step-40 and must NOT be dropped; detected %d", len(got))
	}
}

// recordingLogger captures WarnWithContext calls for assertions.
type recordingLogger struct {
	warns []struct {
		msg    string
		fields map[string]interface{}
	}
}

func (l *recordingLogger) Info(string, map[string]interface{})                              {}
func (l *recordingLogger) Error(string, map[string]interface{})                             {}
func (l *recordingLogger) Warn(string, map[string]interface{})                              {}
func (l *recordingLogger) Debug(string, map[string]interface{})                             {}
func (l *recordingLogger) InfoWithContext(context.Context, string, map[string]interface{})  {}
func (l *recordingLogger) ErrorWithContext(context.Context, string, map[string]interface{}) {}
func (l *recordingLogger) DebugWithContext(context.Context, string, map[string]interface{}) {}
func (l *recordingLogger) WarnWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	l.warns = append(l.warns, struct {
		msg    string
		fields map[string]interface{}
	}{msg, fields})
}

func TestNormalizeTerminalSynthesisPlan_EmitsWarnLog(t *testing.T) {
	known := map[string]struct{}{"step-8": {}, "step-11": {}, "step-17": {}, "step-20": {}}
	rl := &recordingLogger{}
	o := &AIOrchestrator{catalog: catalogWithRealAgents()}
	o.SetLogger(rl)

	// Mixed plan: one real step retained, one synthesis pseudo-step dropped.
	plan := &RoutingPlan{PlanID: "p-log", Terminal: boolPtr(true), Steps: []RoutingStep{
		realActionStep(),      // step-30, kept
		synthesisPseudoStep(), // step-21, dropped
	}}
	if !o.normalizeTerminalSynthesisPlan(context.Background(), plan, known, "req-log") {
		t.Fatal("expected normalization to change the plan")
	}
	if len(rl.warns) != 1 {
		t.Fatalf("expected exactly 1 WARN log, got %d", len(rl.warns))
	}
	f := rl.warns[0].fields
	if f["operation"] != "terminal_synthesis_normalization" {
		t.Errorf("operation = %v, want terminal_synthesis_normalization", f["operation"])
	}
	if f["request_id"] != "req-log" {
		t.Errorf("request_id = %v, want req-log", f["request_id"])
	}
	if f["step_id"] != "step-21" {
		t.Errorf("dropped step_id = %v, want step-21", f["step_id"])
	}
	if f["remaining_steps"] != 1 {
		t.Errorf("remaining_steps = %v, want 1", f["remaining_steps"])
	}
}

func TestBuildIterativePlanningInstructions_ContainsFinalAnswerContract(t *testing.T) {
	result := BuildIterativePlanningInstructions(&IterativePlanConfig{Enabled: true, MaxPhases: 5, MaxTotalSteps: 200})
	for _, want := range []string{
		"The framework writes the final user-facing answer",
		`"terminal": true, "steps": []`,
	} {
		if !strings.Contains(result, want) {
			t.Errorf("BuildIterativePlanningInstructions missing final-answer contract substring %q\nGot:\n%s", want, result)
		}
	}
}
