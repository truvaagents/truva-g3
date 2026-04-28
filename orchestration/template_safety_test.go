package orchestration

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// Tests in this file cover the ORCH-020 multi-layer defense against unresolved
// template passthrough (see orchestration/bugs/BUG_UNRESOLVED_TEMPLATE_PASSTHROUGH_TO_TOOL.md).
// Each RC has its own block; the file ends with a regression-golden that asserts
// the incident plan is flagged by every plan-time validator.

// ─── Helpers ─────────────────────────────────────────────────────────────────

// newOrchestratorWithCatalog returns an AIOrchestrator wired to a catalog that
// knows about the given agents. Used by the plan-time validator tests so each
// test controls which capabilities and output schemas are discoverable.
func newOrchestratorWithCatalog(t *testing.T, agents map[string]*AgentInfo) *AIOrchestrator {
	t.Helper()
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	catalog.agents = agents
	return &AIOrchestrator{catalog: catalog}
}

// simpleAgent builds an AgentInfo with one capability that publishes `fields`
// as its output schema. Shared across the validator tests to keep setup terse.
func simpleAgent(name, capName string, fields ...string) *AgentInfo {
	params := make([]Parameter, len(fields))
	for i, f := range fields {
		params[i] = Parameter{Name: f, Type: "string"}
	}
	return &AgentInfo{
		Registration: &core.ServiceRegistration{ID: name, Name: name},
		Capabilities: []EnhancedCapability{
			{Name: capName, Returns: ReturnType{Fields: params}},
		},
	}
}

// ─── RC1: cross-phase-aware validateTemplatePaths ────────────────────────────

func TestValidateTemplatePaths_CrossPhase_ReferencesMissingStep_Rejects(t *testing.T) {
	o := newOrchestratorWithCatalog(t, map[string]*AgentInfo{
		"jira-tool": simpleAgent("jira-tool", "create_issue"),
	})

	// Plan references step-1 from a prior phase, but executedStepCaps is empty.
	// RC1 must reject — no completed phase supplied step-1, so the template is
	// guaranteed to dispatch as a literal.
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:    "step-3",
			AgentName: "jira-tool",
			Metadata: map[string]interface{}{
				"capability": "create_issue",
				"parameters": map[string]interface{}{
					"title": "{{step-1.response.data.summary}}",
				},
			},
		}},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err == nil {
		t.Fatalf("expected RC1 to reject missing cross-phase step, got nil")
	}
	if !strings.Contains(err.Error(), "step-1") {
		t.Errorf("expected error to name step-1, got: %v", err)
	}
}

func TestValidateTemplatePaths_CrossPhase_ReferencesExecutedStep_Accepts(t *testing.T) {
	o := newOrchestratorWithCatalog(t, map[string]*AgentInfo{
		"research-agent": simpleAgent("research-agent", "analyze_data", "analysis"),
		"jira-tool":      simpleAgent("jira-tool", "create_issue"),
	})

	// step-1 is named in executedStepCaps (prior-phase completed result), so
	// the cross-phase reference resolves against its capability's output
	// schema. RC1 should accept.
	executed := map[string]stepCapability{
		"step-1": {agent: "research-agent", capability: "analyze_data"},
	}
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:       "step-2",
			AgentName:    "jira-tool",
			ImplicitDeps: []string{"step-1"},
			Metadata: map[string]interface{}{
				"capability": "create_issue",
				"parameters": map[string]interface{}{
					"description": "{{step-1.response.data.analysis}}",
				},
			},
		}},
	}

	if err := o.validateTemplatePaths(plan, executed); err != nil {
		t.Errorf("expected acceptance when prior phase supplied step, got: %v", err)
	}
}

func TestValidateTemplatePaths_NestedParam_ReferencesInvalidStep_Rejects(t *testing.T) {
	// ORCH-020 Issue 11: the flat top-level walk used to miss templates that
	// hid inside nested maps or arrays. RC1 now shares the runtime's
	// collectTemplateStrings primitive so nested params are covered.
	o := newOrchestratorWithCatalog(t, map[string]*AgentInfo{
		"jira-tool": simpleAgent("jira-tool", "create_issue"),
	})

	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:    "step-3",
			AgentName: "jira-tool",
			Metadata: map[string]interface{}{
				"capability": "create_issue",
				"parameters": map[string]interface{}{
					"fields": map[string]interface{}{
						"comments": []interface{}{
							map[string]interface{}{
								"body": "{{step-99.response.data.summary}}",
							},
						},
					},
				},
			},
		}},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err == nil {
		t.Fatalf("expected RC1 to flag nested reference to unknown step-99, got nil")
	}
	if !strings.Contains(err.Error(), "step-99") {
		t.Errorf("expected error to name step-99, got: %v", err)
	}
}

// ─── RC2: validateNoUnknownMacros ────────────────────────────────────────────

func TestValidateNoUnknownMacros_RejectsHallucinatedMacro(t *testing.T) {
	// RC2's remit is tokens that LOOK LIKE attempts at the framework's
	// {{step-N.response.data.FIELD}} syntax but get the shape wrong. The
	// narrowing to {{step-...}} keeps the framework from arbitrating
	// tool-specific template contracts (see the passthrough test below).
	cases := map[string]string{
		"bare step-id":    "{{step-1}}",         // no dot, no field path
		"trailing dot":    "{{step-1.}}",        // dot with empty field
		"space in step":   "{{step-1 foo}}",     // whitespace between
		"space in field":  "{{step-1.foo bar}}", // whitespace in field
		"dangling prefix": "{{step-}}",          // just prefix, no id
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			plan := &RoutingPlan{
				Steps: []RoutingStep{{
					StepID: "step-1",
					Metadata: map[string]interface{}{
						"parameters": map[string]interface{}{
							"departure_date": token,
						},
					},
				}},
			}
			err := validateNoUnknownMacros(plan)
			if err == nil {
				t.Fatalf("expected RC2 to reject %q, got nil", token)
			}
			if !strings.Contains(err.Error(), token) {
				t.Errorf("expected error to include the rejected token, got: %v", err)
			}
		})
	}
}

func TestValidateNoUnknownMacros_RejectsHallucinatedMacro_Nested(t *testing.T) {
	// Issue 11 coverage: RC2 walks nested structures via collectTemplateStrings
	// so a malformed framework token buried inside arrays/maps is still caught.
	// The malformed token must match frameworkMacroPattern ({{step-...}}) but
	// fail stepOutputTemplatePattern — {{step-7}} (no field path) is the
	// smallest case that exercises the scope.
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID: "step-1",
			Metadata: map[string]interface{}{
				"parameters": map[string]interface{}{
					"body": map[string]interface{}{
						"items": []interface{}{
							map[string]interface{}{"ref": "{{step-7}}"},
						},
					},
				},
			},
		}},
	}
	err := validateNoUnknownMacros(plan)
	if err == nil {
		t.Fatal("expected RC2 to reject nested malformed framework token, got nil")
	}
	if !strings.Contains(err.Error(), "{{step-7}}") {
		t.Errorf("expected error to include {{step-7}}, got: %v", err)
	}
}

func TestValidateNoUnknownMacros_PassesThroughToolSpecificSyntax(t *testing.T) {
	// Regression: the Prometheus tool's documented input syntax includes
	// {{now}} and {{now-7d}} — tool-specific relative-time expressions
	// handled by the tool itself, not the framework. RC2 must NOT reject
	// these, since doing so would break the tool's input contract before
	// the orchestrator ever dispatches. See examples/prometheus-query-tool/handlers.go.
	//
	// The contract RC2 enforces is narrower: only {{step-...}} tokens are
	// inspected. Anything else is considered tool-specific and passes
	// through — the framework stays domain-agnostic per
	// FRAMEWORK_DESIGN_PRINCIPLES.md §"Framework is domain-agnostic".
	passthroughs := []string{
		"{{now}}",
		"{{now-7d}}",
		"{{now-1h}}",
		"{{today_plus_1}}",
		"{{user.id}}",
		"{{foo}}",
		"Hello {{name}}", // embedded Go-template-ish payload
	}
	for _, token := range passthroughs {
		t.Run(token, func(t *testing.T) {
			plan := &RoutingPlan{
				Steps: []RoutingStep{{
					StepID: "step-1",
					Metadata: map[string]interface{}{
						"parameters": map[string]interface{}{
							"start": token,
						},
					},
				}},
			}
			if err := validateNoUnknownMacros(plan); err != nil {
				t.Errorf("tool-specific %q must pass RC2 (frameworks must not arbitrate tool input contracts), got: %v", token, err)
			}
		})
	}
}

func TestValidateNoUnknownMacros_AcceptsSupportedShape(t *testing.T) {
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:    "step-2",
			DependsOn: []string{"step-1"},
			Metadata: map[string]interface{}{
				"parameters": map[string]interface{}{
					"origin":      "{{step-1.response.data.iata_code}}",
					"destination": "{{step-1.response.data.location.airport}}",
					"static":      "nothing-curly-here",
				},
			},
		}},
	}
	if err := validateNoUnknownMacros(plan); err != nil {
		t.Errorf("supported shape should pass, got: %v", err)
	}
}

func TestValidateNoUnknownMacros_NilPlan(t *testing.T) {
	// Defensive contract: validators should tolerate nil plans.
	if err := validateNoUnknownMacros(nil); err != nil {
		t.Errorf("expected nil plan to pass, got: %v", err)
	}
}

// ─── Helper coverage: shared template-extraction primitives ──────────────────

func TestCollectReferencedStepIDs_NilInput(t *testing.T) {
	// Defensive contract — both validators (RC3) and the runtime sweep (RC4)
	// invoke this with whatever sits in step.Metadata["parameters"], which is
	// nil for steps that take no parameters.
	got := collectReferencedStepIDs(nil)
	if len(got) != 0 {
		t.Errorf("expected empty map for nil input, got %v", got)
	}
}

func TestCollectReferencedStepIDs_ExtractsAndDedupes(t *testing.T) {
	// Same step ID referenced multiple times in nested params should appear
	// once in the result; unrelated tool-specific syntax must not appear.
	params := map[string]interface{}{
		"a": "{{step-1.response.data.x}}",
		"b": []interface{}{"{{step-1.response.data.y}}", "{{step-2.response.data.z}}"},
		"c": map[string]interface{}{"nested": "{{now}}"}, // tool-specific — not a step ref
	}
	got := collectReferencedStepIDs(params)
	if len(got) != 2 {
		t.Errorf("expected 2 unique step refs (step-1, step-2), got %d: %v", len(got), got)
	}
	if _, ok := got["step-1"]; !ok {
		t.Errorf("expected step-1 in extracted refs, got %v", got)
	}
	if _, ok := got["step-2"]; !ok {
		t.Errorf("expected step-2 in extracted refs, got %v", got)
	}
}

func TestParamsContainUnresolvedFrameworkMacro(t *testing.T) {
	// Drives RC5's semantic-fallback gate. Must return true ONLY for
	// framework-form {{step-...}} tokens — tool-specific syntax must not
	// trigger the LLM-resolver code path.
	cases := []struct {
		name string
		in   interface{}
		want bool
	}{
		{"plain string", "no curlies here", false},
		{"framework token", map[string]interface{}{"k": "{{step-1.response.data.x}}"}, true},
		{"tool-specific token", map[string]interface{}{"start": "{{now-7d}}"}, false},
		{"mixed", map[string]interface{}{"a": "{{now}}", "b": "{{step-2.response.data.y}}"}, true},
		{"nested array", map[string]interface{}{"items": []interface{}{
			map[string]interface{}{"v": "{{step-3.response.data.z}}"},
		}}, true},
		{"nil input", nil, false},
		{"empty map", map[string]interface{}{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paramsContainUnresolvedFrameworkMacro(tc.in)
			if got != tc.want {
				t.Errorf("paramsContainUnresolvedFrameworkMacro(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ─── RC3: branches not covered by the existing assertions ────────────────────

func TestValidateDependencyConsistency_NilPlan(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)
	if err := o.validateDependencyConsistency(nil); err != nil {
		t.Errorf("expected nil plan to pass, got: %v", err)
	}
}

func TestValidateDependencyConsistency_InPlanRefWithDependsOn_Accepts(t *testing.T) {
	// Happy path: step-2 references step-1 via template AND lists step-1 in
	// depends_on. RC3 must accept. Exercises both the declaredDepends loop
	// and the in-plan happy-path `continue` branch.
	o := newOrchestratorWithCatalog(t, nil)
	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "geo"},
			{
				StepID:    "step-2",
				AgentName: "weather",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"parameters": map[string]interface{}{
						"lat": "{{step-1.response.data.lat}}",
					},
				},
			},
		},
	}
	if err := o.validateDependencyConsistency(plan); err != nil {
		t.Errorf("expected acceptance for in-plan ref correctly listed in depends_on, got: %v", err)
	}
}

// ─── RC4: covers the ImplicitDeps union path + the logger INFO branch ───────

func TestExecuteStep_SkipsOnImplicitDepsFailed(t *testing.T) {
	// step.ImplicitDeps lists step-1 (without a template referencing it in
	// parameters — defense-in-depth case). step-1 failed. The sweep must
	// still skip step-3 with blocking_reason=template_induced. This exercises
	// the executor.go:1039-1041 ImplicitDeps loop in skipStepsWithFailedDeps.
	executor := newSkipTestExecutor()
	executor.SetLogger(&loggerRecorder{}) // also exercises the logger != nil INFO branch

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "a"},
			{
				StepID:       "step-3",
				AgentName:    "b",
				ImplicitDeps: []string{"step-1"},
				Metadata: map[string]interface{}{
					"capability": "noop",
					// parameters do NOT reference step-1 — only ImplicitDeps does
					"parameters": map[string]interface{}{"k": "v"},
				},
			},
		},
	}
	executed := map[string]bool{"step-1": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "boom"},
	}
	result := &ExecutionResult{}

	n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-implicit")
	if n != 1 {
		t.Fatalf("expected 1 skip via ImplicitDeps, got %d", n)
	}
	if got := stepResults["step-3"]; got == nil || got.Success {
		t.Fatalf("expected step-3 marked skipped via ImplicitDeps, got %+v", got)
	}
	if !strings.Contains(stepResults["step-3"].Error, "step-1") {
		t.Errorf("expected skip error to name step-1, got: %s", stepResults["step-3"].Error)
	}
}

