package telemetry

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

// OTelProvider implements core.Telemetry with OpenTelemetry.
// This is the main integration point between TruvaG3 and OpenTelemetry.
// It manages both tracing and metrics, exporting them via OTLP/HTTP.
//
// Design decisions:
//   - Uses HTTP instead of gRPC for smaller binary size
//   - Batches exports to reduce network overhead
//   - Provides both traces and metrics from a single provider
type OTelProvider struct {
	tracer         trace.Tracer             // For distributed tracing
	meter          metric.Meter             // For metrics
	traceProvider  *sdktrace.TracerProvider // Manages trace export
	metricProvider *sdkmetric.MeterProvider // Manages metric export
	metrics        *MetricInstruments       // Cached metric instruments
	shutdownOnce   sync.Once                // Ensures shutdown happens only once
	shutdown       bool                     // Tracks if provider is shutdown
	mu             sync.RWMutex             // Protects shutdown flag
}

// NewOTelProvider creates a new OpenTelemetry provider using HTTP exporters.
// This sets up the complete telemetry pipeline:
//  1. Creates HTTP exporters for traces and metrics
//  2. Configures batching for efficient export
//  3. Sets up global providers for SDK access
//
// The endpoint should be an OTLP/HTTP endpoint (typically port 4318).
// For backward compatibility, gRPC ports (4317) are automatically converted.
// The serviceType parameter should be "tool" or "agent" to enable dashboard segregation.
func NewOTelProvider(serviceName, serviceType, endpoint string) (*OTelProvider, error) {
	logger := GetLogger()
	startTime := time.Now()

	// Validate service name
	if serviceName == "" {
		logger.Error("Service name is required for telemetry provider", map[string]interface{}{
			"action": "Provide a non-empty service name to identify this service",
			"impact": "Telemetry will not be properly attributed",
		})
		return nil, fmt.Errorf("service name cannot be empty")
	}

	// Default serviceType to empty if not provided (backward compatibility)
	// Can also be set via TRUVAG3_SERVICE_TYPE environment variable
	if serviceType == "" {
		serviceType = os.Getenv("TRUVAG3_SERVICE_TYPE")
	}

	logger.Info("Creating OpenTelemetry provider", map[string]interface{}{
		"service_name": serviceName,
		"service_type": serviceType,
		"endpoint":     endpoint,
		"protocol":     "OTLP/HTTP",
	})

	// Normalize endpoint - support both old gRPC and new HTTP formats
	if endpoint == "" {
		endpoint = "localhost:4318" // Default HTTP endpoint
	}
	// Auto-convert gRPC port to HTTP for backward compatibility
	if endpoint == "localhost:4317" {
		endpoint = "localhost:4318"
	}
	// Strip http:// or https:// prefix if present
	// OTLP exporters expect just host:port, not a full URL
	if len(endpoint) > 7 && endpoint[:7] == "http://" {
		endpoint = endpoint[7:]
	} else if len(endpoint) > 8 && endpoint[:8] == "https://" {
		endpoint = endpoint[8:]
	}

	// Create resource with consistent schema
	logger.Debug("Creating OpenTelemetry resource", map[string]interface{}{
		"service_name": serviceName,
		"service_type": serviceType,
		"version":      "1.0.0",
		"schema_url":   semconv.SchemaURL,
	})

	// Build resource attributes
	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String("1.0.0"),
	}
	// Add service.type if provided (enables tool vs agent segregation in dashboards)
	if serviceType != "" {
		attrs = append(attrs, attribute.String("service.type", serviceType))
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		attrs...,
	)

	ctx := context.Background()

	// Create HTTP trace exporter (instead of gRPC)
	logger.Debug("Creating OTLP/HTTP trace exporter", map[string]interface{}{
		"endpoint": endpoint,
		"insecure": true,
		"path":     "/v1/traces",
	})

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(), // For development; use TLS in production
	)
	if err != nil {
		logger.Error("Failed to create trace exporter", map[string]interface{}{
			"error":    err.Error(),
			"endpoint": endpoint,
			"action":   "Verify OTEL collector is running and accessible",
			"command":  fmt.Sprintf("curl -v http://%s/v1/traces", endpoint),
			"impact":   "No traces will be exported",
		})
		return nil, fmt.Errorf("failed to create trace exporter for endpoint %s: %w", endpoint, err)
	}

	logger.Debug("Trace exporter created successfully", nil)

	// Create HTTP metric exporter (this was missing!)
	logger.Debug("Creating OTLP/HTTP metric exporter", map[string]interface{}{
		"endpoint": endpoint,
		"insecure": true,
		"path":     "/v1/metrics",
	})

	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(), // For development; use TLS in production
	)
	if err != nil {
		// Clean up trace exporter before returning
		if shutdownErr := traceExporter.Shutdown(ctx); shutdownErr != nil {
			logger.Debug("Failed to cleanup trace exporter after metric exporter failure", map[string]interface{}{
				"error": shutdownErr.Error(),
			})
		}

		logger.Error("Failed to create metric exporter", map[string]interface{}{
			"error":    err.Error(),
			"endpoint": endpoint,
			"action":   "Verify OTEL collector is running and accessible",
			"command":  fmt.Sprintf("curl -v http://%s/v1/metrics", endpoint),
			"impact":   "No metrics will be exported",
		})
		return nil, fmt.Errorf("failed to create metric exporter for endpoint %s: %w", endpoint, err)
	}

	logger.Debug("Metric exporter created successfully", nil)

	// Create trace provider
	logger.Debug("Creating trace provider with batching", map[string]interface{}{
		"batch_processor": "default configuration",
		"note":            "Using SDK defaults for batch timeout, size, and queue",
	})

	// AlwaysSample: capture 100% of spans in all environments.
	// Full trace visibility is required for alert investigation, HITL resume,
	// and cross-service debugging.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Create metric provider with periodic reader (exports metrics every 30s)
	logger.Debug("Creating metric provider with periodic reader", map[string]interface{}{
		"export_interval": "30s",
		"export_timeout":  "default",
	})

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				metricExporter,
				sdkmetric.WithInterval(30*time.Second),
			),
		),
		sdkmetric.WithResource(res),
	)

	// Set global providers
	logger.Debug("Setting global OpenTelemetry providers", map[string]interface{}{
		"trace_provider":  "configured",
		"metric_provider": "configured",
		"propagator":      "TraceContext + Baggage (W3C)",
	})

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// Use composite propagator for both trace context and baggage propagation
	// TraceContext: W3C traceparent/tracestate headers for distributed tracing
	// Baggage: W3C baggage header for user-defined context propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create metric instruments
	logger.Debug("Initializing metric instruments", map[string]interface{}{
		"meter_name": "truvag3-telemetry",
	})

	provider := &OTelProvider{
		tracer:         tp.Tracer("truvag3-telemetry"),
		meter:          mp.Meter("truvag3-telemetry"),
		traceProvider:  tp,
		metricProvider: mp,
		metrics:        NewMetricInstruments("truvag3-telemetry"),
	}

	logger.Info("OpenTelemetry provider created successfully", map[string]interface{}{
		"service_name":      serviceName,
		"endpoint":          endpoint,
		"initialization_ms": time.Since(startTime).Milliseconds(),
		"components": map[string]string{
			"trace_exporter":  "OTLP/HTTP",
			"metric_exporter": "OTLP/HTTP",
			"trace_provider":  "BatchSpanProcessor",
			"metric_provider": "PeriodicReader",
		},
	})

	return provider, nil
}

