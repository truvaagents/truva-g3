package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Optional offline interchange with AI-module contract tests. It avoids a
// framework-module import edge and contains synthetic input only, never secrets.
type capturedCallerRequest struct {
	Request *core.AIRequest `json:"request"`
	Legacy  *core.AIOptions `json:"legacy"`
}

type fastCallerCapture struct {
	compactorMockAI
	requests []capturedCallerRequest
}

func (c *fastCallerCapture) Generate(_ context.Context, request *core.AIRequest) (*core.AIResult, error) {
	copy, err := core.CloneAIRequest(request)
	if err != nil {
		return nil, err
	}
	c.requests = append(c.requests, capturedCallerRequest{Request: copy, Legacy: copy.LegacyOptions()})
	content := "Historical synthetic event observed."
	switch request.Purpose {
	case "skill_activation_selection":
		content = `{"selected_skills":[]}`
	case "skill_resource_selection":
		content = `{"selected_resources":[]}`
	case "event-summarization":
		content = `[{"step_id":"synthetic-step","summary":"Synthetic event observed."}]`
	}
	return &core.AIResult{Response: &core.AIResponse{Content: content}}, nil
}

func TestFastCallerOfflineCapture(t *testing.T) {
	client := &fastCallerCapture{}
	fast := "fast"
	runtime := &skillRuntime{aiClient: client, config: SkillConfig{Limits: defaultSkillRuntimeLimits()}, activationOptions: &AIOptionsOverride{Model: &fast}, resourceOptions: &AIOptionsOverride{Model: &fast}}
	if _, err := runtime.resolveActivationPayloadWithAI(t.Context(), []byte(`{"request":"synthetic"}`), nil, SkillBoundaryInitialPlanning, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.resolveResourcesWithAI(t.Context(), SkillResourceResolutionInput{Request: "synthetic", Boundary: SkillBoundaryInitialPlanning, PhaseNumber: 1}, &SkillExecutionState{}); err != nil {
		t.Fatal(err)
	}
	compactor, err := NewLLMActivityCompactor(client, WithActivityCompactorModel("fast"))
	if err != nil {
		t.Fatal(err)
	}
	events := []core.AgentEvent{{EventID: "synthetic-event", Summary: "Synthetic observation", Timestamp: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)}}
	if _, err := compactor.CompactEvents(t.Context(), events, 2500); err != nil {
		t.Fatal(err)
	}
	if _, err := compactor.UpdateDigest(t.Context(), "Earlier synthetic observation", events, 2500); err != nil {
		t.Fatal(err)
	}
	summarizer, err := NewLLMEventSummarizer(client, WithSummarizerModel("fast"), WithSummarizerMaxTokens(10000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summarizer.SummarizeSteps(t.Context(), []core.StepSummaryInput{{StepID: "synthetic-step", Capability: "observe", Success: true, Response: "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 5 {
		t.Fatalf("actual fast callers made %d requests", len(client.requests))
	}
	for _, captured := range client.requests {
		effective := effectiveGeneration(captured.Request.Generation, captured.Legacy)
		if effective.Model != "fast" || effective.Temperature.Mode != core.AIParameterSet {
			t.Fatalf("caller intent changed: %#v", effective)
		}
	}
	// Exercise the same invocation boundary for default-tier contracts; these
	// two probes do not claim to exercise complete planning/synthesis workflows.
	for _, purpose := range []string{"planning", "synthesis"} {
		if _, err := invokeAI(t.Context(), client, aiInvocation{Purpose: purpose, Prompt: "synthetic", Options: &core.AIOptions{Model: "default", Temperature: 0.1, MaxTokens: 30000, ResponseFormat: "json"}}); err != nil {
			t.Fatal(err)
		}
	}
	if path := os.Getenv("TRUVAG3_TEST_CALLER_CAPTURE"); path != "" {
		data, err := json.MarshalIndent(client.requests, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
