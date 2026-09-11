package core_test

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type legacyPipelineHook struct{}

func (legacyPipelineHook) Name() string { return "legacy" }

func (legacyPipelineHook) BeforePlanning(
	context.Context,
	*core.PipelineContext,
) (*core.PipelineShortCircuit, error) {
	return &core.PipelineShortCircuit{"legacy response", "legacy source"}, nil
}

type decisionPipelineHook struct{}

func (decisionPipelineHook) Name() string { return "decision" }

func (decisionPipelineHook) BeforePlanningDecision(
	context.Context,
	*core.PipelineContext,
	core.PipelineGate,
) (*core.PipelineShortCircuitDecision, error) {
	return &core.PipelineShortCircuitDecision{
		ShortCircuit:  &core.PipelineShortCircuit{Response: "cached response", Source: "cache"},
		Kind:          core.PipelineShortCircuitCache,
		CachedAgainst: map[string]string{"synthetic": "fingerprint"},
	}, nil
}

// requiredAfterPlanningDeclaration deliberately omits AfterPlanning. The
// marker must remain independently detectable when a stage method drifts;
// orchestration owns the construction-time compatibility check.
type requiredAfterPlanningDeclaration struct{}

func (requiredAfterPlanningDeclaration) Name() string                 { return "required-declaration" }
func (requiredAfterPlanningDeclaration) RequireAfterPlanningSuccess() {}

func TestPipelineContractsRemainSourceCompatibleAndComposable(t *testing.T) {
	// These positional literals intentionally protect the exact field count and
	// order of the two pre-existing exported structs.
	contextValue := core.PipelineContext{"request", map[string]interface{}{}, map[string]interface{}{}}
	shortCircuit := core.PipelineShortCircuit{"response", "source"}
	if contextValue.Request == "" || shortCircuit.Response == "" {
		t.Fatal("positional compatibility fixture was not initialized")
	}

	var _ core.BeforePlanningHook = legacyPipelineHook{}
	var _ core.BeforePlanningDecisionHook = decisionPipelineHook{}
	var _ core.RequiredAfterPlanningHook = requiredAfterPlanningDeclaration{}
	hooks := []core.PipelineHook{legacyPipelineHook{}, decisionPipelineHook{}}
	if len(hooks) != 2 {
		t.Fatalf("hooks = %d, want 2", len(hooks))
	}
	if _, participates := interface{}(requiredAfterPlanningDeclaration{}).(core.AfterPlanningHook); participates {
		t.Fatal("required marker unexpectedly embeds the after-planning stage interface")
	}
}