// ─── Last-mile coverage of small observability branches ─────────────────────

func TestExecuteStep_SkipFiresContextStepCallback(t *testing.T) {
	// Coverage for executor.go:1121-1123 — skipped steps must invoke the
	// context-attached step callback so UIs that subscribe via WithStepCallback
	// see skip events alongside successful executions.
	executor := newSkipTestExecutor()

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "a"},
			{
				StepID:    "step-2",
				AgentName: "b",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"capability": "noop",
				},
			},
		},
	}
	executed := map[string]bool{"step-1": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "boom"},
	}
	result := &ExecutionResult{}

	called := false
	cb := func(_, _ int, step RoutingStep, _ StepResult) {
		if step.StepID == "step-2" {
			called = true
		}
	}
	ctx := WithStepCallback(context.Background(), cb)

	if n := executor.skipStepsWithFailedDeps(ctx, plan, executed, stepResults, result, "req-cb"); n != 1 {
		t.Fatalf("expected 1 skip, got %d", n)
	}
	if !called {
		t.Errorf("expected context step callback to fire for skipped step-2")
	}
}

// recordingTelemetry is a minimal core.Telemetry stand-in that captures the
// metric names emitted by the code under test. Used to cover BuildSystemPrompt's
// `if d.telemetry != nil` branch without depending on a real telemetry backend.
type recordingTelemetry struct {
	mu      sync.Mutex
	metrics []string
}

func (r *recordingTelemetry) RecordMetric(name string, _ float64, _ map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, name)
}
func (r *recordingTelemetry) StartSpan(ctx context.Context, _ string) (context.Context, core.Span) {
	return ctx, &recordingSpan{}
}

type recordingSpan struct{}

func (recordingSpan) End()                             {}
func (recordingSpan) SetAttribute(string, interface{}) {}
func (recordingSpan) RecordError(error)                {}

// nonSystemPromptBuilder is a stand-in PromptBuilder that intentionally does
// NOT implement SystemPromptBuilder. Used to exercise the orchestrator's
// fallback paths in buildSystemPrompt (which only delegates when the wired
// builder satisfies the SystemPromptBuilder interface).
type nonSystemPromptBuilder struct{}

func (nonSystemPromptBuilder) BuildPlanningPrompt(_ context.Context, _ PromptInput) (string, error) {
	return "", nil
}
func (nonSystemPromptBuilder) SetLogger(core.Logger)       {}
func (nonSystemPromptBuilder) SetTelemetry(core.Telemetry) {}

func TestOrchestratorBuildSystemPrompt_FallbackCarriesRuntimeContext(t *testing.T) {
	// ORCH-020 RC7 universality: every fallback path in
	// AIOrchestrator.buildSystemPrompt must emit <runtime_context>, not just
	// the SystemPromptBuilder delegation path. Without this, an orchestrator
	// constructed without a SystemPromptBuilder-capable PromptBuilder (or
	// none at all) would receive a date-less system prompt and the planner
	// would once again be tempted to invent {{today_plus_1}}-style macros.
	cases := []struct {
		name            string
		setup           func(o *AIOrchestrator)
		wantContainsAll []string
		wantNotContains []string
	}{
		{
			name:  "no PromptBuilder wired, no SystemInstructions configured",
			setup: func(o *AIOrchestrator) { /* zero-value */ },
			wantContainsAll: []string{
				"intelligent orchestrator that creates execution plans",
				"<runtime_context>",
				"Current date (UTC):",
			},
		},
		{
			name: "no PromptBuilder wired, SystemInstructions configured",
			setup: func(o *AIOrchestrator) {
				o.config = &OrchestratorConfig{
					PromptConfig: PromptConfig{SystemInstructions: "You are a travel-planning specialist."},
				}
			},
			wantContainsAll: []string{
				"You are a travel-planning specialist.",
				"As an AI orchestrator",
				"<runtime_context>",
			},
		},
		{
			name: "PromptBuilder wired but does not implement SystemPromptBuilder",
			setup: func(o *AIOrchestrator) {
				o.promptBuilder = nonSystemPromptBuilder{}
			},
			wantContainsAll: []string{
				"intelligent orchestrator that creates execution plans",
				"<runtime_context>",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &AIOrchestrator{config: &OrchestratorConfig{}}
			tc.setup(o)
			got := o.buildSystemPrompt(context.Background(), "request")
			for _, want := range tc.wantContainsAll {
				if !strings.Contains(got, want) {
					t.Errorf("expected system prompt to contain %q, got %q", want, got)
				}
			}
			for _, unwant := range tc.wantNotContains {
				if strings.Contains(got, unwant) {
					t.Errorf("expected system prompt NOT to contain %q, got %q", unwant, got)
				}
			}
		})
	}
}

func TestTemplatePromptBuilder_NilFallback_CarriesRuntimeContext(t *testing.T) {
	// ORCH-020 RC7 universality: TemplatePromptBuilder constructed with no
	// fallback DefaultPromptBuilder must still emit <runtime_context> from
	// its nil-safe default branch. Without this, agents that wire a template
	// builder without supplying a fallback would lose date injection.
	t2 := &TemplatePromptBuilder{}
	got := t2.BuildSystemPrompt(context.Background(), PromptInput{})
	if !strings.Contains(got, "<runtime_context>") {
		t.Errorf("TemplatePromptBuilder nil-fallback must emit <runtime_context>, got %q", got)
	}
	if !strings.Contains(got, "Current date (UTC):") {
		t.Errorf("expected current UTC date hint in nil-fallback output, got %q", got)
	}
}

func TestTemplatePromptBuilder_WithFallback_DelegatesAndCarriesRuntimeContext(t *testing.T) {
	// When a fallback DefaultPromptBuilder is wired, TemplatePromptBuilder
	// delegates to it. The fallback's BuildSystemPrompt already calls
	// appendRuntimeContext, so the result must still carry the date —
	// covers the delegation branch (`if t.fallback != nil { return ... }`).
	fallback, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t2 := &TemplatePromptBuilder{fallback: fallback}
	got := t2.BuildSystemPrompt(context.Background(), PromptInput{})
	if !strings.Contains(got, "<runtime_context>") {
		t.Errorf("delegated TemplatePromptBuilder output must carry <runtime_context>, got %q", got)
	}
}

func TestBuildSystemPrompt_EmitsTelemetryWhenWired(t *testing.T) {
	// Coverage for default_prompt_builder.go:455-458 — RecordMetric is the
	// only side-effect tracked from the system-prompt build, so it's worth
	// asserting it actually fires when telemetry is wired.
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	telem := &recordingTelemetry{}
	builder.SetTelemetry(telem)

	_ = builder.BuildSystemPrompt(context.Background(), PromptInput{})

	telem.mu.Lock()
	defer telem.mu.Unlock()
	found := false
	for _, name := range telem.metrics {
		if name == "orchestrator.prompt.system_prompt_built" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected orchestrator.prompt.system_prompt_built metric, got %v", telem.metrics)
	}
}

func TestValidateTemplatePaths_AgentMissingFromCatalogIsCarvedOut(t *testing.T) {
	// Coverage for orchestrator.go:5846 — the `if agentInfo == nil { continue }`
	// branch covers the documented edge case where a prior-phase step exists
	// in executedStepCaps but its agent is no longer in the catalog (e.g.
	// because the prior-phase step failed at agent discovery, leaving
	// Capability set but the agent unreachable now). RC1 must not crash and
	// must not erroneously reject the field-existence check; RC6 will catch
	// the literal at dispatch if it survives to that point.
	o := newOrchestratorWithCatalog(t, nil) // empty catalog → no agents resolvable
	executed := map[string]stepCapability{
		"step-1": {agent: "ghost-tool", capability: "vanished"},
	}
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:       "step-2",
			AgentName:    "real-tool",
			ImplicitDeps: []string{"step-1"},
			Metadata: map[string]interface{}{
				"capability": "do_thing",
				"parameters": map[string]interface{}{
					"x": "{{step-1.response.data.value}}",
				},
			},
		}},
	}
	if err := o.validateTemplatePaths(plan, executed); err != nil {
		t.Errorf("agent-missing-from-catalog carve-out should pass without erroring, got: %v", err)
	}
}

// loggerRecorder is a minimal core.Logger stand-in that satisfies the interface
// without requiring a real logging backend. Used so the skip path can exercise
// the `if e.logger != nil` INFO branch under test.
type loggerRecorder struct{}

func (loggerRecorder) Debug(msg string, fields map[string]interface{}) {}
func (loggerRecorder) Info(msg string, fields map[string]interface{})  {}
func (loggerRecorder) Warn(msg string, fields map[string]interface{})  {}
func (loggerRecorder) Error(msg string, fields map[string]interface{}) {}
func (loggerRecorder) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (loggerRecorder) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (loggerRecorder) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (loggerRecorder) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}

// ─── RC3: validateDependencyConsistency ──────────────────────────────────────

func TestValidatePlan_TemplateMustAppearInDependsOn(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)

	// step-2 references step-1 via template but omits it from depends_on.
	// Both steps are in the same plan (in-plan reference), so RC3 must
	// require depends_on — not implicit_deps.
	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "geo", Metadata: map[string]interface{}{"capability": "geocode"}},
			{
				StepID:    "step-2",
				AgentName: "weather",
				DependsOn: nil,
				Metadata: map[string]interface{}{
					"capability": "get_weather",
					"parameters": map[string]interface{}{
						"lat": "{{step-1.response.data.lat}}",
					},
				},
			},
		},
	}

	err := o.validateDependencyConsistency(plan)
	if err == nil {
		t.Fatal("expected RC3 to reject in-plan reference missing from depends_on")
	}
	if !strings.Contains(err.Error(), "step-1") || !strings.Contains(err.Error(), "depends_on") {
		t.Errorf("expected error to explain missing depends_on entry for step-1, got: %v", err)
	}
}

func TestValidatePlan_TemplateMustAppearInDependsOn_Nested(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "a"},
			{
				StepID:    "step-2",
				AgentName: "b",
				Metadata: map[string]interface{}{
					"parameters": map[string]interface{}{
						"payload": map[string]interface{}{
							"body": []interface{}{
								map[string]interface{}{
									"id": "{{step-1.response.data.id}}",
								},
							},
						},
					},
				},
			},
		},
	}

	if err := o.validateDependencyConsistency(plan); err == nil {
		t.Fatal("expected RC3 to flag nested in-plan reference missing from depends_on")
	}
}

func TestValidatePlan_CrossPhaseNeedsImplicitDeps(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)

	// step-3 references prior-phase step-1 (not in current plan.Steps).
	// depends_on is correctly empty (same-phase only), but implicit_deps is
	// missing → RC3 rejects.
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:    "step-3",
			AgentName: "flight-tool",
			DependsOn: nil,
			Metadata: map[string]interface{}{
				"capability": "search_flights",
				"parameters": map[string]interface{}{
					"origin": "{{step-1.response.data.iata_code}}",
				},
			},
		}},
	}

	err := o.validateDependencyConsistency(plan)
	if err == nil {
		t.Fatal("expected RC3 to require implicit_deps for cross-phase reference")
	}
	if !strings.Contains(err.Error(), "implicit_deps") {
		t.Errorf("expected error to name implicit_deps, got: %v", err)
	}
}

func TestValidatePlan_CrossPhaseAcceptsWithImplicitDeps(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)

	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:       "step-3",
			AgentName:    "flight-tool",
			ImplicitDeps: []string{"step-1"},
			Metadata: map[string]interface{}{
				"capability": "search_flights",
				"parameters": map[string]interface{}{
					"origin": "{{step-1.response.data.iata_code}}",
				},
			},
		}},
	}

	if err := o.validateDependencyConsistency(plan); err != nil {
		t.Errorf("expected cross-phase ref with implicit_deps to pass, got: %v", err)
	}
}

func TestValidatePlan_SelfReferenceRejected(t *testing.T) {
	o := newOrchestratorWithCatalog(t, nil)

	// A step referencing its own output is a structural defect that cannot be
	// fixed by declaration consistency — RC3 surfaces it explicitly.
	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID: "step-1",
			Metadata: map[string]interface{}{
				"parameters": map[string]interface{}{
					"loop": "{{step-1.response.data.value}}",
				},
			},
		}},
	}
	err := o.validateDependencyConsistency(plan)
	if err == nil {
		t.Fatal("expected self-reference to be rejected")
	}
	if !strings.Contains(err.Error(), "self-reference") {
		t.Errorf("expected self-reference wording in error, got: %v", err)
	}
}

// ─── RC4: executor skip on failed deps (explicit and template-induced) ───────

func TestExecuteStep_SkipsStampsAllFailedDepsOnMetadata(t *testing.T) {
	// RC4 regression (production scenario orch-1776802262936110748): when a
	// single step templates N failed upstreams, the skip must stamp ALL N
	// on Metadata[all_failed_dependencies] — not just the first. Without
	// this, RC9's pattern analyzer sees only one causal failure and the
	// pattern line is never emitted.
	executor := newSkipTestExecutor()

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "a"},
			{StepID: "step-2", AgentName: "b"},
			{
				StepID:       "step-3",
				AgentName:    "flight-tool",
				DependsOn:    nil,
				ImplicitDeps: []string{"step-1", "step-2"},
				Metadata: map[string]interface{}{
					"capability": "search_flights",
					"parameters": map[string]interface{}{
						"origin":      "{{step-1.response.data.iataCode}}",
						"destination": "{{step-2.response.data.iataCode}}",
					},
				},
			},
		},
	}
	executed := map[string]bool{"step-1": true, "step-2": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "Amadeus API error 500"},
		"step-2": {StepID: "step-2", Success: false, Error: "Amadeus API error 500"},
	}
	result := &ExecutionResult{}

	if n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-multidep"); n != 1 {
		t.Fatalf("expected 1 skip for the one step-3, got %d", n)
	}
	md := stepResults["step-3"].Metadata
	if md == nil {
		t.Fatalf("expected metadata stamped on skipped step-3, got nil")
	}
	all, ok := md[metaKeyAllFailedDependencies].([]string)
	if !ok {
		t.Fatalf("Metadata[%q] wrong type or missing: %T %v", metaKeyAllFailedDependencies, md[metaKeyAllFailedDependencies], md[metaKeyAllFailedDependencies])
	}
	// Dedupe + order-agnostic check: must contain both step-1 and step-2.
	seen := make(map[string]bool)
	for _, d := range all {
		seen[d] = true
	}
	if !seen["step-1"] || !seen["step-2"] || len(all) != 2 {
		t.Errorf("Metadata[%q] = %v, want exactly {step-1, step-2}", metaKeyAllFailedDependencies, all)
	}
	// Singular field remains populated for back-compat + operator readability.
	if dep, _ := md[metaKeyFailedDependency].(string); dep != "step-1" && dep != "step-2" {
		t.Errorf("Metadata[%q] = %q, want one of {step-1, step-2}", metaKeyFailedDependency, dep)
	}
}

