package core_test

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type externalRequestClient struct{}

func (externalRequestClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "legacy"}, nil
}

func (externalRequestClient) Generate(context.Context, *core.AIRequest) (*core.AIResult, error) {
	return &core.AIResult{Response: &core.AIResponse{Content: "request"}}, nil
}

type externalStreamingRequestClient struct {
	externalRequestClient
}

func (externalStreamingRequestClient) Stream(
	context.Context,
	*core.AIRequest,
	core.StreamCallback,
) (*core.AIResult, error) {
	return &core.AIResult{Response: &core.AIResponse{Content: "stream"}}, nil
}

var (
	_ core.AIRequestClient          = externalRequestClient{}
	_ core.StreamingAIRequestClient = externalStreamingRequestClient{}
)

func TestAIRequestPublicTypesSupportKeyedLiterals(t *testing.T) {
	request := &core.AIRequest{
		Prompt:  "prompt",
		Purpose: "planning",
		Generation: core.AIGenerationOptions{
			Model:       "model",
			Temperature: core.SetAIParameter(float32(0)),
		},
		Patches: []core.AIProviderPatch{{
			Name: "patch",
			Selector: core.AIProviderSelector{
				Provider: "provider",
			},
		}},
	}

	result, err := core.GenerateAI(context.Background(), externalRequestClient{}, request)
	if err != nil {
		t.Fatalf("GenerateAI returned error: %v", err)
	}
	if result.Response == nil || result.Response.Content != "request" {
		t.Fatalf("unexpected result: %#v", result)
	}
	streamResult, err := core.StreamAI(
		context.Background(),
		externalStreamingRequestClient{},
		request,
		func(core.StreamChunk) error { return nil },
	)
	if err != nil || streamResult.Response == nil || streamResult.Response.Content != "stream" {
		t.Fatalf("unexpected stream result: result=%#v err=%v", streamResult, err)
	}

	_ = core.AIRequestReport{Adjustments: []core.AIRequestAdjustment{{Action: "set"}}}
	_ = core.AIUsageDetails{Counters: map[string]int64{"tokens": 1}}
	_ = core.AICost{Amount: 1, Currency: "USD", Source: "test"}
}
