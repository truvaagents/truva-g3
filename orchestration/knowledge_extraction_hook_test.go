package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
)

func TestKnowledgeExtractionHook_Name(t *testing.T) {
	hook, _ := NewKnowledgeExtractionHook(
		&core.MockSharedKnowledge{}, &core.MockEmbeddingClient{}, &mockAIClient{},
		"agent", "domain",
	)
	assert.Equal(t, "knowledge-extraction", hook.Name())
}

func TestKnowledgeExtractionHook_FailFastNilParams(t *testing.T) {
	_, err := NewKnowledgeExtractionHook(nil, nil, nil, "agent", "domain")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "knowledge store is required")

	_, err = NewKnowledgeExtractionHook(&core.MockSharedKnowledge{}, nil, nil, "agent", "domain")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "embedding client is required")

	_, err = NewKnowledgeExtractionHook(&core.MockSharedKnowledge{}, &core.MockEmbeddingClient{}, nil, "agent", "domain")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AI client is required")
}

func TestKnowledgeExtractionHook_NeverMutatesResponse(t *testing.T) {
	knowledge := &core.MockSharedKnowledge{}
	embedder := &core.MockEmbeddingClient{}
	aiClient := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{Content: `[]`}, nil
		},
	}

	hook, _ := NewKnowledgeExtractionHook(knowledge, embedder, aiClient, "agent", "domain")
	resp, err := hook.AfterSynthesis(context.Background(), &core.PipelineContext{Request: "test"}, "original response")
	assert.NoError(t, err)
	assert.Equal(t, "original response", resp, "response must never be mutated")
}

func TestKnowledgeExtractionHook_ExtractsAndStores(t *testing.T) {
	storedFragments := make(chan core.KnowledgeFragment, 4)
	knowledge := &core.MockSharedKnowledge{
		StoreFn: func(ctx context.Context, fragment core.KnowledgeFragment) error {
			storedFragments <- fragment
			return nil
		},
	}

	embedder := &core.MockEmbeddingClient{
		GenerateEmbedFn: func(ctx context.Context, texts []string, options *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
			return &core.EmbeddingResponse{
				Embeddings: [][]float32{{0.1, 0.2, 0.3}},
			}, nil
		},
	}

	aiClient := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return &core.AIResponse{
				Content: `[{"content": "High latency is caused by GC pressure", "namespace": "incidents", "importance": 8.0}]`,
			}, nil
		},
	}

	hook, _ := NewKnowledgeExtractionHook(knowledge, embedder, aiClient, "test-agent", "infrastructure")
	t.Cleanup(hook.Close)

	// Call AfterSynthesis — extraction runs async
	hook.AfterSynthesis(context.Background(), &core.PipelineContext{Request: "investigate latency"}, "Found high latency caused by GC pressure")

	select {
	case fragment := <-storedFragments:
		assert.Equal(t, "High latency is caused by GC pressure", fragment.Content)
		assert.Equal(t, "incidents", fragment.Namespace)
		assert.Equal(t, 8.0, fragment.Importance)
		assert.Equal(t, core.ScopeSharedDomain, fragment.Scope)
		assert.Equal(t, "infrastructure", fragment.AgentDomain)
		assert.NotEmpty(t, fragment.Embedding)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stored fragment")
	}
}

func TestKnowledgeExtractionHook_SkipsEmptyResponse(t *testing.T) {
	knowledge := &core.MockSharedKnowledge{}
	embedder := &core.MockEmbeddingClient{}
	aiClient := &mockAIClient{}
	hook, _ := NewKnowledgeExtractionHook(knowledge, embedder, aiClient, "agent", "domain")

	resp, err := hook.AfterSynthesis(context.Background(), &core.PipelineContext{}, "")
	assert.NoError(t, err)
	assert.Equal(t, "", resp)
	assert.Equal(t, 0, knowledge.StoreCt)
}

func TestExtractJSONArray(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"clean array", `[{"a": 1}]`, `[{"a": 1}]`},
		{"with prose", `Here are the results: [{"a": 1}] Hope this helps!`, `[{"a": 1}]`},
		{"empty array", `[]`, `[]`},
		{"nested arrays", `[[1, 2], [3, 4]]`, `[[1, 2], [3, 4]]`},
		{"no array", `just text`, ""},
		{"unclosed bracket", `[1, 2, 3`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractJSONArray(tt.input))
		})
	}
}

func TestTruncateForExtraction(t *testing.T) {
	assert.Equal(t, "short", truncateForExtraction("short", 100))
	assert.Equal(t, "ab...[truncated]", truncateForExtraction("abcdef", 2))
}

// Reuses mockAIClient from hybrid_resolver_test.go (same package).

// Verify interface compliance at compile time.
var _ core.AfterSynthesisHook = (*KnowledgeExtractionHook)(nil)
