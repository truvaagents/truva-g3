package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestPlanningEntryPointsPrepareNamedLifecycleBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		want     orchestrationBoundary
		invoke   func(context.Context, *AIOrchestrator) error
		wantText string
	}{
		{
			name: "initial planning", want: boundaryInitialPlanning,
			invoke: func(ctx context.Context, o *AIOrchestrator) error {
				_, err := o.generateExecutionPlan(ctx, "request", "request-id")
				return err
			},
			wantText: "AI client not configured",
		},
		{
			name: "continuation planning", want: boundaryContinuationPlanning,
			invoke: func(ctx context.Context, o *AIOrchestrator) error {
				_, err := o.generateContinuationPlan(ctx, "request", "request-id", nil, nil, "", 2)
				return err
			},
			wantText: "AI client not configured",
		},
		{
			name: "initial regeneration", want: boundaryRegeneration,
			invoke: func(ctx context.Context, o *AIOrchestrator) error {
				_, err := o.regeneratePlan(ctx, "request", "request-id", errors.New("invalid"))
				return err
			},
			wantText: "AI client not configured for plan regeneration",
		},
		{
			name: "continuation regeneration", want: boundaryRegeneration,
			invoke: func(ctx context.Context, o *AIOrchestrator) error {
				_, err := o.regenerateContinuationPlan(
					ctx, "request", "request-id", nil, nil, "", 2, errors.New("invalid"), nil,
				)
				return err
			},
			wantText: "AI client not configured for plan regeneration",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []orchestrationBoundary
			ctx := withBoundaryPreparer(context.Background(), func(ctx context.Context, boundary orchestrationBoundary) (context.Context, error) {
				got = append(got, boundary)
				return ctx, nil
			})
			err := test.invoke(ctx, &AIOrchestrator{})
			if err == nil || err.Error() != test.wantText {
				t.Fatalf("entry-point error = %v, want %q", err, test.wantText)
			}
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("prepared boundaries = %v, want [%v]", got, test.want)
			}
		})
	}
}

func TestPhaseCoordinatorPreparationCapturesLifecycleSnapshot(t *testing.T) {
	state := &executionRunState{}
	snapshot := newExecutionRunSnapshot(
		2,
		map[string]*StepResult{"step-1": {StepID: "step-1"}},
		[]string{"step-1"},
		"continue",
	)
	ctx := withExecutionRunSnapshot(context.Background(), snapshot)

	preparation, err := (phaseCoordinator{}).prepareBoundary(ctx, boundaryResume, state)
	if err != nil {
		t.Fatalf("prepareBoundary() error = %v", err)
	}
	if preparation.Boundary != boundaryResume || preparation.Snapshot.PhaseNumber != 2 ||
		preparation.Snapshot.ContinuationNote != "continue" {
		t.Fatalf("preparation = %+v", preparation)
	}
	if len(state.Phase.PreparationHistory) != 1 || state.Phase.PreparationHistory[0] != boundaryResume {
		t.Fatalf("preparation history = %v", state.Phase.PreparationHistory)
	}
}

func TestSynthesisCoordinatorPreparesBoundaryBeforeDeliveryValidation(t *testing.T) {
	var got []orchestrationBoundary
	ctx := withBoundaryPreparer(context.Background(), func(ctx context.Context, boundary orchestrationBoundary) (context.Context, error) {
		got = append(got, boundary)
		return ctx, nil
	})
	state := &executionRunState{
		Context: ctx,
		Input:   requestRunInput{Delivery: responseDelivery(255)},
	}

	result, err := (synthesisCoordinator{orchestrator: &AIOrchestrator{}}).Run(state)
	if result != nil || !errors.Is(err, ErrInvalidOrchestratorConfig) {
		t.Fatalf("Run() = (%v, %v), want (nil, ErrInvalidOrchestratorConfig)", result, err)
	}
	if len(got) != 1 || got[0] != boundarySynthesis {
		t.Fatalf("prepared boundaries = %v, want [%v]", got, boundarySynthesis)
	}
}

func TestBoundaryPreparationHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (phaseCoordinator{}).prepareBoundary(ctx, boundaryInitialPlanning, &executionRunState{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareBoundary() error = %v, want context.Canceled", err)
	}
}

func TestBoundaryPreparationErrorsRemainClassifiableForRetrySuppression(t *testing.T) {
	want := newSkillDomainError(ErrSkillIntegrity, "load required manifest", SkillRef{
		Namespace: "travel", Name: "weather",
	})
	ctx := withBoundaryPreparer(t.Context(), func(ctx context.Context, _ orchestrationBoundary) (context.Context, error) {
		return ctx, want
	})

	_, err := prepareOrchestrationBoundary(ctx, boundaryInitialPlanning)
	var preparationErr *boundaryPreparationError
	if !errors.As(err, &preparationErr) || preparationErr.boundary != boundaryInitialPlanning ||
		!errors.Is(err, ErrSkillIntegrity) {
		t.Fatalf("boundary error = %v, want typed initial-planning integrity failure", err)
	}
}

func TestPhaseCoordinatorPersistsBoundaryFailureEvidenceWithoutPlannerRetry(t *testing.T) {
	manifest, _ := cacheTestSkillContent(t, "required-boundary-failure")
	corrupt := cloneSkillManifest(manifest)
	corrupt.PlanningInstructions[0] += " corrupted"
	upstream := &cacheTestSkillRegistry{
		manifest: manifest, manifestResponses: []SkillManifest{corrupt, corrupt},
	}
	registry, err := NewImmutableCachedSkillRegistry(upstream, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{{
		Namespace: manifest.Ref.Ref.Namespace, Name: manifest.Ref.Ref.Name,
		Version: "published", Activation: SkillActivationAlways, Required: true,
	}}
	runtime, err := newSkillRuntime(config, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	skillState, cacheContext, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	holder := newSkillExecutionStateHolder(skillState, cacheContext)
	ctx := withSkillExecutionHolder(t.Context(), holder)
	runState := &executionRunState{
		Input: requestRunInput{Request: "weather"}, Context: ctx,
		StartedAt: time.Now(), Correlation: requestCorrelation{RequestID: "required-boundary-failure"},
		Pipeline: &core.PipelineContext{}, SkillState: &skillState,
	}
	store := &terminalRecordStore{}
	orchestrator := &AIOrchestrator{
		config: config, skillRuntime: runtime, executor: &SmartExecutor{}, executionStore: store,
	}

	result, err := (phaseCoordinator{orchestrator: orchestrator}).Run(ctx, runState, nil)
	if result != nil || !errors.Is(err, ErrSkillUnavailable) || !errors.Is(err, ErrSkillIntegrity) {
		t.Fatalf("phase result = %#v, error = %v", result, err)
	}
	if upstream.manifestCalls != 2 {
		t.Fatalf("manifest reads = %d, want exact initial read plus one integrity reread", upstream.manifestCalls)
	}
	if runState.Debug.Skills == nil || len(runState.Debug.Skills.ContentLoads) != 1 ||
		runState.Debug.Skills.ContentLoads[0].DiagnosticCode != "skill_manifest_hash_mismatch" {
		t.Fatalf("persisted skill failure evidence = %#v", runState.Debug.Skills)
	}
	orchestrator.executionWg.Wait()
	records, _ := store.snapshot()
	if len(records) != 1 || records[0].Skills == nil || len(records[0].Skills.ContentLoads) != 1 ||
		records[0].Skills.ContentLoads[0].DiagnosticCode != "skill_manifest_hash_mismatch" {
		t.Fatalf("stored boundary failure evidence = %#v", records)
	}
}

func TestRunRequestPersistsRevocationDiagnosticWhenSkillsAreDisabledOnResume(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	enabledRuntime := resumeSkillRuntime(t, binding, registry, "selector-model")
	skillState, cacheContext, err := enabledRuntime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	config := NewDefaultOrchestratorConfig()
	orchestrator := NewAIOrchestrator(config, nil, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = orchestrator.Shutdown(shutdownCtx)
	})
	store := &terminalRecordStore{}
	orchestrator.SetExecutionStore(store)
	ctx := withCheckpointSkillState(t.Context(), skillState, &cacheContext)

	response, err := orchestrator.ProcessRequest(ctx, "resume request", nil)
	if response != nil || !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("ProcessRequest(disabled skill resume) = %#v, %v", response, err)
	}
	orchestrator.executionWg.Wait()
	records, _ := store.snapshot()
	if len(records) != 1 || records[0].Skills == nil ||
		!hasRuntimeSkillDiagnostic(records[0].Skills.Diagnostics, "skill_binding_revoked") {
		t.Fatalf("stored disabled-resume evidence = %#v", records)
	}
}

