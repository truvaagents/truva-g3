package orchestration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestResourceSelectorDeadlineRecovery(t *testing.T) {
	for _, scenario := range []string{"recover", "exhaust", "parent_cancel", "answer_at_deadline", "provider_error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &compactorMockAI{}
			client.generateFunc = func(callCtx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
				if scenario == "provider_error" {
					return nil, errors.New("unavailable")
				}
				if scenario == "parent_cancel" {
					cancel()
				}
				if client.calls == 1 || scenario == "exhaust" {
					<-callCtx.Done()
					if scenario != "answer_at_deadline" {
						return nil, callCtx.Err()
					}
				}
				return &core.AIResponse{Content: "```json\n{\"selected_resources\":[]}\n```"}, nil
			}
			runtime := &skillRuntime{aiClient: client, config: SkillConfig{Limits: SkillRuntimeLimits{ResourceSelectionAttemptTimeout: time.Millisecond}}}
			state := &SkillExecutionState{}
			_, err := runtime.resolveResourcesWithAI(ctx, SkillResourceResolutionInput{Boundary: SkillBoundaryContinuation, PhaseNumber: 2}, state)
			switch scenario {
			case "recover":
				if err != nil || client.calls != 2 || len(state.Diagnostics) != 1 || state.Diagnostics[0].Action != "retry" {
					t.Fatalf("calls=%d diagnostics=%+v err=%v", client.calls, state.Diagnostics, err)
				}
			case "exhaust":
				if !errors.Is(err, context.DeadlineExceeded) || client.calls != 2 || len(state.Diagnostics) != 2 {
					t.Fatalf("calls=%d diagnostics=%+v err=%v", client.calls, state.Diagnostics, err)
				}
			case "parent_cancel":
				if !errors.Is(err, context.Canceled) || client.calls != 1 {
					t.Fatalf("calls=%d err=%v", client.calls, err)
				}
			case "answer_at_deadline":
				if err != nil || client.calls != 1 {
					t.Fatalf("valid answer discarded: calls=%d err=%v", client.calls, err)
				}
			case "provider_error":
				if err == nil || client.calls != 1 {
					t.Fatalf("provider error retried: calls=%d err=%v", client.calls, err)
				}
			}
		})
	}
}

func TestResourceSelectionNilState(t *testing.T) {
	runtime := &skillRuntime{}
	projected := []projectedSkill{{manifest: SkillManifest{Resources: []SkillResourceMetadata{{Name: "detail"}}}}}
	if len(eligibleSkillResourceEntries(projected, SkillResourceContinuation)) != 1 {
		t.Fatal("test must exercise eligible resources")
	}
	got, err := runtime.selectAndAdmitResources(t.Context(), "request", SkillBoundaryContinuation, 1, nil, projected, SkillSelectorContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, projected) {
		t.Fatal("nil state changed the projection")
	}
}

func TestResourceFenceStrictness(t *testing.T) {
	valid := `{"selected_resources":[]}`
	for _, body := range []string{valid, "```json\n" + valid + "\n```", "```\r\n" + valid + "\r\n```"} {
		if _, err := parseSkillResourceSelection(body, nil); err != nil {
			t.Fatalf("valid envelope: %v", err)
		}
	}
	for _, body := range []string{"text\n```json\n" + valid + "\n```", "```json\n" + valid + "\n```\ntext", "```json\n" + valid + valid + "\n```", "```json\n{\"selected_resources\":[],\"other\":1}\n```", "```json\n{\"selected_resources\":[],\"selected_resources\":[]}\n```"} {
		if _, err := parseSkillResourceSelection(body, nil); err == nil {
			t.Fatalf("invalid envelope accepted: %s", body)
		}
	}
	inner := `{"selected_resources":[],"literal":"**x** [1,2,]"}`
	if got := unwrapSkillResourceJSONFence("```json\n" + inner + "\n```"); got != inner {
		t.Fatalf("payload rewritten: %s", got)
	}
}