// StartSpan starts a new telemetry span
func (o *OTelProvider) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	// Check if provider is shutdown
	o.mu.RLock()
	if o.shutdown {
		o.mu.RUnlock()
		// Return a no-op span if shutdown
		return ctx, &noOpSpan{}
	}
	o.mu.RUnlock()

	// Check for nil tracer (defensive programming)
	if o.tracer == nil {
		return ctx, &noOpSpan{}
	}

	ctx, span := o.tracer.Start(ctx, name)
	return ctx, &otelSpan{span: span}
}

// RecordMetric records a metric - implements core.Telemetry interface.
// This function intelligently routes metrics to the appropriate instrument type
// based on the metric name pattern. This provides a simple API while maintaining
// semantic correctness for different metric types.
//
// Heuristics used:
//   - Names with "duration", "latency", "time" → Histogram
//   - Names with "count", "total", "errors" → Counter
//   - Names with "gauge", "current", "size" → Gauge/Histogram
func (o *OTelProvider) RecordMetric(name string, value float64, labels map[string]string) {
	// Check if provider is shutdown
	o.mu.RLock()
	if o.shutdown {
		o.mu.RUnlock()
		return // Silent no-op if shutdown
	}
	o.mu.RUnlock()

	// Check for nil metrics (defensive programming)
	if o.metrics == nil {
		return // Silent no-op if metrics not initialized
	}

	ctx := context.Background()

	// Convert label map to OpenTelemetry attributes
	// This allocates but is necessary for the OTel API
	var attrs []attribute.KeyValue
	for k, v := range labels {
		attrs = append(attrs, attribute.String(k, v))
	}

	// Determine metric type based on name patterns
	// This is a simplified approach - in production you'd want explicit metric type registration
	switch {
	case contains(name, "duration", "latency", "time"):
		// Record as histogram for timing metrics
		_ = o.metrics.RecordHistogram(ctx, name, value, metric.WithAttributes(attrs...))
	case contains(name, "count", "total", "errors", "success"):
		// Record as counter for cumulative metrics
		_ = o.metrics.RecordCounter(ctx, name, int64(value), metric.WithAttributes(attrs...))
	case contains(name, "gauge", "current", "size", "queue"):
		// For gauges, we'd need to register a callback - for now record as histogram
		_ = o.metrics.RecordHistogram(ctx, name, value, metric.WithAttributes(attrs...))
	default:
		// Default to histogram for unknown metric types
		_ = o.metrics.RecordHistogram(ctx, name, value, metric.WithAttributes(attrs...))
	}
}

