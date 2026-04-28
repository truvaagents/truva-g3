package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// TelemetryLogger provides self-contained logging for telemetry operations.
// It follows the same layered observability pattern as core.ProductionLogger
// but is independent to maintain architectural separation.
//
// Design Principles:
//   - Self-contained: No dependencies on core module
//   - Production-ready: JSON format in K8s, text for local dev
//   - Rate-limited: Prevents log flooding during failures
//   - Thread-safe: Safe for concurrent access
//
// Logging Layers:
//   - Layer 1: Console output (always works, immediate visibility)
//   - Layer 2: Metrics emission (when registry is initialized)
//   - Layer 3: Context correlation (future: trace/span integration)
type TelemetryLogger struct {
	level       string
	debug       bool
	serviceName string
	format      string
	output      io.Writer
	mu          sync.RWMutex

	// Rate limiting to prevent log flooding during failures
	errorLimiter *RateLimiter

	// Metrics emission layer (enabled when registry available)
	metricsEnabled bool
}

// telemetryLoggerSingleton ensures single logger instance for the module
var (
	telemetryLogger     *TelemetryLogger
	telemetryLoggerOnce sync.Once
)

// NewTelemetryLogger creates a logger for telemetry operations.
// Configuration priority:
//  1. Explicit parameters (highest)
//  2. Environment variables (TRUVAG3_LOG_LEVEL, TRUVAG3_DEBUG, TELEMETRY_DEBUG)
//  3. Auto-detection (K8s environment)
//  4. Defaults (lowest)
func NewTelemetryLogger(serviceName string) *TelemetryLogger {
	// Use singleton pattern to ensure consistent logging across telemetry module
	telemetryLoggerOnce.Do(func() {
		telemetryLogger = createTelemetryLogger(serviceName)
	})
	return telemetryLogger
}

// createTelemetryLogger creates the actual logger instance
func createTelemetryLogger(serviceName string) *TelemetryLogger {
	// Determine log level from environment
	level := os.Getenv("TRUVAG3_LOG_LEVEL")
	if level == "" {
		level = "INFO"
	}

	// Debug mode can be enabled via TRUVAG3_DEBUG or TELEMETRY_DEBUG
	debug := os.Getenv("TRUVAG3_DEBUG") == "true" ||
		os.Getenv("TELEMETRY_DEBUG") == "true" ||
		strings.ToUpper(level) == "DEBUG"

	// Auto-detect Kubernetes environment for structured logging
	format := "text"
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		format = "json" // Use JSON in K8s for log aggregation
	}
	// Allow explicit override
	if envFormat := os.Getenv("TRUVAG3_LOG_FORMAT"); envFormat != "" {
		format = envFormat
	}

	return &TelemetryLogger{
		level:        strings.ToUpper(level),
		debug:        debug,
		serviceName:  serviceName,
		format:       format,
		output:       os.Stdout,
		errorLimiter: NewRateLimiter(1 * time.Second), // Max 1 error log per second
	}
}

// Info logs informational messages
func (l *TelemetryLogger) Info(msg string, fields map[string]interface{}) {
	l.log("INFO", msg, fields)
}

// Warn logs warning messages
func (l *TelemetryLogger) Warn(msg string, fields map[string]interface{}) {
	l.log("WARN", msg, fields)
}

// Error logs error messages with rate limiting
func (l *TelemetryLogger) Error(msg string, fields map[string]interface{}) {
	// Rate limit error logs to prevent flooding during failures
	if l.errorLimiter != nil && !l.errorLimiter.Allow() {
		return
	}
	l.log("ERROR", msg, fields)
}

// Debug logs debug messages (only when debug mode is enabled)
func (l *TelemetryLogger) Debug(msg string, fields map[string]interface{}) {
	if !l.debug {
		return
	}
	l.log("DEBUG", msg, fields)
}

// log is the core logging implementation
func (l *TelemetryLogger) log(level, msg string, fields map[string]interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Check if we should log this level
	if !l.shouldLog(level) {
		return
	}

	timestamp := time.Now().Format(time.RFC3339)

	// Layer 1: Console output (always works)
	if l.format == "json" {
		// Structured logging for production/K8s environments
		l.logJSON(timestamp, level, msg, fields)
	} else {
		// Human-readable format for local development
		l.logText(timestamp, level, msg, fields)
	}

	// Layer 2: Metrics emission (when registry is available)
	l.emitLogMetric(level, fields)
}

