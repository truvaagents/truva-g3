package telemetry

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
)

// FrameworkMetricsRegistry implements core.MetricsRegistry
// This enables all framework components to emit metrics through telemetry
type FrameworkMetricsRegistry struct {
	logger *TelemetryLogger
}

// NewFrameworkMetricsRegistry creates a new framework metrics registry
func NewFrameworkMetricsRegistry(logger *TelemetryLogger) *FrameworkMetricsRegistry {
	return &FrameworkMetricsRegistry{
		logger: logger,
	}
}

// Counter implements core.MetricsRegistry
func (f *FrameworkMetricsRegistry) Counter(name string, labels ...string) {
	// Debug log framework emissions
	if f.logger != nil && f.logger.debug {
		f.logger.Debug("Framework metric emission", map[string]interface{}{
			"metric_name": name,
			"type":        "counter",
			"label_count": len(labels) / 2,
			"source":      "framework",
		})
	}

	// Delegate to telemetry's global emission
	Emit(name, 1.0, labels...)
}

// EmitWithContext implements core.MetricsRegistry
func (f *FrameworkMetricsRegistry) EmitWithContext(ctx context.Context, name string, value float64, labels ...string) {
	// Extract context for correlation
	baggage := GetBaggage(ctx)

	if f.logger != nil && f.logger.debug {
		// Log with context awareness
		requestID := ""
		if baggage != nil {
			if id, ok := baggage["request_id"]; ok {
				requestID = id
			}
		}

		f.logger.Debug("Framework context-aware emission", map[string]interface{}{
			"metric_name": name,
			"value":       value,
			"has_baggage": len(baggage) > 0,
			"request_id":  requestID,
			"label_count": len(labels) / 2,
			"source":      "framework",
		})
	}

	// Use telemetry's context-aware emission
	EmitWithContext(ctx, name, value, labels...)
}

// GetBaggage implements core.MetricsRegistry
// Returns W3C Baggage plus OpenTelemetry trace context (trace_id, span_id) for log correlation.
func (f *FrameworkMetricsRegistry) GetBaggage(ctx context.Context) map[string]string {
	// Start with W3C Baggage (custom key-value pairs)
	result := GetBaggage(ctx)
	if result == nil {
		result = make(map[string]string)
	}

	// Add OpenTelemetry trace context for log correlation
	// This enables logs to include trace_id and span_id automatically
	tc := GetTraceContext(ctx)
	if tc.TraceID != "" {
		result["trace_id"] = tc.TraceID
		result["span_id"] = tc.SpanID
	}

	return result
}

// Gauge implements core.MetricsRegistry
func (f *FrameworkMetricsRegistry) Gauge(name string, value float64, labels ...string) {
	if f.logger != nil && f.logger.debug {
		f.logger.Debug("Framework gauge emission", map[string]interface{}{
			"metric_name": name,
			"type":        "gauge",
			"value":       value,
			"label_count": len(labels) / 2,
			"source":      "framework",
		})
	}

	// Use telemetry's Gauge function
	Gauge(name, value, labels...)
}

// Histogram implements core.MetricsRegistry
func (f *FrameworkMetricsRegistry) Histogram(name string, value float64, labels ...string) {
	if f.logger != nil && f.logger.debug {
		f.logger.Debug("Framework histogram emission", map[string]interface{}{
			"metric_name": name,
			"type":        "histogram",
			"value":       value,
			"label_count": len(labels) / 2,
			"source":      "framework",
		})
	}

	// Use telemetry's Histogram function
	Histogram(name, value, labels...)
}

// EnableFrameworkIntegration registers the telemetry module with core
// This must be called after telemetry initialization to enable framework-wide metrics
func EnableFrameworkIntegration(logger *TelemetryLogger) {
	registry := NewFrameworkMetricsRegistry(logger)

	// Register with core to enable framework-wide metrics
	core.SetMetricsRegistry(registry)

	if logger != nil {
		logger.Info("Framework integration enabled", map[string]interface{}{
			"integration": "core.MetricsRegistry",
			"impact":      "All framework components can now emit metrics",
			"methods":     []string{"Counter", "EmitWithContext", "GetBaggage", "Gauge", "Histogram"},
		})
	}
}
