package telemetry

import (
	"sync"
	"time"
)

// RateLimiter implements a simple rate limiter for error logging
type RateLimiter struct {
	interval time.Duration
	lastTime time.Time
	mu       sync.Mutex
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(interval time.Duration) *RateLimiter {
	return &RateLimiter{
		interval: interval,
	}
}

// Allow returns true if an action is allowed based on rate limiting
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if now.Sub(r.lastTime) >= r.interval {
		r.lastTime = now
		return true
	}
	return false
}