func TestExecuteStep_SkipMetadataAllFailedDepsIsSortedDeterministic(t *testing.T) {
	// Template references go through a map (templateRefSet) whose iteration
	// order is randomized in Go. Without an explicit sort, allFailedDeps —
	// and therefore the bullet list in the remediation note + the
	// "additional failed upstreams" enumeration in the skip error — would
	// vary between runs. Sort makes both deterministic for prompt + log
	// stability.
	executor := newSkipTestExecutor()
	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-z", AgentName: "a"},
			{StepID: "step-a", AgentName: "b"},
			{StepID: "step-m", AgentName: "c"},
			{
				StepID:       "step-target",
				AgentName:    "consumer",
				ImplicitDeps: []string{"step-z", "step-a", "step-m"},
				Metadata: map[string]interface{}{
					"capability": "do_thing",
					"parameters": map[string]interface{}{
						"x": "{{step-z.response.data.x}}",
						"y": "{{step-a.response.data.y}}",
						"z": "{{step-m.response.data.z}}",
					},
				},
			},
		},
	}
	executed := map[string]bool{"step-z": true, "step-a": true, "step-m": true}
	stepResults := map[string]*StepResult{
		"step-z": {StepID: "step-z", Success: false, Error: "z err"},
		"step-a": {StepID: "step-a", Success: false, Error: "a err"},
		"step-m": {StepID: "step-m", Success: false, Error: "m err"},
	}
	result := &ExecutionResult{}
	if n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-sort"); n != 1 {
		t.Fatalf("expected 1 skip, got %d", n)
	}
	all, _ := stepResults["step-target"].Metadata[metaKeyAllFailedDependencies].([]string)
	want := []string{"step-a", "step-m", "step-z"}
	if len(all) != len(want) {
		t.Fatalf("expected %v, got %v", want, all)
	}
	for i, w := range want {
		if all[i] != w {
			t.Errorf("position %d: got %q, want %q (full slice: %v)", i, all[i], w, all)
		}
	}
}

func TestExecuteStep_SkipDedupeOnDuplicateDepsOnlyRecordsOnce(t *testing.T) {
	// Defense: if DependsOn contains the same dep twice (LLM
	// hallucination), the skip's all_failed_dependencies must still list
	// that dep exactly once. Exercises the seenFailed dedupe guard in
	// recordFailed.
	executor := newSkipTestExecutor()
	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "a"},
			{
				StepID:    "step-2",
				AgentName: "b",
				DependsOn: []string{"step-1", "step-1"}, // duplicate
			},
		},
	}
	executed := map[string]bool{"step-1": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "boom"},
	}
	result := &ExecutionResult{}
	if n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-dup"); n != 1 {
		t.Fatalf("expected 1 skip, got %d", n)
	}
	all, _ := stepResults["step-2"].Metadata[metaKeyAllFailedDependencies].([]string)
	if len(all) != 1 || all[0] != "step-1" {
		t.Errorf("duplicate DependsOn must produce single-entry all_failed_dependencies, got %v", all)
	}
}

func TestCollectTemplateInducedSkips_ReadsAllFailedDependenciesFromMetadata(t *testing.T) {
	// collectTemplateInducedSkips must surface the plural metadata on
	// TemplateInducedSkip.FailedDeps so the RC9 analyzer can expand its
	// causal window. Accept both []string (production) and []interface{}
	// (JSON-unmarshalled) shapes.
	phaseSteps := []StepResult{{
		StepID:     "step-3",
		AgentName:  "flight-tool",
		Capability: "search_flights",
		Success:    false,
		Error:      "skipped due to failed template dependency: step-1 (...)",
		Metadata: map[string]interface{}{
			metaKeyBlockingReason:        blockingReasonTemplate,
			metaKeyFailedDependency:      "step-1",
			metaKeyAllFailedDependencies: []string{"step-1", "step-2"},
		},
	}}
	got := collectTemplateInducedSkips(phaseSteps, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(got))
	}
	if len(got[0].FailedDeps) != 2 || got[0].FailedDeps[0] != "step-1" || got[0].FailedDeps[1] != "step-2" {
		t.Errorf("FailedDeps = %v, want [step-1 step-2]", got[0].FailedDeps)
	}

	// JSON-unmarshal path: []interface{} should be accepted too.
	phaseSteps[0].Metadata[metaKeyAllFailedDependencies] = []interface{}{"step-1", "step-2"}
	got = collectTemplateInducedSkips(phaseSteps, nil)
	if len(got[0].FailedDeps) != 2 {
		t.Errorf("JSON []interface{} path must produce 2 deps, got %v", got[0].FailedDeps)
	}

	// Legacy-singular fallback: when plural is absent, FailedDeps
	// synthesizes a 1-element slice from FailedDep.
	delete(phaseSteps[0].Metadata, metaKeyAllFailedDependencies)
	got = collectTemplateInducedSkips(phaseSteps, nil)
	if len(got[0].FailedDeps) != 1 || got[0].FailedDeps[0] != "step-1" {
		t.Errorf("legacy fallback FailedDeps = %v, want [step-1]", got[0].FailedDeps)
	}
}

func TestExecuteStep_SkipsOnTemplateInducedFailedDep(t *testing.T) {
	// step-3 has depends_on: [] but references {{step-1.response.data.iata_code}}
	// in its parameters. step-1 already failed. The RC4 sweep must mark step-3
	// as skipped with blocking_reason=template_induced — previously step-3
	// would pass findReadySteps and be dispatched with a literal template.
	executor := newSkipTestExecutor()

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "geo"},
			{
				StepID:    "step-3",
				AgentName: "flight-tool",
				DependsOn: nil,
				Metadata: map[string]interface{}{
					"capability": "search_flights",
					"parameters": map[string]interface{}{
						"origin": "{{step-1.response.data.iata_code}}",
					},
				},
			},
		},
	}
	executed := map[string]bool{"step-1": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "upstream boom"},
	}
	result := &ExecutionResult{}

	n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-1")
	if n != 1 {
		t.Fatalf("expected 1 skip, got %d", n)
	}
	if got := stepResults["step-3"]; got == nil || got.Success {
		t.Fatalf("expected step-3 marked skipped, got %+v", got)
	}
	if !strings.Contains(stepResults["step-3"].Error, "step-1") {
		t.Errorf("expected skip error to name step-1, got: %s", stepResults["step-3"].Error)
	}
	if !strings.Contains(stepResults["step-3"].Error, "template") {
		t.Errorf("expected skip error to note template-induced cause, got: %s", stepResults["step-3"].Error)
	}
	if !executed["step-3"] {
		t.Errorf("step-3 should be marked executed after skip")
	}
	// RC8 contract: structured metadata must be stamped on the skipped
	// StepResult so remediation detection is not string-coupled.
	md := stepResults["step-3"].Metadata
	if reason, _ := md[metaKeyBlockingReason].(string); reason != blockingReasonTemplate {
		t.Errorf("expected Metadata[%q] = %q, got %q", metaKeyBlockingReason, blockingReasonTemplate, reason)
	}
	if dep, _ := md[metaKeyFailedDependency].(string); dep != "step-1" {
		t.Errorf("expected Metadata[%q] = %q, got %q", metaKeyFailedDependency, "step-1", dep)
	}
}

func TestExecuteStep_ExplicitFailedDepTakesPrecedence(t *testing.T) {
	// When a step both explicitly depends on and template-references the same
	// failed step, the blocking_reason reports "explicit_dep" — the more
	// actionable classification for operators.
	executor := newSkipTestExecutor()

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "geo"},
			{
				StepID:    "step-2",
				AgentName: "weather",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"parameters": map[string]interface{}{
						"lat": "{{step-1.response.data.lat}}",
					},
				},
			},
		},
	}
	executed := map[string]bool{"step-1": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "boom"},
	}
	result := &ExecutionResult{}

	n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-2")
	if n != 1 {
		t.Fatalf("expected 1 skip, got %d", n)
	}
	// When both sources flag the same failure, the error text uses the
	// explicit-dep phrasing (no "template" note) so operators see the
	// underlying cause, not the template shadow.
	if strings.Contains(stepResults["step-2"].Error, "template") {
		t.Errorf("explicit-dep failure should not be reported as template-induced: %s", stepResults["step-2"].Error)
	}
	// Structured metadata must reflect the same precedence — explicit_dep,
	// not template_induced. This is what RC8's metadata-primary detection
	// reads.
	md := stepResults["step-2"].Metadata
	if reason, _ := md[metaKeyBlockingReason].(string); reason != blockingReasonExplicit {
		t.Errorf("expected Metadata[%q] = %q, got %q", metaKeyBlockingReason, blockingReasonExplicit, reason)
	}
}

func TestExecuteStep_SkipsNothingWhenDepsSucceed(t *testing.T) {
	executor := newSkipTestExecutor()
	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{StepID: "step-1", AgentName: "a"},
			{
				StepID:    "step-2",
				AgentName: "b",
				DependsOn: []string{"step-1"},
				Metadata: map[string]interface{}{
					"parameters": map[string]interface{}{
						"in": "{{step-1.response.data.value}}",
					},
				},
			},
		},
	}
	executed := map[string]bool{"step-1": true}
	stepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: true},
	}
	result := &ExecutionResult{}

	if n := executor.skipStepsWithFailedDeps(context.Background(), plan, executed, stepResults, result, "req-3"); n != 0 {
		t.Fatalf("expected 0 skips when deps succeeded, got %d", n)
	}
}

// ─── RC5: un-gate template interpolation ─────────────────────────────────────

func TestInterpolateParameters_RunsWithEmptyDeps(t *testing.T) {
	// Proves the interpolator is callable with an empty depResults map and
	// leaves unresolved templates intact (the RC6 guard then catches them).
	// Previously the call site short-circuited on len(depResults) == 0,
	// hiding this path from telemetry.
	executor := NewSmartExecutor(&AgentCatalog{agents: map[string]*AgentInfo{}})

	params := map[string]interface{}{
		"origin":      "{{step-1.response.data.iata_code}}",
		"destination": "LHR", // literal passthrough
	}
	empty := map[string]map[string]interface{}{}

	out := executor.interpolateParameters(params, empty)
	if out == nil {
		t.Fatal("expected interpolated map, got nil")
	}
	if got, _ := out["destination"].(string); got != "LHR" {
		t.Errorf("literal value should pass through, got %q", got)
	}
	if got, _ := out["origin"].(string); got != "{{step-1.response.data.iata_code}}" {
		t.Errorf("unresolved template should survive empty-dep interpolation so RC6 can catch it, got %q", got)
	}
}

// ─── RC6: pre-dispatch guard ─────────────────────────────────────────────────

func TestExecuteStep_GuardRejectsLiteralTemplateAtDispatch(t *testing.T) {
	// End-to-end proof: an agent that IS in the catalog but whose step
	// parameters still contain {{…}} should be refused before any HTTP call.
	// The mock RoundTripper tracks call count — it must stay at zero.
	mockRT := NewMockRoundTripper()
	// Pre-seed a response so any stray call would "succeed" — makes the test
	// unambiguous: if the counter is zero, the guard did its job.
	mockRT.SetResponse("http://localhost:8080/api/search", http.StatusOK, `{"ok":true}`)

	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"flight-tool": {
				Registration: &core.ServiceRegistration{
					ID: "flight-tool", Name: "flight-tool", Address: "localhost", Port: 8080,
				},
				Capabilities: []EnhancedCapability{
					{Name: "search", Endpoint: "/api/search"},
				},
			},
		},
	}
	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(1)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "step-3",
		AgentName: "flight-tool",
		Metadata: map[string]interface{}{
			"capability": "search",
			"parameters": map[string]interface{}{
				"origin": "{{step-1.response.data.iata_code}}",
			},
		},
	}

	res := executor.executeStep(context.Background(), step)
	if res.Success {
		t.Fatalf("guard should have refused dispatch, got success")
	}
	if !strings.Contains(res.Error, "unresolved framework template") {
		t.Errorf("expected error to mention unresolved framework template, got: %s", res.Error)
	}
	if !strings.Contains(res.Error, "step-1") {
		t.Errorf("expected error to cite the unresolved token's step id, got: %s", res.Error)
	}
	if mockRT.GetCallCount() != 0 {
		t.Errorf("guard must block dispatch (HTTP call count = %d, want 0)", mockRT.GetCallCount())
	}
}

// recordingInterruptController is a test double that captures the parameters
// the orchestrator shows to HITL for approval. Always returns a checkpoint so
// the executor pauses on it — the test then inspects what was passed.
type recordingInterruptController struct {
	observedParams map[string]interface{}
	observedStepID string
}

func (c *recordingInterruptController) SetPolicy(InterruptPolicy)          {}
func (c *recordingInterruptController) SetHandler(InterruptHandler)        {}
func (c *recordingInterruptController) SetCheckpointStore(CheckpointStore) {}
func (c *recordingInterruptController) CheckPlanApproval(context.Context, *RoutingPlan) (*ExecutionCheckpoint, error) {
	return nil, nil
}
func (c *recordingInterruptController) CheckBeforeStep(ctx context.Context, step RoutingStep, _ *RoutingPlan) (*ExecutionCheckpoint, error) {
	c.observedStepID = step.StepID
	if p := GetResolvedParams(ctx); p != nil {
		c.observedParams = p
	}
	return &ExecutionCheckpoint{CheckpointID: "hitl-test"}, nil
}
func (c *recordingInterruptController) CheckAfterStep(context.Context, RoutingStep, *StepResult) (*ExecutionCheckpoint, error) {
	return nil, nil
}
func (c *recordingInterruptController) CheckOnError(context.Context, RoutingStep, error, int) (*ExecutionCheckpoint, error) {
	return nil, nil
}
func (c *recordingInterruptController) ProcessCommand(context.Context, *Command) (*ResumeResult, error) {
	return nil, nil
}
func (c *recordingInterruptController) ResumeExecution(context.Context, string) (*ExecutionResult, error) {
	return nil, nil
}
func (c *recordingInterruptController) UpdateCheckpointProgress(context.Context, string, []StepResult) error {
	return nil
}

