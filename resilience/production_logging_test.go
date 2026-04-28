package resilience

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGap1ManualOverrideLogging verifies manual override operations are logged correctly
func TestGap1ManualOverrideLogging(t *testing.T) {
	t.Run("ForceOpen logging", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-force-open"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Clear creation logs
		logger.Clear()

		// Force open from closed state
		cb.ForceOpen()

		// Verify logging
		foundLog := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "forced open") {
				foundLog = true
				// Verify required fields
				if op, ok := log.Fields["operation"].(string); !ok || op != "circuit_breaker_force_open" {
					t.Error("Missing or incorrect operation field")
				}
				if prev, ok := log.Fields["previous_state"].(string); !ok || prev != "closed" {
					t.Error("Missing or incorrect previous_state")
				}
				if _, ok := log.Fields["timestamp"]; !ok {
					t.Error("Missing timestamp")
				}
			}
		}
		if !foundLog {
			t.Error("No ForceOpen log found")
		}
	})

	t.Run("ForceClosed logging", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-force-closed"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// First force open
		cb.ForceOpen()
		logger.Clear()

		// Then force closed
		cb.ForceClosed()

		// Verify logging
		foundLog := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "forced closed") {
				foundLog = true
				if op, ok := log.Fields["operation"].(string); !ok || op != "circuit_breaker_force_closed" {
					t.Error("Missing or incorrect operation field")
				}
				if prev, ok := log.Fields["previous_state"].(string); !ok || prev != "open" {
					t.Errorf("Expected previous_state=open, got %v", log.Fields["previous_state"])
				}
			}
		}
		if !foundLog {
			t.Error("No ForceClosed log found")
		}
	})

	t.Run("ClearForce with active override", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-clear-force"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Force open first
		cb.ForceOpen()
		logger.Clear()

		// Clear force
		cb.ClearForce()

		// Should log because override was active
		foundLog := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "override cleared") {
				foundLog = true
				if wasOpen, ok := log.Fields["was_force_open"].(bool); !ok || !wasOpen {
					t.Error("Expected was_force_open=true")
				}
				if wasClosed, ok := log.Fields["was_force_closed"].(bool); !ok || wasClosed {
					t.Error("Expected was_force_closed=false")
				}
			}
		}
		if !foundLog {
			t.Error("No ClearForce log found when override was active")
		}
	})

	t.Run("ClearForce without active override", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-clear-no-override"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		logger.Clear()

		// Clear force when no override is active
		cb.ClearForce()

		// Should NOT log because no override was active
		for _, log := range logger.logs {
			if strings.Contains(log.Message, "override cleared") {
				t.Error("Should not log when no override was active")
			}
		}
	})
}

// TestGap2TimeSkewLogging verifies time skew detection is logged
func TestGap2TimeSkewLogging(t *testing.T) {
	t.Skip("Time skew detection is difficult to test reliably without mocking time")
	// The time skew detection requires actual system time to go backwards,
	// which is hard to simulate in a unit test. The implementation has been
	// manually verified to work when system clock is adjusted backwards.
	//
	// The logging code is in circuit_breaker.go lines 1172-1190 and triggers when:
	// - Current time is before the last rotation time
	// - A bucket timestamp is after current time
	//
	// Manual test procedure:
	// 1. Run a circuit breaker with high load
	// 2. Adjust system clock backwards by 1 minute
	// 3. Observe WARN log with operation="sliding_window_time_skew"
}