// Saved R5 coordinator response, 2026-09-27T21:16:49.70905378Z,
// maintenance-case-1790542428711248347, phase 6. Only the branch/case reference
// is replaced; envelope, resource identities and reasoning text are retained.
func TestResourceSelectorSavedR5Fence(t *testing.T) {
	body := "```json\n" + `{"selected_resources":[{"namespace":"software-maintenance","name":"draft-pr-reporting","resource":"publication-report-checklist","reason":"The synthesis must state the final draft pull-request outcome, and step-14 recorded a published draft PR (branch diagnostic-case against main); the checklist verifies the publication statement matches the recorded effect."},{"namespace":"software-maintenance","name":"remediation-coordination","resource":"receipt-dependency-guide","reason":"The synthesis must present an evidence-supported disposition that correctly relates the candidate, verification, review, and publication receipts for the remediation effects; the guide explains those evidence relationships needed for an accurate final report."}]}` + "\n```"
	entries := []SkillResourceCatalogEntry{
		{Skill: SkillVersionRef{Ref: SkillRef{Namespace: "software-maintenance", Name: "draft-pr-reporting"}}, Resource: SkillResourceMetadata{Name: "publication-report-checklist"}},
		{Skill: SkillVersionRef{Ref: SkillRef{Namespace: "software-maintenance", Name: "remediation-coordination"}}, Resource: SkillResourceMetadata{Name: "receipt-dependency-guide"}},
	}
	client := &compactorMockAI{generateFunc: func(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
		return &core.AIResponse{Content: body}, nil
	}}
	runtime := &skillRuntime{aiClient: client}
	state := &SkillExecutionState{}
	decision, err := runtime.resolveResourcesWithAI(t.Context(), SkillResourceResolutionInput{Resources: entries, Boundary: SkillBoundarySynthesis, PhaseNumber: 6}, state)
	if err != nil || client.calls != 1 || len(decision.Select) != 2 || len(state.Diagnostics) != 0 {
		t.Fatalf("saved valid answer retried or rejected: calls=%d decision=%+v diagnostics=%+v err=%v", client.calls, decision, state.Diagnostics, err)
	}
	if _, err := parseSkillResourceSelection(body, entries[:1]); err == nil {
		t.Fatal("fence support bypassed catalog authorization")
	}
}

func TestResourceDeadlineConfiguration(t *testing.T) {
	const key = "TRUVAG3_SKILL_RESOURCE_SELECTION_ATTEMPT_TIMEOUT"
	for _, mode := range []EnvironmentMode{EnvironmentStrict, EnvironmentCompatible, EnvironmentDisabled} {
		for _, value := range []string{"30s", "0s", "-1s", "invalid", ""} {
			result, err := ResolveOrchestratorConfig(ConfigResolution{Environment: mode, LookupEnv: lookupFromMap(map[string]string{key: value})})
			invalid := value != "30s" && value != "0s" && mode != EnvironmentDisabled
			if (err != nil) != invalid {
				t.Fatalf("mode=%v value=%q err=%v", mode, value, err)
			}
			if err == nil && mode != EnvironmentDisabled && value == "30s" && result.Config.Skills.Limits.ResourceSelectionAttemptTimeout != 30*time.Second {
				t.Fatal("limit not applied")
			}
		}
	}
	result, err := ResolveOrchestratorConfig(ConfigResolution{Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{key: "invalid"}),
		Options:   []OrchestratorOption{WithSkills(SkillConfig{Limits: SkillRuntimeLimits{ResourceSelectionAttemptTimeout: time.Second}})},
	})
	if err != nil || result.Config.Skills.Limits.ResourceSelectionAttemptTimeout != time.Second {
		t.Fatalf("code precedence: %v", err)
	}
	limits := defaultSkillRuntimeLimits()
	limits.ResourceSelectionAttemptTimeout = -time.Second
	if err := validateSkillRuntimeLimits(limits); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("negative limit: %v", err)
	}
}

