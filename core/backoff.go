package core

import (
	"math"
	"time"
)

// BackoffConfig defines exponential backoff parameters for delay calculation.
// Used by orchestration (step retry) and resilience (retry executor).
// This is a pure calculation utility — no retry loop, no context, no sleeping, no telemetry.
//
// Unlike RetryConfig (config.go), which is a serializable configuration struct loaded
// from environment variables, BackoffConfig is a runtime calculation type with no JSON
// or env tags. RetryConfig defines "what to retry"; BackoffConfig calculates "how long to wait."
type BackoffConfig struct {
	InitialDelay  time.Duration // Base delay for the first attempt (e.g., 500ms)
	MaxDelay      time.Duration // Upper bound — delay never exceeds this (e.g., 10s)
	BackoffFactor float64       // Multiplier per attempt (e.g., 2.0 for doubling)
	JitterEnabled bool          // Add deterministic ±10% jitter (math.Sin-based, not random)
}

// DefaultBackoffConfig returns production defaults.
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDelay:  500 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		BackoffFactor: 2.0,
		JitterEnabled: true,
	}
}

// Delay calculates the backoff duration for a given attempt (1-indexed).
// Algorithm mirrors resilience/retry.go:210-223.
//
// Edge cases:
//   - attempt ≤ 0 is treated as attempt 1 (returns InitialDelay ± jitter)
//   - InitialDelay=0 or BackoffFactor=0 → returns 0 (caller should use DefaultBackoffConfig)
//   - MaxDelay=0 → returns 0 (delay is capped immediately)
//   - Jitter cannot produce negative durations: minimum is 90% of the base delay
//   - When jitter pushes delay above MaxDelay, the final cap (line 50) clamps it
func (c BackoffConfig) Delay(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	delay := c.InitialDelay
	for i := 1; i < attempt; i++ {
		delay = time.Duration(float64(delay) * c.BackoffFactor)
		if delay > c.MaxDelay {
			delay = c.MaxDelay
			break
		}
	}
	if c.JitterEnabled {
		jitter := time.Duration(float64(delay) * 0.1 * math.Sin(float64(attempt)))
		delay += jitter
	}
	if delay > c.MaxDelay {
		delay = c.MaxDelay
	}
	return delay
}
