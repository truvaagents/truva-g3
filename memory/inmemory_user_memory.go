package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// InMemoryUserMemory provides a zero-infrastructure UserMemory implementation.
// Satisfies both core.UserMemory and core.UserMemoryAdmin.
// Suitable for development, testing, and agents that don't need persistence.
//
// Thread-safe via sync.RWMutex. Not stateless (this IS the state store).
// Recall uses substring matching + category filtering (no vector search).
//
// IMPORTANT: Behavioral gap with VectorUserMemory — Recall uses case-insensitive
// substring matching, NOT semantic similarity. "User prefers direct flights" will
// match queryContext="flights" but NOT "air travel". Tests passing with InMemory
// may fail with VectorUserMemory or vice versa due to this fundamental difference.
// Use InMemory for unit tests and local development only.
type InMemoryUserMemory struct {
	mu              sync.RWMutex
	facts           map[string][]core.UserFact // key: userID
	maxSize         int                        // max facts per user
	transientMaxAge time.Duration
}

// NewInMemoryUserMemory creates an in-memory user memory store.
// maxSize limits facts per user (default 1000). Oldest facts are evicted when full.
func NewInMemoryUserMemory(maxSize int) *InMemoryUserMemory {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &InMemoryUserMemory{
		facts:           make(map[string][]core.UserFact),
		maxSize:         maxSize,
		transientMaxAge: transientMaxAgeFromEnv(),
	}
}

// Remember upserts a fact by FactID. Assigns a new FactID if empty.
func (m *InMemoryUserMemory) Remember(ctx context.Context, userID string, fact core.UserFact) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fact.UserID = userID
	if fact.FactID == "" {
		fact.FactID = uuid.New().String()
	}
	now := time.Now()
	if fact.CreatedAt.IsZero() {
		fact.CreatedAt = now
	}
	fact.UpdatedAt = now

	// Upsert by FactID
	userFacts := m.facts[userID]
	for i, existing := range userFacts {
		if existing.FactID == fact.FactID {
			userFacts[i] = fact
			return nil
		}
	}

	// Append, evict oldest if at capacity
	if len(userFacts) >= m.maxSize {
		userFacts = userFacts[1:] // drop oldest
	}
	m.facts[userID] = append(userFacts, fact)
	return nil
}

// Recall retrieves facts matching the query context via substring match.
// namespace="" searches all namespaces. queryContext="" returns all facts.
// Results sorted by confidence descending, then UpdatedAt descending.
func (m *InMemoryUserMemory) Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]core.UserFact, error) {
	start := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []core.UserFact
	for _, fact := range m.facts[userID] {
		if namespace != "" && fact.Namespace != namespace {
			continue
		}
		// In-memory: match by substring in content (no vector search)
		if queryContext != "" && !strings.Contains(strings.ToLower(fact.Content), strings.ToLower(queryContext)) {
			continue
		}
		results = append(results, fact)
	}

	// Sort by confidence descending, then by UpdatedAt descending
	sort.Slice(results, func(i, j int) bool {
		if results[i].Confidence != results[j].Confidence {
			return results[i].Confidence > results[j].Confidence
		}
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	filtered, filteredCount := filterRecalledFactsByLifetime(results, time.Now(), m.transientMaxAge)
	if filteredCount > 0 {
		telemetry.AddSpanEvent(ctx, "user_memory.transient_cleanup.filtered",
			attribute.String("request_id", core.GetRequestID(ctx)),
			attribute.String("user_id", userID),
			attribute.String("namespace", namespace),
			attribute.Int("filtered_count", filteredCount),
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int64("transient_max_age_hours", int64(m.transientMaxAge/time.Hour)),
		)
	}
	results = filtered

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// RecallByCategory retrieves facts in a specific category.
// Results sorted by confidence descending, then UpdatedAt descending (most recent first).
// Sort matches Recall() pattern for consistency — critical for summaries where all
// facts have the same confidence and recency ordering determines which N are returned.
func (m *InMemoryUserMemory) RecallByCategory(ctx context.Context, userID string, namespace string, category string, limit int) ([]core.UserFact, error) {
	start := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []core.UserFact
	for _, fact := range m.facts[userID] {
		if namespace != "" && fact.Namespace != namespace {
			continue
		}
		if fact.Category == category {
			results = append(results, fact)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Confidence != results[j].Confidence {
			return results[i].Confidence > results[j].Confidence
		}
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	filtered, filteredCount := filterRecalledFactsByLifetime(results, time.Now(), m.transientMaxAge)
	if filteredCount > 0 {
		telemetry.AddSpanEvent(ctx, "user_memory.transient_cleanup.filtered",
			attribute.String("request_id", core.GetRequestID(ctx)),
			attribute.String("user_id", userID),
			attribute.String("namespace", namespace),
			attribute.String("category", category),
			attribute.Int("filtered_count", filteredCount),
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int64("transient_max_age_hours", int64(m.transientMaxAge/time.Hour)),
		)
	}
	results = filtered

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Forget deletes ALL facts for a user. GDPR Article 17 compliance.
func (m *InMemoryUserMemory) Forget(ctx context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.facts, userID)
	return nil
}

// ListFacts returns all active facts for a user with pagination.
// Returns (facts, totalCount, error).
func (m *InMemoryUserMemory) ListFacts(ctx context.Context, userID string, namespace string, offset int, limit int) ([]core.UserFact, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var filtered []core.UserFact
	for _, fact := range m.facts[userID] {
		if namespace != "" && fact.Namespace != namespace {
			continue
		}
		filtered = append(filtered, fact)
	}

	total := len(filtered)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

// ForgetNamespace deletes all facts for a user in a specific namespace.
func (m *InMemoryUserMemory) ForgetNamespace(ctx context.Context, userID string, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var kept []core.UserFact
	for _, fact := range m.facts[userID] {
		if fact.Namespace != namespace {
			kept = append(kept, fact)
		}
	}
	if len(kept) == 0 {
		delete(m.facts, userID)
	} else {
		m.facts[userID] = kept
	}
	return nil
}

// ForgetFact deletes a single fact by ID.
func (m *InMemoryUserMemory) ForgetFact(ctx context.Context, userID string, factID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var kept []core.UserFact
	for _, fact := range m.facts[userID] {
		if fact.FactID != factID {
			kept = append(kept, fact)
		}
	}
	if len(kept) == 0 {
		delete(m.facts, userID)
	} else {
		m.facts[userID] = kept
	}
	return nil
}
