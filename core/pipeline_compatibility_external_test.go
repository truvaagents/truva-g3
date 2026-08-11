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
	hooks := []core.PipelineHook{legacyPipelineHook{}, decisionPipelineHook{}}
	if len(hooks) != 2 {
		t.Fatalf("hooks = %d, want 2", len(hooks))
	}
}
