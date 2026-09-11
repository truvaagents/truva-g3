package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type afterPlanningContractHook struct {
	name         string
	calls        int
	apply        func(interface{}) (interface{}, error)
	applyContext func(context.Context, interface{}) (interface{}, error)
}

func (h *afterPlanningContractHook) Name() string { return h.name }

func (h *afterPlanningContractHook) AfterPlanning(
	ctx context.Context,
	_ *core.PipelineContext,
	plan interface{},
) (interface{}, error) {
	h.calls++
	if h.applyContext != nil {
		return h.applyContext(ctx, plan)
	}
	if h.apply == nil {
		return plan, nil
	}
	return h.apply(plan)
}

type requiredAfterPlanningContractHook struct {
	afterPlanningContractHook
}

func (*requiredAfterPlanningContractHook) RequireAfterPlanningSuccess() {}

type requiredAfterPlanningMarkerOnly struct {
	name string
}

func (h *requiredAfterPlanningMarkerOnly) Name() string               { return h.name }
func (*requiredAfterPlanningMarkerOnly) RequireAfterPlanningSuccess() {}

type afterPlanningOtherStageHook struct {
	name string
}

func (h *afterPlanningOtherStageHook) Name() string { return h.name }
func (*afterPlanningOtherStageHook) BeforePlanning(
	context.Context,
	*core.PipelineContext,
) (*core.PipelineShortCircuit, error) {
	return nil, nil
}

type valuePipelineContractHook struct{}

func (valuePipelineContractHook) Name() string { return "value-hook" }

var (
	_ core.AfterPlanningHook         = (*afterPlanningContractHook)(nil)
	_ core.RequiredAfterPlanningHook = (*requiredAfterPlanningContractHook)(nil)
	_ core.RequiredAfterPlanningHook = (*requiredAfterPlanningMarkerOnly)(nil)
	_ core.BeforePlanningHook        = (*afterPlanningOtherStageHook)(nil)
)

func TestValidatePipelineHooks_RequiredAfterPlanningComposition(t *testing.T) {
	typedNil := (*requiredAfterPlanningContractHook)(nil)
	tests := []struct {
		name       string
		hooks      []core.PipelineHook
		wantReason InvalidRequiredAfterPlanningHookReason
		wantConfig bool
	}{
		{
			name:       "required marker with drifted stage method",
			hooks:      []core.PipelineHook{&requiredAfterPlanningMarkerOnly{name: "drifted"}},
			wantReason: InvalidRequiredAfterPlanningStageDrift,
			wantConfig: true,
		},
		{
			name: "multiple required hooks",
			hooks: []core.PipelineHook{
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "first"}},
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "second"}},
			},
			wantReason: InvalidRequiredAfterPlanningMultiple,
			wantConfig: true,
		},
		{
			name: "required hook is not terminal for its stage",
			hooks: []core.PipelineHook{
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "required"}},
				&afterPlanningContractHook{name: "later-mutator"},
			},
			wantReason: InvalidRequiredAfterPlanningNotLast,
			wantConfig: true,
		},
		{
			name:       "nil hook",
			hooks:      []core.PipelineHook{nil},
			wantConfig: true,
		},
		{
			name:       "typed nil hook",
			hooks:      []core.PipelineHook{typedNil},
			wantConfig: true,
		},
		{
			name: "required hook follows optional stage participant",
			hooks: []core.PipelineHook{
				&afterPlanningContractHook{name: "optional"},
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "required"}},
			},
		},
		{
			name: "unrelated stage follows required hook",
			hooks: []core.PipelineHook{
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "required"}},
				&afterPlanningOtherStageHook{name: "before-only"},
			},
		},
		{
			name:  "ordinary partial hook remains valid",
			hooks: []core.PipelineHook{&afterPlanningOtherStageHook{name: "before-only"}},
		},
		{
			name:  "value hook remains valid",
			hooks: []core.PipelineHook{valuePipelineContractHook{}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePipelineHooks(test.hooks)
			if test.wantConfig {
				if err == nil || !errors.Is(err, ErrInvalidOrchestratorConfig) {
					t.Fatalf("validatePipelineHooks() error = %v, want invalid configuration", err)
				}
			} else if err != nil {
				t.Fatalf("validatePipelineHooks() error = %v", err)
			}

			if test.wantReason == "" {
				var typed *InvalidRequiredAfterPlanningHookError
				if errors.As(err, &typed) {
					t.Fatalf("unexpected required-hook error: %v", typed)
				}
				return
			}
			var typed *InvalidRequiredAfterPlanningHookError
			if !errors.As(err, &typed) || typed.Reason != test.wantReason {
				t.Fatalf("required-hook error = %#v, want reason %q", typed, test.wantReason)
			}
			if !IsInvalidRequiredAfterPlanningHook(err) {
				t.Fatal("IsInvalidRequiredAfterPlanningHook returned false")
			}
		})
	}
}

