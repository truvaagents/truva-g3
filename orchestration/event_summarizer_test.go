package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// summarizerMockAI implements core.AIClient for event summarizer tests.
type summarizerMockAI struct {
	generateFunc func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error)
	calls        int
}

func (m *summarizerMockAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.calls++
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt, opts)
	}
	return &core.AIResponse{Content: "[]"}, nil
}

func TestNewLLMEventSummarizer_NilAIClient(t *testing.T) {
	_, err := NewLLMEventSummarizer(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AI client is required")
}

func TestNewLLMEventSummarizer_Defaults(t *testing.T) {
	s, err := NewLLMEventSummarizer(&summarizerMockAI{})
	require.NoError(t, err)
	assert.Equal(t, 4000, s.maxResponseChars)
	assert.Equal(t, 20, s.maxStepsPerBatch)
	assert.Equal(t, 150, s.tokensPerStepBudget)
	assert.Equal(t, "", s.model)
}

func TestNewLLMEventSummarizer_Options(t *testing.T) {
	s, err := NewLLMEventSummarizer(&summarizerMockAI{},
		WithSummarizerModel("gpt-4o-mini"),
		WithSummarizerMaxResponseChars(2000),
		WithSummarizerMaxStepsPerBatch(10),
		WithSummarizerTokensPerStep(50),
		WithSummarizerLogger(&core.NoOpLogger{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o-mini", s.model)
	assert.Equal(t, 2000, s.maxResponseChars)
	assert.Equal(t, 10, s.maxStepsPerBatch)
	assert.Equal(t, 50, s.tokensPerStepBudget)
}

func TestNewLLMEventSummarizer_InvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opt  LLMEventSummarizerOption
		err  string
	}{
		{"nil logger", WithSummarizerLogger(nil), "logger cannot be nil"},
		{"zero maxResponseChars", WithSummarizerMaxResponseChars(0), "maxResponseChars must be positive"},
		{"negative maxStepsPerBatch", WithSummarizerMaxStepsPerBatch(-1), "maxStepsPerBatch must be positive"},
		{"zero tokensPerStep", WithSummarizerTokensPerStep(0), "tokensPerStepBudget must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLLMEventSummarizer(&summarizerMockAI{}, tt.opt)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func TestSummarizeSteps_EmptySteps(t *testing.T) {
	ai := &summarizerMockAI{}
	s, _ := NewLLMEventSummarizer(ai)

	result, err := s.SummarizeSteps(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Equal(t, 0, ai.calls, "should not make LLM call for empty steps")
}

func TestSummarizeSteps_SuccessfulBatch(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `[{"step_id":"step-1","summary":"Created JIRA ticket DEVOPS-43 in project DEVOPS via jira-tool"},{"step_id":"step-2","summary":"Sent message to #incidents channel via slack-tool"}]`,
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	steps := []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "jira-tool", Capability: "create_issue", Response: `{"key":"DEVOPS-43"}`, Success: true},
		{StepID: "step-2", AgentName: "slack-tool", Capability: "send_message", Response: `{"ok":true}`, Success: true},
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err)
	assert.Equal(t, 2, len(result))
	assert.Contains(t, result["step-1"].Summary, "DEVOPS-43")
	assert.Contains(t, result["step-2"].Summary, "#incidents")
	assert.Equal(t, 1, ai.calls, "should make exactly 1 batched call")
}

func TestSummarizeSteps_LLMError_FailOpen(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	steps := []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "jira-tool", Capability: "create_issue", Success: true},
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err, "should not return error (fail-open)")
	assert.Empty(t, result, "should return empty map on LLM failure")
}

func TestSummarizeSteps_MalformedJSON_ProseWrapped(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `Here are the summaries:
[{"step_id":"step-1","summary":"Restarted deployment payment-api via devops-tool"}]
Hope this helps!`,
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	steps := []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "devops-tool", Capability: "rollout_restart", Success: true},
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err)
	assert.Equal(t, 1, len(result))
	assert.Contains(t, result["step-1"].Summary, "payment-api")
}

func TestSummarizeSteps_MalformedJSON_NoParseable(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: "I cannot process this request."}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	steps := []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "tool", Capability: "action", Success: true},
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err, "should not return error (fail-open)")
	assert.Empty(t, result)
}

func TestSummarizeSteps_EmptySummaryOrStepID_Skipped(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `[{"step_id":"step-1","summary":"Valid summary"},{"step_id":"","summary":"Missing ID"},{"step_id":"step-3","summary":""}]`,
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	steps := []core.StepSummaryInput{
		{StepID: "step-1", Success: true},
		{StepID: "step-2", Success: true},
		{StepID: "step-3", Success: true},
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err)
	assert.Equal(t, 1, len(result), "only step-1 should be in result")
	assert.Equal(t, "Valid summary", result["step-1"].Summary)
}

