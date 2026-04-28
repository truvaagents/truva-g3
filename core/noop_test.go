package core

import (
	"context"
	"testing"
)

// Compile-time interface compliance checks
var (
	_ ConversationMemory = (*NoOpConversationMemory)(nil)
	_ SemanticMemory     = (*NoOpSemanticMemory)(nil)
	_ EmbeddingClient    = (*NoOpEmbeddingClient)(nil)
)

func TestNoOpConversationMemory(t *testing.T) {
	m := &NoOpConversationMemory{}
	ctx := context.Background()

	if err := m.AddTurn(ctx, "sess1", ConversationTurn{Role: "user", Content: "hi"}); err != nil {
		t.Errorf("AddTurn: unexpected error: %v", err)
	}

	history, err := m.GetHistory(ctx, "sess1", 10)
	if err != nil {
		t.Errorf("GetHistory: unexpected error: %v", err)
	}
	if history != nil {
		t.Errorf("GetHistory: expected nil, got %v", history)
	}

	if err := m.Clear(ctx, "sess1"); err != nil {
		t.Errorf("Clear: unexpected error: %v", err)
	}
}

func TestNoOpSemanticMemory(t *testing.T) {
	m := &NoOpSemanticMemory{}
	ctx := context.Background()

	if err := m.Store(ctx, "ns", "content", nil); err != nil {
		t.Errorf("Store: unexpected error: %v", err)
	}

	results, err := m.Search(ctx, "ns", "query", 5)
	if err != nil {
		t.Errorf("Search: unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("Search: expected nil, got %v", results)
	}

	if err := m.Delete(ctx, "ns", nil); err != nil {
		t.Errorf("Delete: unexpected error: %v", err)
	}
}

func TestNoOpEmbeddingClient(t *testing.T) {
	c := &NoOpEmbeddingClient{}
	ctx := context.Background()

	texts := []string{"hello", "world"}
	resp, err := c.GenerateEmbeddings(ctx, texts, nil)
	if err != nil {
		t.Errorf("GenerateEmbeddings: unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("GenerateEmbeddings: expected response, got nil")
	}
	if len(resp.Embeddings) != 2 {
		t.Errorf("expected 2 embeddings (one per text), got %d", len(resp.Embeddings))
	}
}