func TestRunRequestRejectsAnyPinnedCheckpointWhenSkillsAreDisabled(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	orchestrator := NewAIOrchestrator(config, nil, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = orchestrator.Shutdown(shutdownCtx)
	})
	store := &terminalRecordStore{}
	orchestrator.SetExecutionStore(store)
	checkpointState := SkillExecutionState{
		Pinned: &SkillSnapshot{},
		ActiveSkills: []ActiveSkill{{
			Skill: SkillVersionRef{
				Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 1,
				ManifestHash: "sha256:" + strings.Repeat("a", 64),
			},
		}},
	}
	ctx := withCheckpointSkillState(t.Context(), checkpointState, &SkillCacheContext{
		Fingerprint: "sha256:" + strings.Repeat("b", 64),
	})

	response, err := orchestrator.ProcessRequest(ctx, "resume malformed checkpoint", nil)
	if response != nil || !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("ProcessRequest(disabled malformed skill resume) = %#v, %v", response, err)
	}
	orchestrator.executionWg.Wait()
	records, _ := store.snapshot()
	if len(records) != 1 || records[0].Skills == nil ||
		!hasRuntimeSkillDiagnostic(records[0].Skills.Diagnostics, "skill_binding_revoked") {
		t.Fatalf("stored malformed disabled-resume evidence = %#v", records)
	}
}

func TestCheckpointHasEffectiveSkillStateAllowsOnlyEmptyCompatibilityState(t *testing.T) {
	if checkpointHasEffectiveSkillState(SkillExecutionState{Pinned: &SkillSnapshot{}}, &SkillCacheContext{}) {
		t.Fatal("explicitly empty compatibility checkpoint was treated as skill-influenced")
	}
	if !checkpointHasEffectiveSkillState(SkillExecutionState{
		Pinned:      &SkillSnapshot{},
		Diagnostics: []SkillDiagnostic{{Code: "skill_version_unavailable"}},
	}, &SkillCacheContext{}) {
		t.Fatal("checkpoint diagnostic was ignored as effective skill state")
	}
	if !checkpointHasEffectiveSkillState(SkillExecutionState{Pinned: &SkillSnapshot{}}, &SkillCacheContext{
		Fingerprint: "sha256:" + strings.Repeat("c", 64),
	}) {
		t.Fatal("checkpoint cache fingerprint was ignored as effective skill state")
	}
}

func TestCompleteRunMarksErrorResponseSpanOnceWithoutLogger(t *testing.T) {
	span := &mockSpan{name: "orchestrator.process_request"}
	state := &executionRunState{
		Context:     t.Context(),
		StartedAt:   time.Now(),
		Correlation: requestCorrelation{RequestID: "request-error-response"},
		Span:        span,
	}
	orchestrator := &AIOrchestrator{config: &OrchestratorConfig{}, metrics: &OrchestratorMetrics{}}
	result := &requestRunResult{Response: OrchestratorResponse{
		RequestID: "request-error-response",
		Errors:    []string{"bounded response error"},
	}}

	orchestrator.completeRun(state, result)
	recordRunSpanFailure(state)

	if span.attributes["status"] != "error" || span.attributes["error_type"] != "request_failed" ||
		len(span.errors) != 1 || span.errors[0].Error() != "orchestration request failed: failed" {
		t.Fatalf("error response span = %#v", span)
	}
}
