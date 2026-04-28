package core

import (
	"context"
	"sync"
	"time"
)

// MockConversationMemory is a test mock for ConversationMemory.
// Set the function fields to control behavior and assert calls.
type MockConversationMemory struct {
	mu        sync.Mutex
	AddTurnFn func(ctx context.Context, sessionID string, turn ConversationTurn) error
	GetHistFn func(ctx context.Context, sessionID string, maxTurns int) ([]ConversationTurn, error)
	GetFullFn func(ctx context.Context, sessionID string) ([]ConversationTurn, error)
	ClearFn   func(ctx context.Context, sessionID string) error
	AddTurnCt int
	GetHistCt int
	GetFullCt int
	ClearCt   int
}

func (m *MockConversationMemory) AddTurn(ctx context.Context, sessionID string, turn ConversationTurn) error {
	m.mu.Lock()
	m.AddTurnCt++
	m.mu.Unlock()
	if m.AddTurnFn != nil {
		return m.AddTurnFn(ctx, sessionID, turn)
	}
	return nil
}

func (m *MockConversationMemory) GetHistory(ctx context.Context, sessionID string, maxTurns int) ([]ConversationTurn, error) {
	m.mu.Lock()
	m.GetHistCt++
	m.mu.Unlock()
	if m.GetHistFn != nil {
		return m.GetHistFn(ctx, sessionID, maxTurns)
	}
	return nil, nil
}

func (m *MockConversationMemory) GetFullHistory(ctx context.Context, sessionID string) ([]ConversationTurn, error) {
	m.mu.Lock()
	m.GetFullCt++
	m.mu.Unlock()
	if m.GetFullFn != nil {
		return m.GetFullFn(ctx, sessionID)
	}
	return nil, nil
}

func (m *MockConversationMemory) Clear(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	m.ClearCt++
	m.mu.Unlock()
	if m.ClearFn != nil {
		return m.ClearFn(ctx, sessionID)
	}
	return nil
}

// MockSemanticMemory is a test mock for SemanticMemory.
type MockSemanticMemory struct {
	mu       sync.Mutex
	StoreFn  func(ctx context.Context, namespace string, content string, metadata map[string]interface{}) error
	SearchFn func(ctx context.Context, namespace string, query string, topK int) ([]MemoryResult, error)
	DeleteFn func(ctx context.Context, namespace string, filter map[string]interface{}) error
	StoreCt  int
	SearchCt int
	DeleteCt int
}

func (m *MockSemanticMemory) Store(ctx context.Context, namespace string, content string, metadata map[string]interface{}) error {
	m.mu.Lock()
	m.StoreCt++
	m.mu.Unlock()
	if m.StoreFn != nil {
		return m.StoreFn(ctx, namespace, content, metadata)
	}
	return nil
}

func (m *MockSemanticMemory) Search(ctx context.Context, namespace string, query string, topK int) ([]MemoryResult, error) {
	m.mu.Lock()
	m.SearchCt++
	m.mu.Unlock()
	if m.SearchFn != nil {
		return m.SearchFn(ctx, namespace, query, topK)
	}
	return nil, nil
}

func (m *MockSemanticMemory) Delete(ctx context.Context, namespace string, filter map[string]interface{}) error {
	m.mu.Lock()
	m.DeleteCt++
	m.mu.Unlock()
	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, namespace, filter)
	}
	return nil
}

// MockEmbeddingClient is a test mock for EmbeddingClient.
type MockEmbeddingClient struct {
	mu              sync.Mutex
	GenerateEmbedFn func(ctx context.Context, texts []string, options *EmbeddingOptions) (*EmbeddingResponse, error)
	GenerateEmbedCt int
}

func (m *MockEmbeddingClient) GenerateEmbeddings(ctx context.Context, texts []string, options *EmbeddingOptions) (*EmbeddingResponse, error) {
	m.mu.Lock()
	m.GenerateEmbedCt++
	m.mu.Unlock()
	if m.GenerateEmbedFn != nil {
		return m.GenerateEmbedFn(ctx, texts, options)
	}
	return &EmbeddingResponse{
		Embeddings: make([][]float32, len(texts)),
	}, nil
}