func TestCreateResolvedOrchestrator_RejectsInvalidRequiredAfterPlanningComposition(t *testing.T) {
	tests := []struct {
		name       string
		hooks      []core.PipelineHook
		wantReason InvalidRequiredAfterPlanningHookReason
	}{
		{
			name:  "nil hook",
			hooks: []core.PipelineHook{nil},
		},
		{
			name:  "typed nil hook",
			hooks: []core.PipelineHook{(*requiredAfterPlanningContractHook)(nil)},
		},
		{
			name:       "stage drift",
			hooks:      []core.PipelineHook{&requiredAfterPlanningMarkerOnly{name: "drifted"}},
			wantReason: InvalidRequiredAfterPlanningStageDrift,
		},
		{
			name: "multiple required hooks",
			hooks: []core.PipelineHook{
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "first"}},
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "second"}},
			},
			wantReason: InvalidRequiredAfterPlanningMultiple,
		},
		{
			name: "required hook not terminal",
			hooks: []core.PipelineHook{
				&requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "required"}},
				&afterPlanningContractHook{name: "later"},
			},
			wantReason: InvalidRequiredAfterPlanningNotLast,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			orchestrator, err := CreateResolvedOrchestrator(DefaultConfig(), OrchestratorDependencies{
				Discovery:     NewMockDiscovery(),
				AIClient:      NewMockAIClient(),
				PipelineHooks: test.hooks,
			})
			if orchestrator != nil || !errors.Is(err, ErrInvalidOrchestratorConfig) {
				t.Fatalf("invalid configuration returned orchestrator=%v error=%v", orchestrator, err)
			}
			var typed *InvalidRequiredAfterPlanningHookError
			if test.wantReason == "" {
				if errors.As(err, &typed) {
					t.Fatalf("nil hook misclassified as required-hook composition: %v", err)
				}
				return
			}
			if !errors.As(err, &typed) || typed.Reason != test.wantReason {
				t.Fatalf("CreateResolvedOrchestrator() error = %v, want reason %q", err, test.wantReason)
			}
		})
	}
}

func TestCreateResolvedOrchestrator_DefensivelyCopiesValidatedHookOrder(t *testing.T) {
	required := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{name: "required"}}
	hooks := []core.PipelineHook{required}
	orchestrator, err := CreateResolvedOrchestrator(DefaultConfig(), OrchestratorDependencies{
		Discovery:     NewMockDiscovery(),
		AIClient:      NewMockAIClient(),
		PipelineHooks: hooks,
	})
	if err != nil {
		t.Fatalf("CreateResolvedOrchestrator() error = %v", err)
	}
	hooks[0] = &afterPlanningContractHook{name: "caller-replacement"}
	if len(orchestrator.pipelineHooks) != 1 || orchestrator.pipelineHooks[0] != required {
		t.Fatalf("constructed hook order changed through caller-owned slice: %#v", orchestrator.pipelineHooks)
	}
}

func TestInvalidRequiredAfterPlanningHookError_NilReceiverIsSafe(t *testing.T) {
	var invalid *InvalidRequiredAfterPlanningHookError
	if got := invalid.Error(); got != "invalid required after-planning hook" {
		t.Fatalf("nil receiver Error() = %q", got)
	}
	if cause := invalid.Unwrap(); cause != nil {
		t.Fatalf("nil receiver Unwrap() = %v, want nil", cause)
	}
	invalid = &InvalidRequiredAfterPlanningHookError{HookName: "governance", Reason: InvalidRequiredAfterPlanningStageDrift}
	if got := invalid.Error(); got != `invalid required after-planning hook "governance": stage_drift` {
		t.Fatalf("Error() = %q", got)
	}
	wrapped := fmt.Errorf("construction failed: %w", invalid)
	if !errors.Is(wrapped, ErrInvalidOrchestratorConfig) || !IsInvalidRequiredAfterPlanningHook(wrapped) {
		t.Fatalf("wrapped error lost classification: %v", wrapped)
	}
}

func TestRequiredAfterPlanningErrorPreservesCauseAndHandlesNil(t *testing.T) {
	cause := errors.New("governance unavailable")
	for _, test := range []struct {
		name string
		err  *RequiredAfterPlanningError
		text string
		want error
	}{
		{name: "nil receiver", text: "required after-planning hook failed"},
		{name: "nil cause", err: &RequiredAfterPlanningError{HookName: "governance"}, text: `required after-planning hook "governance" failed`},
		{name: "cause", err: &RequiredAfterPlanningError{HookName: "governance", Cause: cause}, text: `required after-planning hook "governance" failed: governance unavailable`, want: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.text {
				t.Fatalf("Error() = %q, want %q", got, test.text)
			}
			if got := test.err.Unwrap(); got != test.want {
				t.Fatalf("Unwrap() = %v, want %v", got, test.want)
			}
			if test.err != nil {
				wrapped := fmt.Errorf("request failed: %w", test.err)
				if !IsRequiredAfterPlanningError(wrapped) || (test.want != nil && !errors.Is(wrapped, test.want)) {
					t.Fatalf("wrapped error lost required-hook classification or cause: %v", wrapped)
				}
			}
		})
	}
}

type afterPlanningDecisionCapture struct {
	core.NoOpLogger
	reasons []string
}