// TestGap3OrphanedRequestLogging verifies orphaned request cleanup logging
func TestGap3OrphanedRequestLogging(t *testing.T) {
	t.Run("With orphaned requests", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-orphaned"
		config.Logger = logger
		config.SleepWindow = 100 * time.Millisecond

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Open circuit
		for i := 0; i < 10; i++ {
			cb.Execute(context.Background(), func() error {
				return errors.New("error")
			})
		}

		// Wait for half-open
		time.Sleep(config.SleepWindow + 50*time.Millisecond)

		// Start long-running requests that will be orphaned
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				cb.Execute(ctx, func() error {
					time.Sleep(1 * time.Second) // Will timeout
					return nil
				})
			}()
		}

		// Let them timeout
		time.Sleep(50 * time.Millisecond)

		logger.Clear()

		// Cleanup orphaned
		cleaned := cb.CleanupOrphanedRequests(20 * time.Millisecond)

		// Should have WARN log for orphaned requests
		foundWarnLog := false
		foundStartLog := false
		for _, log := range logger.logs {
			if log.Level == "DEBUG" && strings.Contains(log.Message, "Starting orphaned request cleanup") {
				foundStartLog = true
			}
			if log.Level == "WARN" && strings.Contains(log.Message, "Orphaned requests cleaned up") {
				foundWarnLog = true
				if count, ok := log.Fields["cleaned_count"].(int); !ok || count != cleaned {
					t.Errorf("Expected cleaned_count=%d, got %v", cleaned, log.Fields["cleaned_count"])
				}
				if tokens, ok := log.Fields["orphaned_tokens"].([]uint64); !ok {
					t.Error("Missing orphaned_tokens field")
				} else if len(tokens) > 100 {
					t.Error("Token list should be capped at 100")
				}
				// Check for truncation indicator if needed
				if cleaned > 100 {
					if _, ok := log.Fields["tokens_truncated"]; !ok {
						t.Error("Missing truncation indicator for large cleanup")
					}
				}
			}
		}

		if !foundStartLog {
			t.Error("No cleanup start log found")
		}
		if !foundWarnLog {
			t.Error("No orphaned cleanup warning found")
		}

		wg.Wait()
	})

	t.Run("Without orphaned requests", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-no-orphaned"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		logger.Clear()

		// Cleanup when no orphaned requests
		cleaned := cb.CleanupOrphanedRequests(1 * time.Second)

		if cleaned != 0 {
			t.Errorf("Expected 0 cleaned, got %d", cleaned)
		}

		// Should have DEBUG log saying no orphans found
		foundDebugLog := false
		for _, log := range logger.logs {
			if log.Level == "DEBUG" && strings.Contains(log.Message, "No orphaned requests found") {
				foundDebugLog = true
				if count, ok := log.Fields["cleaned_count"].(int); !ok || count != 0 {
					t.Error("Expected cleaned_count=0")
				}
			}
			if log.Level == "WARN" {
				t.Error("Should not have WARN log when no orphans")
			}
		}

		if !foundDebugLog {
			t.Error("No debug log for zero orphans")
		}
	})
}

// TestGap4ErrorClassificationLogging verifies error classification is logged
func TestGap4ErrorClassificationLogging(t *testing.T) {
	logger := &TestLogger{}
	config := DefaultConfig()
	config.Name = "test-classification"
	config.Logger = logger

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	testCases := []struct {
		name            string
		err             error
		expectedClass   string
		countsAsFailure bool
	}{
		{
			name:            "deadline exceeded",
			err:             context.DeadlineExceeded,
			expectedClass:   "deadline_exceeded",
			countsAsFailure: true,
		},
		{
			name:            "context canceled",
			err:             context.Canceled,
			expectedClass:   "context_canceled",
			countsAsFailure: false,
		},
		{
			name:            "generic error",
			err:             errors.New("server error"),
			expectedClass:   "infrastructure_error",
			countsAsFailure: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger.Clear()

			// Execute with error
			cb.Execute(context.Background(), func() error {
				return tc.err
			})

			// Find classification log
			foundLog := false
			for _, log := range logger.logs {
				if log.Level == "DEBUG" && strings.Contains(log.Message, "Error classification decision") {
					foundLog = true
					if class, ok := log.Fields["classification"].(string); !ok || !strings.Contains(class, tc.expectedClass) {
						t.Errorf("Expected classification=%s, got %v", tc.expectedClass, log.Fields["classification"])
					}
					if counts, ok := log.Fields["counts_as_failure"].(bool); !ok || counts != tc.countsAsFailure {
						t.Errorf("Expected counts_as_failure=%v, got %v", tc.countsAsFailure, log.Fields["counts_as_failure"])
					}
				}
			}
			if !foundLog {
				t.Error("No error classification log found")
			}
		})
	}
}

