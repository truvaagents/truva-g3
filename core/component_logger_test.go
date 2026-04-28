package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProductionLoggerImplementsComponentAwareLogger verifies that ProductionLogger
// implements the ComponentAwareLogger interface
func TestProductionLoggerImplementsComponentAwareLogger(t *testing.T) {
	logger := NewProductionLogger(
		LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
		DevelopmentConfig{},
		"test-service",
	)

	// Verify ProductionLogger implements ComponentAwareLogger
	_, ok := logger.(ComponentAwareLogger)
	assert.True(t, ok, "ProductionLogger should implement ComponentAwareLogger interface")
}

// TestWithComponentCreatesNewLogger verifies that WithComponent creates a new
// logger instance with the specified component
func TestWithComponentCreatesNewLogger(t *testing.T) {
	// Create parent logger
	parentLogger := NewProductionLogger(
		LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
		DevelopmentConfig{},
		"test-service",
	)

	// Cast to ComponentAwareLogger and create child
	cal, ok := parentLogger.(ComponentAwareLogger)
	require.True(t, ok, "ProductionLogger should implement ComponentAwareLogger")

	childLogger := cal.WithComponent("agent/test-agent")

	// Verify child logger is not the same instance
	assert.NotSame(t, parentLogger, childLogger, "WithComponent should create a new logger instance")

	// Verify child logger also implements ComponentAwareLogger
	_, ok = childLogger.(ComponentAwareLogger)
	assert.True(t, ok, "Child logger should also implement ComponentAwareLogger")
}

// TestWithComponentPreservesConfiguration verifies that WithComponent preserves
// the parent logger's configuration (level, format, serviceName)
func TestWithComponentPreservesConfiguration(t *testing.T) {
	// Create parent logger with specific configuration
	parentLogger := NewProductionLogger(
		LoggingConfig{
			Level:  "debug",
			Format: "json",
			Output: "stdout",
		},
		DevelopmentConfig{},
		"parent-service",
	)

	cal, ok := parentLogger.(ComponentAwareLogger)
	require.True(t, ok)

	childLogger := cal.WithComponent("framework/orchestration")

	// Cast both to ProductionLogger to inspect internal state
	parentPL, ok := parentLogger.(*ProductionLogger)
	require.True(t, ok)

	childPL, ok := childLogger.(*ProductionLogger)
	require.True(t, ok)

	// Verify configuration is preserved
	assert.Equal(t, parentPL.level, childPL.level, "Log level should be preserved")
	assert.Equal(t, parentPL.serviceName, childPL.serviceName, "Service name should be preserved")
	assert.Equal(t, parentPL.format, childPL.format, "Format should be preserved")
	assert.Equal(t, parentPL.metricsEnabled, childPL.metricsEnabled, "Metrics enabled should be preserved")

	// Verify component is different
	assert.NotEqual(t, parentPL.component, childPL.component, "Component should be different")
	assert.Equal(t, "framework/orchestration", childPL.component, "Child should have new component")
}

// TestLogOutputIncludesComponent verifies that log output includes the component field
func TestLogOutputIncludesComponent(t *testing.T) {
	// Create a buffer to capture log output
	var buf bytes.Buffer

	// Create logger with custom output
	logger := &ProductionLogger{
		level:          LogLevelInfo,
		serviceName:    "test-service",
		component:      "framework/core",
		format:         "json",
		output:         &buf,
		metricsEnabled: false,
	}

	// Log a message
	logger.Info("test message", map[string]interface{}{
		"key": "value",
	})

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err, "Log output should be valid JSON")

	// Verify component field is present
	component, ok := logEntry["component"]
	assert.True(t, ok, "Log entry should have component field")
	assert.Equal(t, "framework/core", component, "Component should match")

	// Verify other fields
	assert.Equal(t, "test-service", logEntry["service"])
	assert.Equal(t, "INFO", logEntry["level"])
	assert.Equal(t, "test message", logEntry["message"])
}

// TestWithComponentChangesLogOutput verifies that WithComponent changes the
// component field in log output
func TestWithComponentChangesLogOutput(t *testing.T) {
	// Create a buffer to capture log output
	var buf bytes.Buffer

	// Create parent logger
	parentLogger := &ProductionLogger{
		level:          LogLevelInfo,
		serviceName:    "test-service",
		component:      "framework/core",
		format:         "json",
		output:         &buf,
		metricsEnabled: false,
	}

	// Create child logger with different component
	// Since parentLogger is *ProductionLogger which implements ComponentAwareLogger,
	// we can call WithComponent directly
	childLogger := parentLogger.WithComponent("agent/test-agent")

	// Log using child logger
	childLogger.Info("child message", nil)

	// Parse the JSON output
	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err, "Log output should be valid JSON")

	// Verify component field is the child's component
	component, ok := logEntry["component"]
	assert.True(t, ok, "Log entry should have component field")
	assert.Equal(t, "agent/test-agent", component, "Component should be child's component")
}