func (l *afterPlanningDecisionCapture) WarnWithContext(
	_ context.Context,
	_ string,
	fields map[string]interface{},
) {
	if fields["operation"] != "after_planning_hook" {
		return
	}
	if reason, ok := fields["reason"].(string); ok {
		l.reasons = append(l.reasons, reason)
	}
}

func TestValidatedAfterPlanningHooks_OptionalFailuresRemainFailOpen(t *testing.T) {
	hookFailure := errors.New("optional hook failed")
	tests := []struct {
		name       string
		plan       func() *RoutingPlan
		apply      func(interface{}) (interface{}, error)
		wantReason string
		wantCalls  int
	}{
		{
			name: "clone failure",
			plan: func() *RoutingPlan {
				plan := validAfterPlanningContractPlan()
				plan.Steps[0].Metadata["unsupported"] = make(chan struct{})
				return plan
			},
			wantReason: "clone_failed",
		},
		{
			name: "hook error",
			plan: validAfterPlanningContractPlan,
			apply: func(plan interface{}) (interface{}, error) {
				return plan, hookFailure
			},
			wantReason: "hook_error",
			wantCalls:  1,
		},
		{
			name: "wrong result type",
			plan: validAfterPlanningContractPlan,
			apply: func(interface{}) (interface{}, error) {
				return "not-a-plan", nil
			},
			wantReason: "invalid_type",
			wantCalls:  1,
		},
		{
			name: "invalid plan",
			plan: validAfterPlanningContractPlan,
			apply: func(interface{}) (interface{}, error) {
				return &RoutingPlan{
					PlanID: "invalid",
					Steps:  []RoutingStep{{StepID: "step-1", AgentName: "missing-agent"}},
				}, nil
			},
			wantReason: "invalid_plan",
			wantCalls:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan()
			hook := &afterPlanningContractHook{name: "optional", apply: test.apply}
			logger := &afterPlanningDecisionCapture{}
			orchestrator := newAfterPlanningContractOrchestrator(t)
			orchestrator.pipelineHooks = []core.PipelineHook{hook}
			orchestrator.logger = logger
			holder := newPipelineHookExecutionHolder()
			ctx := withPipelineHookExecutionHolder(t.Context(), holder)

			result, err := orchestrator.runValidatedAfterPlanningHooks(
				ctx,
				&core.PipelineContext{Request: "request", Enrichments: map[string]interface{}{}},
				plan, nil, nil, 1, "request-id",
			)
			if err != nil {
				t.Fatalf("optional hook returned error: %v", err)
			}
			if result != plan {
				t.Fatalf("optional rejection returned plan %p, want prior plan %p", result, plan)
			}
			if hook.calls != test.wantCalls {
				t.Fatalf("hook calls = %d, want %d", hook.calls, test.wantCalls)
			}
			if len(logger.reasons) != 1 || logger.reasons[0] != test.wantReason {
				t.Fatalf("decision reasons = %#v, want %q", logger.reasons, test.wantReason)
			}
			assertAfterPlanningRejectionRecord(t, holder.Snapshot(), test.wantReason)
		})
	}
}

func TestValidatedAfterPlanningHooks_RequiredFailuresFailClosed(t *testing.T) {
	hookFailure := errors.New("required hook failed")
	tests := []struct {
		name       string
		plan       func() *RoutingPlan
		apply      func(interface{}) (interface{}, error)
		wantReason string
		wantCause  error
		wantCalls  int
	}{
		{
			name: "clone failure",
			plan: func() *RoutingPlan {
				plan := validAfterPlanningContractPlan()
				plan.Steps[0].Metadata["unsupported"] = make(chan struct{})
				return plan
			},
			wantReason: "clone_failed",
			wantCause:  errAfterPlanningCloneFailed,
		},
		{
			name: "hook error",
			plan: validAfterPlanningContractPlan,
			apply: func(plan interface{}) (interface{}, error) {
				return plan, hookFailure
			},
			wantReason: "hook_error",
			wantCause:  hookFailure,
			wantCalls:  1,
		},
		{
			name: "wrong result type",
			plan: validAfterPlanningContractPlan,
			apply: func(interface{}) (interface{}, error) {
				return map[string]interface{}{"not": "a plan"}, nil
			},
			wantReason: "invalid_type",
			wantCause:  errAfterPlanningInvalidResultType,
			wantCalls:  1,
		},
		{
			name: "invalid plan",
			plan: validAfterPlanningContractPlan,
			apply: func(interface{}) (interface{}, error) {
				return &RoutingPlan{
					PlanID: "invalid",
					Steps:  []RoutingStep{{StepID: "step-1", AgentName: "missing-agent"}},
				}, nil
			},
			wantReason: "invalid_plan",
			wantCause:  errAfterPlanningInvalidPlan,
			wantCalls:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hook := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{
				name:  "required",
				apply: test.apply,
			}}
			logger := &afterPlanningDecisionCapture{}
			orchestrator := newAfterPlanningContractOrchestrator(t)
			orchestrator.pipelineHooks = []core.PipelineHook{hook}
			orchestrator.logger = logger
			holder := newPipelineHookExecutionHolder()
			ctx := withPipelineHookExecutionHolder(t.Context(), holder)
			spanTelemetry := &pipelineHookSpanTelemetry{}
			orchestrator.telemetry = spanTelemetry

			result, err := orchestrator.runValidatedAfterPlanningHooks(
				ctx,
				&core.PipelineContext{Request: "request", Enrichments: map[string]interface{}{}},
				test.plan(), nil, nil, 1, "request-id",
			)
			if result != nil {
				t.Fatalf("required rejection returned a plan: %#v", result)
			}
			var typed *RequiredAfterPlanningError
			if !errors.As(err, &typed) || typed.HookName != "required" {
				t.Fatalf("required failure = %#v, %v", typed, err)
			}
			if !IsRequiredAfterPlanningError(err) || !errors.Is(err, test.wantCause) {
				t.Fatalf("required failure cause = %v, want %v", err, test.wantCause)
			}
			if hook.calls != test.wantCalls {
				t.Fatalf("hook calls = %d, want %d", hook.calls, test.wantCalls)
			}
			if len(logger.reasons) != 1 || logger.reasons[0] != test.wantReason {
				t.Fatalf("decision reasons = %#v, want %q", logger.reasons, test.wantReason)
			}
			assertAfterPlanningRejectionRecord(t, holder.Snapshot(), test.wantReason)
			if len(spanTelemetry.spans) != 1 || !spanTelemetry.spans[0].ended || len(spanTelemetry.spans[0].errors) != 1 {
				t.Fatalf("required rejection span = %+v", spanTelemetry.spans)
			}
			if got := spanTelemetry.spans[0].errors[0].Error(); got != holder.Snapshot()[0].Error {
				t.Fatalf("span error = %q, stored error = %q", got, holder.Snapshot()[0].Error)
			}
		})
	}
}