func TestExecuteStep_HITLSeesUnresolvedParamsBeforeGuard(t *testing.T) {
	// ORCH-020 RC6 is positioned AFTER the HITL pre-step approval so a human
	// reviewer gets the chance to correct unresolved parameters manually. If
	// RC6 ran before HITL (as in the first implementation), HITL-enabled
	// workflows would hard-fail on exactly the parameters the human is
	// supposed to approve/fix.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"flight-tool": {
				Registration: &core.ServiceRegistration{
					ID: "flight-tool", Name: "flight-tool", Address: "localhost", Port: 8080,
				},
				Capabilities: []EnhancedCapability{
					{Name: "search", Endpoint: "/api/search"},
				},
			},
		},
	}
	controller := &recordingInterruptController{}
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/search", http.StatusOK, `{"ok":true}`)
	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(1)
	executor.interruptController = controller
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "step-3",
		AgentName: "flight-tool",
		Metadata: map[string]interface{}{
			"capability": "search",
			"parameters": map[string]interface{}{
				"origin": "{{step-1.response.data.iata_code}}",
			},
		},
	}

	res := executor.executeStep(context.Background(), step)

	// Expect: HITL was called and observed the still-unresolved params.
	if controller.observedStepID != "step-3" {
		t.Errorf("HITL should have received the step for approval, got observedStepID=%q", controller.observedStepID)
	}
	if got, _ := controller.observedParams["origin"].(string); got != "{{step-1.response.data.iata_code}}" {
		t.Errorf("HITL must see unresolved params so the human can correct them, got origin=%q", got)
	}
	// The step returns as interrupted — not as the RC6 "unresolved framework template" error.
	if strings.Contains(res.Error, "unresolved framework template") {
		t.Errorf("RC6 must run AFTER HITL, not before — got RC6 error: %s", res.Error)
	}
	// HTTP was not called either (the interrupt returned before dispatch).
	if mockRT.GetCallCount() != 0 {
		t.Errorf("HITL interrupt must return before any HTTP call, got count=%d", mockRT.GetCallCount())
	}
}

func TestExecuteStep_GuardPassesThroughToolSpecificSyntax(t *testing.T) {
	// Regression: tool-specific {{now}} / {{now-7d}} syntax used by
	// examples/prometheus-query-tool MUST reach the tool. RC6 is scoped to
	// framework-form {{step-...}} tokens only — anything else passes through
	// so the tool can handle its own template contract.
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:9090/api/query_range", http.StatusOK, `{"ok":true}`)

	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"prometheus-tool": {
				Registration: &core.ServiceRegistration{
					ID: "prometheus-tool", Name: "prometheus-tool", Address: "localhost", Port: 9090,
				},
				Capabilities: []EnhancedCapability{
					{Name: "query_range", Endpoint: "/api/query_range"},
				},
			},
		},
	}
	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(1)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID:    "step-1",
		AgentName: "prometheus-tool",
		Metadata: map[string]interface{}{
			"capability": "query_range",
			"parameters": map[string]interface{}{
				"query": "up{job=\"api\"}",
				"start": "{{now-7d}}",
				"end":   "{{now}}",
			},
		},
	}

	res := executor.executeStep(context.Background(), step)
	if !res.Success {
		t.Fatalf("tool-specific {{now}}/{{now-7d}} must pass RC6 and reach the tool, got error: %s", res.Error)
	}
	if mockRT.GetCallCount() != 1 {
		t.Errorf("expected exactly 1 HTTP call to the tool, got %d", mockRT.GetCallCount())
	}
}

// ─── RC8: Remediation on template-induced skips ─────────────────────────────

func TestCollectTemplateInducedSkips_MetadataPrimaryPath(t *testing.T) {
	// Primary detection path: RC4 stamps structured metadata on the
	// skipped StepResult (blocking_reason + failed_dependency). RC8 reads
	// those directly — no error-string parsing. This is what production
	// traffic exercises.
	phaseSteps := []StepResult{
		{
			StepID:     "step-3",
			AgentName:  "flight-tool",
			Capability: "search_flights",
			Success:    false,
			Error:      "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed)",
			Metadata: map[string]interface{}{
				metaKeyBlockingReason:   blockingReasonTemplate,
				metaKeyFailedDependency: "step-1",
			},
		},
		{
			// Unrelated successful step — must not appear in the result.
			StepID: "step-4", Success: true,
		},
		{
			// Explicit-dep failure — different blocking_reason, must not appear.
			StepID:  "step-5",
			Success: false,
			Error:   "skipped due to failed dependency: step-2",
			Metadata: map[string]interface{}{
				metaKeyBlockingReason:   blockingReasonExplicit,
				metaKeyFailedDependency: "step-2",
			},
		},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "Amadeus API error 500"},
	}

	got := collectTemplateInducedSkips(phaseSteps, prior)
	if len(got) != 1 {
		t.Fatalf("expected 1 template-induced skip, got %d: %+v", len(got), got)
	}
	if got[0].StepID != "step-3" {
		t.Errorf("StepID = %q, want step-3", got[0].StepID)
	}
	if got[0].FailedDep != "step-1" {
		t.Errorf("FailedDep = %q, want step-1", got[0].FailedDep)
	}
	if got[0].FailedDepError != "Amadeus API error 500" {
		t.Errorf("FailedDepError = %q, want 'Amadeus API error 500'", got[0].FailedDepError)
	}
	if got[0].Capability != "search_flights" {
		t.Errorf("Capability = %q, want search_flights", got[0].Capability)
	}
}

func TestCollectTemplateInducedSkips_LegacyStringFallback(t *testing.T) {
	// Fallback path: StepResult has no structured metadata but the error
	// string matches the legacy prefix. Covers replays of pre-structured-
	// metadata traffic and hand-constructed StepResults (e.g. in tests /
	// tools). Fallback parsing extracts the dep id from the whitespace-
	// delimited tail.
	phaseSteps := []StepResult{{
		StepID:  "step-3",
		Success: false,
		Error:   "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed)",
		// Metadata intentionally absent — force the fallback path.
	}}
	got := collectTemplateInducedSkips(phaseSteps, nil)
	if len(got) != 1 {
		t.Fatalf("legacy string-prefix fallback must still detect skips, got %d", len(got))
	}
	if got[0].FailedDep != "step-1" {
		t.Errorf("FailedDep = %q, want step-1 (via fallback parse)", got[0].FailedDep)
	}
}

func TestCollectTemplateInducedSkips_MetadataOverridesLegacyError(t *testing.T) {
	// If both metadata AND error prefix are present, metadata wins.
	// Specifically: if metadata says blocking_reason=explicit_dep, the step
	// is NOT treated as template-induced even if the error string looks
	// template-shaped (defense against stale/inconsistent producers).
	phaseSteps := []StepResult{{
		StepID:  "step-3",
		Success: false,
		Error:   "skipped due to failed template dependency: step-1 (...)",
		Metadata: map[string]interface{}{
			metaKeyBlockingReason:   blockingReasonExplicit,
			metaKeyFailedDependency: "step-1",
		},
	}}
	if got := collectTemplateInducedSkips(phaseSteps, nil); len(got) != 0 {
		t.Errorf("metadata should override legacy-string detection when reason=explicit_dep, got %+v", got)
	}
}

func TestCollectTemplateInducedSkips_EmptyWhenNoSkips(t *testing.T) {
	phaseSteps := []StepResult{
		{StepID: "step-1", Success: true},
		{StepID: "step-2", Success: false, Error: "boom"}, // non-skip failure
	}
	if got := collectTemplateInducedSkips(phaseSteps, nil); len(got) != 0 {
		t.Errorf("expected zero skips for non-RC4 failures, got %+v", got)
	}
}

func TestCollectTemplateInducedSkips_ErrorWithoutTrailingDetail(t *testing.T) {
	// Covers the fallback branch where the error message ends immediately
	// after the failed-dep id (no whitespace / trailing parenthetical).
	// Defensive behaviour: the whole tail is treated as the dep id so the
	// downstream remediation note still has something actionable.
	phaseSteps := []StepResult{{
		StepID: "step-3",
		Error:  "skipped due to failed template dependency: step-1",
	}}
	got := collectTemplateInducedSkips(phaseSteps, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(got))
	}
	if got[0].FailedDep != "step-1" {
		t.Errorf("FailedDep = %q, want step-1 (no-trailing-detail branch)", got[0].FailedDep)
	}
}

func TestCollectTemplateInducedSkips_NilPriorResults(t *testing.T) {
	// Defensive contract: FailedDepError is best-effort — absence shouldn't
	// crash, just leaves the field empty.
	phaseSteps := []StepResult{{
		StepID: "step-3",
		Error:  "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed)",
	}}
	got := collectTemplateInducedSkips(phaseSteps, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(got))
	}
	if got[0].FailedDep != "step-1" {
		t.Errorf("expected FailedDep=step-1, got %q", got[0].FailedDep)
	}
	if got[0].FailedDepError != "" {
		t.Errorf("expected empty FailedDepError when priorResults is nil, got %q", got[0].FailedDepError)
	}
}

func TestBuildRemediationContinuationNote_ContainsAllSignals(t *testing.T) {
	// The planner must see (1) what was skipped, (2) why (upstream failure),
	// (3) a directive to change approach, and (4) the two allowed remediation
	// outcomes (alternative plan OR empty-steps terminal). Phrased as
	// positive directives per EFFECTIVE_PROMPTS_GUIDE §2.4.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:         "step-3",
		Capability:     "search_flights",
		FailedDep:      "step-1",
		FailedDepError: "Amadeus API error 500: An internal error occurred",
	}}, nil, 80)

	wantContains := []string{
		"step-3",
		"search_flights",
		"step-1",
		"Amadeus API error 500",
		"materially different approach",
		"{\"terminal\": true, \"steps\": []}",
	}
	for _, want := range wantContains {
		if !strings.Contains(note, want) {
			t.Errorf("remediation note must contain %q, got: %s", want, note)
		}
	}

	// Guard against §2.4 regression: no negative directives.
	bannedTokens := []string{"Do not", "don't", "never", "must not"}
	for _, token := range bannedTokens {
		if strings.Contains(note, token) {
			t.Errorf("remediation note must avoid negative directive %q (EFFECTIVE_PROMPTS_GUIDE §2.4), got: %s", token, note)
		}
	}
}

func TestBuildRemediationContinuationNote_EmptyInput(t *testing.T) {
	if got := buildRemediationContinuationNote(nil, nil, 80); got != "" {
		t.Errorf("expected empty note for nil input, got %q", got)
	}
	if got := buildRemediationContinuationNote([]TemplateInducedSkip{}, nil, 80); got != "" {
		t.Errorf("expected empty note for empty slice, got %q", got)
	}
}

func TestBuildRemediationContinuationNote_TruncatesLongErrorBody(t *testing.T) {
	// Upstream errors can include full HTTP bodies (hundreds of chars); the
	// remediation note truncates them to keep the continuation prompt within
	// its token budget per EFFECTIVE_PROMPTS_GUIDE §4.5.
	longErr := strings.Repeat("x", 600)
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:         "step-3",
		FailedDep:      "step-1",
		FailedDepError: longErr,
	}}, nil, 80)
	if strings.Contains(note, strings.Repeat("x", 300)) {
		t.Errorf("expected error body to be truncated, note length = %d", len(note))
	}
	if !strings.Contains(note, "…") {
		t.Errorf("expected truncation marker '…' in note, got: %s", note)
	}
}

func TestBuildRemediationContinuationNote_PreservesFirstLineOnly(t *testing.T) {
	// Multi-line upstream errors should render as a single line so the
	// continuation prompt stays readable.
	multiline := "Amadeus API error 500\nline 2 noise\nline 3 stack trace"
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-3", FailedDep: "step-1", FailedDepError: multiline,
	}}, nil, 80)
	if strings.Contains(note, "line 2") {
		t.Errorf("expected only first line of upstream error in note, got: %s", note)
	}
	if !strings.Contains(note, "Amadeus API error 500") {
		t.Errorf("expected first line preserved, got: %s", note)
	}
}

func TestBuildRemediationContinuationNote_WrapsBodyInUpstreamFailuresTag(t *testing.T) {
	// EFFECTIVE_PROMPTS_GUIDE §2.10 / §8.5 check 8: every major section in a
	// prompt should be delimited by an XML tag so the model can attend to it
	// as a unit. The remediation note is a major section ("here's what
	// happened upstream and what to do") and is wrapped in <upstream_failures>.
	// Empty input still returns "" — only non-empty notes get the wrapper.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-3", FailedDep: "step-1", FailedDepError: "boom",
	}}, nil, 80)

	if !strings.HasPrefix(note, "<upstream_failures>\n") {
		t.Errorf("note must open with <upstream_failures>, got:\n%s", note)
	}
	if !strings.HasSuffix(note, "\n</upstream_failures>") {
		t.Errorf("note must close with </upstream_failures>, got:\n%s", note)
	}
	// Tag must appear exactly once each — guards against duplicate wrapping
	// in case future edits add another sb.WriteString of the open/close pair.
	if c := strings.Count(note, "<upstream_failures>"); c != 1 {
		t.Errorf("expected exactly 1 opening tag, got %d", c)
	}
	if c := strings.Count(note, "</upstream_failures>"); c != 1 {
		t.Errorf("expected exactly 1 closing tag, got %d", c)
	}

	// Empty-skip case must NOT emit the wrapper — keeps slim continuations
	// per §4.5.
	if got := buildRemediationContinuationNote(nil, nil, 80); got != "" {
		t.Errorf("nil input must return empty string (no wrapper), got %q", got)
	}
	if got := buildRemediationContinuationNote([]TemplateInducedSkip{}, nil, 80); got != "" {
		t.Errorf("empty slice must return empty string (no wrapper), got %q", got)
	}
}

