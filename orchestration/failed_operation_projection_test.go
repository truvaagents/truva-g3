package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type failureInputPolicyFunc func(context.Context, FailedOperationIdentity, string, interface{}) (bool, error)

func (f failureInputPolicyFunc) AllowFailedOperationInput(ctx context.Context, id FailedOperationIdentity, key string, value interface{}) (bool, error) {
	return f(ctx, id, key, value)
}

func failureEvidenceFixture() StepResult {
	return StepResult{StepID: "step-3", AgentName: "generic-tool", Capability: "read", Error: "existing terminal error", Parameters: map[string]interface{}{"id": "planned-not-sent"}, Dispatches: []StepDispatch{
		{Number: 1, Parameters: map[string]interface{}{"id": "wrong-id", "object": map[string]interface{}{"nested": "original"}}, Response: `{"success":false,"error":{"code":"BAD_INPUT","message":"raw-message-not-newly-exposed"}}`, Error: "raw transport prose"},
		{Number: 2, Success: true, Parameters: map[string]interface{}{"id": "successful-attempt-not-a-failure"}},
		{Number: 3, Parameters: map[string]interface{}{"id": "second-id"}, Response: `{"success":false,"error":{"code":"NOT_FOUND"}}`},
	}}
}

func TestFailureEvidenceSameAttemptAndImmutable(t *testing.T) {
	step := failureEvidenceFixture()
	step.Dispatches[0].Parameters["typed"] = map[string]string{"text": "original"}
	before, _ := json.Marshal(step)
	views, err := ProjectFailedOperationEvidence(t.Context(), []StepResult{step}, FailedOperationViewConfig{})
	require.NoError(t, err)
	var view failedOperationEvidence
	require.NoError(t, json.Unmarshal(views[step.StepID], &view))
	require.Equal(t, "complete", view.ViewStatus)
	require.Equal(t, "existing terminal error", view.Error)
	require.Len(t, view.Attempts, 2)
	require.Equal(t, "NOT_FOUND", view.Attempts[0].ErrorCode)
	require.Equal(t, "second-id", view.Attempts[0].Inputs["id"])
	require.Equal(t, "BAD_INPUT", view.Attempts[1].ErrorCode)
	require.Equal(t, "wrong-id", view.Attempts[1].Inputs["id"])
	for _, forbidden := range []string{"planned-not-sent", "successful-attempt-not-a-failure", "raw-message-not-newly-exposed", "raw transport prose"} {
		require.NotContains(t, string(views[step.StepID]), forbidden)
	}
	after, _ := json.Marshal(step)
	require.Equal(t, before, after)
}

func TestFailureEvidencePolicyFailsClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "restrict", true: "error"}[fail], func(t *testing.T) {
			step := failureEvidenceFixture()
			before, _ := json.Marshal(step)
			config := FailedOperationViewConfig{InputPolicy: failureInputPolicyFunc(func(_ context.Context, id FailedOperationIdentity, key string, value interface{}) (bool, error) {
				require.Equal(t, "step-3", id.StepID)
				if object, ok := value.(map[string]interface{}); ok {
					object["nested"] = "policy-mutated"
				}
				if fail {
					return false, errors.New("policy unavailable")
				}
				return key != "id", nil
			})}
			views, err := ProjectFailedOperationEvidence(t.Context(), []StepResult{step}, config)
			require.NoError(t, err)
			require.NotContains(t, string(views[step.StepID]), "wrong-id")
			require.NotContains(t, string(views[step.StepID]), "policy-mutated")
			var view failedOperationEvidence
			require.NoError(t, json.Unmarshal(views[step.StepID], &view))
			require.Equal(t, "partial", view.ViewStatus)
			if fail {
				for _, attempt := range view.Attempts {
					require.True(t, attempt.PolicyError)
					require.Empty(t, attempt.Inputs)
				}
			}
			after, _ := json.Marshal(step)
			require.Equal(t, before, after)
		})
	}
}

func TestFailureEvidenceBudgetsAndUnknownDispatch(t *testing.T) {
	step := failureEvidenceFixture()
	step.Dispatches[0].Parameters["large"] = strings.Repeat("é\n\"", 1000)
	encode := func(s string) (string, error) { data, err := json.Marshal(s); return string(data), err }
	for _, budget := range []int{0, 1, 100, 250, 450, 800, 4000} {
		views, err := projectFailedOperations(t.Context(), []StepResult{step}, FailedOperationViewConfig{MaxValueBytes: 100, MaxStepBytes: 5000, MaxPromptBytes: 5000}, budget, encode)
		require.NoError(t, err)
		view := views[step.StepID]
		require.LessOrEqual(t, len(view.prepared), budget)
		if view.raw != "" {
			require.True(t, json.Valid([]byte(view.raw)))
			require.NotContains(t, view.raw, strings.Repeat("é", 1000))
		}
	}
	steps := []StepResult{{StepID: "legacy", Error: "original failure"}, {StepID: "local", Metadata: map[string]interface{}{"failure_kind": "binding_shape_mismatch"}}, {StepID: "success", Success: true}}
	views, err := ProjectFailedOperationEvidence(t.Context(), steps, FailedOperationViewConfig{})
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Contains(t, string(views["legacy"]), `"dispatch_state":"unavailable"`)
	require.Contains(t, string(views["local"]), `"dispatch_state":"not_dispatched"`)
	require.NotContains(t, string(views["local"]), `"attempt":`)
	views, err = ProjectFailedOperationEvidence(t.Context(), steps, FailedOperationViewConfig{Disabled: true})
	require.NoError(t, err)
	require.Empty(t, views)
	_, err = ProjectFailedOperationEvidence(t.Context(), steps, FailedOperationViewConfig{MaxValueBytes: -1})
	require.Error(t, err)
	_, err = projectFailedOperations(t.Context(), steps, FailedOperationViewConfig{}, -1, func(string) (string, error) { return "", errors.New("encoding error") })
	require.Error(t, err)
}

