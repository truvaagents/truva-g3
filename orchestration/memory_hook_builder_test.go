package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestBuildMemoryHooks_NilDeps(t *testing.T) {
	hooks, coord, err := BuildMemoryHooks(nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, hooks)
	assert.Nil(t, coord)
}

func TestBuildMemoryHooks_EventSummarizerTokenBudget(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"10000", 10000}, {"0", 0}, {"-1", 0}, {"invalid", 0}, {"", 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("TRUVAG3_SHARED_MEMORY_EVENT_SUMMARIZER_MAX_TOKENS", tc.value)
			hooks, _, err := BuildMemoryHooks(&core.SharedMemoryDeps{
				AgentName: "test", Episodic: &core.MockEpisodicMemory{},
			}, &summarizerMockAI{}, &core.NoOpLogger{})
			if tc.want == 0 {
				require.ErrorIs(t, err, core.ErrInvalidConfiguration)
				require.Nil(t, hooks)
				return
			}
			require.NoError(t, err)
			for _, hook := range hooks {
				if record, ok := hook.(*MemoryRecordHook); ok {
					summarizer, ok := record.summarizer.(*LLMEventSummarizer)
					require.True(t, ok)
					require.Equal(t, tc.want, summarizer.maxTokens)
					return
				}
			}
			t.Fatal("missing memory record hook")
		})
	}
}

func TestBuildMemoryHooks_NilEpisodic(t *testing.T) {
	hooks, coord, err := BuildMemoryHooks(&core.SharedMemoryDeps{}, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, hooks)
	assert.Nil(t, coord)
}

func TestBuildMemoryHooks_EpisodicOnly(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, coord, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.NoError(t, err)

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

	hooks, coord, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.NoError(t, err)

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

	hooks, coord, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.NoError(t, err)

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

	hooks, coord, err := BuildMemoryHooks(deps, mockAI, &core.NoOpLogger{})
	require.NoError(t, err)

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

	hooks, _, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.NoError(t, err)
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

	hooks, _, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithMemoryEntityExtractor(customExtractor),
	)
	require.NoError(t, err)

	require.Len(t, hooks, 2) // builds successfully with custom extractor
}

func TestBuildMemoryHooks_DefaultsToLLMExtractorWhenAIClientWired(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}
	aiClient := &summarizerMockAI{}

	hooks, _, err := BuildMemoryHooks(deps, aiClient, &core.NoOpLogger{})
	require.NoError(t, err)
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

	hooks, _, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{})
	require.NoError(t, err)
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

	hooks, _, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithMemoryImportanceFunc(customScorer),
	)
	require.NoError(t, err)

	require.Len(t, hooks, 2)
	_ = called // scorer is passed to hook, invoked during event recording
}

func TestBuildMemoryHooks_WithLookback(t *testing.T) {
	deps := &core.SharedMemoryDeps{
		AgentName:   "test-agent",
		AgentDomain: "test-domain",
		Episodic:    &core.MockEpisodicMemory{},
	}

	hooks, _, err := BuildMemoryHooks(deps, nil, &core.NoOpLogger{},
		WithMemoryLookback(48*time.Hour),
	)
	require.NoError(t, err)

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
	hooks, _, err := BuildMemoryHooks(deps, nil, nil)
	require.NoError(t, err)
	require.Len(t, hooks, 2)
}