// TestDefaultComponentIsFrameworkCore verifies that new loggers default to
// framework/core component
func TestDefaultComponentIsFrameworkCore(t *testing.T) {
	logger := NewProductionLogger(
		LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
		DevelopmentConfig{},
		"test-service",
	)

	pl, ok := logger.(*ProductionLogger)
	require.True(t, ok)

	assert.Equal(t, "framework/core", pl.component, "Default component should be framework/core")
}

// TestComponentNamingConventions verifies common component naming patterns work
func TestComponentNamingConventions(t *testing.T) {
	testCases := []struct {
		name      string
		component string
	}{
		{"framework core", "framework/core"},
		{"framework orchestration", "framework/orchestration"},
		{"framework ai", "framework/ai"},
		{"framework resilience", "framework/resilience"},
		{"framework telemetry", "framework/telemetry"},
		{"agent with name", "agent/travel-research-orchestration"},
		{"tool with name", "tool/weather-service"},
		{"simple agent", "agent/test"},
		{"simple tool", "tool/test"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer

			logger := &ProductionLogger{
				level:          LogLevelInfo,
				serviceName:    "test-service",
				component:      "framework/core",
				format:         "json",
				output:         &buf,
				metricsEnabled: false,
			}

			childLogger := logger.WithComponent(tc.component)
			childLogger.Info("test", nil)

			var logEntry map[string]interface{}
			err := json.Unmarshal(buf.Bytes(), &logEntry)
			require.NoError(t, err)

			assert.Equal(t, tc.component, logEntry["component"])
		})
	}
}

// TestCreateComponentLoggerHelper verifies the createComponentLogger helper function
func TestCreateComponentLoggerHelper(t *testing.T) {
	t.Run("with component-aware logger", func(t *testing.T) {
		baseLogger := NewProductionLogger(
			LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
			DevelopmentConfig{},
			"test-service",
		)

		result := createComponentLogger(baseLogger, "agent/test-agent")

		// Should return a new logger with the component
		pl, ok := result.(*ProductionLogger)
		require.True(t, ok)
		assert.Equal(t, "agent/test-agent", pl.component)
	})

	t.Run("with non-component-aware logger", func(t *testing.T) {
		// NoOpLogger doesn't implement ComponentAwareLogger
		baseLogger := &NoOpLogger{}

		result := createComponentLogger(baseLogger, "agent/test-agent")

		// Should return the same logger unchanged
		assert.Same(t, baseLogger, result)
	})
}

// TestTextFormatWorksWithComponent verifies that text format logs work correctly
// even when component is set. Note: text format is for human-readable local development
// and does not include the component field (component is for JSON log aggregation).
func TestTextFormatWorksWithComponent(t *testing.T) {
	var buf bytes.Buffer

	logger := &ProductionLogger{
		level:          LogLevelInfo,
		serviceName:    "test-service",
		component:      "agent/test-agent",
		format:         "text",
		output:         &buf,
		metricsEnabled: false,
	}

	logger.Info("test message", map[string]interface{}{
		"key": "value",
	})

	output := buf.String()

	// Verify text format still works with component set
	// Text format is for local development and shows: timestamp [LEVEL] [service] message fields
	assert.True(t, strings.Contains(output, "test-service"),
		"Text format should include service name, got: %s", output)
	assert.True(t, strings.Contains(output, "INFO"),
		"Text format should include log level, got: %s", output)
	assert.True(t, strings.Contains(output, "test message"),
		"Text format should include message, got: %s", output)

	// Verify component is stored correctly on the logger
	assert.Equal(t, "agent/test-agent", logger.component,
		"Logger should have component set")
}

// TestChainedWithComponent verifies that WithComponent can be called multiple times
func TestChainedWithComponent(t *testing.T) {
	var buf bytes.Buffer

	logger := &ProductionLogger{
		level:          LogLevelInfo,
		serviceName:    "test-service",
		component:      "framework/core",
		format:         "json",
		output:         &buf,
		metricsEnabled: false,
	}

	// Chain WithComponent calls
	// logger is *ProductionLogger, so call WithComponent directly
	logger2 := logger.WithComponent("framework/orchestration")

	// logger2 is Logger interface, need type assertion to access WithComponent
	cal2, _ := logger2.(ComponentAwareLogger)
	logger3 := cal2.WithComponent("agent/final-agent")

	// Log with final logger
	logger3.Info("test", nil)

	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	// Final component should be used
	assert.Equal(t, "agent/final-agent", logEntry["component"])
}
