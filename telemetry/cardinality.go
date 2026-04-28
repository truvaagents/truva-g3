package telemetry

import (
	"sync"
	"time"
)

// CardinalityLimiter prevents unbounded metric cardinality
type CardinalityLimiter struct {
	limits map[string]int
	seen   sync.Map // Thread-safe: map[metricLabel]map[value]time.Time

	// Cleanup control
	stopChan chan struct{}
	stopped  sync.Once
}

// NewCardinalityLimiter creates a new cardinality limiter
func NewCardinalityLimiter(limits map[string]int) *CardinalityLimiter {
	c := &CardinalityLimiter{
		limits:   limits,
		stopChan: make(chan struct{}),
	}
	// Periodic cleanup to prevent memory leak
	go c.cleanupLoop()
	return c
}

// CheckAndLimit checks and limits cardinality for a metric label
func (c *CardinalityLimiter) CheckAndLimit(metric, label, value string) string {
	key := metric + "." + label

	// Check if we have a limit for this label
	limit, hasLimit := c.limits[label]
	if !hasLimit {
		// No limit defined, pass through
		return value
	}

	// Load or create the value map
	valMapI, _ := c.seen.LoadOrStore(key, &sync.Map{})
	valMap := valMapI.(*sync.Map)

	// Check current cardinality
	count := 0
	valMap.Range(func(k, v interface{}) bool {
		count++
		return count < limit
	})

	if count >= limit {
		// Check if this value exists
		if _, exists := valMap.Load(value); !exists {
			return "other" // Over limit, use "other"
		}
	}

	// Store with timestamp for cleanup
	valMap.Store(value, time.Now())
	return value
}

// CurrentCardinality returns the current total cardinality count
func (c *CardinalityLimiter) CurrentCardinality() int {
	total := 0
	c.seen.Range(func(key, valMapI interface{}) bool {
		valMap := valMapI.(*sync.Map)
		count := 0
		valMap.Range(func(k, v interface{}) bool {
			count++
			return true
		})
		total += count
		return true
	})
	return total
}

// MaxCardinality returns the maximum allowed cardinality
func (c *CardinalityLimiter) MaxCardinality() int {
	total := 0
	for _, limit := range c.limits {
		total += limit
	}
	return total
}

// cleanupLoop periodically removes old entries to prevent memory leaks
func (c *CardinalityLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanup()
		case <-c.stopChan:
			return
		}
	}
}

// cleanup removes entries older than 10 minutes
func (c *CardinalityLimiter) cleanup() {
	cutoff := time.Now().Add(-10 * time.Minute)
	c.seen.Range(func(key, valMapI interface{}) bool {
		valMap := valMapI.(*sync.Map)
		valMap.Range(func(val, timeI interface{}) bool {
			if timeI.(time.Time).Before(cutoff) {
				valMap.Delete(val)
			}
			return true
		})
		return true
	})
}

// Stop stops the cleanup goroutine
func (c *CardinalityLimiter) Stop() {
	c.stopped.Do(func() {
		close(c.stopChan)
	})
}
