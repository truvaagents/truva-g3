package requestpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestDerivedBuiltInsAreIsolatedAndPreservePolicy(t *testing.T) {
	selector := core.AIProviderSelector{AllProviders: true}
	engine, err := NewEngine(Config{Mode: CompatibilityStrict})
	require.NoError(t, err)
	rule := patch("capacity", selector, map[string]interface{}{"/value": 50})
	derived, err := engine.WithBuiltInRules(rule)
	require.NoError(t, err)
	rule.Set["/value"] = 999
	newDraft := func() *testDraft {
		return newTestDraft(t, map[string]interface{}{"value": 100}, []string{"/value"})
	}
	draft := newDraft()
	_, err = derived.Apply(t.Context(), draft, nil)
	require.ErrorContains(t, err, "compatibility")
	value, _ := draft.Get("/value")
	require.Equal(t, 50, value)
	draft = newDraft()
	_, err = engine.Apply(t.Context(), draft, nil)
	require.NoError(t, err)
	value, _ = draft.Get("/value")
	require.Equal(t, 100, value)
	_, err = derived.Apply(t.Context(), newDraft(), []core.AIProviderPatch{patch("ack", selector, map[string]interface{}{"/value": 40})})
	require.NoError(t, err)
	_, err = engine.WithBuiltInRules(core.AIProviderPatch{})
	require.Error(t, err)
	var absent *Engine
	_, err = absent.WithBuiltInRules(rule)
	require.Error(t, err)
}