func TestSummarizeSteps_BatchTruncation(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			// Count how many <step> tags in prompt
			count := strings.Count(prompt, "<step id=")
			return &core.AIResponse{
				Content: fmt.Sprintf(`[{"step_id":"step-1","summary":"processed %d steps"}]`, count),
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai, WithSummarizerMaxStepsPerBatch(3))

	// Send 5 steps, expect only 3 in the batch
	steps := make([]core.StepSummaryInput, 5)
	for i := range steps {
		steps[i] = core.StepSummaryInput{StepID: fmt.Sprintf("step-%d", i+1), Success: true}
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err)
	assert.Contains(t, result["step-1"].Summary, "processed 3 steps")
}

func TestSummarizeSteps_ResponseTruncation(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			// Check that the response in the prompt is truncated
			responseTag := "<response>"
			idx := strings.Index(prompt, responseTag)
			if idx < 0 {
				return &core.AIResponse{Content: `[{"step_id":"step-1","summary":"no response tag"}]`}, nil
			}
			endIdx := strings.Index(prompt[idx:], "</response>")
			responseLen := endIdx - len(responseTag)
			return &core.AIResponse{
				Content: fmt.Sprintf(`[{"step_id":"step-1","summary":"response was %d chars"}]`, responseLen),
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai, WithSummarizerMaxResponseChars(100))

	steps := []core.StepSummaryInput{
		{StepID: "step-1", Response: strings.Repeat("x", 500), Success: true},
	}

	result, err := s.SummarizeSteps(context.Background(), steps)
	require.NoError(t, err)
	// Response should be truncated to ~100 chars (plus truncation suffix)
	assert.NotContains(t, result["step-1"].Summary, "500 chars")
}

func TestSummarizeSteps_MaxTokensScalesWithBatch(t *testing.T) {
	var capturedOpts *core.AIOptions
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			capturedOpts = opts
			return &core.AIResponse{Content: "[]"}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai, WithSummarizerTokensPerStep(75))

	steps := make([]core.StepSummaryInput, 4)
	for i := range steps {
		steps[i] = core.StepSummaryInput{StepID: fmt.Sprintf("step-%d", i+1), Success: true}
	}

	_, _ = s.SummarizeSteps(context.Background(), steps)
	require.NotNil(t, capturedOpts)
	assert.Equal(t, 300, capturedOpts.MaxTokens, "should be 75 * 4 steps")
}

func TestSummarizeSteps_MaxTokensFloor(t *testing.T) {
	var capturedOpts *core.AIOptions
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			capturedOpts = opts
			return &core.AIResponse{Content: "[]"}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai) // default 100 tokens/step

	// Single step: 100 * 1 = 100, but floor is 200
	steps := []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "tool-a", Capability: "query", Success: true},
	}

	_, _ = s.SummarizeSteps(context.Background(), steps)
	require.NotNil(t, capturedOpts)
	assert.Equal(t, 300, capturedOpts.MaxTokens, "single step (150) should get floor of 300 tokens")
}

func TestSummarizeSteps_SystemPromptSet(t *testing.T) {
	var capturedOpts *core.AIOptions
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			capturedOpts = opts
			return &core.AIResponse{Content: "[]"}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	steps := []core.StepSummaryInput{{StepID: "step-1", Success: true}}
	_, _ = s.SummarizeSteps(context.Background(), steps)

	require.NotNil(t, capturedOpts)
	assert.Contains(t, capturedOpts.SystemPrompt, "<identity>")
	assert.Contains(t, capturedOpts.SystemPrompt, "<instructions>")
	assert.Contains(t, capturedOpts.SystemPrompt, "<example>")
}

func TestSummarizeSteps_ModelOverride(t *testing.T) {
	var capturedOpts *core.AIOptions
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			capturedOpts = opts
			return &core.AIResponse{Content: "[]"}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai, WithSummarizerModel("gpt-4o-mini"))

	steps := []core.StepSummaryInput{{StepID: "step-1", Success: true}}
	_, _ = s.SummarizeSteps(context.Background(), steps)

	require.NotNil(t, capturedOpts)
	assert.Equal(t, "gpt-4o-mini", capturedOpts.Model)
}

