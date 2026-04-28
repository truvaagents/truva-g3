package telemetry

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestTelemetryLogger tests the basic functionality of TelemetryLogger
func TestTelemetryLogger(t *testing.T) {
	// Create a buffer to capture output
	var buf bytes.Buffer

	// Create logger with test service name
	logger := createTelemetryLogger("test-service")
	logger.SetOutput(&buf)

	// Test Info logging
	logger.Info("Test info message", map[string]interface{}{
		"key1": "value1",
		"key2": 42,
	})

	output := buf.String()
	if !strings.Contains(output, "Test info message") {
		t.Errorf("Info message not found in output: %s", output)
	}
	if !strings.Contains(output, "INFO") {
		t.Errorf("INFO level not found in output: %s", output)
	}

	// Test Warn logging
	buf.Reset()
	logger.Warn("Test warning", map[string]interface{}{
		"warning_type": "test",
	})

	output = buf.String()
	if !strings.Contains(output, "Test warning") {
		t.Errorf("Warning message not found in output: %s", output)
	}
	if !strings.Contains(output, "WARN") {
		t.Errorf("WARN level not found in output: %s", output)
	}

	// Test Error logging
	buf.Reset()
	logger.Error("Test error", map[string]interface{}{
		"error": "something went wrong",
	})

	output = buf.String()
	if !strings.Contains(output, "Test error") {
		t.Errorf("Error message not found in output: %s", output)
	}
	if !strings.Contains(output, "ERROR") {
		t.Errorf("ERROR level not found in output: %s", output)
	}

	// Test Debug logging (should not appear when debug is false)
	buf.Reset()
	logger.debug = false
	logger.Debug("Debug message", nil)

	output = buf.String()
	if output != "" {
		t.Errorf("Debug message should not appear when debug is false: %s", output)
	}

	// Enable debug and test again
	buf.Reset()
	logger.debug = true
	logger.SetLevel("DEBUG") // Also need to set the level to DEBUG
	logger.Debug("Debug message", nil)

	output = buf.String()
	if !strings.Contains(output, "Debug message") {
		t.Errorf("Debug message not found when debug is enabled: %s", output)
	}
}

// TestTelemetryLoggerJSON tests JSON format output
func TestTelemetryLoggerJSON(t *testing.T) {
	var buf bytes.Buffer

	logger := createTelemetryLogger("test-service")
	logger.SetFormat("json")
	logger.SetOutput(&buf)

	logger.Info("JSON test", map[string]interface{}{
		"field1": "value1",
		"field2": 123,
	})

	output := buf.String()
	if !strings.Contains(output, `"level":"INFO"`) {
		t.Errorf("JSON output missing level field: %s", output)
	}
	if !strings.Contains(output, `"message":"JSON test"`) {
		t.Errorf("JSON output missing message field: %s", output)
	}
	if !strings.Contains(output, `"field1":"value1"`) {
		t.Errorf("JSON output missing custom field: %s", output)
	}
	if !strings.Contains(output, `"service":"test-service"`) {
		t.Errorf("JSON output missing service field: %s", output)
	}
	if !strings.Contains(output, `"component":"telemetry"`) {
		t.Errorf("JSON output missing component field: %s", output)
	}
}

