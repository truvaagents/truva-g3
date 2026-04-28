package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"

	"github.com/google/uuid"
)

// Compile-time interface compliance checks.
var (
	_ core.EpisodicMemory           = (*InMemoryEpisodicMemory)(nil)
	_ core.InvestigationCoordinator = (*InMemoryInvestigationCoordinator)(nil)
)

// --- In-Memory Episodic Memory ---

// InMemoryEpisodicMemory implements EpisodicMemory using pure Go data structures.
// Suitable for single-process multi-agent scenarios (multiple agents in one binary),
// development, testing, and edge deployments where Valkey is not available.
//
// NOT shared across processes — this is the trade-off for zero infrastructure.
// Events are stored in a thread-safe slice with oldest-first eviction.
type InMemoryEpisodicMemory struct {
	mu      sync.RWMutex
	events  []core.AgentEvent
	maxSize int
	domain  string
}

// NewInMemoryEpisodicMemory creates a new in-memory episodic memory.
// maxSize controls the maximum number of events (default 10000, oldest evicted first).
func NewInMemoryEpisodicMemory(domain string, maxSize int) *InMemoryEpisodicMemory {
	if maxSize <= 0 {
		maxSize = 10000
	}
	if domain == "" {
		domain = "default"
	}
	return &InMemoryEpisodicMemory{
		events:  make([]core.AgentEvent, 0, 256),
		maxSize: maxSize,
		domain:  domain,
	}
}

