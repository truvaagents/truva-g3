package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// RoutingCache provides caching for routing plans
type RoutingCache interface {
	Get(prompt string) (*RoutingPlan, bool)
	Set(prompt string, plan *RoutingPlan, ttl time.Duration)
	Clear()
	Stats() CacheStats
}

// CacheStats provides cache performance metrics
type CacheStats struct {
	Size        int     `json:"size"`
	Hits        int64   `json:"hits"`
	Misses      int64   `json:"misses"`
	Evictions   int64   `json:"evictions"`
	HitRate     float64 `json:"hit_rate"`
	MemoryUsage int64   `json:"memory_bytes"`
}

// SimpleCache provides a basic in-memory cache for routing plans
type SimpleCache struct {
	mu              sync.RWMutex
	items           map[string]*cacheItem
	stats           CacheStats
	maxSize         int
	cleanupInterval time.Duration
	stopCleanup     chan bool
}

type cacheItem struct {
	plan      *RoutingPlan
	expiresAt time.Time
}

// NewSimpleCache creates a new simple cache instance
func NewSimpleCache() *SimpleCache {
	return NewSimpleCacheWithOptions(1000, 5*time.Minute)
}

// NewSimpleCacheWithOptions creates a cache with custom settings
func NewSimpleCacheWithOptions(maxSize int, cleanupInterval time.Duration) *SimpleCache {
	c := &SimpleCache{
		items:           make(map[string]*cacheItem),
		maxSize:         maxSize,
		cleanupInterval: cleanupInterval,
		stopCleanup:     make(chan bool),
	}

	go c.cleanupRoutine()

	return c
}

// Get retrieves a cached routing plan
func (c *SimpleCache) Get(prompt string) (*RoutingPlan, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := c.hashPrompt(prompt)
	item, found := c.items[key]

	if !found {
		c.stats.Misses++
		return nil, false
	}

	if time.Now().After(item.expiresAt) {
		c.stats.Misses++
		return nil, false
	}

	c.stats.Hits++
	c.updateHitRate()
	return item.plan, true
}

// Set stores a routing plan in cache
func (c *SimpleCache) Set(prompt string, plan *RoutingPlan, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.items) >= c.maxSize {
		c.evictExpired()

		if len(c.items) >= c.maxSize {
			c.evictOldest()
		}
	}

	key := c.hashPrompt(prompt)
	c.items[key] = &cacheItem{
		plan:      plan,
		expiresAt: time.Now().Add(ttl),
	}

	c.stats.Size = len(c.items)
	c.updateMemoryUsage()
}

// Clear removes all cached plans
func (c *SimpleCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*cacheItem)
	c.stats.Size = 0
	c.stats.MemoryUsage = 0
}

// Stats returns cache statistics
func (c *SimpleCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := c.stats
	stats.Size = len(c.items)
	return stats
}

// Stop stops the cleanup routine
func (c *SimpleCache) Stop() {
	close(c.stopCleanup)
}

func (c *SimpleCache) hashPrompt(prompt string) string {
	h := sha256.New()
	h.Write([]byte(prompt))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c *SimpleCache) cleanupRoutine() {
	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			c.evictExpired()
			c.stats.Size = len(c.items)
			c.updateMemoryUsage()
			c.mu.Unlock()
		case <-c.stopCleanup:
			return
		}
	}
}

func (c *SimpleCache) evictExpired() {
	now := time.Now()
	for key, item := range c.items {
		if now.After(item.expiresAt) {
			delete(c.items, key)
			c.stats.Evictions++
		}
	}
}

func (c *SimpleCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for key, item := range c.items {
		if oldestTime.IsZero() || item.expiresAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = item.expiresAt
		}
	}

	if oldestKey != "" {
		delete(c.items, oldestKey)
		c.stats.Evictions++
	}
}

func (c *SimpleCache) updateHitRate() {
	total := c.stats.Hits + c.stats.Misses
	if total > 0 {
		c.stats.HitRate = float64(c.stats.Hits) / float64(total)
	}
}

func (c *SimpleCache) updateMemoryUsage() {
	c.stats.MemoryUsage = int64(len(c.items) * 1024)
}

// LRUCache provides an LRU cache implementation for routing plans
type LRUCache struct {
	mu       sync.RWMutex
	capacity int
	items    map[string]*lruItem
	head     *lruItem
	tail     *lruItem
	stats    CacheStats
}

type lruItem struct {
	key       string
	plan      *RoutingPlan
	expiresAt time.Time
	prev      *lruItem
	next      *lruItem
}

// NewLRUCache creates a new LRU cache
func NewLRUCache(capacity int) *LRUCache {
	return &LRUCache{
		capacity: capacity,
		items:    make(map[string]*lruItem),
	}
}

// Get retrieves a cached routing plan and moves it to front
func (l *LRUCache) Get(prompt string) (*RoutingPlan, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := l.hashPrompt(prompt)
	item, found := l.items[key]

	if !found {
		l.stats.Misses++
		return nil, false
	}

	if time.Now().After(item.expiresAt) {
		l.removeItem(item)
		l.stats.Misses++
		return nil, false
	}

	l.moveToFront(item)
	l.stats.Hits++
	l.updateHitRate()

	return item.plan, true
}

// Set stores a routing plan in cache
func (l *LRUCache) Set(prompt string, plan *RoutingPlan, ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := l.hashPrompt(prompt)

	if item, found := l.items[key]; found {
		item.plan = plan
		item.expiresAt = time.Now().Add(ttl)
		l.moveToFront(item)
		return
	}

	if len(l.items) >= l.capacity {
		l.removeLRU()
	}

	item := &lruItem{
		key:       key,
		plan:      plan,
		expiresAt: time.Now().Add(ttl),
	}

	l.items[key] = item
	l.addToFront(item)

	l.stats.Size = len(l.items)
}

// Clear removes all cached plans
func (l *LRUCache) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.items = make(map[string]*lruItem)
	l.head = nil
	l.tail = nil
	l.stats.Size = 0
}

// Stats returns cache statistics
func (l *LRUCache) Stats() CacheStats {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stats := l.stats
	stats.Size = len(l.items)
	return stats
}

func (l *LRUCache) hashPrompt(prompt string) string {
	h := sha256.New()
	h.Write([]byte(prompt))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (l *LRUCache) moveToFront(item *lruItem) {
	if item == l.head {
		return
	}

	l.removeFromList(item)
	l.addToFront(item)
}

func (l *LRUCache) addToFront(item *lruItem) {
	item.prev = nil
	item.next = l.head

	if l.head != nil {
		l.head.prev = item
	}

	l.head = item

	if l.tail == nil {
		l.tail = item
	}
}

func (l *LRUCache) removeFromList(item *lruItem) {
	if item.prev != nil {
		item.prev.next = item.next
	} else {
		l.head = item.next
	}

	if item.next != nil {
		item.next.prev = item.prev
	} else {
		l.tail = item.prev
	}
}

func (l *LRUCache) removeItem(item *lruItem) {
	l.removeFromList(item)
	delete(l.items, item.key)
	l.stats.Evictions++
}

func (l *LRUCache) removeLRU() {
	if l.tail != nil {
		l.removeItem(l.tail)
	}
}

func (l *LRUCache) updateHitRate() {
	total := l.stats.Hits + l.stats.Misses
	if total > 0 {
		l.stats.HitRate = float64(l.stats.Hits) / float64(total)
	}
}