func TestFailureEvidenceSynthesisPreparationAndBudget(t *testing.T) {
	steps := []StepResult{failureEvidenceFixture()}
	views, err := synthesisFailureViews(t.Context(), steps, FailedOperationViewConfig{})
	require.NoError(t, err)
	_, _, content, err := prepareFailedSynthesisStep(t.Context(), steps[0], views)
	require.NoError(t, err)
	require.Equal(t, views[steps[0].StepID].prepared, content)
	views, err = synthesisFailureViews(t.Context(), steps, FailedOperationViewConfig{MaxPromptBytes: 1})
	require.NoError(t, err)
	_, _, content, err = prepareFailedSynthesisStep(t.Context(), steps[0], views)
	require.NoError(t, err)
	require.Contains(t, content, "omitted for budget")
	_, _, content, err = prepareFailedSynthesisStep(t.Context(), steps[0], nil)
	require.NoError(t, err)
	require.Equal(t, steps[0].Error, content)
	require.Contains(t, renderContinuationStepResult(&steps[0], "", true), "omitted for budget")
}

func TestFailureEvidenceFinalPromptParity(t *testing.T) {
	for _, skills := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "skills"}[skills], func(t *testing.T) {
			ctx := t.Context()
			if skills {
				ctx = withPromptInputPreparer(ctx, skillPromptInputPreparer{})
			}
			step := failureEvidenceFixture()
			results := &ExecutionResult{Steps: []StepResult{step}}
			ordinary := NewAISynthesizer(nil)
			first, err := ordinary.buildPreparedSynthesisPrompt(ctx, "diagnose", results)
			require.NoError(t, err)
			orch := newDigestTestOrchestrator(32768)
			second, err := orch.buildPreparedSynthesisPrompt(ctx, "diagnose", results)
			require.NoError(t, err)
			cont, err := orch.buildContinuationPrompt(ctx, "diagnose", map[string]*StepResult{step.StepID: &step}, []string{step.StepID}, "", 2)
			require.NoError(t, err)
			for _, prompt := range []string{first, second, cont.Prompt} {
				require.Contains(t, prompt, "wrong-id")
				require.Contains(t, prompt, "BAD_INPUT")
				require.Contains(t, prompt, "second-id")
				require.Contains(t, prompt, "NOT_FOUND")
				require.NotContains(t, prompt, "planned-not-sent")
				require.NotContains(t, prompt, "raw-message-not-newly-exposed")
			}
			prepared, err := synthesisFailureViews(ctx, results.Steps, FailedOperationViewConfig{})
			require.NoError(t, err)
			require.Contains(t, first, prepared[step.StepID].prepared)
			require.Contains(t, second, prepared[step.StepID].prepared)
		})
	}
	steps := []StepResult{{StepID: "step-1", Success: true}, {StepID: "step-2"}, {StepID: "step-3", Success: true}}
	require.Equal(t, []string{"step-3"}, continuationSuccessfulRetention(steps, []string{"a", "b", "c"}, 100, 150))
	require.Equal(t, []string{"step-1", "step-3"}, continuationSuccessfulRetention(steps, []string{"a", "b", "c"}, 0, 1000))
}

func TestFailureEvidenceConfigResolution(t *testing.T) {
	const variable = "TRUVAG3_FAILED_OPERATION_MAX_PROMPT_BYTES"
	base := NewDefaultOrchestratorConfig()
	require.Equal(t, 32768, base.FailedOperationView.MaxPromptBytes)
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{Base: base, Environment: EnvironmentStrict, LookupEnv: lookupFromMap(map[string]string{variable: "2048", "TRUVAG3_FAILED_OPERATION_VIEW_DISABLED": "true"})})
	require.NoError(t, err)
	require.Equal(t, 2048, resolved.Config.FailedOperationView.MaxPromptBytes)
	require.True(t, resolved.Config.FailedOperationView.Disabled)
	require.Equal(t, 32768, base.FailedOperationView.MaxPromptBytes)
	_, err = ResolveOrchestratorConfig(ConfigResolution{Environment: EnvironmentStrict, LookupEnv: lookupFromMap(map[string]string{variable: "0"})})
	require.Error(t, err)
	base.FailedOperationView.MaxStepBytes = -1
	require.Error(t, ValidateOrchestratorConfig(base))
	base = NewDefaultOrchestratorConfig()
	base.FailedOperationView.InputPolicy = failureInputPolicyFunc(func(context.Context, FailedOperationIdentity, string, interface{}) (bool, error) { return true, nil })
	identity, err := ResolveOrchestratorConfigIdentity(base)
	require.NoError(t, err)
	require.False(t, identity.CacheEligible)
	require.Error(t, NewAISynthesizer(nil).SetFailedOperationView(FailedOperationViewConfig{MaxValueBytes: -1}))
	resolved, err = ResolveOrchestratorConfig(ConfigResolution{Environment: EnvironmentStrict, LookupEnv: lookupFromMap(map[string]string{variable: "2048"}), Options: []OrchestratorOption{WithFailedOperationView(FailedOperationViewConfig{MaxPromptBytes: 8192})}})
	require.NoError(t, err)
	require.Equal(t, 8192, resolved.Config.FailedOperationView.MaxPromptBytes)
}