func TestOptionalResourceTimeoutReportsOmitted(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "detail", Description: "Weather detail", LoadWhen: "Weather detail requested",
		ContentType: "text/plain", Content: "Check forecast timing.",
	})
	client := &compactorMockAI{generateFunc: func(ctx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, nil)
	runtime.config.Limits.ResourceSelectionAttemptTimeout = time.Millisecond
	capture := &mockTelemetry{}
	runtime.telemetry = capture
	state, _, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "weather details", nil, 1)
	if err != nil {
		t.Fatalf("optional omission failed request: %v", err)
	}
	if !hasRuntimeSkillDiagnostic(state.Diagnostics, "skill_resource_selection_timeout") {
		t.Fatal("missing timeout diagnostic")
	}
	assertResourceTimeoutDiagnostics(t, state, 2, 1)
	if client.calls != 2 {
		t.Fatalf("expected exactly two attempts, got %d", client.calls)
	}
	observations := 0
	for _, record := range capture.metricRecords {
		if record.name == skillOperationTotalMetric && record.labels["stage"] == "resource_selection" {
			observations++
			if record.labels["outcome"] != "omitted" {
				t.Fatalf("false selected outcome: %v", record.labels)
			}
		}
	}
	if observations != 1 {
		t.Fatalf("expected one resource operation observation, got %d", observations)
	}
}

func assertResourceTimeoutDiagnostics(t *testing.T, state SkillExecutionState, attempts, omissions int) {
	t.Helper()
	for view, diagnostics := range map[string][]SkillDiagnostic{"state": state.Diagnostics, "debug": state.Debug.Diagnostics} {
		var gotAttempts, gotOmissions int
		for _, diagnostic := range diagnostics {
			if diagnostic.Action == "optional_resource_selection_skipped" {
				gotOmissions++
			} else if diagnostic.Code == "skill_resource_selection_timeout" {
				gotAttempts++
				if diagnostic.Detail == "" || (diagnostic.Action != "retry" && diagnostic.Action != "attempt_failed") {
					t.Fatalf("%s: missing attempt evidence: %+v", view, diagnostic)
				}
			}
		}
		if gotAttempts != attempts || gotOmissions != omissions {
			t.Fatalf("%s: attempts=%d omissions=%d, want %d and %d: %+v", view, gotAttempts, gotOmissions, attempts, omissions, diagnostics)
		}
	}
}

func TestOptionalResourceBoundaryCancellationAndEmptySelection(t *testing.T) {
	for _, scenario := range []string{"parent_cancel", "parent_deadline", "empty", "recover_empty"} {
		t.Run(scenario, func(t *testing.T) {
			binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways}
			registry := activationRegistryForBindings(t, []SkillBinding{binding})
			addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
				Name: "detail", Description: "Weather detail", LoadWhen: "Weather detail requested",
				ContentType: "text/plain", Content: "Check forecast timing.",
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "parent_deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
			}
			client := &compactorMockAI{}
			client.generateFunc = func(callCtx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
				switch scenario {
				case "parent_cancel":
					cancel()
					return nil, callCtx.Err()
				case "parent_deadline":
					<-callCtx.Done()
					return nil, callCtx.Err()
				case "recover_empty":
					if client.calls == 1 {
						<-callCtx.Done()
						return nil, callCtx.Err()
					}
				}
				return &core.AIResponse{Content: `{"selected_resources":[]}`}, nil
			}
			runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, nil)
			runtime.config.Limits.ResourceSelectionAttemptTimeout = time.Millisecond
			if scenario == "parent_deadline" {
				runtime.config.Limits.ResourceSelectionAttemptTimeout = time.Minute
			}
			capture := &mockTelemetry{}
			runtime.telemetry = capture
			state, _, err := runtime.PinCandidates(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, state, err = runtime.prepareInitialBoundary(ctx, state, "weather details", nil, 1)
			wantCalls, wantAttempts, wantOutcome := 1, 0, "noop"
			switch scenario {
			case "parent_cancel", "parent_deadline":
				if !errors.Is(err, ctx.Err()) || err == nil {
					t.Fatalf("parent error swallowed by fail-open: %v", err)
				}
				wantOutcome = "error"
			case "recover_empty":
				wantCalls, wantAttempts = 2, 1
				fallthrough
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if client.calls != wantCalls {
				t.Fatalf("calls=%d, want %d", client.calls, wantCalls)
			}
			assertResourceTimeoutDiagnostics(t, state, wantAttempts, 0)
			observations := 0
			for _, record := range capture.metricRecords {
				if record.name == skillOperationTotalMetric && record.labels["stage"] == "resource_selection" {
					observations++
					if record.labels["outcome"] != wantOutcome {
						t.Fatalf("outcome=%q, want %q", record.labels["outcome"], wantOutcome)
					}
				}
			}
			if observations != 1 {
				t.Fatalf("operation counted %d times", observations)
			}
		})
	}
}