// contains checks if the metric name contains any of the given substrings.
// Used for heuristic metric type detection based on naming patterns.
// Checks both prefix and suffix to handle common naming conventions:
//   - "request_count" (suffix)
//   - "duration_ms" (suffix)
//   - "total_requests" (prefix)
func contains(name string, substrings ...string) bool {
	for _, substr := range substrings {
		if len(name) >= len(substr) &&
			(name[len(name)-len(substr):] == substr || // Check suffix
				name[:len(substr)] == substr) { // Check prefix
			return true
		}
	}
	return false
}

// Shutdown gracefully shuts down the telemetry provider
// This method is idempotent and thread-safe - it can be called multiple times safely
func (o *OTelProvider) Shutdown(ctx context.Context) (shutdownErr error) {
	logger := GetLogger()
	startTime := time.Now()

	// Use sync.Once to ensure shutdown happens only once
	o.shutdownOnce.Do(func() {
		// Mark as shutdown immediately to stop new operations
		o.mu.Lock()
		o.shutdown = true
		o.mu.Unlock()

		shutdownErr = o.doShutdown(ctx, logger, startTime)
	})

	return shutdownErr
}

// doShutdown performs the actual shutdown operations
// This is separated to work with sync.Once pattern
func (o *OTelProvider) doShutdown(ctx context.Context, logger *TelemetryLogger, startTime time.Time) error {
	// Extract deadline for logging if available
	var timeoutStr string
	if deadline, ok := ctx.Deadline(); ok {
		timeoutStr = time.Until(deadline).String()
	} else {
		timeoutStr = "no deadline"
	}

	logger.Info("Shutting down OpenTelemetry provider", map[string]interface{}{
		"timeout": timeoutStr,
	})

	var errs []error

	// Shutdown metrics instruments
	logger.Debug("Shutting down metric instruments", nil)
	if err := o.metrics.Shutdown(); err != nil {
		logger.Error("Failed to shutdown metric instruments", map[string]interface{}{
			"error":  err.Error(),
			"impact": "Some metric registrations may leak",
		})
		errs = append(errs, fmt.Errorf("failed to shutdown metrics: %w", err))
	} else {
		logger.Debug("Metric instruments shut down successfully", nil)
	}

	// Shutdown metric provider (flushes pending metrics)
	if o.metricProvider != nil {
		logger.Info("Flushing and shutting down metric provider", map[string]interface{}{
			"action": "Exporting any pending metrics",
		})
		if err := o.metricProvider.Shutdown(ctx); err != nil {
			logger.Error("Failed to shutdown metric provider", map[string]interface{}{
				"error":  err.Error(),
				"impact": "Some metrics may not have been exported",
			})
			errs = append(errs, fmt.Errorf("failed to shutdown metric provider: %w", err))
		} else {
			logger.Info("Metric provider shut down successfully", map[string]interface{}{
				"final_export": "completed",
			})
		}
	}

	// Shutdown trace provider
	if o.traceProvider != nil {
		logger.Info("Flushing and shutting down trace provider", map[string]interface{}{
			"action": "Exporting any pending traces",
		})
		if err := o.traceProvider.Shutdown(ctx); err != nil {
			logger.Error("Failed to shutdown trace provider", map[string]interface{}{
				"error":  err.Error(),
				"impact": "Some traces may not have been exported",
			})
			errs = append(errs, fmt.Errorf("failed to shutdown trace provider: %w", err))
		} else {
			logger.Info("Trace provider shut down successfully", map[string]interface{}{
				"final_export": "completed",
			})
		}
	}

	if len(errs) > 0 {
		logger.Error("OpenTelemetry provider shutdown completed with errors", map[string]interface{}{
			"error_count": len(errs),
			"errors":      fmt.Sprintf("%v", errs),
			"shutdown_ms": time.Since(startTime).Milliseconds(),
		})
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	logger.Info("OpenTelemetry provider shut down successfully", map[string]interface{}{
		"shutdown_ms": time.Since(startTime).Milliseconds(),
		"components_shutdown": []string{
			"metric_instruments",
			"metric_provider",
			"trace_provider",
		},
	})

	return nil
}

// noOpSpan implements core.Span with no-op operations
// Used when provider is shutdown or not properly initialized
type noOpSpan struct{}

func (s *noOpSpan) End()                                       {}
func (s *noOpSpan) SetAttribute(key string, value interface{}) {}
func (s *noOpSpan) RecordError(err error)                      {}

// otelSpan wraps an OpenTelemetry span to implement core.Span
type otelSpan struct {
	span trace.Span
}

func (s *otelSpan) End() {
	s.span.End()
}

func (s *otelSpan) SetAttribute(key string, value interface{}) {
	switch v := value.(type) {
	case string:
		s.span.SetAttributes(attribute.String(key, v))
	case int:
		s.span.SetAttributes(attribute.Int(key, v))
	case int64:
		s.span.SetAttributes(attribute.Int64(key, v))
	case float64:
		s.span.SetAttributes(attribute.Float64(key, v))
	case bool:
		s.span.SetAttributes(attribute.Bool(key, v))
	default:
		s.span.SetAttributes(attribute.String(key, fmt.Sprintf("%v", v)))
	}
}

// RecordError records the error as an exception event AND marks the span as
// failed (otel.status_code=ERROR). Without the SetStatus call the span shows
// the exception but Jaeger's "errors only" filter won't match it. Callers that
// only want to attach diagnostic info (without marking the span failed) should
// use AddSpanEvent with an exception event instead.
func (s *otelSpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.span.RecordError(err)
	s.span.SetStatus(codes.Error, err.Error())
}

// EnableTelemetry adds telemetry to a core BaseAgent
// This is how you upgrade a basic tool with telemetry
func EnableTelemetry(agent *core.BaseAgent, endpoint string) error {
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if endpoint == "" {
			endpoint = "localhost:4318" // Default HTTP port
		}
	}

	// Automatically infer service type from component
	serviceType := string(agent.GetType())

	provider, err := NewOTelProvider(agent.GetName(), serviceType, endpoint)
	if err != nil {
		return fmt.Errorf("failed to create telemetry provider: %w", err)
	}

	agent.Telemetry = provider

	agent.Logger.Info("Telemetry enabled", map[string]interface{}{
		"endpoint": endpoint,
		"agent":    agent.GetName(),
	})

	return nil
}
