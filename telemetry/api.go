// Package telemetry provides simple, production-ready metrics emission.
// The API is designed with progressive disclosure:
// Level 1 (this file) covers 90% of use cases with simple functions.
// Level 2 adds type-specific helpers.
// Level 3 (EmitWithOptions) provides full control when needed.
package telemetry

import (
	"context"
	"time"
)

// Level 1: Dead simple API (90% of usage)
// These functions cover the most common telemetry needs.

// Counter increments a counter metric by 1.
// Use for counting events: requests, errors, operations, etc.
// Labels should be provided as key-value pairs.
// Example: Counter("requests.total", "method", "GET", "status", "200")
func Counter(name string, labels ...string) {
	Emit(name, 1, labels...)
}

// Histogram records a value in a distribution.
// Perfect for latencies, request sizes, queue lengths, etc.
// The telemetry backend will calculate percentiles automatically.
// Example: Histogram("latency.ms", 125.3, "endpoint", "/api/users")
func Histogram(name string, value float64, labels ...string) {
	Emit(name, value, labels...)
}

// Gauge sets a gauge value (current value metrics).
// Use for values that go up and down: active connections, memory usage, queue size.
// For increment/decrement, use positive/negative values.
// Example: Gauge("connections.active", 42, "pool", "database")
func Gauge(name string, value float64, labels ...string) {
	// Implementation note: We record gauges as histograms internally
	// because OpenTelemetry gauges require callbacks. This gives us
	// similar functionality without the complexity.
	registry := globalRegistry.Load()
	if registry != nil {
		r := registry.(*Registry)
		// Mark this as a gauge internally for proper handling
		_ = r.metrics.RecordHistogram(context.Background(), name, value)
	}
	Emit(name, value, labels...)
}

// Duration records elapsed time since startTime in milliseconds.
// Convenience function for the common pattern of timing operations.
// Example:
//
//	start := time.Now()
//	defer Duration("operation.duration_ms", start, "op", "process")
func Duration(name string, startTime time.Time, labels ...string) {
	ms := float64(time.Since(startTime).Milliseconds())
	Emit(name, ms, labels...)
}

// Level 2: Type-specific helpers (9% of usage)
// These functions provide semantic meaning for specific metric types.

// RecordError records an error occurrence with type classification
func RecordError(name string, errorType string, labels ...string) {
	allLabels := append(labels, "error_type", errorType)
	Counter(name, allLabels...)
}

// RecordSuccess records a successful operation
func RecordSuccess(name string, labels ...string) {
	allLabels := append(labels, "status", "success")
	Counter(name, allLabels...)
}

// RecordLatency records operation latency with automatic bucketing
func RecordLatency(name string, milliseconds float64, labels ...string) {
	// Add latency bucket for easier aggregation
	bucket := getLatencyBucket(milliseconds)
	allLabels := append(labels, "latency_bucket", bucket)
	Histogram(name, milliseconds, allLabels...)
}

// RecordBytes records byte counts
func RecordBytes(name string, bytes int64, labels ...string) {
	Emit(name, float64(bytes), labels...)
}

// Level 3: Full control API (1% of usage)

// EmitOption configures advanced emission options
type EmitOption func(*emitConfig)

// emitConfig holds advanced emission configuration
type emitConfig struct {
	timestamp   time.Time
	labels      map[string]string
	unit        Unit
	sampleRate  float64
	skipCircuit bool
}

// Unit represents a metric unit
type Unit string

const (
	UnitMilliseconds Unit = "ms"
	UnitBytes        Unit = "bytes"
	UnitPercent      Unit = "percent"
	UnitCount        Unit = "count"
)

// EmitWithOptions provides full control over metric emission
func EmitWithOptions(ctx context.Context, name string, value float64, opts ...EmitOption) {
	cfg := &emitConfig{
		timestamp:  time.Now(),
		labels:     make(map[string]string),
		sampleRate: 1.0,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// Apply sampling
	if cfg.sampleRate < 1.0 && !shouldSample(cfg.sampleRate) {
		return
	}

	// Convert labels map to variadic
	var labelPairs []string
	for k, v := range cfg.labels {
		labelPairs = append(labelPairs, k, v)
	}

	// Use context-aware emission if available
	EmitWithContext(ctx, name, value, labelPairs...)
}

// WithTimestamp sets a custom timestamp
func WithTimestamp(t time.Time) EmitOption {
	return func(c *emitConfig) { c.timestamp = t }
}

// WithUnit sets the metric unit
func WithUnit(u Unit) EmitOption {
	return func(c *emitConfig) { c.unit = u }
}

// WithLabels adds multiple labels at once
func WithLabels(labels map[string]string) EmitOption {
	return func(c *emitConfig) {
		for k, v := range labels {
			c.labels[k] = v
		}
	}
}

// WithLabel adds a single label
func WithLabel(key, value string) EmitOption {
	return func(c *emitConfig) {
		c.labels[key] = value
	}
}

// WithSampleRate sets a custom sample rate (0.0-1.0)
func WithSampleRate(rate float64) EmitOption {
	return func(c *emitConfig) { c.sampleRate = rate }
}

// WithoutCircuitBreaker bypasses circuit breaker checks
func WithoutCircuitBreaker() EmitOption {
	return func(c *emitConfig) { c.skipCircuit = true }
}

// Helper functions

// getLatencyBucket returns a human-readable latency bucket
func getLatencyBucket(ms float64) string {
	switch {
	case ms < 1:
		return "<1ms"
	case ms < 10:
		return "1-10ms"
	case ms < 100:
		return "10-100ms"
	case ms < 1000:
		return "100ms-1s"
	case ms < 10000:
		return "1-10s"
	default:
		return ">10s"
	}
}

// shouldSample determines if a metric should be sampled
func shouldSample(rate float64) bool {
	if rate >= 1.0 {
		return true
	}
	if rate <= 0.0 {
		return false
	}
	// Simple random sampling - in production, use better algorithm
	return time.Now().UnixNano()%100 < int64(rate*100)
}

// Convenience functions for common patterns

// TimeOperation times an operation and records its duration
func TimeOperation(name string, labels ...string) func() {
	start := time.Now()
	return func() {
		Duration(name, start, labels...)
	}
}

// TrackGoroutines tracks the number of active goroutines
func TrackGoroutines(name string, delta int, labels ...string) {
	registry := globalRegistry.Load()
	if registry != nil {
		r := registry.(*Registry)
		// Use UpDownCounter for tracking goroutines
		ctx := context.Background()
		_ = r.metrics.RecordUpDownCounter(ctx, name, int64(delta))
	}
}

// BatchEmit emits multiple metrics in a single operation (for efficiency)
func BatchEmit(metrics []struct {
	Name   string
	Value  float64
	Labels []string
}) {
	for _, m := range metrics {
		Emit(m.Name, m.Value, m.Labels...)
	}
}