// ─── Layer 3 (Cause 2b): multi-failed-dep enumeration ───────────────────────

func TestBuildRemediationContinuationNote_MultiFailedDepsEnumeratesAll(t *testing.T) {
	// Layer 3: when a single skipped step references N failed upstream deps,
	// the note must enumerate all N (not just FailedDep). Without this,
	// remediation replans drop steps that depend on the unseen failed deps —
	// the exact bug (Cause 2b) from the original orchestration trace.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:     "step-5",
		Capability: "send_slack",
		FailedDep:  "step-1",
		FailedDeps: []string{"step-1", "step-3"},
		FailedDepsErrors: map[string]string{
			"step-1": "stock-tool: concurrent map writes (panic)",
			"step-3": "news-tool: 503 service unavailable",
		},
	}}, nil, 80)

	for _, want := range []string{
		"step-5",
		"send_slack",
		"step-1",
		"step-3",
		"concurrent map writes",
		"503 service unavailable",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("multi-dep render missing %q, got:\n%s", want, note)
		}
	}
}

func TestBuildRemediationContinuationNote_SingleDepUsesSingleLineFormat(t *testing.T) {
	// Single-dep (N=1) must keep the original single-line render. This is the
	// common case (most skips reference one upstream); switching to the
	// indented multi-line format here would needlessly bloat continuation
	// prompts for the 90% case.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:     "step-3",
		Capability: "search_flights",
		FailedDep:  "step-1",
		FailedDeps: []string{"step-1"},
		FailedDepsErrors: map[string]string{
			"step-1": "Amadeus API error 500",
		},
	}}, nil, 80)

	// Single-line marker: "- step-3 ... skipped because step-1 failed: ...\n"
	// The multi-line format uses "    - " (four-space indent) on a follow-up
	// line, which must NOT appear for N=1.
	if strings.Contains(note, "    - step-1") {
		t.Errorf("N=1 must not use indented multi-line format, got:\n%s", note)
	}
	if !strings.Contains(note, "step-1 failed: Amadeus API error 500") {
		t.Errorf("N=1 must use single-line format, got:\n%s", note)
	}
}

func TestBuildRemediationContinuationNote_MultiFailedDepsWithoutErrors(t *testing.T) {
	// When per-dep errors aren't recorded (FailedDepsErrors absent), each dep
	// must still be listed by id alone — the planner needs to know the SET of
	// failed upstreams even without per-cause detail.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:     "step-5",
		FailedDep:  "step-1",
		FailedDeps: []string{"step-1", "step-3", "step-4"},
		// FailedDepsErrors deliberately nil
	}}, nil, 80)

	for _, want := range []string{"step-1", "step-3", "step-4"} {
		if !strings.Contains(note, want) {
			t.Errorf("missing failed-dep id %q in render, got:\n%s", want, note)
		}
	}
}

func TestBuildRemediationContinuationNote_LegacyFallbackFromFailedDep(t *testing.T) {
	// Legacy: hand-built test fixtures (and replayed pre-RC9 StepResults) may
	// set only FailedDep with no FailedDeps. The renderer must fall back so
	// these still produce a usable note.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:         "step-3",
		FailedDep:      "step-1",
		FailedDepError: "Amadeus API error 500",
		// FailedDeps deliberately nil — pre-plural fixture
	}}, nil, 80)

	if !strings.Contains(note, "step-1 failed: Amadeus API error 500") {
		t.Errorf("legacy fallback must use FailedDep + FailedDepError, got:\n%s", note)
	}
}

func TestBuildRemediationContinuationNote_NoDepsAtAllStillRenders(t *testing.T) {
	// Defensive: even if both FailedDeps and FailedDep are empty (a malformed
	// skip), the renderer must emit a non-empty line so the planner sees
	// something happened. Better to acknowledge ambiguous failure than to
	// silently drop the skip.
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-3", Capability: "search_flights",
	}}, nil, 80)

	if !strings.Contains(note, "step-3") {
		t.Errorf("empty-deps skip must still mention step id, got:\n%s", note)
	}
	if !strings.Contains(note, "upstream") {
		t.Errorf("empty-deps skip must still mention upstream failure, got:\n%s", note)
	}
}

func TestBuildRemediationContinuationNote_MultiFailedDepsTruncatesLongErrors(t *testing.T) {
	// Each per-dep error truncation must apply independently — one verbose
	// upstream cannot bloat the entire note by leaking a 5KB body across the
	// other deps' lines.
	longBody := strings.Repeat("x", 5000)
	multiline := "first line\nsecond line\nthird line stack"
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID:     "step-5",
		FailedDep:  "step-1",
		FailedDeps: []string{"step-1", "step-2"},
		FailedDepsErrors: map[string]string{
			"step-1": longBody,
			"step-2": multiline,
		},
	}}, nil, 80)

	// Long body must not contain a 300-char run of x's after truncation.
	if strings.Contains(note, strings.Repeat("x", 300)) {
		t.Errorf("expected per-dep truncation, got long run in note:\n%s", note)
	}
	if !strings.Contains(note, "…") {
		t.Errorf("expected truncation ellipsis in render, got:\n%s", note)
	}
	// Multi-line error must single-line independently.
	if strings.Contains(note, "second line") {
		t.Errorf("multi-line per-dep error must be single-lined, got:\n%s", note)
	}
	if !strings.Contains(note, "first line") {
		t.Errorf("first line of step-2 error must appear, got:\n%s", note)
	}
}

func TestCollectTemplateInducedSkips_PopulatesFailedDepsErrors(t *testing.T) {
	// collectTemplateInducedSkips must resolve per-dep errors for the FULL
	// FailedDeps set, not just FailedDep. Without this, buildRemediationContinuationNote's
	// multi-dep render falls back to listing dep ids alone — defeating Layer 3.
	phaseSteps := []StepResult{{
		StepID:    "step-5",
		AgentName: "slack-tool",
		Success:   false,
		Error:     "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed)",
		Metadata: map[string]interface{}{
			metaKeyBlockingReason:        blockingReasonTemplate,
			metaKeyFailedDependency:      "step-1",
			metaKeyAllFailedDependencies: []string{"step-1", "step-3"},
		},
	}}
	priorResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "stock-tool panic"},
		"step-3": {StepID: "step-3", Success: false, Error: "news-tool 503"},
	}

	skips := collectTemplateInducedSkips(phaseSteps, priorResults)
	if len(skips) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(skips))
	}
	got := skips[0]

	if got.FailedDepsErrors == nil {
		t.Fatalf("expected FailedDepsErrors to be populated, got nil")
	}
	if got.FailedDepsErrors["step-1"] != "stock-tool panic" {
		t.Errorf("FailedDepsErrors[step-1] = %q, want %q", got.FailedDepsErrors["step-1"], "stock-tool panic")
	}
	if got.FailedDepsErrors["step-3"] != "news-tool 503" {
		t.Errorf("FailedDepsErrors[step-3] = %q, want %q", got.FailedDepsErrors["step-3"], "news-tool 503")
	}
}

func TestCollectTemplateInducedSkips_FailedDepsErrorsNilWhenNoPriorResults(t *testing.T) {
	// When priorResults is nil (legacy/replay path), the per-dep error map
	// must remain nil — render falls back to listing dep ids alone.
	phaseSteps := []StepResult{{
		StepID:    "step-5",
		AgentName: "slack-tool",
		Success:   false,
		Error:     "skipped due to failed template dependency: step-1",
		Metadata: map[string]interface{}{
			metaKeyBlockingReason:        blockingReasonTemplate,
			metaKeyFailedDependency:      "step-1",
			metaKeyAllFailedDependencies: []string{"step-1", "step-3"},
		},
	}}
	skips := collectTemplateInducedSkips(phaseSteps, nil)
	if len(skips) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(skips))
	}
	if skips[0].FailedDepsErrors != nil {
		t.Errorf("expected nil FailedDepsErrors with nil priorResults, got %v", skips[0].FailedDepsErrors)
	}
}

// ─── Layer 3 optional (Cause 2c): skip error string enumerates all deps ─────

func TestSkipErrorString_SingleDepFormatUnchanged(t *testing.T) {
	// Single-dep skips must produce the original error string format.
	// Compatibility check: the legacy templateInducedSkipErrorPrefix parser
	// (used by collectTemplateInducedSkips when Metadata is absent) extracts
	// the first whitespace-delimited token after the prefix as failedDep.
	// We verify that path still works on the new single-dep error string.
	phaseSteps := []StepResult{{
		StepID:  "step-3",
		Success: false,
		Error:   "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed)",
		// No Metadata → forces the legacy parser branch
	}}
	skips := collectTemplateInducedSkips(phaseSteps, nil)
	if len(skips) != 1 {
		t.Fatalf("expected 1 skip from legacy parse, got %d", len(skips))
	}
	if skips[0].FailedDep != "step-1" {
		t.Errorf("legacy parser must extract step-1 as FailedDep, got %q", skips[0].FailedDep)
	}
}

func TestSkipErrorString_MultiDepTemplateIncludedRetainsPrefix(t *testing.T) {
	// Layer 3 optional: when multiple deps fail with template-induced
	// blocking, the error string must still start with templateInducedSkipErrorPrefix
	// and the FIRST token after the prefix must be parseable by the legacy
	// fallback. A multi-dep error string still leads with a single dep id at
	// the same position to preserve replay/legacy compat.
	multiDepErr := "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed; additional failed upstreams: step-3, step-4)"

	if !strings.HasPrefix(multiDepErr, templateInducedSkipErrorPrefix) {
		t.Fatalf("multi-dep error must keep the legacy prefix, got: %s", multiDepErr)
	}

	phaseSteps := []StepResult{{
		StepID:  "step-5",
		Success: false,
		Error:   multiDepErr,
		// No Metadata → forces legacy parser
	}}
	skips := collectTemplateInducedSkips(phaseSteps, nil)
	if len(skips) != 1 {
		t.Fatalf("expected 1 skip from legacy parse, got %d", len(skips))
	}
	// Legacy parser extracts the FIRST token — must be "step-1".
	if skips[0].FailedDep != "step-1" {
		t.Errorf("legacy parser on multi-dep error must extract step-1, got %q", skips[0].FailedDep)
	}
}

func TestDecideRemediation_GateMatrix(t *testing.T) {
	// Single place that exercises every decision path of RC8's gate. Each
	// case declares inputs and the exact reason/trigger pair expected. Keeps
	// the gate logic regression-proof without having to stand up the full
	// executePhaseLoop harness.
	skippedSteps := []StepResult{{
		StepID:    "step-3",
		AgentName: "flight-tool",
		Success:   false,
		Error:     "skipped due to failed template dependency: step-1 (parameter references {{step-1...}} but that step failed)",
	}}
	priorResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "Amadeus API error 500"},
	}

	cases := []struct {
		name                 string
		remediationAttempted bool
		iterativeEnabled     bool
		phaseCount           int
		maxPhases            int
		totalSteps           int
		maxTotalSteps        int
		phaseSteps           []StepResult
		prior                map[string]*StepResult
		wantTrigger          bool
		wantReason           RemediationReason
	}{
		{
			name:             "happy path — first template-induced skip, budget remains",
			iterativeEnabled: true,
			phaseCount:       2, maxPhases: 5, totalSteps: 3, maxTotalSteps: 10,
			phaseSteps: skippedSteps, prior: priorResults,
			wantTrigger: true, wantReason: RemediationTriggered,
		},
		{
			name:                 "already attempted — one-shot guard",
			remediationAttempted: true,
			iterativeEnabled:     true,
			phaseCount:           2, maxPhases: 5, totalSteps: 3, maxTotalSteps: 10,
			phaseSteps: skippedSteps, prior: priorResults,
			wantTrigger: false, wantReason: RemediationAlreadyAttempted,
		},
		{
			name:             "iterative planning disabled — no next phase to route into",
			iterativeEnabled: false,
			phaseCount:       2, maxPhases: 5, totalSteps: 3, maxTotalSteps: 10,
			phaseSteps: skippedSteps, prior: priorResults,
			wantTrigger: false, wantReason: RemediationIterativeDisabled,
		},
		{
			name:             "phase budget exhausted",
			iterativeEnabled: true,
			phaseCount:       5, maxPhases: 5, totalSteps: 3, maxTotalSteps: 10,
			phaseSteps: skippedSteps, prior: priorResults,
			wantTrigger: false, wantReason: RemediationMaxPhases,
		},
		{
			name:             "step budget exhausted",
			iterativeEnabled: true,
			phaseCount:       2, maxPhases: 5, totalSteps: 10, maxTotalSteps: 10,
			phaseSteps: skippedSteps, prior: priorResults,
			wantTrigger: false, wantReason: RemediationMaxSteps,
		},
		{
			name:             "no template-induced skips — nothing to remediate",
			iterativeEnabled: true,
			phaseCount:       2, maxPhases: 5, totalSteps: 3, maxTotalSteps: 10,
			phaseSteps: []StepResult{
				{StepID: "step-1", Success: true},
				{StepID: "step-2", Success: false, Error: "skipped due to failed dependency: step-0"}, // explicit, not template
			},
			prior:       map[string]*StepResult{"step-0": {StepID: "step-0", Success: false}},
			wantTrigger: false, wantReason: RemediationNoTemplateSkips,
		},
	}

	defaultPatternCfg := FailurePatternConfig{MinFailures: 2, SignatureLen: 120, DisplayLen: 80}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideRemediation(
				tc.remediationAttempted, tc.iterativeEnabled,
				tc.phaseCount, tc.maxPhases, tc.totalSteps, tc.maxTotalSteps,
				tc.phaseSteps, tc.prior,
				defaultPatternCfg,
			)
			if got.Trigger != tc.wantTrigger {
				t.Errorf("Trigger = %v, want %v (reason=%q)", got.Trigger, tc.wantTrigger, got.Reason)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if tc.wantTrigger {
				if got.SkipCount() == 0 {
					t.Errorf("expected positive SkipCount when triggered, got 0")
				}
				if len(got.SkipIDs) == 0 {
					t.Errorf("expected non-empty SkipIDs when triggered, got nil/empty")
				}
				if got.Note == "" {
					t.Errorf("expected non-empty Note when triggered")
				}
			}
		})
	}
}

