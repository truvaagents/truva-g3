package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestGenerousDefaultsAcrossConstructionPaths(t *testing.T) {
	cfg := NewDefaultOrchestratorConfig()
	require.Equal(t, 20000, cfg.PlanMaxTokens)
	require.Equal(t, 30000, cfg.SynthesisMaxTokens)
	require.Equal(t, 20, cfg.IterativePlanning.MaxPhases)
	require.Equal(t, 25*time.Minute, cfg.IterativePlanning.PhaseTimeout)
	require.Equal(t, 15*time.Minute, cfg.ExecutionOptions.StepTimeout)
	require.Equal(t, 20*time.Minute, cfg.ExecutionOptions.TotalTimeout)
	require.Equal(t, 65536, cfg.ResultTrim.MaxResultBytes)
	require.Equal(t, 196608, cfg.ResultTrim.MaxTotalPromptBytes)
	require.Equal(t, 196608, cfg.ContinuationResultMaxTotalChars)
	require.Equal(t, 6000, cfg.Skills.Limits.SynthesisTokenBudget)
	require.GreaterOrEqual(t, cfg.ExecutionOptions.TotalTimeout, cfg.ExecutionOptions.StepTimeout+cfg.HITL.DefaultTimeout)
	require.Greater(t, cfg.IterativePlanning.PhaseTimeout, cfg.ExecutionOptions.TotalTimeout)
	for _, config := range []*OrchestratorConfig{cfg, {RoutingMode: ModeAutonomous}} {
		orch := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())
		require.Equal(t, 20*time.Minute, orch.executor.httpClient.Timeout)
	}
	synth := NewAISynthesizer(NewMockAIClient())
	require.Equal(t, 30000, synth.synthesisMaxTokens)
}

func TestDefaultHTTPAllowanceStillHonorsParentCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	orch := NewAIOrchestrator(NewDefaultOrchestratorConfig(), NewMockDiscovery(), NewMockAIClient())
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := orch.executor.httpClient.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEventSummaryDefaultAndExplicitBudgetPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []LLMEventSummarizerOption
		want int
	}{
		{"default", nil, 10000},
		{"calculated", []LLMEventSummarizerOption{WithSummarizerTokensPerStep(200)}, 400},
		{"fixed before calculated", []LLMEventSummarizerOption{WithSummarizerMaxTokens(700), WithSummarizerTokensPerStep(200)}, 700},
		{"fixed after calculated", []LLMEventSummarizerOption{WithSummarizerTokensPerStep(200), WithSummarizerMaxTokens(700)}, 700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &summarizerMockAI{generateFunc: func(_ context.Context, _ string, opts *core.AIOptions) (*core.AIResponse, error) {
				require.Equal(t, tc.want, opts.MaxTokens)
				return &core.AIResponse{Content: "[]"}, nil
			}}
			summarizer, err := NewLLMEventSummarizer(client, tc.opts...)
			require.NoError(t, err)
			_, err = summarizer.SummarizeSteps(t.Context(), []core.StepSummaryInput{{StepID: "a"}, {StepID: "b"}})
			require.NoError(t, err)
			require.Equal(t, 1, client.calls)
		})
	}
}
