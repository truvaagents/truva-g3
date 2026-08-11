package orchestration

import (
	"context"
	"testing"
	"time"
)

func TestTieredSelectionDebugUsesEffectiveRequest(t *testing.T) {
	t.Parallel()

	catalog := setupTestCatalog(25)
	client := NewTieredTestAIClient()
	client.SetResponse(`["test-agent/capability_0"]`)
	store := &tieredTestDebugStore{}
	provider := NewTieredCapabilityProvider(catalog, client, nil)
	provider.SetLLMDebugStore(store)
	provider.SetAIOptionsOverride(&AIOptionsOverride{
		SystemPrompt: StringPtr("developer tiered system prompt"),
		Temperature:  Float32Ptr(0.37),
		MaxTokens:    IntPtr(321),
		Model:        StringPtr("developer-tiered-model"),
	})

	if _, err := provider.GetCapabilities(context.Background(), "test request", nil); err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.interactions) != 1 {
		t.Fatalf("interactions = %d, want 1", len(store.interactions))
	}
	interaction := store.interactions[0]
	if interaction.SystemPrompt != "developer tiered system prompt" ||
		interaction.Temperature != 0.37 || interaction.MaxTokens != 321 {
		t.Fatalf("effective debug generation = %#v", interaction)
	}
	if interaction.Model != "test-model" || interaction.Provider != "test-provider" {
		t.Fatalf("effective debug identity = (%q, %q)", interaction.Model, interaction.Provider)
	}
	if client.LastOptions() == nil || client.LastOptions().SystemPrompt != interaction.SystemPrompt ||
		client.LastOptions().MaxTokens != interaction.MaxTokens {
		t.Fatalf("provider options = %#v, debug = %#v", client.LastOptions(), interaction)
	}
}