// mockAIClientRemediationFlow returns a hand-crafted 3-call sequence that
// reproduces the ORCH-020 remediation scenario end-to-end:
//   - call 1 (phase-1 plan): one step that will fail upstream
//   - call 2 (phase-2 plan): a step that template-references the phase-1 step,
//     declared via implicit_deps (passes RC1/RC2/RC3 at plan time)
//   - call 3 (phase-3 plan = RC8 remediation replan): captured by the mock so
//     the test can assert the continuation prompt carries the remediation note
//   - synthesis call: returns a canned user-facing string
//
// Everything after call 3 gets a safety-net empty plan so the test never hangs
// if the orchestration decides to do one more round for some reason.
type mockAIClientRemediationFlow struct {
	planningCalls        int
	phase3PlanningPrompt string
	synthCalls           int
}

func (m *mockAIClientRemediationFlow) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	isSynthesis := strings.Contains(prompt, "<agent_responses>") ||
		strings.Contains(prompt, "Synthesize the above") ||
		strings.Contains(prompt, "synthesize")
	isPlanning := strings.Contains(prompt, "<available_agents>") ||
		strings.Contains(prompt, "plan_id") ||
		strings.Contains(prompt, "execution plan")

	if isSynthesis && !isPlanning {
		m.synthCalls++
		return &core.AIResponse{Content: "Service unavailable — the upstream airport lookup returned errors."}, nil
	}
	if !isPlanning {
		return &core.AIResponse{Content: `{"plan_id":"noop","steps":[]}`}, nil
	}

	m.planningCalls++
	switch m.planningCalls {
	case 1:
		return &core.AIResponse{Content: `{
			"plan_id": "phase-1",
			"original_request": "test",
			"mode": "autonomous",
			"terminal": false,
			"continuation_note": "need to search flights after resolving airport codes",
			"steps": [{
				"step_id": "step-1",
				"agent_name": "flight-tool",
				"depends_on": [],
				"metadata": {
					"capability": "search_airports",
					"parameters": {"keyword": "NYC"}
				}
			}]
		}`}, nil
	case 2:
		return &core.AIResponse{Content: `{
			"plan_id": "phase-2",
			"original_request": "test",
			"mode": "autonomous",
			"terminal": true,
			"steps": [{
				"step_id": "step-2",
				"agent_name": "flight-tool",
				"depends_on": [],
				"implicit_deps": ["step-1"],
				"metadata": {
					"capability": "search_flights",
					"parameters": {"origin": "{{step-1.response.data.iataCode}}"}
				}
			}]
		}`}, nil
	case 3:
		// RC8 remediation: capture the prompt so the test can assert the
		// note was actually fed through. Return an empty-steps terminal
		// plan per the remediation note's option (b) — the synthesizer
		// will then tell the user the service is unavailable.
		m.phase3PlanningPrompt = prompt
		return &core.AIResponse{Content: `{
			"plan_id": "phase-3-remediation",
			"original_request": "test",
			"mode": "autonomous",
			"terminal": true,
			"steps": []
		}`}, nil
	}
	// Safety net — shouldn't be hit.
	return &core.AIResponse{Content: `{"plan_id":"noop","terminal":true,"steps":[]}`}, nil
}

func TestRC8_WiringFeedsRemediationNoteIntoContinuationPrompt(t *testing.T) {
	// End-to-end verification of the RC8 wiring. The unit tests prove the
	// gate (decideRemediation) and the helpers; this test proves the
	// PLUMBING — that the remediation note from a Trigger==true decision
	// actually reaches the next-phase planning prompt via continuationNote
	// → generateContinuationPlan → buildContinuationPrompt → <previous_note>.
	//
	// Setup:
	//   - MockDiscovery + catalog registers a single flight-tool agent.
	//   - MockRoundTripper returns 500 on the airport URL so phase-1 step-1 fails.
	//   - mockAIClientRemediationFlow returns 3 sequential planning responses.
	// Expected:
	//   - 3 planning calls (phase 1, phase 2, remediation phase 3).
	//   - Phase 3 prompt contains the remediation note's signature tokens.
	//   - Synthesis is called and emits the "service unavailable" string.

	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:      "flight-1",
		Name:    "flight-tool",
		Address: "localhost",
		Port:    8080,
		Capabilities: []core.Capability{
			{Name: "search_airports"},
			{Name: "search_flights"},
		},
	})

	mockAI := &mockAIClientRemediationFlow{}
	o := NewAIOrchestrator(DefaultConfig(), discovery, mockAI)
	o.catalog.agents = map[string]*AgentInfo{
		"flight-1": {
			Registration: &core.ServiceRegistration{
				ID: "flight-1", Name: "flight-tool", Address: "localhost", Port: 8080,
			},
			Capabilities: []EnhancedCapability{
				{Name: "search_airports", Endpoint: "/api/airports"},
				{Name: "search_flights", Endpoint: "/api/flights"},
			},
		},
	}

	// Wire a fresh executor so we can inject the HTTP mock and pin maxAttempts.
	o.executor = NewSmartExecutor(o.catalog)
	o.executor.SetMaxAttempts(1) // keep the test fast — one shot per step
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/airports", 500, `{"error":"Amadeus API error 500"}`)
	o.executor.httpClient = &http.Client{Transport: mockRT}

	_, err := o.ProcessRequest(context.Background(), "find flights from NYC", nil)
	if err != nil {
		t.Fatalf("ProcessRequest returned error: %v", err)
	}

	// Planning must have happened at least 3 times — phase 1, phase 2, RC8 remediation.
	if mockAI.planningCalls < 3 {
		t.Fatalf("expected ≥3 planning calls (phase 1 + phase 2 + RC8 remediation), got %d", mockAI.planningCalls)
	}
	// The remediation prompt must carry the skip summary and the positive directive.
	if mockAI.phase3PlanningPrompt == "" {
		t.Fatal("phase-3 prompt was not captured — the 3rd planning call did not happen")
	}
	wantTokens := []string{
		"skipped because",               // skip summary prefix
		"step-1",                        // failed upstream step id
		"materially different approach", // positive-directive instruction
	}
	for _, want := range wantTokens {
		if !strings.Contains(mockAI.phase3PlanningPrompt, want) {
			t.Errorf("remediation prompt missing %q", want)
		}
	}

	// Synthesis should have been invoked so the user sees the terminal message.
	if mockAI.synthCalls < 1 {
		t.Errorf("expected synthesis to be invoked after remediation, got %d calls", mockAI.synthCalls)
	}
}

// mockAIClientRC9PatternFlow is the 2-failure variant used by the RC9
// end-to-end observability test. Phase 1 dispatches TWO airport lookups,
// both fail; phase 2 templates reference both; RC4 skips; RC8 triggers; RC9
// emits a pattern summary (same error, same agent/capability for both
// failures). Phase 3 captures the remediation prompt.
type mockAIClientRC9PatternFlow struct {
	planningCalls        int
	phase3PlanningPrompt string
	synthCalls           int
}

func (m *mockAIClientRC9PatternFlow) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	isSynthesis := strings.Contains(prompt, "<agent_responses>") ||
		strings.Contains(prompt, "Synthesize the above") ||
		strings.Contains(prompt, "synthesize")
	isPlanning := strings.Contains(prompt, "<available_agents>") ||
		strings.Contains(prompt, "plan_id") ||
		strings.Contains(prompt, "execution plan")

	if isSynthesis && !isPlanning {
		m.synthCalls++
		return &core.AIResponse{Content: "Service unavailable — upstream airport lookup failed."}, nil
	}
	if !isPlanning {
		return &core.AIResponse{Content: `{"plan_id":"noop","steps":[]}`}, nil
	}

	m.planningCalls++
	switch m.planningCalls {
	case 1:
		// Phase 1: TWO causal steps — both will fail upstream. Same agent
		// and capability so the pattern analyzer produces a
		// single-attribution DominantAttribution.
		return &core.AIResponse{Content: `{
			"plan_id": "phase-1-pattern",
			"original_request": "test",
			"mode": "autonomous",
			"terminal": false,
			"continuation_note": "resolve airport codes",
			"steps": [
				{
					"step_id": "step-1",
					"agent_name": "flight-tool",
					"depends_on": [],
					"metadata": {"capability": "search_airports", "parameters": {"keyword": "NYC"}}
				},
				{
					"step_id": "step-2",
					"agent_name": "flight-tool",
					"depends_on": [],
					"metadata": {"capability": "search_airports", "parameters": {"keyword": "LHR"}}
				}
			]
		}`}, nil
	case 2:
		// Phase 2: ONE step that template-references BOTH failed upstreams
		// — the production topology from orch-1776802262936110748. RC4
		// records ONE skip, but stamps the full causal set
		// {step-1, step-2} on Metadata[all_failed_dependencies]. RC9's
		// summarizer reads the plural list to build its causal window,
		// so the pattern fires despite there being only one skip. If this
		// test were refactored to two separate phase-2 steps, the pattern
		// would work for a different reason and mask the production
		// scenario.
		return &core.AIResponse{Content: `{
			"plan_id": "phase-2-pattern",
			"original_request": "test",
			"mode": "autonomous",
			"terminal": true,
			"steps": [{
				"step_id": "step-3",
				"agent_name": "flight-tool",
				"depends_on": [],
				"implicit_deps": ["step-1", "step-2"],
				"metadata": {
					"capability": "search_flights",
					"parameters": {
						"origin":      "{{step-1.response.data.iataCode}}",
						"destination": "{{step-2.response.data.iataCode}}"
					}
				}
			}]
		}`}, nil
	case 3:
		// RC8 remediation — capture the prompt so the test can assert
		// the pattern line made it into the continuation prompt.
		m.phase3PlanningPrompt = prompt
		return &core.AIResponse{Content: `{
			"plan_id": "phase-3-remediation",
			"original_request": "test",
			"mode": "autonomous",
			"terminal": true,
			"steps": []
		}`}, nil
	}
	return &core.AIResponse{Content: `{"plan_id":"noop","terminal":true,"steps":[]}`}, nil
}

// rc9CapturingLogger records the "remediation_failure_pattern" DEBUG log
// fields so M4's observability assertion can verify the RC9 path actually
// fires. Only captures the fields map for the operation we care about.
type rc9CapturingLogger struct {
	mu              sync.Mutex
	patternLogFired bool
	patternFields   map[string]interface{}
}

func (l *rc9CapturingLogger) Debug(string, map[string]interface{}) {}
func (l *rc9CapturingLogger) Info(string, map[string]interface{})  {}
func (l *rc9CapturingLogger) Warn(string, map[string]interface{})  {}
func (l *rc9CapturingLogger) Error(string, map[string]interface{}) {}
func (l *rc9CapturingLogger) InfoWithContext(context.Context, string, map[string]interface{}) {
}
func (l *rc9CapturingLogger) WarnWithContext(context.Context, string, map[string]interface{}) {
}
func (l *rc9CapturingLogger) ErrorWithContext(context.Context, string, map[string]interface{}) {
}
func (l *rc9CapturingLogger) DebugWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	if op, _ := fields["operation"].(string); op == "remediation_failure_pattern" {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.patternLogFired = true
		// Copy the fields map so async-run tests don't race on it.
		snap := make(map[string]interface{}, len(fields))
		for k, v := range fields {
			snap[k] = v
		}
		l.patternFields = snap
	}
}

func TestRC9_WiringEmitsPatternAndObservability(t *testing.T) {
	// M4 regression + end-to-end coverage of RC9's pattern-emission path:
	//   - Phase 1 has 2 causal steps, both failing with the same Amadeus error
	//     → pattern analyzer produces a strong single-attribution pattern.
	//   - Phase 2 plan has 2 steps each template-referencing one failure
	//     → RC4 skips both (2 skips with 2 distinct FailedDeps) → RC8 triggers.
	//   - RC9 emits a DEBUG log with operation="remediation_failure_pattern",
	//     emitted=true, total_failed=2, dominant_count=2.
	//   - Phase 3 prompt contains the pattern line with single-attribution
	//     (flight-tool/search_airports) rendering.
	//
	// Note on the span-event assertion: `has_failure_pattern` is stamped on
	// the `orchestrator.remediation.triggered` span event only when the
	// orchestrator has a wired `core.Telemetry` instance — in this unit
	// test o.telemetry is nil so the phase span is a no-op and the event
	// doesn't record. The stamp itself is line-level visible in the
	// production code; the DEBUG-log assertion below serves as the
	// regression guard for the RC9 observability surface in this harness.

	discovery := NewMockDiscovery()
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID: "flight-1", Name: "flight-tool", Address: "localhost", Port: 8080,
		Capabilities: []core.Capability{
			{Name: "search_airports"}, {Name: "search_flights"},
		},
	})

	mockAI := &mockAIClientRC9PatternFlow{}
	o := NewAIOrchestrator(DefaultConfig(), discovery, mockAI)
	logger := &rc9CapturingLogger{}
	o.logger = logger
	o.catalog.agents = map[string]*AgentInfo{
		"flight-1": {
			Registration: &core.ServiceRegistration{
				ID: "flight-1", Name: "flight-tool", Address: "localhost", Port: 8080,
			},
			Capabilities: []EnhancedCapability{
				{Name: "search_airports", Endpoint: "/api/airports"},
				{Name: "search_flights", Endpoint: "/api/flights"},
			},
		},
	}
	o.executor = NewSmartExecutor(o.catalog)
	o.executor.SetMaxAttempts(1)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse("http://localhost:8080/api/airports", 500, `{"error":"Amadeus API error 500"}`)
	o.executor.httpClient = &http.Client{Transport: mockRT}

	_, err := o.ProcessRequest(context.Background(), "find flights", nil)
	if err != nil {
		t.Fatalf("ProcessRequest errored: %v", err)
	}

	// Phase 3 prompt must carry the pattern line.
	if mockAI.phase3PlanningPrompt == "" {
		t.Fatal("phase-3 prompt not captured; remediation didn't reach the planner")
	}
	for _, want := range []string{
		"Upstream failure pattern:",
		"2 of 2 prior steps failed",
		"flight-tool/search_airports",
	} {
		if !strings.Contains(mockAI.phase3PlanningPrompt, want) {
			t.Errorf("phase-3 prompt missing %q", want)
		}
	}

	// DEBUG log with the pattern-diagnostic fields must have fired with
	// emitted=true. Lets operators tell "pattern was emitted" from
	// "pattern rejected" from telemetry alone.
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if !logger.patternLogFired {
		t.Fatal("expected operation=remediation_failure_pattern DEBUG log to fire")
	}
	if emitted, _ := logger.patternFields["emitted"].(bool); !emitted {
		t.Errorf("emitted field must be true when pattern fires, got fields: %v", logger.patternFields)
	}
	if tf, _ := logger.patternFields["total_failed"].(int); tf != 2 {
		t.Errorf("total_failed field = %v, want 2", logger.patternFields["total_failed"])
	}
	if dc, _ := logger.patternFields["dominant_count"].(int); dc != 2 {
		t.Errorf("dominant_count field = %v, want 2", logger.patternFields["dominant_count"])
	}
}