func assertAfterPlanningRejectionRecord(t *testing.T, records []PipelineHookExecution, reason string) {
	t.Helper()
	want := PipelineHookFailed
	if reason == "clone_failed" {
		want = PipelineHookSkipped
	}
	if len(records) != 1 || records[0].Status != want || records[0].Error == "" ||
		records[0].PlanPhase != 1 || records[0].Phase != PipelineHookPhaseAfterPlanning ||
		records[0].StartedAt.IsZero() || records[0].Duration < 0 {
		t.Fatalf("%s invocation record = %+v, want %s with exact error and timing", reason, records, want)
	}
}

func TestValidatedAfterPlanningHooks_RequiredSuccessContinuesWithGovernedPlan(t *testing.T) {
	hook := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{
		name: "required",
		apply: func(plan interface{}) (interface{}, error) {
			mutated := plan.(*RoutingPlan)
			mutated.PlanID = "governed-plan"
			return mutated, nil
		},
	}}
	orchestrator := newAfterPlanningContractOrchestrator(t)
	orchestrator.pipelineHooks = []core.PipelineHook{hook}
	original := validAfterPlanningContractPlan()

	result, err := orchestrator.runValidatedAfterPlanningHooks(
		context.Background(),
		&core.PipelineContext{Request: "request", Enrichments: map[string]interface{}{}},
		original, nil, nil, 1, "request-id",
	)
	if err != nil {
		t.Fatalf("required success returned error: %v", err)
	}
	if result == original || result.PlanID != "governed-plan" {
		t.Fatalf("governed result = %#v", result)
	}
	if original.PlanID != "original-plan" {
		t.Fatalf("required hook mutated the prior plan in place: %#v", original)
	}
}

type requiredBoundaryInterruptController struct {
	planApprovalCalls int
	beforeStepCalls   int
}

func (*requiredBoundaryInterruptController) SetPolicy(InterruptPolicy)          {}
func (*requiredBoundaryInterruptController) SetHandler(InterruptHandler)        {}
func (*requiredBoundaryInterruptController) SetCheckpointStore(CheckpointStore) {}
func (c *requiredBoundaryInterruptController) CheckPlanApproval(
	context.Context,
	*RoutingPlan,
) (*ExecutionCheckpoint, error) {
	c.planApprovalCalls++
	return nil, nil
}
func (c *requiredBoundaryInterruptController) CheckBeforeStep(
	context.Context,
	RoutingStep,
	*RoutingPlan,
) (*ExecutionCheckpoint, error) {
	c.beforeStepCalls++
	return nil, nil
}
func (*requiredBoundaryInterruptController) CheckAfterStep(
	context.Context,
	RoutingStep,
	*StepResult,
) (*ExecutionCheckpoint, error) {
	return nil, nil
}
func (*requiredBoundaryInterruptController) CheckOnError(
	context.Context,
	RoutingStep,
	error,
	int,
) (*ExecutionCheckpoint, error) {
	return nil, nil
}
func (*requiredBoundaryInterruptController) ProcessCommand(
	context.Context,
	*Command,
) (*ResumeResult, error) {
	return nil, nil
}
func (*requiredBoundaryInterruptController) ResumeExecution(
	context.Context,
	string,
) (*ExecutionResult, error) {
	return nil, nil
}
func (*requiredBoundaryInterruptController) UpdateCheckpointProgress(
	context.Context,
	string,
	[]StepResult,
) error {
	return nil
}