// logJSON outputs structured JSON logs
func (l *TelemetryLogger) logJSON(timestamp, level, msg string, fields map[string]interface{}) {
	logEntry := map[string]interface{}{
		"timestamp": timestamp,
		"level":     level,
		"service":   l.serviceName,
		"component": "telemetry",
		"message":   msg,
	}

	// Add all fields
	for k, v := range fields {
		// Avoid overwriting core fields
		if k != "timestamp" && k != "level" && k != "service" && k != "component" && k != "message" {
			logEntry[k] = v
		}
	}

	// Output as JSON
	if data, err := json.Marshal(logEntry); err == nil {
		_, _ = fmt.Fprintln(l.output, string(data)) // Error writing logs can be safely ignored
	}
}

// logText outputs human-readable text logs
func (l *TelemetryLogger) logText(timestamp, level, msg string, fields map[string]interface{}) {
	// Build field string
	var fieldStr strings.Builder
	if len(fields) > 0 {
		fieldStr.WriteString(" ")
		// Sort common fields first for readability
		if endpoint, ok := fields["endpoint"]; ok {
			fmt.Fprintf(&fieldStr, "endpoint=%v ", endpoint)
			delete(fields, "endpoint")
		}
		if err, ok := fields["error"]; ok {
			fmt.Fprintf(&fieldStr, "error=\"%v\" ", err)
			delete(fields, "error")
		}
		if action, ok := fields["action"]; ok {
			fmt.Fprintf(&fieldStr, "action=\"%v\" ", action)
			delete(fields, "action")
		}
		if impact, ok := fields["impact"]; ok {
			fmt.Fprintf(&fieldStr, "impact=\"%v\" ", impact)
			delete(fields, "impact")
		}
		// Add remaining fields
		for k, v := range fields {
			fmt.Fprintf(&fieldStr, "%s=%v ", k, v)
		}
	}

	// Output formatted log line following document pattern
	_, _ = fmt.Fprintf(l.output, "%s [%s] [telemetry:%s] %s%s\n",
		timestamp, level, l.serviceName, msg, fieldStr.String()) // Error writing logs can be safely ignored
}

// shouldLog determines if a log level should be output
func (l *TelemetryLogger) shouldLog(level string) bool {
	// Define level hierarchy
	levels := map[string]int{
		"DEBUG": 0,
		"INFO":  1,
		"WARN":  2,
		"ERROR": 3,
	}

	// Get numeric values for comparison
	currentLevel, ok1 := levels[l.level]
	messageLevel, ok2 := levels[level]

	// Default to logging if levels are unknown
	if !ok1 || !ok2 {
		return true
	}

	// Log if message level >= configured level
	return messageLevel >= currentLevel
}

// SetLevel dynamically updates the log level
func (l *TelemetryLogger) SetLevel(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = strings.ToUpper(level)
	// Update debug flag based on new level
	l.debug = l.level == "DEBUG"
}

// SetFormat dynamically updates the log format
func (l *TelemetryLogger) SetFormat(format string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.format = format
}

// SetOutput changes the output writer (useful for testing)
func (l *TelemetryLogger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.output = w
}

// emitLogMetric emits metrics about logging operations
// This implements Layer 2 of the observability architecture
func (l *TelemetryLogger) emitLogMetric(level string, fields map[string]interface{}) {
	// Only emit metrics if registry is initialized
	if !l.metricsEnabled || globalRegistry.Load() == nil {
		return
	}

	// Build labels with cardinality awareness (only low-cardinality fields)
	labels := []string{
		"level", level,
		"service", l.serviceName,
		"component", "telemetry",
	}

	// Add only low-cardinality fields as labels
	for k, v := range fields {
		switch k {
		case "operation", "status", "error_type", "provider":
			labels = append(labels, k, fmt.Sprintf("%v", v))
		}
	}

	// Emit telemetry operations metric
	Emit("truvag3.telemetry.operations", 1.0, labels...)
}

// EnableMetrics is called when the telemetry registry is initialized
func (l *TelemetryLogger) EnableMetrics() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.metricsEnabled = true
}

// GetLogger returns the global telemetry logger instance
// This is used internally by telemetry components when needed
// It derives the service name from the global registry if available
func GetLogger() *TelemetryLogger {
	telemetryLoggerOnce.Do(func() {
		serviceName := "telemetry"
		// Try to get service name from registry if available
		if registry := globalRegistry.Load(); registry != nil {
			if r, ok := registry.(*Registry); ok && r.config.ServiceName != "" {
				serviceName = r.config.ServiceName
			}
		}
		telemetryLogger = createTelemetryLogger(serviceName)
	})
	return telemetryLogger
}