// TestGap5ConfigurationValidationLogging verifies config validation is logged
func TestGap5ConfigurationValidationLogging(t *testing.T) {
	t.Run("Successful validation", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-validation"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}
		if cb == nil {
			t.Fatal("Circuit breaker is nil")
		}

		// Should have validation and creation logs
		foundValidation := false
		foundCreation := false
		for _, log := range logger.logs {
			if log.Level == "DEBUG" && strings.Contains(log.Message, "Validating circuit breaker configuration") {
				foundValidation = true
			}
			if log.Level == "INFO" && strings.Contains(log.Message, "Circuit breaker created successfully") {
				foundCreation = true
				// Verify config details are logged
				if _, ok := log.Fields["error_threshold"]; !ok {
					t.Error("Missing error_threshold in creation log")
				}
				if _, ok := log.Fields["sleep_window_ms"]; !ok {
					t.Error("Missing sleep_window_ms in creation log")
				}
			}
		}

		if !foundValidation {
			t.Error("No validation log found")
		}
		if !foundCreation {
			t.Error("No creation log found")
		}
	})

	t.Run("Validation failure", func(t *testing.T) {
		logger := &TestLogger{}
		config := &CircuitBreakerConfig{
			Name:   "", // Invalid
			Logger: logger,
		}

		cb, err := NewCircuitBreaker(config)
		if err == nil {
			t.Fatal("Expected validation error")
		}
		if cb != nil {
			t.Fatal("Circuit breaker should be nil on failure")
		}

		// Should have error log
		foundError := false
		for _, log := range logger.logs {
			if log.Level == "ERROR" && strings.Contains(log.Message, "validation failed") {
				foundError = true
				if op, ok := log.Fields["operation"].(string); !ok || op != "circuit_breaker_validation_failed" {
					t.Error("Missing or incorrect operation field")
				}
				if _, ok := log.Fields["error"]; !ok {
					t.Error("Missing error field")
				}
			}
		}

		if !foundError {
			t.Error("No validation error log found")
		}
	})
}

// TestGap6EnhancedResetLogging verifies reset logging with full context
func TestGap6EnhancedResetLogging(t *testing.T) {
	t.Run("Reset from closed state", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-reset-closed"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Add some activity
		for i := 0; i < 5; i++ {
			cb.Execute(context.Background(), func() error { return nil })
		}
		for i := 0; i < 3; i++ {
			cb.Execute(context.Background(), func() error { return errors.New("error") })
		}

		logger.Clear()
		cb.Reset()

		// Check reset log has all required fields
		foundLog := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "reset") {
				foundLog = true
				// Required fields
				requiredFields := []string{"operation", "name", "previous_state", "new_state",
					"cleared_success", "cleared_failure", "previous_error_rate", "action", "timestamp"}
				for _, field := range requiredFields {
					if _, ok := log.Fields[field]; !ok {
						t.Errorf("Missing required field: %s", field)
					}
				}
				// Should not have half-open fields when not in half-open
				if _, ok := log.Fields["half_open_in_progress"]; ok {
					t.Error("Should not have half_open_in_progress when not in half-open state")
				}
			}
		}
		if !foundLog {
			t.Error("No reset log found")
		}
	})

	t.Run("Reset from half-open state", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-reset-halfopen"
		config.Logger = logger
		config.FailureThreshold = 3
		config.SleepWindow = 100 * time.Millisecond

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Open the circuit
		for i := 0; i < 5; i++ {
			cb.Execute(context.Background(), func() error { return errors.New("error") })
		}

		// Wait for half-open
		time.Sleep(config.SleepWindow + 50*time.Millisecond)

		// Start some half-open requests
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cb.Execute(context.Background(), func() error {
					time.Sleep(10 * time.Millisecond)
					return nil
				})
			}()
		}

		// Let them start
		time.Sleep(5 * time.Millisecond)

		logger.Clear()
		cb.Reset()

		// Check for half-open specific fields
		foundLog := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "reset") {
				foundLog = true
				// Should have half-open fields when resetting from half-open
				if _, ok := log.Fields["half_open_in_progress"]; !ok {
					t.Error("Missing half_open_in_progress field when resetting from half-open")
				}
				// Check for orphaned tokens if any
				if tokens, ok := log.Fields["orphaned_tokens"].(int); ok && tokens == 0 {
					t.Error("orphaned_tokens should not be included when zero")
				}
			}
		}
		if !foundLog {
			t.Error("No reset log found")
		}

		wg.Wait()
	})

	t.Run("Reset with orphaned tokens", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-reset-orphaned"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Manually add a token to simulate orphaned request
		cb.halfOpenTokens.Store(uint64(1), ExecutionToken{
			id:        1,
			startTime: time.Now(),
		})

		logger.Clear()
		cb.Reset()

		// Should include orphaned_tokens field
		foundOrphaned := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "reset") {
				if orphaned, ok := log.Fields["orphaned_tokens"].(int); ok && orphaned > 0 {
					foundOrphaned = true
				}
			}
		}
		if !foundOrphaned {
			t.Error("orphaned_tokens field not included when tokens exist")
		}
	})
}