// --- Shared Agent Memory Mocks ---

// MockEpisodicMemory is a test mock for EpisodicMemory.
// Set the function fields to control behavior and assert calls.
type MockEpisodicMemory struct {
	mu                  sync.Mutex
	RecordEventFn       func(ctx context.Context, event AgentEvent) error
	QueryEventsFn       func(ctx context.Context, callerDomain string, filter EventFilter) ([]AgentEvent, error)
	QueryEntityHistFn   func(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]AgentEvent, error)
	QueryRecentEventsFn func(ctx context.Context, domain string, since time.Time, limit int) ([]AgentEvent, error)
	DeleteEventsFn      func(ctx context.Context, eventIDs []string) error
	RecordEventCt       int
	QueryEventsCt       int
	QueryEntityHistCt   int
	QueryRecentEventsCt int
	DeleteEventsCt      int
}

func (m *MockEpisodicMemory) RecordEvent(ctx context.Context, event AgentEvent) error {
	m.mu.Lock()
	m.RecordEventCt++
	m.mu.Unlock()
	if m.RecordEventFn != nil {
		return m.RecordEventFn(ctx, event)
	}
	return nil
}

func (m *MockEpisodicMemory) QueryEvents(ctx context.Context, callerDomain string, filter EventFilter) ([]AgentEvent, error) {
	m.mu.Lock()
	m.QueryEventsCt++
	m.mu.Unlock()
	if m.QueryEventsFn != nil {
		return m.QueryEventsFn(ctx, callerDomain, filter)
	}
	return nil, nil
}

func (m *MockEpisodicMemory) QueryEntityHistory(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]AgentEvent, error) {
	m.mu.Lock()
	m.QueryEntityHistCt++
	m.mu.Unlock()
	if m.QueryEntityHistFn != nil {
		return m.QueryEntityHistFn(ctx, callerDomain, entityType, entityID, since)
	}
	return nil, nil
}

func (m *MockEpisodicMemory) QueryRecentEvents(ctx context.Context, domain string, since time.Time, limit int) ([]AgentEvent, error) {
	m.mu.Lock()
	m.QueryRecentEventsCt++
	m.mu.Unlock()
	if m.QueryRecentEventsFn != nil {
		return m.QueryRecentEventsFn(ctx, domain, since, limit)
	}
	return nil, nil
}

func (m *MockEpisodicMemory) DeleteEvents(ctx context.Context, eventIDs []string) error {
	m.mu.Lock()
	m.DeleteEventsCt++
	m.mu.Unlock()
	if m.DeleteEventsFn != nil {
		return m.DeleteEventsFn(ctx, eventIDs)
	}
	return nil
}

// MockInvestigationCoordinator is a test mock for InvestigationCoordinator.
type MockInvestigationCoordinator struct {
	mu          sync.Mutex
	ClaimFn     func(ctx context.Context, agentName, entityID string, ttl time.Duration) (bool, string, error)
	ReleaseFn   func(ctx context.Context, agentName, entityID string) error
	GetActiveFn func(ctx context.Context) (map[string]string, error)
	ClaimCt     int
	ReleaseCt   int
	GetActiveCt int
}

func (m *MockInvestigationCoordinator) ClaimInvestigation(ctx context.Context, agentName, entityID string, ttl time.Duration) (bool, string, error) {
	m.mu.Lock()
	m.ClaimCt++
	m.mu.Unlock()
	if m.ClaimFn != nil {
		return m.ClaimFn(ctx, agentName, entityID, ttl)
	}
	return true, "", nil
}

func (m *MockInvestigationCoordinator) ReleaseInvestigation(ctx context.Context, agentName, entityID string) error {
	m.mu.Lock()
	m.ReleaseCt++
	m.mu.Unlock()
	if m.ReleaseFn != nil {
		return m.ReleaseFn(ctx, agentName, entityID)
	}
	return nil
}

func (m *MockInvestigationCoordinator) GetActiveInvestigations(ctx context.Context) (map[string]string, error) {
	m.mu.Lock()
	m.GetActiveCt++
	m.mu.Unlock()
	if m.GetActiveFn != nil {
		return m.GetActiveFn(ctx)
	}
	return nil, nil
}

