package core

import (
	"math"
	"testing"
	"time"
)

func TestDefaultBackoffConfig(t *testing.T) {
	cfg := DefaultBackoffConfig()
	if cfg.InitialDelay != 500*time.Millisecond {
		t.Errorf("InitialDelay = %v, want 500ms", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 10*time.Second {
		t.Errorf("MaxDelay = %v, want 10s", cfg.MaxDelay)
	}
	if cfg.BackoffFactor != 2.0 {
		t.Errorf("BackoffFactor = %v, want 2.0", cfg.BackoffFactor)
	}
	if !cfg.JitterEnabled {
		t.Error("JitterEnabled should be true by default")
	}
}

func TestBackoffConfig_Delay(t *testing.T) {
	tests := []struct {
		name      string
		config    BackoffConfig
		attempt   int
		wantMin   time.Duration
		wantMax   time.Duration
		wantExact time.Duration // 0 means use range check
	}{
		{
			name:    "attempt 1 with defaults (jitter enabled)",
			config:  DefaultBackoffConfig(),
			attempt: 1,
			wantMin: 450 * time.Millisecond, // 500ms - 10%
			wantMax: 550 * time.Millisecond, // 500ms + 10%
		},
		{
			name:    "attempt 2 with defaults (jitter enabled)",
			config:  DefaultBackoffConfig(),
			attempt: 2,
			wantMin: 900 * time.Millisecond,  // 1s - 10%
			wantMax: 1100 * time.Millisecond, // 1s + 10%
		},
		{
			name:    "attempt 3 with defaults (jitter enabled)",
			config:  DefaultBackoffConfig(),
			attempt: 3,
			wantMin: 1800 * time.Millisecond, // 2s - 10%
			wantMax: 2200 * time.Millisecond, // 2s + 10%
		},
		{
			name: "attempt 1 without jitter — deterministic",
			config: BackoffConfig{
				InitialDelay:  500 * time.Millisecond,
				MaxDelay:      10 * time.Second,
				BackoffFactor: 2.0,
				JitterEnabled: false,
			},
			attempt:   1,
			wantExact: 500 * time.Millisecond,
		},
		{
			name: "attempt 3 without jitter — deterministic",
			config: BackoffConfig{
				InitialDelay:  500 * time.Millisecond,
				MaxDelay:      10 * time.Second,
				BackoffFactor: 2.0,
				JitterEnabled: false,
			},
			attempt:   3,
			wantExact: 2 * time.Second, // 500ms * 2 * 2
		},
		{
			name: "capped at MaxDelay",
			config: BackoffConfig{
				InitialDelay:  500 * time.Millisecond,
				MaxDelay:      10 * time.Second,
				BackoffFactor: 2.0,
				JitterEnabled: false,
			},
			attempt:   100,
			wantExact: 10 * time.Second,
		},
		{
			name: "attempt 0 treated as attempt 1",
			config: BackoffConfig{
				InitialDelay:  500 * time.Millisecond,
				MaxDelay:      10 * time.Second,
				BackoffFactor: 2.0,
				JitterEnabled: false,
			},
			attempt:   0,
			wantExact: 500 * time.Millisecond,
		},
		{
			name: "negative attempt treated as attempt 1",
			config: BackoffConfig{
				InitialDelay:  500 * time.Millisecond,
				MaxDelay:      10 * time.Second,
				BackoffFactor: 2.0,
				JitterEnabled: false,
			},
			attempt:   -5,
			wantExact: 500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.Delay(tt.attempt)
			if tt.wantExact > 0 {
				if got != tt.wantExact {
					t.Errorf("Delay(%d) = %v, want exactly %v", tt.attempt, got, tt.wantExact)
				}
			} else {
				if got < tt.wantMin || got > tt.wantMax {
					t.Errorf("Delay(%d) = %v, want between %v and %v", tt.attempt, got, tt.wantMin, tt.wantMax)
				}
			}
		})
	}
}

func TestBackoffConfig_Delay_JitterWithinBounds(t *testing.T) {
	cfg := BackoffConfig{
		InitialDelay:  1 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		JitterEnabled: true,
	}

	// Jitter uses math.Sin which is bounded to [-1, 1], so ±10% of base delay
	for attempt := 1; attempt <= 10; attempt++ {
		got := cfg.Delay(attempt)

		// Calculate expected base delay without jitter
		baseDelay := cfg.InitialDelay
		for i := 1; i < attempt; i++ {
			baseDelay = time.Duration(float64(baseDelay) * cfg.BackoffFactor)
			if baseDelay > cfg.MaxDelay {
				baseDelay = cfg.MaxDelay
				break
			}
		}

		minExpected := time.Duration(float64(baseDelay) * 0.9) // -10%
		maxExpected := time.Duration(float64(baseDelay) * 1.1) // +10%
		if maxExpected > cfg.MaxDelay {
			maxExpected = cfg.MaxDelay
		}

		if got < minExpected || got > maxExpected {
			t.Errorf("Attempt %d: Delay = %v, expected between %v and %v (base: %v)",
				attempt, got, minExpected, maxExpected, baseDelay)
		}
	}
}

func TestBackoffConfig_Delay_DeterministicJitter(t *testing.T) {
	// Same config + same attempt should always produce the same delay
	// (math.Sin is deterministic, unlike rand-based jitter)
	cfg := DefaultBackoffConfig()

	for attempt := 1; attempt <= 5; attempt++ {
		d1 := cfg.Delay(attempt)
		d2 := cfg.Delay(attempt)
		if d1 != d2 {
			t.Errorf("Attempt %d: non-deterministic jitter: %v != %v", attempt, d1, d2)
		}
	}
}

func TestBackoffConfig_Delay_MatchesResilienceAlgorithm(t *testing.T) {
	// Verify our algorithm produces the same results as resilience/retry.go:210-223
	cfg := BackoffConfig{
		InitialDelay:  500 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		BackoffFactor: 2.0,
		JitterEnabled: true,
	}

	// Manually compute expected using the resilience algorithm
	for attempt := 1; attempt <= 5; attempt++ {
		// Simulate resilience/retry.go algorithm
		delay := cfg.InitialDelay
		for i := 1; i < attempt; i++ {
			delay = time.Duration(float64(delay) * cfg.BackoffFactor)
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
				break
			}
		}
		jitter := time.Duration(float64(delay) * 0.1 * math.Sin(float64(attempt)))
		expected := delay + jitter
		if expected > cfg.MaxDelay {
			expected = cfg.MaxDelay
		}

		got := cfg.Delay(attempt)
		if got != expected {
			t.Errorf("Attempt %d: Delay = %v, expected %v (matching resilience algorithm)", attempt, got, expected)
		}
	}
}
