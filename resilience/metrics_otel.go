package resilience

import (
	"context"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OTelMetricsCollector implements MetricsCollector using OpenTelemetry
type OTelMetricsCollector struct {
	metrics *telemetry.MetricInstruments
	ctx     context.Context
}

// NewOTelMetricsCollector creates a new OpenTelemetry metrics collector
func NewOTelMetricsCollector(ctx context.Context) *OTelMetricsCollector {
	return &OTelMetricsCollector{
		metrics: telemetry.NewMetricInstruments("truvag3-resilience"),
		ctx:     ctx,
	}
}

// RecordSuccess records a successful circuit breaker execution
func (o *OTelMetricsCollector) RecordSuccess(name string) {
	_ = o.metrics.RecordCounter(o.ctx, telemetry.MetricCircuitBreakerSuccess, 1,
		metric.WithAttributes(
			attribute.String("circuit_breaker", name),
			attribute.String("result", "success"),
		))
}

// RecordFailure records a failed circuit breaker execution
func (o *OTelMetricsCollector) RecordFailure(name string, errorType string) {
	_ = o.metrics.RecordCounter(o.ctx, telemetry.MetricCircuitBreakerFailure, 1,
		metric.WithAttributes(
			attribute.String("circuit_breaker", name),
			attribute.String("error_type", errorType),
			attribute.String("result", "failure"),
		))
}

// RecordStateChange records a circuit breaker state transition
func (o *OTelMetricsCollector) RecordStateChange(name string, from, to string) {
	// Record the state change as an event/counter
	_ = o.metrics.RecordCounter(o.ctx, "circuit_breaker.state_change", 1,
		metric.WithAttributes(
			attribute.String("circuit_breaker", name),
			attribute.String("from_state", from),
			attribute.String("to_state", to),
		))

	// Also update a gauge for current state
	// This would need a callback registration in practice
	stateValue := 0.0
	switch to {
	case "closed":
		stateValue = 0.0
	case "open":
		stateValue = 1.0
	case "half_open":
		stateValue = 0.5
	}

	_ = o.metrics.RecordHistogram(o.ctx, "circuit_breaker.state", stateValue,
		metric.WithAttributes(
			attribute.String("circuit_breaker", name),
			attribute.String("state", to),
		))
}

// RecordRejection records when circuit breaker rejects a request
func (o *OTelMetricsCollector) RecordRejection(name string) {
	_ = o.metrics.RecordCounter(o.ctx, telemetry.MetricCircuitBreakerRejected, 1,
		metric.WithAttributes(
			attribute.String("circuit_breaker", name),
			attribute.String("result", "rejected"),
		))
}

// RegisterStateGauge registers an observable gauge for circuit breaker state
func (o *OTelMetricsCollector) RegisterStateGauge(name string, stateFunc func() string) error {
	return o.metrics.RegisterGauge(
		"circuit_breaker.current_state",
		func(ctx context.Context, observer metric.Observer) error {
			state := stateFunc()
			stateValue := 0.0
			switch state {
			case "closed":
				stateValue = 0.0
			case "open":
				stateValue = 1.0
			case "half_open":
				stateValue = 0.5
			}

			observer.(metric.Float64Observer).Observe(stateValue,
				metric.WithAttributes(
					attribute.String("circuit_breaker", name),
					attribute.String("state", state),
				))
			return nil
		},
		metric.WithDescription("Current state of the circuit breaker (0=closed, 0.5=half_open, 1=open)"),
	)
}

// Shutdown cleans up the metrics collector
func (o *OTelMetricsCollector) Shutdown() error {
	return o.metrics.Shutdown()
}