// TestTelemetryLoggerLevels tests log level filtering
func TestTelemetryLoggerLevels(t *testing.T) {
	tests := []struct {
		logLevel     string
		testLevel    string
		shouldAppear bool
		message      string
	}{
		{"INFO", "INFO", true, "Info should appear at INFO level"},
		{"INFO", "DEBUG", false, "Debug should not appear at INFO level"},
		{"DEBUG", "DEBUG", true, "Debug should appear at DEBUG level"},
		{"ERROR", "INFO", false, "Info should not appear at ERROR level"},
		{"ERROR", "WARN", false, "Warn should not appear at ERROR level"},
		{"ERROR", "ERROR", true, "Error should appear at ERROR level"},
		{"WARN", "WARN", true, "Warn should appear at WARN level"},
		{"WARN", "INFO", false, "Info should not appear at WARN level"},
		{"WARN", "ERROR", true, "Error should appear at WARN level"},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		logger := createTelemetryLogger("test-service")
		logger.SetLevel(tt.logLevel)
		logger.SetOutput(&buf)

		// Enable debug mode if testing debug level
		if tt.testLevel == "DEBUG" {
			logger.debug = true
		}

		switch tt.testLevel {
		case "DEBUG":
			logger.Debug("test", nil)
		case "INFO":
			logger.Info("test", nil)
		case "WARN":
			logger.Warn("test", nil)
		case "ERROR":
			logger.Error("test", nil)
		}

		output := buf.String()
		if tt.shouldAppear && output == "" {
			t.Errorf("%s: expected log output but got none", tt.message)
		}
		if !tt.shouldAppear && output != "" {
			t.Errorf("%s: expected no output but got: %s", tt.message, output)
		}
	}
}

// TestTelemetryLoggerRateLimiting tests error rate limiting
func TestTelemetryLoggerRateLimiting(t *testing.T) {
	var buf bytes.Buffer
	logger := createTelemetryLogger("test-service")
	logger.SetOutput(&buf)

	// First error should appear
	logger.Error("Error 1", nil)
	output1 := buf.String()
	if !strings.Contains(output1, "Error 1") {
		t.Error("First error should appear")
	}

	// Second error immediately after should be rate limited
	buf.Reset()
	logger.Error("Error 2", nil)
	output2 := buf.String()
	if output2 != "" {
		t.Error("Second error should be rate limited")
	}

	// After waiting (in real scenario would wait 1 second)
	// For testing, we'll manually reset the rate limiter's last time
	logger.errorLimiter.lastTime = logger.errorLimiter.lastTime.Add(-2 * logger.errorLimiter.interval)

	buf.Reset()
	logger.Error("Error 3", nil)
	output3 := buf.String()
	if !strings.Contains(output3, "Error 3") {
		t.Error("Error after rate limit interval should appear")
	}
}

// TestTelemetryLoggerEnvironmentVariables tests environment variable configuration
func TestTelemetryLoggerEnvironmentVariables(t *testing.T) {
	// Save original env vars
	origLogLevel := os.Getenv("TRUVAG3_LOG_LEVEL")
	origDebug := os.Getenv("TRUVAG3_DEBUG")
	origK8s := os.Getenv("KUBERNETES_SERVICE_HOST")
	defer func() {
		os.Setenv("TRUVAG3_LOG_LEVEL", origLogLevel)
		os.Setenv("TRUVAG3_DEBUG", origDebug)
		os.Setenv("KUBERNETES_SERVICE_HOST", origK8s)
	}()

	// Test log level from env
	os.Setenv("TRUVAG3_LOG_LEVEL", "WARN")
	os.Setenv("TRUVAG3_DEBUG", "")
	os.Setenv("KUBERNETES_SERVICE_HOST", "")

	// Reset singleton to pick up new env vars
	telemetryLogger = nil
	telemetryLoggerOnce = sync.Once{}

	logger := NewTelemetryLogger("test-service")
	if logger.level != "WARN" {
		t.Errorf("Expected log level WARN from env, got %s", logger.level)
	}

	// Test debug mode from env
	os.Setenv("TRUVAG3_DEBUG", "true")
	telemetryLogger = nil
	telemetryLoggerOnce = sync.Once{}

	logger = NewTelemetryLogger("test-service")
	if !logger.debug {
		t.Error("Expected debug mode to be enabled from env")
	}

	// Test Kubernetes environment detection
	os.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	telemetryLogger = nil
	telemetryLoggerOnce = sync.Once{}

	logger = NewTelemetryLogger("test-service")
	if logger.format != "json" {
		t.Errorf("Expected JSON format in Kubernetes environment, got %s", logger.format)
	}
}