var _ InterruptController = (*requiredBoundaryInterruptController)(nil)

func TestRequiredAfterPlanningFailureStopsBeforeHITLAndExecution(t *testing.T) {
	hookFailure := errors.New("trusted binding unavailable")
	client := &promptCapturingAIClient{responses: []string{`{
		"plan_id":"planner-plan",
		"original_request":"request",
		"mode":"autonomous",
		"steps":[{
			"step_id":"step-1",
			"agent_name":"test-agent",
			"namespace":"default",
			"instruction":"run",
			"depends_on":[],
			"metadata":{"capability":"test_capability","parameters":{}}
		}],
		"terminal":true
	}`}}
	orchestrator := newAfterPlanningContractOrchestrator(t)
	orchestrator.aiClient = client
	orchestrator.synthesizer = NewAISynthesizer(client)
	orchestrator.config.HITL.Enabled = true
	required := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{
		name: "required",
		apply: func(plan interface{}) (interface{}, error) {
			return plan, hookFailure
		},
	}}
	orchestrator.pipelineHooks = []core.PipelineHook{required}
	controller := &requiredBoundaryInterruptController{}
	orchestrator.SetInterruptController(controller)
	transport := NewMockRoundTripper()
	orchestrator.executor.httpClient = &http.Client{Transport: transport}

	response, err := orchestrator.ProcessRequest(context.Background(), "request", nil)
	if response != nil {
		t.Fatalf("ProcessRequest() response = %#v, want nil", response)
	}
	if !IsRequiredAfterPlanningError(err) || !errors.Is(err, hookFailure) {
		t.Fatalf("ProcessRequest() error = %v", err)
	}
	if controller.planApprovalCalls != 0 || controller.beforeStepCalls != 0 {
		t.Fatalf("HITL calls = plan:%d step:%d, want zero", controller.planApprovalCalls, controller.beforeStepCalls)
	}
	if transport.GetCallCount() != 0 {
		t.Fatalf("tool HTTP calls = %d, want zero", transport.GetCallCount())
	}
	if client.callCount != 1 {
		t.Fatalf("AI calls = %d, want planner only", client.callCount)
	}
}

func newAfterPlanningContractOrchestrator(t *testing.T) *AIOrchestrator {
	t.Helper()
	orchestrator := setupTestOrchestrator(t, NewMockAIClient())
	if err := orchestrator.discovery.Register(t.Context(), &core.ServiceRegistration{
		ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
		Type: core.ComponentTypeTool,
	}); err != nil {
		t.Fatalf("register test agent: %v", err)
	}
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-agent": {
			Registration: &core.ServiceRegistration{
				ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
			},
			Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
		},
	}
	return orchestrator
}

func validAfterPlanningContractPlan() *RoutingPlan {
	return &RoutingPlan{
		PlanID:          "original-plan",
		OriginalRequest: "request",
		Mode:            ModeAutonomous,
		Steps: []RoutingStep{{
			StepID:      "step-1",
			AgentName:   "test-agent",
			Namespace:   "default",
			Instruction: "run",
			Metadata: map[string]interface{}{
				"capability": "test_capability",
				"parameters": map[string]interface{}{},
			},
		}},
	}
}

type requiredHookStreamingClient struct {
	*promptCapturingAIClient
	native      bool
	streamCalls int
}

func (c *requiredHookStreamingClient) SupportsStreaming() bool { return c.native }

func (c *requiredHookStreamingClient) StreamResponse(
	context.Context, string, *core.AIOptions, core.StreamCallback,
) (*core.AIResponse, error) {
	c.streamCalls++
	return nil, errors.New("synthesis must not run after required rejection")
}

type requiredHookRetentionStore struct {
	*NoOpLLMDebugStore
	mu   sync.Mutex
	ttls map[string]time.Duration
}

func (s *requiredHookRetentionStore) PreserveRetention(_ context.Context, id string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttls[id] = ttl
	return nil
}

func (s *requiredHookRetentionStore) ttl(id string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttls[id]
}

func newRequiredHookRecordingFixture(t *testing.T, responses []string) (
	*AIOrchestrator, *requiredHookStreamingClient, *MockRoundTripper, *terminalRecordStore, *requiredHookRetentionStore,
) {
	t.Helper()
	o := newAfterPlanningContractOrchestrator(t)
	client := &requiredHookStreamingClient{promptCapturingAIClient: &promptCapturingAIClient{responses: responses}}
	o.aiClient = client
	o.synthesizer = NewAISynthesizer(client)
	o.config.IterativePlanning.MaxPhases = 3
	o.config.IterativePlanning.MaxTotalSteps = 10
	o.config.ExecutionStore.TTL = 2 * time.Hour
	o.config.ExecutionStore.ErrorTTL = 13 * time.Hour
	transport := NewMockRoundTripper()
	transport.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result":"completed first tool"}`)
	o.executor.httpClient = &http.Client{Transport: transport}
	store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
	debug := &requiredHookRetentionStore{NoOpLLMDebugStore: NewNoOpLLMDebugStore(), ttls: make(map[string]time.Duration)}
	o.executionStore = store
	o.debugStore = debug
	t.Cleanup(func() { shutdownRequiredHookFixture(t, o) })
	return o, client, transport, store, debug
}