func TestBuildPrompt_XMLStructure(t *testing.T) {
	s, _ := NewLLMEventSummarizer(&summarizerMockAI{})

	steps := []core.StepSummaryInput{
		{
			StepID:      "step-1",
			AgentName:   "jira-tool",
			Capability:  "create_issue",
			Instruction: "Create a ticket",
			Parameters:  map[string]interface{}{"project_key": "DEVOPS"},
			Response:    `{"key":"DEVOPS-43"}`,
			Success:     true,
		},
	}

	prompt := s.buildPrompt(steps)
	assert.Contains(t, prompt, "<steps>")
	assert.Contains(t, prompt, "</steps>")
	assert.Contains(t, prompt, `<step id="step-1">`)
	assert.Contains(t, prompt, "<agent>jira-tool</agent>")
	assert.Contains(t, prompt, "<capability>create_issue</capability>")
	assert.Contains(t, prompt, "<instruction>Create a ticket</instruction>")
	assert.Contains(t, prompt, "<parameters>")
	assert.Contains(t, prompt, "DEVOPS")
	assert.Contains(t, prompt, "<response>")
	assert.Contains(t, prompt, "DEVOPS-43")
	assert.Contains(t, prompt, "<outcome>success</outcome>")
}

func TestSummarizeSteps_MarkdownCodeFenceResponse(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: "```json\n" + `[{"step_id":"step-1","summary":"Did something via tool-a"}]` + "\n```",
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)

	result, err := s.SummarizeSteps(context.Background(), []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "tool-a", Capability: "query", Success: true},
	})
	assert.NoError(t, err)
	assert.Equal(t, "Did something via tool-a", result["step-1"].Summary)
}

func TestStripMarkdownCodeFence(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "json fence",
			input:    "```json\n[{\"a\":1}]\n```",
			expected: `[{"a":1}]`,
		},
		{
			name:     "plain fence",
			input:    "```\n[{\"a\":1}]\n```",
			expected: `[{"a":1}]`,
		},
		{
			name:     "no fence",
			input:    `[{"a":1}]`,
			expected: `[{"a":1}]`,
		},
		{
			name:     "fence with whitespace",
			input:    "  ```json\n[{\"a\":1}]\n```  ",
			expected: `[{"a":1}]`,
		},
		{
			name:     "unclosed fence (truncated response)",
			input:    "```json\n[{\"step_id\":\"step-1\",\"summary\":\"partial",
			expected: `[{"step_id":"step-1","summary":"partial`,
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, stripMarkdownCodeFence(tt.input))
		})
	}
}

func TestBuildPrompt_FailedStep(t *testing.T) {
	s, _ := NewLLMEventSummarizer(&summarizerMockAI{})

	steps := []core.StepSummaryInput{
		{StepID: "step-1", Success: false},
	}

	prompt := s.buildPrompt(steps)
	assert.Contains(t, prompt, "<outcome>failure</outcome>")
}

func TestSummarizeSteps_ExtractsEntities(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `[{"step_id":"step-1","summary":"Restarted pod foo","entities":[{"type":"pod","id":"foo-abc12"}]}]`,
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)
	result, err := s.SummarizeSteps(context.Background(), []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "devops-tool", Capability: "restart_pod"},
	})
	require.NoError(t, err)
	require.Contains(t, result, "step-1")
	assert.Equal(t, "Restarted pod foo", result["step-1"].Summary)
	require.Len(t, result["step-1"].Entities, 1)
	assert.Equal(t, "pod", result["step-1"].Entities[0].Type)
	assert.Equal(t, "foo-abc12", result["step-1"].Entities[0].ID)
}

func TestSummarizeSteps_EmptyEntitiesArrayHandled(t *testing.T) {
	ai := &summarizerMockAI{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `[{"step_id":"step-1","summary":"Did a thing","entities":[]}]`,
			}, nil
		},
	}
	s, _ := NewLLMEventSummarizer(ai)
	result, err := s.SummarizeSteps(context.Background(), []core.StepSummaryInput{
		{StepID: "step-1", AgentName: "test-tool", Capability: "do_thing"},
	})
	require.NoError(t, err)
	require.Contains(t, result, "step-1")
	assert.Equal(t, "Did a thing", result["step-1"].Summary)
	assert.Empty(t, result["step-1"].Entities, "empty entities array should produce nil/empty Entities")
}

func TestSummarizeSteps_PromptContainsEntityExtractionGuidance(t *testing.T) {
	require.Contains(t, eventSummarizerSystemPrompt, "directly observable in the step",
		"system prompt must include the positive entity-extraction directive")
	require.Contains(t, eventSummarizerSystemPrompt, "non-fatal",
		"system prompt must include the concrete English-compound counter-example in the example section")
}