// ─── RC9: summarizeUpstreamFailurePattern ───────────────────────────────────

// Threshold defaults used across RC9 tests, matching DefaultConfig().
const (
	rc9DefaultMinFailures  = 2
	rc9DefaultSignatureLen = 120
	rc9DefaultDisplayLen   = 80
)

func TestSummarizeUpstreamFailurePattern_NilAndEmptyInput(t *testing.T) {
	// Defensive contract: nil skips, empty skips, and nil priorResults all
	// return nil with "insufficient_failures" — no crash.
	if p, r := summarizeUpstreamFailurePattern(nil, nil, rc9DefaultMinFailures, rc9DefaultSignatureLen); p != nil || r != "insufficient_failures" {
		t.Errorf("nil input: got (%v, %q), want (nil, insufficient_failures)", p, r)
	}
	if p, r := summarizeUpstreamFailurePattern([]TemplateInducedSkip{}, map[string]*StepResult{}, rc9DefaultMinFailures, rc9DefaultSignatureLen); p != nil || r != "insufficient_failures" {
		t.Errorf("empty input: got (%v, %q), want (nil, insufficient_failures)", p, r)
	}
}

func TestSummarizeUpstreamFailurePattern_SingleSkipWithTwoFailedUpstreams(t *testing.T) {
	// Production-topology regression (orch-1776802262936110748, 2026-04-21):
	// ONE skipped step templates TWO failed upstreams. The skip's FailedDeps
	// field carries the full causal set {step-1, step-2}; the analyzer
	// expands its window from that slice so the pattern fires despite there
	// being only one skip. Pre-fix behaviour: analyzer used skip.FailedDep
	// (singular) and saw only 1 causal failure → insufficient_failures.
	skips := []TemplateInducedSkip{{
		StepID:     "step-3",
		AgentName:  "flight-tool",
		Capability: "search_flights",
		FailedDep:  "step-1",
		FailedDeps: []string{"step-1", "step-2"},
	}}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "flight-tool", Capability: "search_airports", Success: false, Error: "Amadeus API error 500", RetryExhausted: true},
		"step-2": {StepID: "step-2", AgentName: "flight-tool", Capability: "search_airports", Success: false, Error: "Amadeus API error 500", RetryExhausted: true},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected pattern from 1 skip with FailedDeps=[step-1, step-2], got nil (reject=%q)", r)
	}
	if p.TotalFailed != 2 {
		t.Errorf("TotalFailed = %d, want 2 (window must expand to the full FailedDeps union)", p.TotalFailed)
	}
	if p.DominantCount != 2 {
		t.Errorf("DominantCount = %d, want 2 (both upstreams share the same error)", p.DominantCount)
	}
	if p.DominantAttribution != "flight-tool/search_airports" {
		t.Errorf("DominantAttribution = %q, want flight-tool/search_airports", p.DominantAttribution)
	}
}

func TestSummarizeUpstreamFailurePattern_FallsBackToSingularFailedDep(t *testing.T) {
	// Back-compat: a skip produced by pre-fix code carries only the
	// singular FailedDep (FailedDeps is nil/empty). summarizeUpstream
	// synthesizes a 1-element slice from FailedDep so the analyzer still
	// runs. Behaviourally this case stays below MinFailures=2 → returns nil
	// with reason insufficient_failures (correct — fix doesn't invent data).
	skips := []TemplateInducedSkip{{
		StepID:    "step-3",
		FailedDep: "step-1",
		// FailedDeps intentionally empty
	}}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "e"},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p != nil {
		t.Errorf("legacy skip with 1 FailedDep must stay below MinFailures, got %+v", p)
	}
	if r != "insufficient_failures" {
		t.Errorf("reject_reason = %q, want insufficient_failures", r)
	}
}

func TestSummarizeUpstreamFailurePattern_SingleCausalFailure_BelowThreshold(t *testing.T) {
	// One failed causal step — below MinFailures=2, analyzer returns nil.
	skips := []TemplateInducedSkip{{StepID: "step-2", FailedDep: "step-1"}}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "flight-tool", Success: false, Error: "boom"},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p != nil {
		t.Errorf("expected nil pattern for single failure, got %+v", p)
	}
	if r != "insufficient_failures" {
		t.Errorf("expected reject_reason=insufficient_failures, got %q", r)
	}
}

func TestSummarizeUpstreamFailurePattern_OutOfWindowFailureIgnored(t *testing.T) {
	// F3 regression: priorResults contains an unrelated failure that is NOT
	// in the skips[].FailedDep set. The analyzer must ignore it and (given
	// only one causal failure remains) return insufficient_failures.
	skips := []TemplateInducedSkip{{StepID: "step-3", FailedDep: "step-1"}}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "flight-tool", Success: false, Error: "Amadeus error"},
		// Out-of-window: not referenced by any skip. Must be ignored.
		"step-99": {StepID: "step-99", AgentName: "other-tool", Success: false, Error: "Amadeus error"},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p != nil {
		t.Fatalf("expected nil pattern — out-of-window failure must not count toward MinFailures, got %+v", p)
	}
	if r != "insufficient_failures" {
		t.Errorf("expected reject_reason=insufficient_failures, got %q", r)
	}
}

func TestSummarizeUpstreamFailurePattern_FiftyFiftySplit_ReturnsNil(t *testing.T) {
	// F1 regression: 4 causal failures split 2/2 across two distinct error
	// signatures. Strict-majority gate must reject — 50/50 is not a majority.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
		{StepID: "s-c", FailedDep: "step-3"},
		{StepID: "s-d", FailedDep: "step-4"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "Amadeus error 500"},
		"step-2": {StepID: "step-2", Success: false, Error: "Amadeus error 500"},
		"step-3": {StepID: "step-3", Success: false, Error: "Redis connection timeout"},
		"step-4": {StepID: "step-4", Success: false, Error: "Redis connection timeout"},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p != nil {
		t.Errorf("50/50 split must not produce a pattern, got %+v", p)
	}
	if r != "no_majority_error" {
		t.Errorf("expected reject_reason=no_majority_error, got %q", r)
	}
}

func TestSummarizeUpstreamFailurePattern_MultiFailureDifferentErrors_ReturnsNil(t *testing.T) {
	// Three causal failures, each with a distinct error signature. No
	// dominant error, pattern is nil.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
		{StepID: "s-c", FailedDep: "step-3"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: false, Error: "err alpha"},
		"step-2": {StepID: "step-2", Success: false, Error: "err beta"},
		"step-3": {StepID: "step-3", Success: false, Error: "err gamma"},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p != nil {
		t.Errorf("no dominant error — pattern must be nil, got %+v", p)
	}
	if r != "no_majority_error" {
		t.Errorf("expected reject_reason=no_majority_error, got %q", r)
	}
}

func TestSummarizeUpstreamFailurePattern_HappyPathSingleAttribution(t *testing.T) {
	// 2 causal failures, both same error + same agent/capability. Full
	// pattern with DominantAttribution populated.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
	}
	prior := map[string]*StepResult{
		"step-1": {
			StepID: "step-1", AgentName: "flight-tool", Capability: "search_airports",
			Success: false, Error: "Amadeus API error 500", RetryExhausted: true,
		},
		"step-2": {
			StepID: "step-2", AgentName: "flight-tool", Capability: "search_airports",
			Success: false, Error: "Amadeus API error 500", RetryExhausted: true,
		},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern, got nil (reason=%q)", r)
	}
	if p.TotalFailed != 2 || p.DominantCount != 2 {
		t.Errorf("expected TotalFailed=2 DominantCount=2, got %d/%d", p.TotalFailed, p.DominantCount)
	}
	if p.DominantAttribution != "flight-tool/search_airports" {
		t.Errorf("DominantAttribution = %q, want flight-tool/search_airports (F4 granularity)", p.DominantAttribution)
	}
	if p.AttributionKinds != 1 {
		t.Errorf("single-attribution pattern must have AttributionKinds = 1, got %d", p.AttributionKinds)
	}
	if p.DominantErrorSample != "Amadeus API error 500" {
		t.Errorf("DominantErrorSample = %q, want Amadeus API error 500", p.DominantErrorSample)
	}
	if !p.RetriesExhaustedOnAll {
		t.Errorf("RetriesExhaustedOnAll must be true when every failure has RetryExhausted=true")
	}
}

func TestSummarizeUpstreamFailurePattern_MultiAttribution_EmptyDominant(t *testing.T) {
	// Two failures share the error but hit DIFFERENT agents — stronger
	// persistent-outage signal. DominantAttribution left empty; renderer
	// will append "across multiple agents/capabilities".
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "flight-tool", Capability: "search_airports", Success: false, Error: "Amadeus API error 500"},
		"step-2": {StepID: "step-2", AgentName: "hotel-tool", Capability: "search_hotels", Success: false, Error: "Amadeus API error 500"},
	}
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern, got nil")
	}
	if p.DominantAttribution != "" {
		t.Errorf("multi-agent split must leave DominantAttribution empty, got %q", p.DominantAttribution)
	}
	if p.AttributionKinds != 2 {
		t.Errorf("two distinct agents → AttributionKinds must be 2, got %d", p.AttributionKinds)
	}
	if p.DominantCount != 2 || p.TotalFailed != 2 {
		t.Errorf("expected 2/2 dominant, got %d/%d", p.DominantCount, p.TotalFailed)
	}
}

func TestSummarizeUpstreamFailurePattern_PartialRetriesExhausted(t *testing.T) {
	// Two failures share the error — both agents too — but only one exhausted
	// retries. RetriesExhaustedOnAll must be false.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "flight-tool", Capability: "x", Success: false, Error: "e", RetryExhausted: true},
		"step-2": {StepID: "step-2", AgentName: "flight-tool", Capability: "x", Success: false, Error: "e", RetryExhausted: false},
	}
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern, got nil")
	}
	if p.RetriesExhaustedOnAll {
		t.Errorf("partial retries-exhausted must leave flag false")
	}
}

func TestSummarizeUpstreamFailurePattern_F2Regression_OrchestratorCapability(t *testing.T) {
	// F2 regression: orchestrator capabilities are forced to maxAttempts=1
	// at executor.go:2777, so Attempts=1 + RetryExhausted=true correctly
	// classifies as "exhausted." The analyzer reads RetryExhausted, not
	// Attempts — this test proves it.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "orch-1"},
		{StepID: "s-b", FailedDep: "orch-2"},
	}
	prior := map[string]*StepResult{
		"orch-1": {StepID: "orch-1", AgentName: "a", Capability: "c", Success: false, Error: "e", Attempts: 1, RetryExhausted: true},
		"orch-2": {StepID: "orch-2", AgentName: "a", Capability: "c", Success: false, Error: "e", Attempts: 1, RetryExhausted: true},
	}
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern, got nil")
	}
	if !p.RetriesExhaustedOnAll {
		t.Errorf("Attempts=1 + RetryExhausted=true must be classified as exhausted (orchestrator-capability case)")
	}
}

func TestSummarizeUpstreamFailurePattern_EmptyAgentName_SkippedFromAttribution(t *testing.T) {
	// Agent-less failures don't contribute to the attribution tally. The
	// dominant-error check still passes on count; DominantAttribution stays
	// empty AND AttributionKinds stays 0. The renderer uses
	// AttributionKinds to distinguish "no data" from "multi-agent" so this
	// case does NOT produce a misleading "across multiple agents" claim.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "", Success: false, Error: "e"},
		"step-2": {StepID: "step-2", AgentName: "", Success: false, Error: "e"},
	}
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern despite empty AgentName, got nil")
	}
	if p.DominantAttribution != "" {
		t.Errorf("empty AgentName means no attribution, got %q", p.DominantAttribution)
	}
	if p.AttributionKinds != 0 {
		t.Errorf("no agents tallied → AttributionKinds must be 0, got %d", p.AttributionKinds)
	}
}

func TestBuildRemediationContinuationNote_PatternSuppressesMultiAgentPhraseWhenNoAttribution(t *testing.T) {
	// M3 regression: if every failed step has empty AgentName (e.g. resume
	// path), the pattern emerges with DominantAttribution="" and
	// AttributionKinds=0. The renderer must NOT emit "across multiple
	// agents/capabilities" — that phrase implies a multi-agent outage,
	// which is a stronger signal than "we have no attribution data" and
	// would mislead the LLM.
	pattern := &FailurePattern{
		TotalFailed:         2,
		DominantCount:       2,
		DominantAttribution: "",
		DominantErrorSample: "Amadeus API error 500",
		AttributionKinds:    0, // no agent data
	}
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-3", FailedDep: "step-1",
	}}, pattern, 80)
	if strings.Contains(note, "across multiple agents") {
		t.Errorf("no-attribution pattern must not emit 'across multiple agents/capabilities', got: %s", note)
	}
	// Positive check — the pattern line still includes counts + error.
	if !strings.Contains(note, "2 of 2 prior steps failed") {
		t.Errorf("pattern line still expected, got: %s", note)
	}
	if !strings.Contains(note, "Amadeus API error 500") {
		t.Errorf("error signature still expected, got: %s", note)
	}
}

