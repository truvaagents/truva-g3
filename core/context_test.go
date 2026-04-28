package core

import (
	"context"
	"testing"
)

func TestWithPipelineEnrichments_RoundTrip(t *testing.T) {
	enrichments := map[string]interface{}{
		EnrichmentConversationHistory: []string{"hello", "world"},
		EnrichmentRAGContext:          "some context",
	}

	ctx := WithPipelineEnrichments(context.Background(), enrichments)
	got := GetPipelineEnrichments(ctx)

	if got == nil {
		t.Fatal("expected enrichments, got nil")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 enrichments, got %d", len(got))
	}
	if got[EnrichmentRAGContext] != "some context" {
		t.Errorf("expected rag_context='some context', got %v", got[EnrichmentRAGContext])
	}
}

func TestGetPipelineEnrichments_NilContext(t *testing.T) {
	// No enrichments set — should return nil without panic
	got := GetPipelineEnrichments(context.Background())
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestWithPipelineEnrichments_EmptyMap(t *testing.T) {
	ctx := WithPipelineEnrichments(context.Background(), map[string]interface{}{})
	got := GetPipelineEnrichments(ctx)

	if got == nil {
		t.Fatal("expected empty map, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %d entries", len(got))
	}
}