// RecordEvent appends an event to the in-memory store.
func (m *InMemoryEpisodicMemory) RecordEvent(ctx context.Context, event core.AgentEvent) error {
	if event.EventID == "" {
		event.EventID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.events = append(m.events, event)

	// Evict oldest if over capacity
	if len(m.events) > m.maxSize {
		// Remove oldest 10% to avoid evicting on every insert
		evictCount := m.maxSize / 10
		if evictCount < 1 {
			evictCount = 1
		}
		m.events = m.events[evictCount:]
	}

	return nil
}

// QueryEvents retrieves events matching the filter criteria.
func (m *InMemoryEpisodicMemory) QueryEvents(ctx context.Context, callerDomain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	var results []core.AgentEvent

	// Iterate in reverse (most recent first)
	for i := len(m.events) - 1; i >= 0; i-- {
		event := m.events[i]

		// Scope filtering
		if !isVisibleInMemory(&event, callerDomain, filter.AgentName) {
			continue
		}

		// Time filtering
		if !filter.Since.IsZero() && event.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && event.Timestamp.After(filter.Until) {
			continue
		}

		// Entity filtering — check Entities slice first, fall back to singular fields
		if filter.EntityType != "" || filter.EntityID != "" {
			matched := false
			for _, entity := range event.Entities {
				if (filter.EntityType == "" || entity.Type == filter.EntityType) &&
					(filter.EntityID == "" || entity.ID == filter.EntityID) {
					matched = true
					break
				}
			}
			if !matched {
				// Backward compat: check singular fields
				if (filter.EntityType == "" || event.EntityType == filter.EntityType) &&
					(filter.EntityID == "" || event.EntityID == filter.EntityID) {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}

		// Agent filtering
		if filter.AgentName != "" && event.AgentName != filter.AgentName {
			continue
		}
		if filter.AgentDomain != "" && event.AgentDomain != filter.AgentDomain {
			continue
		}

		// Action type filtering
		if len(filter.ActionTypes) > 0 && !containsString(filter.ActionTypes, event.ActionType) {
			continue
		}

		results = append(results, event)
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// QueryEntityHistory returns all events for a specific entity, ordered chronologically.
func (m *InMemoryEpisodicMemory) QueryEntityHistory(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]core.AgentEvent, error) {
	events, err := m.QueryEvents(ctx, callerDomain, core.EventFilter{
		EntityType: entityType,
		EntityID:   entityID,
		Since:      since,
		Limit:      50,
	})
	if err != nil {
		return nil, err
	}

	// QueryEvents returns most-recent-first; reverse for chronological
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	return events, nil
}

// QueryRecentEvents returns the most recent events across all entities.
// Results ordered by timestamp descending (most recent first).
// Enforces scope-based visibility using callerDomain.
func (m *InMemoryEpisodicMemory) QueryRecentEvents(ctx context.Context, callerDomain string, since time.Time, limit int) ([]core.AgentEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	// Walk backwards (newest first) since events are stored oldest-first
	var results []core.AgentEvent
	for i := len(m.events) - 1; i >= 0 && len(results) < limit; i-- {
		e := m.events[i]
		if !since.IsZero() && e.Timestamp.Before(since) {
			continue // Defensive: use continue (not break) in case of out-of-order timestamps
		}
		if !isVisibleInMemory(&e, callerDomain, "") {
			continue // Scope filtering — don't leak private/cross-domain events
		}
		results = append(results, e)
	}
	return results, nil
}

// DeleteEvents removes events by ID from the in-memory store.
// Idempotent — missing IDs are silently ignored.
func (m *InMemoryEpisodicMemory) DeleteEvents(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}

	deleteSet := make(map[string]bool, len(eventIDs))
	for _, id := range eventIDs {
		deleteSet[id] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	filtered := m.events[:0]
	for _, e := range m.events {
		if !deleteSet[e.EventID] {
			filtered = append(filtered, e)
		}
	}
	m.events = filtered

	return nil
}

func isVisibleInMemory(event *core.AgentEvent, callerDomain, callerAgent string) bool {
	switch event.Scope {
	case core.ScopeGlobal:
		return true
	case core.ScopeSharedDomain:
		return callerDomain == event.AgentDomain
	case core.ScopePrivate:
		return callerDomain == event.AgentDomain && callerAgent != "" && callerAgent == event.AgentName
	default:
		return true
	}
}

// --- In-Memory Investigation Coordinator ---

type investigationClaim struct {
	agentName string
	expiresAt time.Time
}

// InMemoryInvestigationCoordinator implements InvestigationCoordinator using
// pure Go sync primitives. Suitable for single-process scenarios where multiple
// agent goroutines need coordination without external infrastructure.
type InMemoryInvestigationCoordinator struct {
	mu     sync.Mutex
	claims map[string]investigationClaim // entityID → claim
}

// NewInMemoryInvestigationCoordinator creates a new in-memory investigation coordinator.
func NewInMemoryInvestigationCoordinator() *InMemoryInvestigationCoordinator {
	return &InMemoryInvestigationCoordinator{
		claims: make(map[string]investigationClaim),
	}
}

// ClaimInvestigation attempts to claim exclusive investigation of an entity.
// TTL is enforced via lazy expiry — checked on every access.
func (m *InMemoryInvestigationCoordinator) ClaimInvestigation(ctx context.Context, agentName, entityID string, ttl time.Duration) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check existing claim (with lazy expiry)
	if existing, ok := m.claims[entityID]; ok {
		if time.Now().Before(existing.expiresAt) {
			return false, existing.agentName, nil // Still active
		}
		// Expired — remove and allow new claim
		delete(m.claims, entityID)
	}

	m.claims[entityID] = investigationClaim{
		agentName: agentName,
		expiresAt: time.Now().Add(ttl),
	}
	return true, "", nil
}

// ReleaseInvestigation releases a previously claimed investigation.
// Only the agent that holds the claim can release it.
func (m *InMemoryInvestigationCoordinator) ReleaseInvestigation(ctx context.Context, agentName, entityID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if claim, ok := m.claims[entityID]; ok {
		if claim.agentName == agentName {
			delete(m.claims, entityID)
		}
	}
	return nil
}

// GetActiveInvestigations returns all currently active (non-expired) claims.
func (m *InMemoryInvestigationCoordinator) GetActiveInvestigations(ctx context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	result := make(map[string]string)
	var expired []string

	for entityID, claim := range m.claims {
		if now.Before(claim.expiresAt) {
			result[entityID] = claim.agentName
		} else {
			expired = append(expired, entityID)
		}
	}

	// Clean up expired claims
	for _, entityID := range expired {
		delete(m.claims, entityID)
	}

	return result, nil
}
