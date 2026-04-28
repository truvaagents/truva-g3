package core

import (
	"context"
	"time"
)

// NoOpConversationMemory is a no-op implementation of ConversationMemory.
// Used as a zero-config default when no memory backend is configured.
type NoOpConversationMemory struct{}

func (n *NoOpConversationMemory) AddTurn(ctx context.Context, sessionID string, turn ConversationTurn) error {
	return nil
}

func (n *NoOpConversationMemory) GetHistory(ctx context.Context, sessionID string, maxTurns int) ([]ConversationTurn, error) {
	return nil, nil
}

func (n *NoOpConversationMemory) Clear(ctx context.Context, sessionID string) error {
	return nil
}

// NoOpSemanticMemory is a no-op implementation of SemanticMemory.
type NoOpSemanticMemory struct{}

func (n *NoOpSemanticMemory) Store(ctx context.Context, namespace string, content string, metadata map[string]interface{}) error {
	return nil
}

func (n *NoOpSemanticMemory) Search(ctx context.Context, namespace string, query string, topK int) ([]MemoryResult, error) {
	return nil, nil
}

func (n *NoOpSemanticMemory) Delete(ctx context.Context, namespace string, filter map[string]interface{}) error {
	return nil
}

// NoOpEmbeddingClient is a no-op implementation of EmbeddingClient.
type NoOpEmbeddingClient struct{}

func (n *NoOpEmbeddingClient) GenerateEmbeddings(ctx context.Context, texts []string, options *EmbeddingOptions) (*EmbeddingResponse, error) {
	return &EmbeddingResponse{
		Embeddings: make([][]float32, len(texts)),
	}, nil
}

// --- Shared Agent Memory NoOps ---

// NoOpEpisodicMemory is a no-op implementation of EpisodicMemory.
// Used when cross-agent shared memory is not configured. Events are silently discarded.
type NoOpEpisodicMemory struct{}

func (n *NoOpEpisodicMemory) RecordEvent(ctx context.Context, event AgentEvent) error {
	return nil
}

func (n *NoOpEpisodicMemory) QueryEvents(ctx context.Context, callerDomain string, filter EventFilter) ([]AgentEvent, error) {
	return nil, nil
}

func (n *NoOpEpisodicMemory) QueryEntityHistory(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]AgentEvent, error) {
	return nil, nil
}

func (n *NoOpEpisodicMemory) QueryRecentEvents(ctx context.Context, domain string, since time.Time, limit int) ([]AgentEvent, error) {
	return nil, nil
}

func (n *NoOpEpisodicMemory) DeleteEvents(ctx context.Context, eventIDs []string) error {
	return nil
}

// NoOpInvestigationCoordinator is a no-op implementation of InvestigationCoordinator.
// Claims always succeed (no coordination). Suitable for single-agent or dev/test.
type NoOpInvestigationCoordinator struct{}

func (n *NoOpInvestigationCoordinator) ClaimInvestigation(ctx context.Context, agentName, entityID string, ttl time.Duration) (bool, string, error) {
	return true, "", nil // Always succeeds — no contention
}

func (n *NoOpInvestigationCoordinator) ReleaseInvestigation(ctx context.Context, agentName, entityID string) error {
	return nil
}

func (n *NoOpInvestigationCoordinator) GetActiveInvestigations(ctx context.Context) (map[string]string, error) {
	return nil, nil
}

// NoOpSharedKnowledge is a no-op implementation of SharedKnowledge.
// Stores nothing, returns no results. Used when Phase 2 (semantic knowledge) is not configured.
type NoOpSharedKnowledge struct{}

func (n *NoOpSharedKnowledge) StoreKnowledge(ctx context.Context, fragment KnowledgeFragment) error {
	return nil
}

func (n *NoOpSharedKnowledge) SearchKnowledge(ctx context.Context, callerDomain string, namespace string, query string, topK int, weights RetrievalWeights) ([]ScoredKnowledge, error) {
	return nil, nil
}

func (n *NoOpSharedKnowledge) UpdateImportance(ctx context.Context, fragmentID string, newImportance float64) error {
	return nil
}

// NoOpMemoryReflector is a no-op implementation of MemoryReflector.
// Generates no knowledge, performs no compaction. Used when Phase 3 (reflection) is not configured.
type NoOpMemoryReflector struct{}

func (n *NoOpMemoryReflector) Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]KnowledgeFragment, error) {
	return nil, nil
}

func (n *NoOpMemoryReflector) Compact(ctx context.Context, config CompactionConfig) error {
	return nil
}

// NoOpEventSummarizer is a no-op implementation of EventSummarizer.
// Returns empty map — caller falls back to heuristic summaries.
type NoOpEventSummarizer struct{}

func (n *NoOpEventSummarizer) SummarizeSteps(ctx context.Context, steps []StepSummaryInput) (map[string]StepSummary, error) {
	return map[string]StepSummary{}, nil
}

// NoOpActivityCompactor returns empty digest — caller falls back to raw events.
type NoOpActivityCompactor struct{}

func (n *NoOpActivityCompactor) CompactEvents(ctx context.Context, events []AgentEvent, maxTokens int) (string, error) {
	return "", nil
}

func (n *NoOpActivityCompactor) UpdateDigest(ctx context.Context, previousDigest string, newEvents []AgentEvent, maxTokens int) (string, error) {
	return previousDigest, nil
}

// NoOpDigestCache always returns cache miss — full compaction every request.
type NoOpDigestCache struct{}

func (n *NoOpDigestCache) GetDigest(ctx context.Context, domain string) ([]byte, error) {
	return nil, nil
}

func (n *NoOpDigestCache) SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error {
	return nil
}

// NoOpActivityCoordinator is a no-op implementation of ActivityCoordinator.
// All operations succeed silently — no signals emitted or read.
type NoOpActivityCoordinator struct{}

func (n *NoOpActivityCoordinator) AnnounceActivity(ctx context.Context, signal ActivitySignal) error {
	return nil
}
func (n *NoOpActivityCoordinator) UpdateStatus(ctx context.Context, requestID, status string) error {
	return nil
}
func (n *NoOpActivityCoordinator) GetDomainActivities(ctx context.Context, domain string) ([]ActivitySignal, error) {
	return nil, nil
}
func (n *NoOpActivityCoordinator) CompleteActivity(ctx context.Context, requestID string) error {
	return nil
}

// NoOpDistributedLock always acquires — suitable for single-replica deployments.
type NoOpDistributedLock struct{}

func (n *NoOpDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return true, nil
}
func (n *NoOpDistributedLock) Release(ctx context.Context, key string) error { return nil }
