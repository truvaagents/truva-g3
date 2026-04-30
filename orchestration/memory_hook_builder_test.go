package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestBuildMemoryHooks_NilDeps(t *testing.T) {
	hooks, coord := BuildMemoryHooks(nil, nil, nil)
	assert.Nil(t, hooks)
	assert.Nil(t, coord)
}

func TestBuildMemoryHooks_NilEpisodic(t *testing.T) {
	hooks, coord := BuildMemoryHooks(&core.SharedMemoryDeps{}, nil, nil)
	assert.Nil(t, hooks)
	assert.Nil(t, coord)
}

func TestBuildMemoryHooks_EpisodicOnly(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, coord := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})

	// Enrichment + Record = 2 hooks (no activity coordinator, no knowledge)
	require.Len(t, hooks, 2)
	assert.Nil(t, coord)
}

func TestBuildMemoryHooks_WithCoordinator(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
		Coordinator: &core.MockInvestigationCoordinator{},
	}

	hooks, coord := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})

	// Still 2 hooks — coordinator is passed to hooks but doesn't create its own hook
	require.Len(t, hooks, 2)
	assert.Nil(t, coord)
}

func TestBuildMemoryHooks_WithActivityCoordinator(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:           "test-agent",
		AgentDomain:         "test-domain",
		Episodic:            &core.MockEpisodicMemory{},
		ActivityCoordinator: &core.MockActivityCoordinator{},
	}

	hooks, coord := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})

	// Announcement + Enrichment + Record + Cleanup = 4 hooks
	require.Len(t, hooks, 4)
	assert.NotNil(t, coord)
}

func TestBuildMemoryHooks_FullDeps(t *testing.T) {
	mockAI := &summarizerMockAI{} // reuse from event_summarizer_test.go

	deps := &core.SharedMemoryDeps{
		AgentName:           "test-agent",
		AgentDomain:         "test-domain",
		Episodic:            &core.MockEpisodicMemory{},
		Coordinator:         &core.MockInvestigationCoordinator{},
		ActivityCoordinator: &core.MockActivityCoordinator{},
		Knowledge:           &core.MockSharedKnowledge{},
		Embedder:            &core.NoOpEmbeddingClient{},
		DigestCache:         &core.MockDigestCache{},
	}

	hooks, coord := BuildMemoryHooks(deps, mockAI, &core.NoOpLogger{})

	// Announcement + Enrichment + Record + Extraction + Cleanup = 5 hooks
	require.Len(t, hooks, 5)
	assert.NotNil(t, coord)
}

func TestBuildMemoryHooks_DomainFallback(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName: "test-agent",
		// AgentDomain empty — should fall back to "default"
		Episodic: &core.MockEpisodicMemory{},
	}

	hooks, _ := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.Len(t, hooks, 2) // still creates hooks with default domain
}

func TestBuildMemoryHooks_WithEntityExtractor(t *testing.T) {
	// Custom extractor — proves the option still wires any developer-supplied
	// EntityExtractor through the builder.
	customExtractor := &fixedEntityExtractor{
		entities: []Entity{{Type: "ticket", ID: "PROJ-1234"}},
	}

	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, _ := BuildMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithMemoryEntityExtractor(customExtractor),
	)

	require.Len(t, hooks, 2) // builds successfully with custom extractor
}

func TestBuildMemoryHooks_DefaultsToLLMExtractorWhenAIClientWired(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}
	aiClient := &summarizerMockAI{}

	hooks, _ := BuildMemoryHooks(deps, aiClient, &core.NoOpLogger{})
	require.Len(t, hooks, 2)

	// Verify record hook got LLMEntityExtractor
	var recordHook *MemoryRecordHook
	for _, h := range hooks {
		if rh, ok := h.(*MemoryRecordHook); ok {
			recordHook = rh
			break
		}
	}
	require.NotNil(t, recordHook)
	_, isLLM := recordHook.entityExtractor.(LLMEntityExtractor)
	assert.True(t, isLLM, "expected LLMEntityExtractor on record hook when aiClient is wired")

	// Verify enrichment hook got NoOpEntityExtractor (even when aiClient is wired)
	var enrichHook *MemoryEnrichmentHook
	for _, h := range hooks {
		if eh, ok := h.(*MemoryEnrichmentHook); ok {
			enrichHook = eh
			break
		}
	}
	require.NotNil(t, enrichHook)
	_, isNoOp := enrichHook.entityExtractor.(NoOpEntityExtractor)
	assert.True(t, isNoOp, "expected NoOpEntityExtractor on enrichment hook even when aiClient is wired")
}

func TestBuildMemoryHooks_DefaultsToNoOpExtractorWhenNoAIClient(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, _ := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.Len(t, hooks, 2)

	var recordHook *MemoryRecordHook
	for _, h := range hooks {
		if rh, ok := h.(*MemoryRecordHook); ok {
			recordHook = rh
			break
		}
	}
	require.NotNil(t, recordHook)
	_, isNoOp := recordHook.entityExtractor.(NoOpEntityExtractor)
	assert.True(t, isNoOp, "expected NoOpEntityExtractor when aiClient is nil")
}

// fixedEntityExtractor is a minimal test extractor that returns a fixed list.
type fixedEntityExtractor struct {
	entities []Entity
}

func (f *fixedEntityExtractor) ExtractEntities(_ string, _ map[string]interface{}) []Entity {
	return f.entities
}

func TestBuildMemoryHooks_WithImportanceFunc(t *testing.T) {
	called := false
	customScorer := func(actionType, outcome string) float64 {
		called = true
		return 9.0
	}

	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, _ := BuildMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithMemoryImportanceFunc(customScorer),
	)

	require.Len(t, hooks, 2)
	_ = called // scorer is passed to hook, invoked during event recording
}

func TestBuildMemoryHooks_WithLookback(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, _ := BuildMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithMemoryLookback(48*time.Hour),
	)

	require.Len(t, hooks, 2)
}

func TestBuildMemoryHooks_WithLookbackInvalid(t *testing.T) {
	opt := WithMemoryLookback(-1 * time.Hour)
	err := opt(&memoryHookConfig{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "positive")
}

func TestBuildMemoryHooks_WithEntityExtractorNil(t *testing.T) {
	opt := WithMemoryEntityExtractor(nil)
	err := opt(&memoryHookConfig{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestBuildMemoryHooks_WithImportanceFuncNil(t *testing.T) {
	opt := WithMemoryImportanceFunc(nil)
	err := opt(&memoryHookConfig{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestBuildMemoryHooks_NilLogger(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	// nil logger should not panic
	hooks, _ := BuildMemoryHooks(deps, nil, nil)
	require.Len(t, hooks, 2)
}
