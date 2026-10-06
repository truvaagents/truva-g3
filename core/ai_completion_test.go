package core

import (
	"context"
	"errors"
	"testing"
)

func TestCanonicalDispatchPreservesCompletionFacts(t *testing.T) {
	response := &AIResponse{Content: "answer", ReasoningContent: "reason", FinishReason: "length", ContentSource: AIContentSourceFinal}
	partial := errors.Join(ErrStreamPartiallyCompleted, errors.New("connection reset"))
	legacy := &legacyRequestClient{response: response}
	advanced := &advancedRequestClient{result: &AIResult{Response: response}}
	for _, client := range []AIClient{legacy, advanced} {
		result, err := GenerateAI(context.Background(), client, NewAIRequest("request", "test"))
		if err != nil || result.Response != response {
			t.Fatalf("completion facts lost: %+v %v", result, err)
		}
	}
	streamLegacy := &legacyStreamingRequestClient{supported: true, streamResponse: response, streamErr: partial}
	streamAdvanced := &advancedStreamingRequestClient{streamResult: &AIResult{Response: response}, streamErr: partial}
	for _, client := range []AIClient{streamLegacy, streamAdvanced} {
		result, err := StreamAI(context.Background(), client, NewAIRequest("request", "test"), func(StreamChunk) error { return nil })
		if !errors.Is(err, ErrStreamPartiallyCompleted) || result.Response != response {
			t.Fatalf("stream completion facts lost: %+v %v", result, err)
		}
	}
}