// MockSharedKnowledge is a test mock for SharedKnowledge.
type MockSharedKnowledge struct {
	mu                 sync.Mutex
	StoreFn            func(ctx context.Context, fragment KnowledgeFragment) error
	SearchFn           func(ctx context.Context, callerDomain string, namespace string, query string, topK int, weights RetrievalWeights) ([]ScoredKnowledge, error)
	UpdateImportanceFn func(ctx context.Context, fragmentID string, newImportance float64) error
	StoreCt            int
	SearchCt           int
	UpdateImportanceCt int
}

func (m *MockSharedKnowledge) StoreKnowledge(ctx context.Context, fragment KnowledgeFragment) error {
	m.mu.Lock()
	m.StoreCt++
	m.mu.Unlock()
	if m.StoreFn != nil {
		return m.StoreFn(ctx, fragment)
	}
	return nil
}

func (m *MockSharedKnowledge) SearchKnowledge(ctx context.Context, callerDomain string, namespace string, query string, topK int, weights RetrievalWeights) ([]ScoredKnowledge, error) {
	m.mu.Lock()
	m.SearchCt++
	m.mu.Unlock()
	if m.SearchFn != nil {
		return m.SearchFn(ctx, callerDomain, namespace, query, topK, weights)
	}
	return nil, nil
}

func (m *MockSharedKnowledge) UpdateImportance(ctx context.Context, fragmentID string, newImportance float64) error {
	m.mu.Lock()
	m.UpdateImportanceCt++
	m.mu.Unlock()
	if m.UpdateImportanceFn != nil {
		return m.UpdateImportanceFn(ctx, fragmentID, newImportance)
	}
	return nil
}

// MockMemoryReflector is a test mock for MemoryReflector.
type MockMemoryReflector struct {
	mu        sync.Mutex
	ReflectFn func(ctx context.Context, entityType, entityID string, since time.Time) ([]KnowledgeFragment, error)
	CompactFn func(ctx context.Context, config CompactionConfig) error
	ReflectCt int
	CompactCt int
}

func (m *MockMemoryReflector) Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]KnowledgeFragment, error) {
	m.mu.Lock()
	m.ReflectCt++
	m.mu.Unlock()
	if m.ReflectFn != nil {
		return m.ReflectFn(ctx, entityType, entityID, since)
	}
	return nil, nil
}

func (m *MockMemoryReflector) Compact(ctx context.Context, config CompactionConfig) error {
	m.mu.Lock()
	m.CompactCt++
	m.mu.Unlock()
	if m.CompactFn != nil {
		return m.CompactFn(ctx, config)
	}
	return nil
}

// MockEventSummarizer is a test mock for EventSummarizer.
type MockEventSummarizer struct {
	mu               sync.Mutex
	SummarizeStepsFn func(ctx context.Context, steps []StepSummaryInput) (map[string]StepSummary, error)
	SummarizeStepsCt int
}

func (m *MockEventSummarizer) SummarizeSteps(ctx context.Context, steps []StepSummaryInput) (map[string]StepSummary, error) {
	m.mu.Lock()
	m.SummarizeStepsCt++
	m.mu.Unlock()
	if m.SummarizeStepsFn != nil {
		return m.SummarizeStepsFn(ctx, steps)
	}
	return map[string]StepSummary{}, nil
}

// MockActivityCompactor is a test mock for ActivityCompactor.
type MockActivityCompactor struct {
	mu              sync.Mutex
	CompactEventsFn func(ctx context.Context, events []AgentEvent, maxTokens int) (string, error)
	CompactEventsCt int
	UpdateDigestFn  func(ctx context.Context, previousDigest string, newEvents []AgentEvent, maxTokens int) (string, error)
	UpdateDigestCt  int
}

func (m *MockActivityCompactor) CompactEvents(ctx context.Context, events []AgentEvent, maxTokens int) (string, error) {
	m.mu.Lock()
	m.CompactEventsCt++
	m.mu.Unlock()
	if m.CompactEventsFn != nil {
		return m.CompactEventsFn(ctx, events, maxTokens)
	}
	return "", nil
}

