package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Compile-time interface compliance checks
var (
	_ ConversationMemory = (*MockConversationMemory)(nil)
	_ SemanticMemory     = (*MockSemanticMemory)(nil)
	_ EmbeddingClient    = (*MockEmbeddingClient)(nil)
)

func TestMockConversationMemory_Counters(t *testing.T) {
	m := &MockConversationMemory{}
	ctx := context.Background()

	_ = m.AddTurn(ctx, "s1", ConversationTurn{Role: "user", Content: "hi"})
	_ = m.AddTurn(ctx, "s1", ConversationTurn{Role: "assistant", Content: "hello"})
	_, _ = m.GetHistory(ctx, "s1", 10)
	_ = m.Clear(ctx, "s1")

	if m.AddTurnCt != 2 {
		t.Errorf("AddTurnCt: expected 2, got %d", m.AddTurnCt)
	}
	if m.GetHistCt != 1 {
		t.Errorf("GetHistCt: expected 1, got %d", m.GetHistCt)
	}
	if m.ClearCt != 1 {
		t.Errorf("ClearCt: expected 1, got %d", m.ClearCt)
	}
}

func TestMockConversationMemory_FunctionOverrides(t *testing.T) {
	turns := []ConversationTurn{
		{Role: "user", Content: "hi", Timestamp: time.Now()},
	}
	m := &MockConversationMemory{
		GetHistFn: func(ctx context.Context, sessionID string, maxTurns int) ([]ConversationTurn, error) {
			return turns, nil
		},
		AddTurnFn: func(ctx context.Context, sessionID string, turn ConversationTurn) error {
			return errors.New("storage full")
		},
	}
	ctx := context.Background()

	err := m.AddTurn(ctx, "s1", ConversationTurn{})
	if err == nil || err.Error() != "storage full" {
		t.Errorf("expected 'storage full' error, got %v", err)
	}

	history, err := m.GetHistory(ctx, "s1", 10)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("expected 1 turn, got %d", len(history))
	}
}

func TestMockSemanticMemory_Counters(t *testing.T) {
	m := &MockSemanticMemory{}
	ctx := context.Background()

	_ = m.Store(ctx, "ns", "content", nil)
	_, _ = m.Search(ctx, "ns", "query", 5)
	_ = m.Delete(ctx, "ns", nil)

	if m.StoreCt != 1 {
		t.Errorf("StoreCt: expected 1, got %d", m.StoreCt)
	}
	if m.SearchCt != 1 {
		t.Errorf("SearchCt: expected 1, got %d", m.SearchCt)
	}
	if m.DeleteCt != 1 {
		t.Errorf("DeleteCt: expected 1, got %d", m.DeleteCt)
	}
}

func TestMockSemanticMemory_FunctionOverrides(t *testing.T) {
	m := &MockSemanticMemory{
		SearchFn: func(ctx context.Context, namespace string, query string, topK int) ([]MemoryResult, error) {
			return []MemoryResult{
				{Content: "result1", Score: 0.95},
				{Content: "result2", Score: 0.80},
			}, nil
		},
	}
	ctx := context.Background()

	results, err := m.Search(ctx, "ns", "query", 2)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	if results[0].Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", results[0].Score)
	}
}

func TestMockEmbeddingClient_Counters(t *testing.T) {
	m := &MockEmbeddingClient{}
	ctx := context.Background()

	_, _ = m.GenerateEmbeddings(ctx, []string{"a", "b"}, nil)
	_, _ = m.GenerateEmbeddings(ctx, []string{"c"}, &EmbeddingOptions{Model: "test"})

	if m.GenerateEmbedCt != 2 {
		t.Errorf("GenerateEmbedCt: expected 2, got %d", m.GenerateEmbedCt)
	}
}

func TestMockEmbeddingClient_FunctionOverride(t *testing.T) {
	m := &MockEmbeddingClient{
		GenerateEmbedFn: func(ctx context.Context, texts []string, options *EmbeddingOptions) (*EmbeddingResponse, error) {
			embeddings := make([][]float32, len(texts))
			for i := range texts {
				embeddings[i] = []float32{0.1, 0.2, 0.3}
			}
			return &EmbeddingResponse{
				Embeddings: embeddings,
				Model:      "test-model",
				Provider:   "test",
			}, nil
		},
	}
	ctx := context.Background()

	resp, err := m.GenerateEmbeddings(ctx, []string{"hello"}, nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp.Model != "test-model" {
		t.Errorf("expected model 'test-model', got %s", resp.Model)
	}
	if len(resp.Embeddings[0]) != 3 {
		t.Errorf("expected 3 dimensions, got %d", len(resp.Embeddings[0]))
	}
}