func TestSummarizeUpstreamFailurePattern_CapabilityFallbackToAgent(t *testing.T) {
	// F4 fallback: when Capability is empty (e.g. resume path), attribution
	// key uses AgentName alone.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "flight-tool", Capability: "", Success: false, Error: "e"},
		"step-2": {StepID: "step-2", AgentName: "flight-tool", Capability: "", Success: false, Error: "e"},
	}
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern, got nil")
	}
	if p.DominantAttribution != "flight-tool" {
		t.Errorf("empty Capability should fall back to AgentName alone, got %q", p.DominantAttribution)
	}
}

func TestSummarizeUpstreamFailurePattern_NilPriorEntryIgnored(t *testing.T) {
	// Tolerates nil and success entries in priorResults. Only non-nil, failed
	// entries count.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
		{StepID: "s-c", FailedDep: "step-3"}, // maps to nil entry
		{StepID: "s-d", FailedDep: "step-4"}, // maps to success entry
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Capability: "c", Success: false, Error: "e"},
		"step-2": {StepID: "step-2", AgentName: "a", Capability: "c", Success: false, Error: "e"},
		"step-3": nil,
		"step-4": {StepID: "step-4", AgentName: "a", Capability: "c", Success: true},
	}
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p == nil {
		t.Fatalf("expected a pattern from the 2 valid failures, got nil")
	}
	if p.TotalFailed != 2 {
		t.Errorf("TotalFailed = %d, want 2 (nil + success entries must be ignored)", p.TotalFailed)
	}
}

func TestSummarizeUpstreamFailurePattern_DeduplicatesRepeatedFailedDep(t *testing.T) {
	// Two skips may reference the same FailedDep (e.g., two templated params
	// on different fields). The analyzer dedupes so the step is counted once.
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-1"}, // duplicate
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Capability: "c", Success: false, Error: "e"},
	}
	p, r := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, rc9DefaultSignatureLen)
	if p != nil {
		t.Errorf("dedup must drop the duplicate FailedDep → only 1 failure → insufficient. Got %+v", p)
	}
	if r != "insufficient_failures" {
		t.Errorf("expected insufficient_failures, got %q", r)
	}
}

func TestSummarizeUpstreamFailurePattern_SignatureLenCapsClassification(t *testing.T) {
	// Classification uses the leading signatureLen chars of each error.
	// Two errors that DIVERGE past the cap get collapsed to the same bucket.
	longPrefix := strings.Repeat("x", 100)
	skips := []TemplateInducedSkip{
		{StepID: "s-a", FailedDep: "step-1"},
		{StepID: "s-b", FailedDep: "step-2"},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Capability: "c", Success: false, Error: longPrefix + "_UNIQUE_1"},
		"step-2": {StepID: "step-2", AgentName: "a", Capability: "c", Success: false, Error: longPrefix + "_UNIQUE_2"},
	}
	// signatureLen=50 → both get truncated to the first 50 xs, same bucket.
	p, _ := summarizeUpstreamFailurePattern(skips, prior, rc9DefaultMinFailures, 50)
	if p == nil {
		t.Fatalf("narrow signatureLen should collapse divergent-tail errors, got nil")
	}
	if p.DominantCount != 2 {
		t.Errorf("DominantCount = %d, want 2 (tails past signatureLen must be ignored)", p.DominantCount)
	}
}

// ─── RC9: buildRemediationContinuationNote pattern renders ──────────────────

func TestBuildRemediationContinuationNote_PatternSingleAttribution(t *testing.T) {
	pattern := &FailurePattern{
		TotalFailed:           2,
		DominantCount:         2,
		DominantAttribution:   "flight-tool/search_airports",
		DominantErrorSample:   "Amadeus API error 500",
		RetriesExhaustedOnAll: true,
	}
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-3", Capability: "search_flights",
		FailedDep: "step-1", FailedDepError: "Amadeus API error 500",
	}}, pattern, 80)

	for _, want := range []string{
		"Upstream failure pattern:",
		"2 of 2 prior steps failed",
		"against flight-tool/search_airports",
		"Amadeus API error 500",
		"retries exhausted",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("single-attribution render missing %q, got: %s", want, note)
		}
	}
}

func TestBuildRemediationContinuationNote_PatternMultiAttribution(t *testing.T) {
	// DominantAttribution empty + AttributionKinds >= 2 → "across multiple
	// agents/capabilities". AttributionKinds distinguishes this (real
	// multi-agent outage) from AttributionKinds == 0 (no attribution data).
	pattern := &FailurePattern{
		TotalFailed:         2,
		DominantCount:       2,
		DominantAttribution: "",
		DominantErrorSample: "Amadeus API error 500",
		AttributionKinds:    2, // flight-tool/search_airports + hotel-tool/search_hotels
	}
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-4", FailedDep: "step-1", FailedDepError: "Amadeus API error 500",
	}}, pattern, 80)

	if !strings.Contains(note, "across multiple agents/capabilities") {
		t.Errorf("multi-attribution render must include 'across multiple agents/capabilities', got: %s", note)
	}
	if strings.Contains(note, "retries exhausted") {
		t.Errorf("did not set RetriesExhaustedOnAll, note must not claim exhausted: %s", note)
	}
}

func TestBuildRemediationContinuationNote_PatternDisplayLenTruncation(t *testing.T) {
	// Signature longer than displayLen renders with trailing "…".
	longSig := strings.Repeat("E", 150)
	pattern := &FailurePattern{
		TotalFailed: 2, DominantCount: 2,
		DominantAttribution: "a/b",
		DominantErrorSample: longSig,
	}
	note := buildRemediationContinuationNote([]TemplateInducedSkip{{
		StepID: "step-3", FailedDep: "step-1",
	}}, pattern, 40)
	// Expect only ~40 Es in the rendered signature, ending in "…".
	if strings.Contains(note, strings.Repeat("E", 100)) {
		t.Errorf("displayLen=40 must truncate signature, got: %s", note)
	}
	if !strings.Contains(note, "…") {
		t.Errorf("truncated render must end with '…', got: %s", note)
	}
}

// ─── RC9: DefaultConfig RemediationFailurePattern* tunables ─────────────────

func TestDefaultConfig_RemediationFailurePattern_Defaults(t *testing.T) {
	for _, v := range []string{
		"TRUVAG3_FAILURE_PATTERN_MIN_FAILURES",
		"TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN",
		"TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN",
	} {
		prev := os.Getenv(v)
		_ = os.Unsetenv(v)
		t.Cleanup(func() { _ = os.Setenv(v, prev) })
	}
	cfg := DefaultConfig()
	if cfg.RemediationFailurePatternMinFailures != 2 {
		t.Errorf("MinFailures default = %d, want 2", cfg.RemediationFailurePatternMinFailures)
	}
	if cfg.RemediationFailurePatternSignatureLen != 120 {
		t.Errorf("SignatureLen default = %d, want 120", cfg.RemediationFailurePatternSignatureLen)
	}
	if cfg.RemediationFailurePatternDisplayLen != 80 {
		t.Errorf("DisplayLen default = %d, want 80", cfg.RemediationFailurePatternDisplayLen)
	}
}

func TestDefaultConfig_RemediationFailurePattern_EnvOverride(t *testing.T) {
	cases := []struct {
		envKey    string
		envVal    string
		fieldName string
		want      int
		get       func(*OrchestratorConfig) int
	}{
		{"TRUVAG3_FAILURE_PATTERN_MIN_FAILURES", "5", "MinFailures", 5,
			func(c *OrchestratorConfig) int { return c.RemediationFailurePatternMinFailures }},
		{"TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN", "256", "SignatureLen", 256,
			func(c *OrchestratorConfig) int { return c.RemediationFailurePatternSignatureLen }},
		{"TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN", "40", "DisplayLen", 40,
			func(c *OrchestratorConfig) int { return c.RemediationFailurePatternDisplayLen }},
	}
	for _, tc := range cases {
		t.Run(tc.fieldName, func(t *testing.T) {
			prev := os.Getenv(tc.envKey)
			t.Cleanup(func() { _ = os.Setenv(tc.envKey, prev) })
			_ = os.Setenv(tc.envKey, tc.envVal)
			cfg := DefaultConfig()
			if got := tc.get(cfg); got != tc.want {
				t.Errorf("%s = %d, want %d (env override)", tc.fieldName, got, tc.want)
			}
		})
	}
}

func TestDefaultConfig_RemediationFailurePattern_InvalidEnvKeepsDefault(t *testing.T) {
	cases := []struct {
		envKey    string
		envVal    string
		fieldName string
		want      int
		get       func(*OrchestratorConfig) int
	}{
		{"TRUVAG3_FAILURE_PATTERN_MIN_FAILURES", "not-an-int", "MinFailures", 2,
			func(c *OrchestratorConfig) int { return c.RemediationFailurePatternMinFailures }},
		{"TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN", "0", "SignatureLen", 120,
			func(c *OrchestratorConfig) int { return c.RemediationFailurePatternSignatureLen }},
		{"TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN", "-10", "DisplayLen", 80,
			func(c *OrchestratorConfig) int { return c.RemediationFailurePatternDisplayLen }},
	}
	for _, tc := range cases {
		t.Run(tc.fieldName, func(t *testing.T) {
			prev := os.Getenv(tc.envKey)
			t.Cleanup(func() { _ = os.Setenv(tc.envKey, prev) })
			_ = os.Setenv(tc.envKey, tc.envVal)
			cfg := DefaultConfig()
			if got := tc.get(cfg); got != tc.want {
				t.Errorf("%s with invalid env %q = %d, want default %d", tc.fieldName, tc.envVal, got, tc.want)
			}
		})
	}
}

// ─── RC9: executor stamps RetryExhausted on retry-budget exhaustion ─────────

func TestExecuteStep_StampsRetryExhaustedOnBudgetExhaustion(t *testing.T) {
	// Executor retry loop must stamp StepResult.RetryExhausted=true when
	// maxAttempts is consumed without a successful attempt. RC9's pattern
	// analyzer reads this authoritative bit.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID: "agent-1", Name: "test-agent", Address: "localhost", Port: 8080,
				},
				Capabilities: []EnhancedCapability{{Name: "test_cap", Endpoint: "/api/test"}},
			},
		},
	}
	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(2)
	mockRT := NewMockRoundTripper()
	// 500s on every attempt → retry-budget exhaustion.
	mockRT.SetResponse("http://localhost:8080/api/test", http.StatusInternalServerError, `{"err":"upstream"}`)
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID: "s-1", AgentName: "test-agent",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	}
	result := executor.executeStep(context.Background(), step)
	if result.Success {
		t.Fatalf("test expects failure; got success")
	}
	if !result.RetryExhausted {
		t.Errorf("RetryExhausted must be true after %d failed attempts, got false", result.Attempts)
	}
}

func TestExecuteStep_DoesNotStampRetryExhaustedOnSuccess(t *testing.T) {
	// Successful steps must leave RetryExhausted=false even after retries —
	// a retry that eventually succeeds is not an exhaustion signal.
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"agent-1": {
				Registration: &core.ServiceRegistration{
					ID: "agent-1", Name: "test-agent", Address: "localhost", Port: 8080,
				},
				Capabilities: []EnhancedCapability{{Name: "test_cap", Endpoint: "/api/test"}},
			},
		},
	}
	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(3)
	mockRT := NewMockRoundTripper()
	// One failure then a success.
	mockRT.SetRetryResponses("http://localhost:8080/api/test", []struct {
		StatusCode int
		Body       string
	}{
		{StatusCode: http.StatusInternalServerError, Body: "boom"},
		{StatusCode: http.StatusOK, Body: `{"ok":true}`},
	})
	executor.httpClient = &http.Client{Transport: mockRT}

	step := RoutingStep{
		StepID: "s-1", AgentName: "test-agent",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	}
	result := executor.executeStep(context.Background(), step)
	if !result.Success {
		t.Fatalf("expected success after retry, got error: %s", result.Error)
	}
	if result.RetryExhausted {
		t.Errorf("RetryExhausted must be false when the step eventually succeeded")
	}
}

// ─── Regression golden: the ORCH-020 incident plan ───────────────────────────

func TestRegression_ORCH020_IncidentPlan_AllValidatorsFlag(t *testing.T) {
	// Reconstructs the Phase-2 defects from the incident (orch-1776708595788303964)
	// in a form that exercises each new validator:
	//   - RC2: the plan contains a malformed framework template ({{step-1}}, no
	//     field path) — exactly the shape RC2 targets under its narrowed scope.
	//   - RC3: the {{step-1...}} ref is cross-phase with neither depends_on nor
	//     implicit_deps declared.
	//   - RC1: step-1 is not in executedStepCaps either (nil here), so the
	//     cross-phase existence check must fail.
	//
	// {{today_plus_1}}-style hallucinations no longer fall under RC2 (tool-
	// specific tokens pass through so tools like Prometheus retain their
	// contract). In real incidents that class is caught earlier by RC7's
	// <runtime_context> date injection (the planner has no reason to invent
	// date macros) or by the tool returning an error that feeds back to
	// error_analyzer.
	o := newOrchestratorWithCatalog(t, nil)

	plan := &RoutingPlan{
		Steps: []RoutingStep{{
			StepID:    "step-3",
			AgentName: "flight-tool",
			DependsOn: nil,
			Metadata: map[string]interface{}{
				"capability": "search_flights",
				"parameters": map[string]interface{}{
					"origin":         "JFK",
					"destination":    "{{step-1.response.data.iata_code}}",
					"departure_date": "2026-04-21",
					"bad_ref":        "{{step-1}}", // malformed: missing .response.data.FIELD
				},
			},
		}},
	}

	if err := validateNoUnknownMacros(plan); err == nil {
		t.Error("RC2 must flag {{step-1}} as a malformed framework template")
	}
	if err := o.validateDependencyConsistency(plan); err == nil {
		t.Error("RC3 must flag missing implicit_deps for cross-phase step-1 reference")
	}
	if err := o.validateTemplatePaths(plan, nil); err == nil {
		t.Error("RC1 must flag step-1 as absent from current plan and executedStepCaps")
	}
}

// ─── Test wiring helpers ─────────────────────────────────────────────────────

// newSkipTestExecutor builds a SmartExecutor usable by the RC4 sweep tests
// without needing real networking. All we need is a valid catalog and the
// defaults from NewSmartExecutor (semaphore, logger, etc.).
func newSkipTestExecutor() *SmartExecutor {
	return NewSmartExecutor(&AgentCatalog{agents: map[string]*AgentInfo{}})
}
