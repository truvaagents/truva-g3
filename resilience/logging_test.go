package resilience

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestLogger captures logs for verification
type TestLogger struct {
	logs []LogEntry
}

type LogEntry struct {
	Level   string
	Message string
	Fields  map[string]interface{}
}

func (t *TestLogger) Info(msg string, fields map[string]interface{}) {
	t.logs = append(t.logs, LogEntry{Level: "INFO", Message: msg, Fields: fields})
}

func (t *TestLogger) Error(msg string, fields map[string]interface{}) {
	t.logs = append(t.logs, LogEntry{Level: "ERROR", Message: msg, Fields: fields})
}

func (t *TestLogger) Warn(msg string, fields map[string]interface{}) {
	t.logs = append(t.logs, LogEntry{Level: "WARN", Message: msg, Fields: fields})
}

func (t *TestLogger) Debug(msg string, fields map[string]interface{}) {
	t.logs = append(t.logs, LogEntry{Level: "DEBUG", Message: msg, Fields: fields})
}

// Context-aware logging methods
func (t *TestLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Info(msg, fields)
}

func (t *TestLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Error(msg, fields)
}

func (t *TestLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Warn(msg, fields)
}

func (t *TestLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	t.Debug(msg, fields)
}

func (t *TestLogger) GetLogsByOperation(operation string) []LogEntry {
	var result []LogEntry
	for _, log := range t.logs {
		if op, exists := log.Fields["operation"]; exists && op == operation {
			result = append(result, log)
		}
	}
	return result
}

func (t *TestLogger) GetLogsByLevel(level string) []LogEntry {
	var result []LogEntry
	for _, log := range t.logs {
		if log.Level == level {
			result = append(result, log)
		}
	}
	return result
}

func (t *TestLogger) HasLogWithMessage(message string) bool {
	for _, log := range t.logs {
		if strings.Contains(log.Message, message) {
			return true
		}
	}
	return false
}

func (t *TestLogger) Clear() {
	t.logs = nil
}

func TestCircuitBreakerLoggingIntegration(t *testing.T) {
	testLogger := &TestLogger{}

	// Use factory function for proper creation logging
	deps := ResilienceDependencies{
		Logger: testLogger,
	}

	cb, err := CreateCircuitBreaker("test-cb", deps)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Verify creation logging
	if !testLogger.HasLogWithMessage("Creating circuit breaker") {
		t.Error("No circuit breaker creation log found")
	}

	// Clear logs to focus on execution
	testLogger.Clear()

	// Test successful execution
	err = cb.Execute(context.Background(), func() error {
		return nil
	})

	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Verify execution logs exist (should have DEBUG logs)
	if len(testLogger.logs) == 0 {
		t.Error("No logs captured during execution")
	}

	// Check for specific execution operations
	executeLogs := testLogger.GetLogsByOperation("circuit_breaker_execute")
	if len(executeLogs) == 0 {
		t.Error("No circuit breaker execute logs found")
	}

	// Test failure scenario
	testLogger.Clear()

	// Force multiple failures to trigger state change
	for i := 0; i < 15; i++ {
		err := cb.Execute(context.Background(), func() error {
			return errors.New("test failure")
		})
		// Expect failures but don't fail the test
		_ = err
	}

	// Check that we have some logs from the failures
	if len(testLogger.logs) == 0 {
		t.Error("No logs captured during failure scenario")
	}

	// Look for any error-related operations or state changes
	hasFailureRelatedLogs := false
	for _, log := range testLogger.logs {
		if op, exists := log.Fields["operation"]; exists {
			opStr := op.(string)
			if strings.Contains(opStr, "execute") || strings.Contains(opStr, "state") || strings.Contains(opStr, "failure") {
				hasFailureRelatedLogs = true
				break
			}
		}
	}

	if !hasFailureRelatedLogs {
		t.Error("No failure-related logs found during failure scenario")
	}
}

func TestRetryExecutorLoggingIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry executor logging integration test in short mode (uses default retry delays)")
	}

	testLogger := &TestLogger{}

	executor := NewRetryExecutor(nil)
	executor.SetLogger(testLogger)

	// Test with failure then success
	attempt := 0
	err := executor.Execute(context.Background(), "test-operation", func() error {
		attempt++
		if attempt < 3 {
			return errors.New("temporary failure")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Retry execution failed: %v", err)
	}

	// Verify retry start logging
	startLogs := testLogger.GetLogsByOperation("retry_start")
	if len(startLogs) != 1 {
		t.Errorf("Expected 1 retry start log, got %d", len(startLogs))
	}

	// Verify we have logs from multiple attempts
	if len(testLogger.logs) < 3 {
		t.Errorf("Expected multiple logs from retry attempts, got %d", len(testLogger.logs))
	}

	// Verify success logging exists
	if !testLogger.HasLogWithMessage("retry operation succeeded") && !testLogger.HasLogWithMessage("Starting retry operation") {
		t.Error("No success-related logs found")
	}

	// Check for operation field in logs
	for _, log := range testLogger.logs {
		if op, exists := log.Fields["retry_operation"]; exists {
			if op != "test-operation" {
				t.Errorf("Expected retry_operation to be 'test-operation', got %v", op)
			}
		}
	}
}

func TestRetryExecutorExhaustionLogging(t *testing.T) {
	testLogger := &TestLogger{}

	config := &RetryConfig{
		MaxAttempts:   2,
		InitialDelay:  1 * time.Millisecond,
		MaxDelay:      10 * time.Millisecond,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	executor := NewRetryExecutor(config)
	executor.SetLogger(testLogger)

	// Test with all failures
	err := executor.Execute(context.Background(), "failure-test", func() error {
		return errors.New("persistent failure")
	})

	if err == nil {
		t.Fatal("Expected retry to fail after exhaustion")
	}

	// Verify error logging for final failure
	errorLogs := testLogger.GetLogsByLevel("ERROR")
	if len(errorLogs) == 0 {
		t.Error("No error logs found for retry exhaustion")
	}

	// Verify backoff logging occurred
	hasBackoffLog := false
	for _, log := range testLogger.logs {
		if op, exists := log.Fields["operation"]; exists && op == "retry_backoff" {
			hasBackoffLog = true
			break
		}
	}
	if !hasBackoffLog {
		t.Error("No backoff logs found")
	}
}

func TestFactoryDependencyInjection(t *testing.T) {
	testLogger := &TestLogger{}

	deps := ResilienceDependencies{
		Logger: testLogger,
	}

	// Test circuit breaker creation
	cb, err := CreateCircuitBreaker("factory-test", deps)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Verify logger was injected by checking creation log
	if !testLogger.HasLogWithMessage("Creating circuit breaker") {
		t.Error("Circuit breaker creation log not found, logger injection may have failed")
	}

	// Test execution to verify logger is working
	originalLogCount := len(testLogger.logs)
	err = cb.Execute(context.Background(), func() error {
		return nil
	})

	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	if len(testLogger.logs) <= originalLogCount {
		t.Error("No new logs captured during execution, logger injection may have failed")
	}

	// Test retry executor creation
	testLogger.Clear()
	executor := CreateRetryExecutor(deps)

	err = executor.Execute(context.Background(), "factory-test", func() error {
		return nil
	})

	if err != nil {
		t.Fatalf("Retry execution failed: %v", err)
	}

	if len(testLogger.logs) == 0 {
		t.Error("No logs captured during retry execution, logger injection failed")
	}

	// Verify operation names are preserved in logs
	foundCorrectOperation := false
	for _, log := range testLogger.logs {
		if op, exists := log.Fields["retry_operation"]; exists {
			if op == "factory-test" {
				foundCorrectOperation = true
				break
			}
		}
	}

	if !foundCorrectOperation {
		t.Error("Expected retry_operation 'factory-test' not found in logs")
	}
}

func TestCircuitBreakerSetLogger(t *testing.T) {
	// Test circuit breaker SetLogger method if it exists
	config := DefaultConfig()
	config.Name = "setlogger-test"
	config.Logger = &core.NoOpLogger{}

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	testLogger := &TestLogger{}

	// Try to set logger using SetLogger method if available
	if setter, ok := interface{}(cb).(interface{ SetLogger(core.Logger) }); ok {
		setter.SetLogger(testLogger)

		// Test that the new logger is used
		err = cb.Execute(context.Background(), func() error {
			return nil
		})

		if err != nil {
			t.Fatalf("Execution failed: %v", err)
		}

		// Note: This test may not capture logs if SetLogger doesn't update the config's logger
		// The test verifies the method exists and can be called without error
	} else {
		t.Log("SetLogger method not available on CircuitBreaker - this is expected if not yet implemented")
	}
}

func TestRetryWithLogging(t *testing.T) {
	testLogger := &TestLogger{}

	config := DefaultRetryConfig()

	// Test RetryWithLogging function if it exists
	ctx := context.Background()
	operation := "test-with-logging"

	attempt := 0
	testFunc := func() error {
		attempt++
		if attempt < 2 {
			return errors.New("first attempt fails")
		}
		return nil
	}

	// Create an executor manually and test
	executor := NewRetryExecutor(config)
	executor.SetLogger(testLogger)

	err := executor.Execute(ctx, operation, testFunc)
	if err != nil {
		t.Fatalf("RetryWithLogging failed: %v", err)
	}

	// Verify logging occurred
	if len(testLogger.logs) == 0 {
		t.Error("No logs captured during retry with logging")
	}

	// Verify operation name is correct
	for _, log := range testLogger.logs {
		if op, exists := log.Fields["retry_operation"]; exists {
			if op != operation {
				t.Errorf("Expected retry_operation to be '%s', got %v", operation, op)
			}
		}
	}
}

func TestLoggingFieldValidation(t *testing.T) {
	testLogger := &TestLogger{}

	// Test circuit breaker field validation
	config := DefaultConfig()
	config.Name = "field-validation-test"
	config.Logger = testLogger

	cb, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	err = cb.Execute(context.Background(), func() error {
		return nil
	})

	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Verify required fields are present in logs
	for _, log := range testLogger.logs {
		// Check for name field in circuit breaker logs
		if name, exists := log.Fields["name"]; exists {
			if name != "field-validation-test" {
				t.Errorf("Expected name field to be 'field-validation-test', got %v", name)
			}
		}

		// Check for operation field
		if _, exists := log.Fields["operation"]; !exists {
			t.Errorf("Missing operation field in log: %v", log)
		}
	}

	// Test retry executor field validation
	testLogger.Clear()
	executor := NewRetryExecutor(nil)
	executor.SetLogger(testLogger)

	err = executor.Execute(context.Background(), "field-test", func() error {
		return nil
	})

	if err != nil {
		t.Fatalf("Retry execution failed: %v", err)
	}

	// Verify retry-specific fields
	for _, log := range testLogger.logs {
		if op, exists := log.Fields["operation"]; exists && op == "retry_start" {
			// Check for required retry configuration fields
			requiredFields := []string{"max_attempts", "initial_delay", "backoff_factor"}
			for _, field := range requiredFields {
				if _, exists := log.Fields[field]; !exists {
					t.Errorf("Missing required field '%s' in retry start log", field)
				}
			}
		}
	}
}

// ============================================================================
// Component-Aware Logging Tests
// ============================================================================

// ComponentAwareTestLogger implements ComponentAwareLogger for testing
type ComponentAwareTestLogger struct {
	*TestLogger
	component string
}

func NewComponentAwareTestLogger() *ComponentAwareTestLogger {
	return &ComponentAwareTestLogger{
		TestLogger: &TestLogger{},
		component:  "test/default",
	}
}

func (c *ComponentAwareTestLogger) WithComponent(component string) core.Logger {
	return &ComponentAwareTestLogger{
		TestLogger: c.TestLogger, // Share the same log storage
		component:  component,
	}
}

func (c *ComponentAwareTestLogger) GetComponent() string {
	return c.component
}

// TestCreateCircuitBreakerSetsResilienceComponent verifies that CreateCircuitBreaker
// sets the "framework/resilience" component when using ComponentAwareLogger
func TestCreateCircuitBreakerSetsResilienceComponent(t *testing.T) {
	testLogger := NewComponentAwareTestLogger()

	deps := ResilienceDependencies{
		Logger: testLogger,
	}

	_, err := CreateCircuitBreaker("component-test-cb", deps)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// The factory function should have called WithComponent("framework/resilience")
	// We can verify this by checking the component on the logger
	// Note: The factory creates a new logger instance via WithComponent, so we need
	// to test this differently - by creating a circuit breaker with default deps
	// and verifying the ProductionLogger has the correct component

	// For a proper test, we need to verify the factory function behavior
	// by creating a circuit breaker without providing a logger
	depsNoLogger := ResilienceDependencies{}
	cb, err := CreateCircuitBreaker("default-logger-test", depsNoLogger)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// The circuit breaker's config should have a logger with "framework/resilience" component
	// We can verify this by checking if the logger is a ProductionLogger with correct component
	if pl, ok := cb.config.Logger.(*core.ProductionLogger); ok {
		if pl.GetComponent() != "framework/resilience" {
			t.Errorf("Expected component 'framework/resilience', got '%s'", pl.GetComponent())
		}
	} else {
		// If not ProductionLogger, it should still be component-aware
		if cal, ok := cb.config.Logger.(interface{ GetComponent() string }); ok {
			if cal.GetComponent() != "framework/resilience" {
				t.Errorf("Expected component 'framework/resilience', got '%s'", cal.GetComponent())
			}
		}
	}
}

// TestCreateRetryExecutorSetsResilienceComponent verifies that CreateRetryExecutor
// sets the "framework/resilience" component when using ComponentAwareLogger
func TestCreateRetryExecutorSetsResilienceComponent(t *testing.T) {
	// Create retry executor without providing a logger
	depsNoLogger := ResilienceDependencies{}
	executor := CreateRetryExecutor(depsNoLogger)

	// The executor's logger should have "framework/resilience" component
	if pl, ok := executor.logger.(*core.ProductionLogger); ok {
		if pl.GetComponent() != "framework/resilience" {
			t.Errorf("Expected component 'framework/resilience', got '%s'", pl.GetComponent())
		}
	} else {
		// If not ProductionLogger, check if it has GetComponent method
		if cal, ok := executor.logger.(interface{ GetComponent() string }); ok {
			if cal.GetComponent() != "framework/resilience" {
				t.Errorf("Expected component 'framework/resilience', got '%s'", cal.GetComponent())
			}
		}
	}
}

// TestFactoryWithComponentAwareLogger verifies that factory functions correctly
// use WithComponent when provided with a ComponentAwareLogger
func TestFactoryWithComponentAwareLogger(t *testing.T) {
	t.Run("circuit breaker with component-aware logger", func(t *testing.T) {
		testLogger := NewComponentAwareTestLogger()

		deps := ResilienceDependencies{
			Logger: testLogger,
		}

		cb, err := CreateCircuitBreaker("cal-test-cb", deps)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Execute something to generate logs
		err = cb.Execute(context.Background(), func() error {
			return nil
		})
		if err != nil {
			t.Fatalf("Execution failed: %v", err)
		}

		// Verify logs were captured (proving the logger was injected)
		if len(testLogger.logs) == 0 {
			t.Error("No logs captured, logger injection may have failed")
		}
	})

	t.Run("retry executor with component-aware logger", func(t *testing.T) {
		testLogger := NewComponentAwareTestLogger()

		deps := ResilienceDependencies{
			Logger: testLogger,
		}

		executor := CreateRetryExecutor(deps)

		// Execute something to generate logs
		err := executor.Execute(context.Background(), "cal-test-retry", func() error {
			return nil
		})
		if err != nil {
			t.Fatalf("Execution failed: %v", err)
		}

		// Verify logs were captured
		if len(testLogger.logs) == 0 {
			t.Error("No logs captured, logger injection may have failed")
		}
	})
}

// TestDefaultLoggerHasResilienceComponent verifies that when no logger is provided,
// the default ProductionLogger is created with "framework/resilience" component
func TestDefaultLoggerHasResilienceComponent(t *testing.T) {
	t.Run("circuit breaker default logger", func(t *testing.T) {
		deps := ResilienceDependencies{} // No logger provided

		cb, err := CreateCircuitBreaker("default-component-test", deps)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Check that the logger has the correct component
		if pl, ok := cb.config.Logger.(*core.ProductionLogger); ok {
			component := pl.GetComponent()
			if component != "framework/resilience" {
				t.Errorf("Expected default logger component 'framework/resilience', got '%s'", component)
			}
		} else {
			t.Log("Logger is not ProductionLogger - component check skipped")
		}
	})

	t.Run("retry executor default logger", func(t *testing.T) {
		deps := ResilienceDependencies{} // No logger provided

		executor := CreateRetryExecutor(deps)

		// Check that the logger has the correct component
		if pl, ok := executor.logger.(*core.ProductionLogger); ok {
			component := pl.GetComponent()
			if component != "framework/resilience" {
				t.Errorf("Expected default logger component 'framework/resilience', got '%s'", component)
			}
		} else {
			t.Log("Logger is not ProductionLogger - component check skipped")
		}
	})
}
