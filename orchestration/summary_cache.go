package orchestration

import (
	"container/list"
	"fmt"
	"sync"
)

type SummaryState struct {
	Summary             string
	LastTurnFingerprint string
	LastTurnOrdinal     int
	LastCompactedCount  int
}

type summaryCacheEntry struct {
	sessionKey string
	state      SummaryState
}

type SummaryCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	entries  map[string]*list.Element
}

func NewSummaryCache(capacity int) (*SummaryCache, error) {
	if capacity < 1 {
		return nil, fmt.Errorf("conversation summary cache size must be >= 1, got %d", capacity)
	}
	return &SummaryCache{
		capacity: capacity,
		ll:       list.New(),
		entries:  make(map[string]*list.Element, capacity),
	}, nil
}

func (c *SummaryCache) Get(sessionKey string) (SummaryState, bool) {
	if c == nil || sessionKey == "" {
		return SummaryState{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[sessionKey]
	if !ok {
		return SummaryState{}, false
	}
	c.ll.MoveToFront(elem)
	return elem.Value.(*summaryCacheEntry).state, true
}

func (c *SummaryCache) Set(sessionKey string, state SummaryState) {
	if c == nil || sessionKey == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.entries[sessionKey]; ok {
		elem.Value.(*summaryCacheEntry).state = state
		c.ll.MoveToFront(elem)
		return
	}

	elem := c.ll.PushFront(&summaryCacheEntry{
		sessionKey: sessionKey,
		state:      state,
	})
	c.entries[sessionKey] = elem

	if c.ll.Len() > c.capacity {
		last := c.ll.Back()
		if last != nil {
			c.ll.Remove(last)
			delete(c.entries, last.Value.(*summaryCacheEntry).sessionKey)
		}
	}
}

func (c *SummaryCache) Delete(sessionKey string) {
	if c == nil || sessionKey == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[sessionKey]
	if !ok {
		return
	}
	c.ll.Remove(elem)
	delete(c.entries, sessionKey)
}

func (c *SummaryCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
