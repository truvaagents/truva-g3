package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestSummarizerInvocationDeadline(t *testing.T) {
	steps := []core.StepSummaryInput{{StepID: "step-1", Success: true}}
	for _, test := range []struct {
		name                  string
		cancelParent, success bool
		want                  string
	}{
		{"child timeout", false, false, "summary_timeout"},
		{"parent cancellation", true, false, "parent_cancelled"},
		{"successful deadline race", false, true, "complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &summarizerMockAI{generateFunc: func(ctx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
				if test.cancelParent {
					cancel()
				}
				<-ctx.Done()
				if test.success {
					return &core.AIResponse{Content: `[{"step_id":"step-1","summary":"Completed operation","entities":[]}]`}, nil
				}
				return nil, ctx.Err()
			}}
			s, err := NewLLMEventSummarizer(client, WithSummarizerTimeout(time.Millisecond))
			require.NoError(t, err)
			result := s.summarizeWithOutcome(parent, steps)
			require.Equal(t, test.want, result.Outcome)
			require.Equal(t, 1, client.calls)
			if !test.cancelParent {
				require.NoError(t, parent.Err())
			}
		})
	}
	s, err := NewLLMEventSummarizer(&summarizerMockAI{})
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, s.timeout)
	for _, duration := range []time.Duration{0, -1} {
		_, err := NewLLMEventSummarizer(&summarizerMockAI{}, WithSummarizerTimeout(duration))
		require.Error(t, err)
	}
}

func TestSummaryTimeoutFallbackAndParentCancellation(t *testing.T) {
	for _, parentCancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "child-timeout", true: "parent-cancelled"}[parentCancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &summarizerMockAI{generateFunc: func(callCtx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
				if parentCancelled {
					cancel()
				}
				<-callCtx.Done()
				return nil, callCtx.Err()
			}}
			s, err := NewLLMEventSummarizer(client, WithSummarizerTimeout(time.Millisecond))
			require.NoError(t, err)
			writes := 0
			backend := &core.MockEpisodicMemory{RecordEventFn: func(writeCtx context.Context, _ core.AgentEvent) error {
				require.NoError(t, writeCtx.Err())
				writes++
				return nil
			}}
			hook, err := NewMemoryRecordHook(backend, nil, "generic-agent", "test", WithEventSummarizer(s))
			require.NoError(t, err)
			err = hook.AfterExecution(ctx, &core.PipelineContext{}, &ExecutionResult{Steps: []StepResult{{StepID: "one", Success: true}, {StepID: "two", Success: true}}})
			if parentCancelled {
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, writes)
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, writes)
			}
			require.Equal(t, 1, client.calls)
		})
	}
}