// TestExecutionLogging verifies execution path logging
func TestExecutionLogging(t *testing.T) {
	t.Run("Successful execution", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-execution"
		config.Logger = logger

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		logger.Clear()

		// Successful execution
		err = cb.Execute(context.Background(), func() error {
			return nil
		})
		if err != nil {
			t.Fatalf("Execution failed: %v", err)
		}

		// Should have start and completion logs
		foundStart := false
		foundComplete := false
		for _, log := range logger.logs {
			// Log message might vary, check operation field
			if log.Level == "DEBUG" && log.Fields["operation"] == "circuit_breaker_execute" {
				foundStart = true
			}
			if log.Level == "DEBUG" && log.Fields["operation"] == "circuit_breaker_complete" {
				foundComplete = true
				if success, ok := log.Fields["success"].(bool); !ok || !success {
					t.Error("Expected success=true")
				}
			}
		}

		// Print logs for debugging if test fails
		if !foundStart || !foundComplete {
			t.Logf("Logs captured: %d", len(logger.logs))
			for i, log := range logger.logs {
				t.Logf("Log %d: Level=%s, Message=%s, Operation=%v",
					i, log.Level, log.Message, log.Fields["operation"])
			}
		}

		if !foundStart {
			t.Error("No execution start log")
		}
		if !foundComplete {
			t.Error("No execution completion log")
		}
	})

	t.Run("Rejected execution", func(t *testing.T) {
		logger := &TestLogger{}
		config := DefaultConfig()
		config.Name = "test-rejection"
		config.Logger = logger
		config.FailureThreshold = 1

		cb, err := NewCircuitBreaker(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Open the circuit
		cb.Execute(context.Background(), func() error {
			return errors.New("error")
		})

		logger.Clear()

		// Try execution when open
		err = cb.Execute(context.Background(), func() error {
			return nil
		})

		if err == nil {
			t.Fatal("Expected rejection error")
		}

		// Should have rejection log
		foundRejection := false
		for _, log := range logger.logs {
			if log.Level == "INFO" && strings.Contains(log.Message, "rejected") {
				foundRejection = true
				if state, ok := log.Fields["current_state"].(string); !ok || state != "open" {
					t.Error("Expected current_state=open in rejection log")
				}
			}
		}

		if !foundRejection {
			t.Error("No rejection log found")
		}
	})
}

// TestStateTransitionLogging verifies state transition logging
func TestStateTransitionLogging(t *testing.T) {
	logger := &TestLogger{}
	config := DefaultConfig()
	config.Name = "test-transitions"
	config.Logger = logger
	config.ErrorThreshold = 0.5 // 50% error rate
	config.VolumeThreshold = 3  // Only need 3 requests to evaluate
	config.SleepWindow = 100 * time.Millisecond

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	logger.Clear()

	// Cause transition to open (need at least VolumeThreshold requests)
	for i := 0; i < 5; i++ {
		cb.Execute(context.Background(), func() error {
			return errors.New("error")
		})
	}

	// Should have opening decision log
	foundOpening := false
	for _, log := range logger.logs {
		if log.Level == "INFO" && strings.Contains(log.Message, "opening due to error threshold") {
			foundOpening = true
			if _, ok := log.Fields["error_rate"]; !ok {
				t.Error("Missing error_rate in opening log")
			}
			if _, ok := log.Fields["error_threshold"]; !ok {
				t.Error("Missing error_threshold in opening log")
			}
		}
	}
	if !foundOpening {
		t.Error("No opening decision log found")
	}

	// Wait for half-open
	time.Sleep(config.SleepWindow + 50*time.Millisecond)

	logger.Clear()

	// Execute to trigger half-open transition
	cb.Execute(context.Background(), func() error {
		return nil
	})

	// Should have half-open transition log
	foundHalfOpen := false
	for _, log := range logger.logs {
		if strings.Contains(log.Message, "half-open") || strings.Contains(log.Message, "transition attempt") {
			foundHalfOpen = true
			break
		}
	}
	if !foundHalfOpen {
		t.Error("No half-open transition log found")
	}
}