func shutdownRequiredHookFixture(t *testing.T, o *AIOrchestrator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := o.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestRequiredAfterPlanningFailureRetainsExecutionEvidence(t *testing.T) {
	for _, mode := range []string{"buffered", "native", "simulated"} {
		for _, rejectPhase := range []int{1, 2} {
			for _, recording := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/phase_%d/recording_%t", mode, rejectPhase, recording), func(t *testing.T) {
					o, client, transport, store, debug := newRequiredHookRecordingFixture(t, []string{
						foundationNonTerminalSingleStepPlan("phase-1", "step-1"),
						foundationNonTerminalSingleStepPlan("phase-2", "step-2"),
					})
					client.native = mode == "native"
					if !recording {
						o.executionStore, o.debugStore = nil, nil
					}
					o.config.HITL.Enabled = true
					controller := &requiredBoundaryInterruptController{}
					o.SetInterruptController(controller)
					failure := errors.New("required hook unavailable")
					var calls int
					hook := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{
						name: "governance", apply: func(plan interface{}) (interface{}, error) {
							calls++
							if calls == rejectPhase {
								return nil, failure
							}
							return plan, nil
						},
					}}
					postHook := &terminalHook{}
					o.pipelineHooks = []core.PipelineHook{hook, postHook}
					var err error
					if mode == "buffered" {
						response, requestErr := o.ProcessRequest(t.Context(), "request", nil)
						err = requestErr
						if response != nil {
							t.Fatal("rejected request returned a response")
						}
					} else {
						response, requestErr := o.ProcessRequestStreaming(t.Context(), "request", nil, func(chunk core.StreamChunk) error {
							if chunk.Metadata["type"] != "phase_complete" {
								t.Errorf("unexpected response chunk after rejection: %+v", chunk)
							}
							return nil
						})
						err = requestErr
						if response != nil {
							t.Fatal("rejected stream returned a final response")
						}
					}
					shutdownRequiredHookFixture(t, o)
					if !IsRequiredAfterPlanningError(err) || !errors.Is(err, failure) {
						t.Fatalf("request error = %v", err)
					}
					if transport.GetCallCount() != rejectPhase-1 || controller.planApprovalCalls != rejectPhase-1 ||
						controller.beforeStepCalls != rejectPhase-1 || client.callCount != rejectPhase || client.streamCalls != 0 ||
						postHook.AfterExecutionDone() || postHook.Finished() {
						t.Fatalf("protected work ran: tool=%d HITL=%+v AI=%d stream=%d post=%t/%t", transport.GetCallCount(), controller,
							client.callCount, client.streamCalls, postHook.AfterExecutionDone(), postHook.Finished())
					}
					records, _ := store.snapshot()
					if !recording {
						if len(records) != 0 {
							t.Fatal("disabled recording wrote an execution")
						}
						return
					}
					if len(records) != rejectPhase {
						t.Fatalf("record count = %d, want %d", len(records), rejectPhase)
					}
					last := records[len(records)-1]
					assertRequiredHookFailureSnapshot(t, last, rejectPhase, failure.Error())
					if rejectPhase == 2 && !reflect.DeepEqual(records[0].Result.Steps, last.Result.Steps) {
						t.Fatal("terminal write changed the completed phase's step data")
					}
					if got := debug.ttl(last.RequestID); got != o.config.ExecutionStore.ErrorTTL {
						t.Fatalf("recorder supplied LLM retention = %s, want %s", got, o.config.ExecutionStore.ErrorTTL)
					}
					assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
				})
			}
		}
	}
}

func assertRequiredHookFailureSnapshot(t *testing.T, record *StoredExecution, rejectPhase int, hookError string) {
	t.Helper()
	wantCompleted := rejectPhase - 1
	if record.Result == nil || record.Result.Success || record.Result.TotalDuration <= 0 ||
		len(record.Result.Steps) != wantCompleted || record.PhaseCount != wantCompleted ||
		len(record.PhasePlans) != wantCompleted || record.Interrupted || record.FinalResponse != nil {
		t.Fatalf("failed terminal snapshot lost state: %+v", record)
	}
	if len(record.PipelineHooks) != rejectPhase {
		t.Fatalf("hook records = %+v", record.PipelineHooks)
	}
	for index, hook := range record.PipelineHooks {
		wantStatus, wantError := PipelineHookSucceeded, ""
		wantDecision := PipelineHookDecision{PipelineHookFailClosed, PipelineHookContinue, "accepted"}
		if index == rejectPhase-1 {
			wantStatus, wantError = PipelineHookFailed, hookError
			wantDecision.Action, wantDecision.Reason = PipelineHookTerminate, "hook_error"
		}
		assertPipelineHookDecision(t, hook.Decision, wantDecision)
		if hook.Status != wantStatus || hook.Error != wantError || hook.PlanPhase != index+1 || hook.Sequence != 1 || hook.StartedAt.IsZero() {
			t.Fatalf("hook %d = %+v", index, hook)
		}
	}
	if wantCompleted == 0 {
		if record.Plan != nil || record.Result.PlanID != "" {
			t.Fatal("rejected first plan was persisted as accepted")
		}
	} else if record.Plan == nil || record.Plan.PlanID != "phase-1" || record.Result.PlanID != "phase-1" ||
		!record.Result.Steps[0].Success || record.Result.Steps[0].StepID != "step-1" || record.Result.Steps[0].Response == "" {
		t.Fatal("earlier successful phase was lost or rejected candidate was admitted")
	}
}

