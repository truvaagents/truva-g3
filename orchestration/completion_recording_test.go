package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func TestCompletionFactsSurviveBothDebugRecordPaths(t *testing.T) {
	response := &core.AIResponse{Content: "answer", ReasoningContent: "reason *exact*", FinishReason: "length", ContentSource: core.AIContentSourceFinal}
	invocation := aiInvocation{Prompt: "prompt", Purpose: "test"}
	interaction := withEffectiveAIRequest(LLMInteraction{Response: response.Content}, nil, invocation, response, nil)
	store := &mockLLMDebugStore{}
	err := NewLLMCallRecorderAdapter(store).RecordLLMCall(context.Background(), "request", telemetry.LLMCallRecord{
		Response: response.Content, ReasoningContent: response.ReasoningContent, FinishReason: response.FinishReason, ContentSource: response.ContentSource,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise both the adapter and format-compatible direct telemetry writer.
	wire, err := json.Marshal(telemetry.LLMCallRecord{Response: response.Content, ReasoningContent: response.ReasoningContent, FinishReason: response.FinishReason, ContentSource: response.ContentSource})
	if err != nil {
		t.Fatal(err)
	}
	var direct LLMInteraction
	if err := json.Unmarshal(wire, &direct); err != nil {
		t.Fatal(err)
	}
	for _, record := range []LLMInteraction{interaction, store.interactions[0], direct} {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var restored LLMInteraction
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Response != response.Content || restored.ReasoningContent != response.ReasoningContent || restored.FinishReason != response.FinishReason || restored.ContentSource != response.ContentSource {
			t.Fatalf("completion facts lost during recording: %+v", restored)
		}
	}
}