func (m *MockActivityCompactor) UpdateDigest(ctx context.Context, previousDigest string, newEvents []AgentEvent, maxTokens int) (string, error) {
	m.mu.Lock()
	m.UpdateDigestCt++
	m.mu.Unlock()
	if m.UpdateDigestFn != nil {
		return m.UpdateDigestFn(ctx, previousDigest, newEvents, maxTokens)
	}
	return previousDigest, nil
}

// MockDigestCache is a test mock for DigestCache.
type MockDigestCache struct {
	mu    sync.Mutex
	GetFn func(ctx context.Context, domain string) ([]byte, error)
	GetCt int
	SetFn func(ctx context.Context, domain string, data []byte, ttl time.Duration) error
	SetCt int
}

func (m *MockDigestCache) GetDigest(ctx context.Context, domain string) ([]byte, error) {
	m.mu.Lock()
	m.GetCt++
	m.mu.Unlock()
	if m.GetFn != nil {
		return m.GetFn(ctx, domain)
	}
	return nil, nil
}

func (m *MockDigestCache) SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error {
	m.mu.Lock()
	m.SetCt++
	m.mu.Unlock()
	if m.SetFn != nil {
		return m.SetFn(ctx, domain, data, ttl)
	}
	return nil
}

// MockActivityCoordinator is a test mock for ActivityCoordinator.
type MockActivityCoordinator struct {
	mu                    sync.Mutex
	AnnounceActivityFn    func(ctx context.Context, signal ActivitySignal) error
	AnnounceActivityCt    int
	UpdateStatusFn        func(ctx context.Context, requestID, status string) error
	UpdateStatusCt        int
	GetDomainActivitiesFn func(ctx context.Context, domain string) ([]ActivitySignal, error)
	GetDomainActivitiesCt int
	CompleteActivityFn    func(ctx context.Context, requestID string) error
	CompleteActivityCt    int
}

func (m *MockActivityCoordinator) AnnounceActivity(ctx context.Context, signal ActivitySignal) error {
	m.mu.Lock()
	m.AnnounceActivityCt++
	m.mu.Unlock()
	if m.AnnounceActivityFn != nil {
		return m.AnnounceActivityFn(ctx, signal)
	}
	return nil
}

func (m *MockActivityCoordinator) UpdateStatus(ctx context.Context, requestID, status string) error {
	m.mu.Lock()
	m.UpdateStatusCt++
	m.mu.Unlock()
	if m.UpdateStatusFn != nil {
		return m.UpdateStatusFn(ctx, requestID, status)
	}
	return nil
}

func (m *MockActivityCoordinator) GetDomainActivities(ctx context.Context, domain string) ([]ActivitySignal, error) {
	m.mu.Lock()
	m.GetDomainActivitiesCt++
	m.mu.Unlock()
	if m.GetDomainActivitiesFn != nil {
		return m.GetDomainActivitiesFn(ctx, domain)
	}
	return nil, nil
}

func (m *MockActivityCoordinator) CompleteActivity(ctx context.Context, requestID string) error {
	m.mu.Lock()
	m.CompleteActivityCt++
	m.mu.Unlock()
	if m.CompleteActivityFn != nil {
		return m.CompleteActivityFn(ctx, requestID)
	}
	return nil
}

// MockDistributedLock is a test mock for DistributedLock.
type MockDistributedLock struct {
	mu        sync.Mutex
	AcquireFn func(ctx context.Context, key string, ttl time.Duration) (bool, error)
	AcquireCt int
	ReleaseFn func(ctx context.Context, key string) error
	ReleaseCt int
}

func (m *MockDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	m.AcquireCt++
	m.mu.Unlock()
	if m.AcquireFn != nil {
		return m.AcquireFn(ctx, key, ttl)
	}
	return true, nil
}

func (m *MockDistributedLock) Release(ctx context.Context, key string) error {
	m.mu.Lock()
	m.ReleaseCt++
	m.mu.Unlock()
	if m.ReleaseFn != nil {
		return m.ReleaseFn(ctx, key)
	}
	return nil
}
