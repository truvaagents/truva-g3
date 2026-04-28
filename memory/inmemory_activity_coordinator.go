package memory

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface check.
var _ core.ActivityCoordinator = (*InMemoryActivityCoordinator)(nil)

// InMemoryActivityCoordinator is a single-process implementation of ActivityCoordinator
// using sync.RWMutex + map. Signals expire based on TTL via lazy cleanup on read.
// Suitable for development, testing, and single-pod deployments.
type InMemoryActivityCoordinator struct {
	mu      sync.RWMutex
	signals map[string]storedSignal // requestID → signal + expiry
}

type storedSignal struct {
	Signal    core.ActivitySignal
	ExpiresAt time.Time
}

// NewInMemoryActivityCoordinator creates an in-memory activity coordinator.
func NewInMemoryActivityCoordinator(domain string) (*InMemoryActivityCoordinator, error) {
	return &InMemoryActivityCoordinator{
		signals: make(map[string]storedSignal),
	}, nil
}

func (c *InMemoryActivityCoordinator) AnnounceActivity(ctx context.Context, signal core.ActivitySignal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Sweep expired entries while holding write lock (prevents unbounded growth)
	now := time.Now()
	for id, s := range c.signals {
		if s.ExpiresAt.Before(now) {
			delete(c.signals, id)
		}
	}
	c.signals[signal.RequestID] = storedSignal{
		Signal:    signal,
		ExpiresAt: now.Add(signal.TTL),
	}
	return nil
}

func (c *InMemoryActivityCoordinator) UpdateStatus(ctx context.Context, requestID, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stored, ok := c.signals[requestID]; ok {
		stored.Signal.Status = status
		c.signals[requestID] = stored
	}
	return nil
}

func (c *InMemoryActivityCoordinator) GetDomainActivities(ctx context.Context, domain string) ([]core.ActivitySignal, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	var result []core.ActivitySignal
	for _, stored := range c.signals {
		if stored.ExpiresAt.Before(now) {
			continue // Expired — lazy cleanup
		}
		if stored.Signal.AgentDomain == domain {
			result = append(result, stored.Signal)
		}
	}
	return result, nil
}

func (c *InMemoryActivityCoordinator) CompleteActivity(ctx context.Context, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.signals, requestID)
	return nil
}

// MarshalSignal serializes an ActivitySignal to JSON. Shared with Redis implementation.
func MarshalSignal(signal core.ActivitySignal) ([]byte, error) {
	return json.Marshal(signal)
}

// UnmarshalSignal deserializes an ActivitySignal from JSON.
func UnmarshalSignal(data []byte) (core.ActivitySignal, error) {
	var signal core.ActivitySignal
	err := json.Unmarshal(data, &signal)
	return signal, err
}