func assertRequiredHookStoreRetention(t *testing.T, record *StoredExecution, config ExecutionStoreConfig, want time.Duration) {
	t.Helper()
	provider := newMockStorageProvider()
	store := NewExecutionStoreWithProvider(provider, config, nil)
	if err := store.Store(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	provider.mu.RLock()
	got := provider.ttls[config.KeyPrefix+record.RequestID]
	provider.mu.RUnlock()
	if got != want {
		t.Fatalf("provider TTL = %s, want %s", got, want)
	}
	mr, redisStore := newRedisExecutionConversationTestStore(t, config)
	if err := redisStore.Store(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if got := mr.TTL(redisStore.recordKey(record.RequestID)); got != want {
		t.Fatalf("Redis adapter TTL = %s, want %s", got, want)
	}
	for name, backend := range map[string]ExecutionStore{"provider": store, "redis": redisStore} {
		stored, err := backend.Get(t.Context(), record.RequestID)
		if err != nil || !reflect.DeepEqual(stored, record) {
			t.Fatalf("%s stored record differs from submitted snapshot: %v", name, err)
		}
	}
}

func TestAfterPlanningSuccessfulRequestKeepsNormalRetention(t *testing.T) {
	for _, required := range []bool{true, false} {
		t.Run(fmt.Sprintf("required_%t", required), func(t *testing.T) {
			o, _, transport, store, debug := newRequiredHookRecordingFixture(t, []string{foundationTerminalSingleStepPlan, "final answer"})
			hook := afterPlanningContractHook{name: "plan-hook", applyContext: func(ctx context.Context, plan interface{}) (interface{}, error) {
				if err := core.ReportPipelineHookEffect(ctx, core.PipelineHookEffect{
					EffectID: "diagnostic", Name: "Exact evidence", Status: core.PipelineHookEffectSucceeded,
					Data: json.RawMessage(`{"input":"exact hook data"}`),
				}); err != nil {
					return nil, err
				}
				if !required {
					return nil, errors.New("optional enrichment unavailable")
				}
				plan.(*RoutingPlan).PlanID = "governed-plan"
				return plan, nil
			}}
			if required {
				o.pipelineHooks = []core.PipelineHook{&requiredAfterPlanningContractHook{afterPlanningContractHook: hook}}
			} else {
				o.pipelineHooks = []core.PipelineHook{&hook}
			}
			if _, err := o.ProcessRequest(t.Context(), "request", nil); err != nil {
				t.Fatal(err)
			}
			shutdownRequiredHookFixture(t, o)
			records, _ := store.snapshot()
			if len(records) == 0 || transport.GetCallCount() != 1 {
				t.Fatalf("records=%d tool calls=%d", len(records), transport.GetCallCount())
			}
			last := records[len(records)-1]
			if last.Result == nil || !last.Result.Success || len(last.PipelineHooks) != 1 {
				t.Fatalf("unexpected final execution: %+v", last)
			}
			wantStatus, wantError := PipelineHookSucceeded, ""
			if !required {
				wantStatus, wantError = PipelineHookFailed, "optional enrichment unavailable"
			}
			got := last.PipelineHooks[0]
			wantDecision := PipelineHookDecision{PipelineHookFailClosed, PipelineHookContinue, "accepted"}
			if !required {
				wantDecision.FailurePolicy, wantDecision.Reason = PipelineHookFailOpen, "hook_error"
			}
			assertPipelineHookDecision(t, got.Decision, wantDecision)
			if got.Status != wantStatus || got.Error != wantError || len(got.Effects) != 1 || string(got.Effects[0].Data) != `{"input":"exact hook data"}` {
				t.Fatalf("hook evidence = %+v", got)
			}
			if required && last.Plan.PlanID != "governed-plan" {
				t.Fatal("accepted required mutation was not executed")
			}
			if debug.ttl(last.RequestID) != o.config.ExecutionStore.TTL {
				t.Fatal("successful request received error retention")
			}
			assertRequiredHookStoreRetention(t, last, o.config.ExecutionStore, o.config.ExecutionStore.TTL)
		})
	}
}

func TestRequiredAfterPlanningAsyncEffectPreservesFailedSnapshot(t *testing.T) {
	o, _, _, _, debug := newRequiredHookRecordingFixture(t, []string{
		foundationNonTerminalSingleStepPlan("phase-1", "step-1"),
		foundationNonTerminalSingleStepPlan("phase-2", "step-2"),
	})
	store := &pipelineHookCaptureExecutionStore{NoOpExecutionStore: NewNoOpExecutionStore(), records: make(chan *StoredExecution, 3)}
	o.executionStore = store
	failure := errors.New("second phase rejected")
	var hookContext context.Context
	hook := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{
		name: "required", applyContext: func(ctx context.Context, plan interface{}) (interface{}, error) {
			if plan.(*RoutingPlan).PlanID == "phase-1" {
				return plan, nil
			}
			hookContext = ctx
			if err := core.ReportPipelineHookEffect(ctx, core.PipelineHookEffect{
				EffectID: "background", Name: "Background diagnostic", Status: core.PipelineHookEffectPending,
			}); err != nil {
				return nil, err
			}
			return nil, failure
		},
	}}
	o.pipelineHooks = []core.PipelineHook{hook}
	_, err := o.ProcessRequest(t.Context(), "request", nil)
	if !errors.Is(err, failure) {
		t.Fatalf("request error = %v", err)
	}
	readRecord := func() *StoredExecution {
		t.Helper()
		select {
		case record := <-store.records:
			return record
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for execution write")
			return nil
		}
	}
	_ = readRecord() // Completed phase 1.
	initial := readRecord()
	if len(initial.PipelineHooks) != 2 || len(initial.PipelineHooks[1].Effects) != 1 || initial.PipelineHooks[1].Effects[0].Status != core.PipelineHookEffectPending {
		t.Fatalf("pending effect was not captured: %+v", initial.PipelineHooks)
	}
	if err := core.ReportPipelineHookEffect(hookContext, core.PipelineHookEffect{
		EffectID: "background", Name: "Background diagnostic", Status: core.PipelineHookEffectFailed,
		Error: "exact diagnostic failure", Data: json.RawMessage(`{"detail":"not redacted"}`),
	}); err != nil {
		t.Fatal(err)
	}
	updated := readRecord()
	shutdownRequiredHookFixture(t, o)
	assertRequiredHookFailureSnapshot(t, initial, 2, failure.Error())
	assertRequiredHookFailureSnapshot(t, updated, 2, failure.Error())
	if !reflect.DeepEqual(initial.Result, updated.Result) || !reflect.DeepEqual(initial.PhasePlans, updated.PhasePlans) {
		t.Fatal("async effect changed the terminal execution result")
	}
	effect := updated.PipelineHooks[1].Effects[0]
	if effect.Status != core.PipelineHookEffectFailed || effect.Error != "exact diagnostic failure" || string(effect.Data) != `{"detail":"not redacted"}` {
		t.Fatalf("completed effect = %+v", effect)
	}
	if debug.ttl(updated.RequestID) != o.config.ExecutionStore.ErrorTTL {
		t.Fatal("async effect rewrite lost failed-request retention")
	}
	assertRequiredHookStoreRetention(t, updated, o.config.ExecutionStore, o.config.ExecutionStore.ErrorTTL)
}

func TestRequiredAfterPlanningFailurePreservesRegenerationEvidence(t *testing.T) {
	// Malformed template syntax reaches the phase-loop validation gauntlet;
	// duplicate IDs are repaired inside the continuation generator instead.
	invalidCandidate := strings.Replace(
		foundationNonTerminalSingleStepPlan("malformed-template-plan", "step-2"),
		`"parameters":{}`, `"parameters":{"input":"{{step-1}}"}`, 1,
	)
	o, client, transport, store, _ := newRequiredHookRecordingFixture(t, []string{
		foundationNonTerminalSingleStepPlan("phase-1", "step-1"),
		invalidCandidate,
		foundationNonTerminalSingleStepPlan("phase-2", "step-2"),
	})
	failure := errors.New("regenerated candidate rejected")
	hook := &requiredAfterPlanningContractHook{afterPlanningContractHook: afterPlanningContractHook{
		name: "required", apply: func(plan interface{}) (interface{}, error) {
			if plan.(*RoutingPlan).PlanID == "phase-2" {
				return nil, failure
			}
			return plan, nil
		},
	}}
	o.pipelineHooks = []core.PipelineHook{hook}
	_, err := o.ProcessRequest(t.Context(), "request", nil)
	shutdownRequiredHookFixture(t, o)
	if !errors.Is(err, failure) || client.callCount != 3 || hook.calls != 2 || transport.GetCallCount() != 1 {
		t.Fatalf("error=%v AI=%d hooks=%d tools=%d", err, client.callCount, hook.calls, transport.GetCallCount())
	}
	records, _ := store.snapshot()
	if len(records) != 2 {
		t.Fatalf("stored records = %d, want 2", len(records))
	}
	last := records[len(records)-1]
	assertRequiredHookFailureSnapshot(t, last, 2, failure.Error())
	var events []map[string]interface{}
	if err := json.Unmarshal([]byte(last.Metadata[MetadataKeyPlanRegenerations]), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0]["original_plan_id"] != "malformed-template-plan" ||
		events[0]["regenerated_plan_id"] != "phase-2" || events[0]["validation_error"] == "" {
		t.Fatalf("regeneration evidence = %+v", events)
	}
}
