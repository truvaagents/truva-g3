package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"
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
			ctx := withBoundaryPreparer(context.Background(), func(_ context.Context, boundary orchestrationBoundary) error {
				got = append(got, boundary)
				return nil
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
	ctx := withBoundaryPreparer(context.Background(), func(_ context.Context, boundary orchestrationBoundary) error {
		got = append(got, boundary)
		return nil
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
