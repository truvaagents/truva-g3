package telemetry

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricInstruments holds cached metric instruments for efficient recording
type MetricInstruments struct {
	meter          metric.Meter
	counters       map[string]metric.Int64Counter
	floatCounters  map[string]metric.Float64Counter
	upDownCounters map[string]metric.Int64UpDownCounter
	histograms     map[string]metric.Float64Histogram
	gauges         map[string]gaugeCallback
	mu             sync.RWMutex
}

// gaugeCallback holds gauge registration info
type gaugeCallback struct {
	registration metric.Registration
	callback     metric.Callback
	gauge        metric.Float64ObservableGauge
}

// NewMetricInstruments creates a new metrics instrument cache
func NewMetricInstruments(meterName string) *MetricInstruments {
	return &MetricInstruments{
		meter:          otel.Meter(meterName),
		counters:       make(map[string]metric.Int64Counter),
		floatCounters:  make(map[string]metric.Float64Counter),
		upDownCounters: make(map[string]metric.Int64UpDownCounter),
		histograms:     make(map[string]metric.Float64Histogram),
		gauges:         make(map[string]gaugeCallback),
	}
}

// RecordCounter increments a counter metric
func (m *MetricInstruments) RecordCounter(ctx context.Context, name string, value int64, opts ...metric.AddOption) error {
	m.mu.RLock()
	counter, exists := m.counters[name]
	m.mu.RUnlock()

	if !exists {
		m.mu.Lock()
		// Double-check after acquiring write lock
		if counter, exists = m.counters[name]; !exists {
			var err error
			counter, err = m.meter.Int64Counter(name)
			if err != nil {
				m.mu.Unlock()
				return fmt.Errorf("failed to create counter %s: %w", name, err)
			}
			m.counters[name] = counter
		}
		m.mu.Unlock()
	}

	counter.Add(ctx, value, opts...)
	return nil
}

// RecordFloatCounter increments a float counter metric (for costs, rates, etc.)
func (m *MetricInstruments) RecordFloatCounter(ctx context.Context, name string, value float64, opts ...metric.AddOption) error {
	m.mu.RLock()
	counter, exists := m.floatCounters[name]
	m.mu.RUnlock()

	if !exists {
		m.mu.Lock()
		if counter, exists = m.floatCounters[name]; !exists {
			var err error
			counter, err = m.meter.Float64Counter(name)
			if err != nil {
				m.mu.Unlock()
				return fmt.Errorf("failed to create float counter %s: %w", name, err)
			}
			m.floatCounters[name] = counter
		}
		m.mu.Unlock()
	}

	counter.Add(ctx, value, opts...)
	return nil
}

// RecordUpDownCounter records a value that can go up or down (like queue size)
func (m *MetricInstruments) RecordUpDownCounter(ctx context.Context, name string, value int64, opts ...metric.AddOption) error {
	m.mu.RLock()
	counter, exists := m.upDownCounters[name]
	m.mu.RUnlock()

	if !exists {
		m.mu.Lock()
		if counter, exists = m.upDownCounters[name]; !exists {
			var err error
			counter, err = m.meter.Int64UpDownCounter(name)
			if err != nil {
				m.mu.Unlock()
				return fmt.Errorf("failed to create up-down counter %s: %w", name, err)
			}
			m.upDownCounters[name] = counter
		}
		m.mu.Unlock()
	}

	counter.Add(ctx, value, opts...)
	return nil
}

// RecordHistogram records a value distribution (like latencies)
func (m *MetricInstruments) RecordHistogram(ctx context.Context, name string, value float64, opts ...metric.RecordOption) error {
	m.mu.RLock()
	histogram, exists := m.histograms[name]
	m.mu.RUnlock()

	if !exists {
		m.mu.Lock()
		if histogram, exists = m.histograms[name]; !exists {
			var err error
			histogram, err = m.meter.Float64Histogram(name)
			if err != nil {
				m.mu.Unlock()
				return fmt.Errorf("failed to create histogram %s: %w", name, err)
			}
			m.histograms[name] = histogram
		}
		m.mu.Unlock()
	}

	histogram.Record(ctx, value, opts...)
	return nil
}

// RegisterGauge registers an observable gauge with a callback
func (m *MetricInstruments) RegisterGauge(name string, callback metric.Callback, opts ...metric.Float64ObservableGaugeOption) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.gauges[name]; exists {
		return fmt.Errorf("gauge %s already registered", name)
	}

	gauge, err := m.meter.Float64ObservableGauge(name, opts...)
	if err != nil {
		return fmt.Errorf("failed to create gauge %s: %w", name, err)
	}

	registration, err := m.meter.RegisterCallback(callback, gauge)
	if err != nil {
		return fmt.Errorf("failed to register callback for gauge %s: %w", name, err)
	}

	m.gauges[name] = gaugeCallback{
		registration: registration,
		callback:     callback,
		gauge:        gauge,
	}

	return nil
}

// UnregisterGauge unregisters a gauge callback
func (m *MetricInstruments) UnregisterGauge(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	gauge, exists := m.gauges[name]
	if !exists {
		return fmt.Errorf("gauge %s not found", name)
	}

	if err := gauge.registration.Unregister(); err != nil {
		return fmt.Errorf("failed to unregister gauge %s: %w", name, err)
	}

	delete(m.gauges, name)
	return nil
}

// Shutdown unregisters all gauge callbacks
func (m *MetricInstruments) Shutdown() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, gauge := range m.gauges {
		if err := gauge.registration.Unregister(); err != nil {
			errs = append(errs, fmt.Errorf("failed to unregister gauge %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}

	return nil
}

// Helper functions for common metric patterns

// RecordDuration records a duration in milliseconds as a histogram
func (m *MetricInstruments) RecordDuration(ctx context.Context, name string, milliseconds float64, opts ...metric.RecordOption) error {
	return m.RecordHistogram(ctx, name, milliseconds, opts...)
}

// RecordBytesTransferred records bytes as a counter
func (m *MetricInstruments) RecordBytesTransferred(ctx context.Context, name string, bytes int64, opts ...metric.AddOption) error {
	return m.RecordCounter(ctx, name, bytes, opts...)
}

// RecordError increments an error counter with error type
func (m *MetricInstruments) RecordError(ctx context.Context, name string, errorType string) error {
	return m.RecordCounter(ctx, name, 1,
		metric.WithAttributes(attribute.String("error.type", errorType)))
}

// RecordSuccess increments a success counter
func (m *MetricInstruments) RecordSuccess(ctx context.Context, name string) error {
	return m.RecordCounter(ctx, name, 1,
		metric.WithAttributes(attribute.String("status", "success")))
}

// Agent-specific metric constants
const (
	// Agent lifecycle metrics
	MetricAgentStartup      = "agent.startup.duration"
	MetricAgentUptime       = "agent.uptime"
	MetricAgentHealth       = "agent.health"
	MetricAgentCapabilities = "agent.capabilities.count"
	MetricAgentShutdown     = "agent.shutdown.duration"

	// Capability metrics
	MetricCapabilityExecutions = "agent.capability.executions"
	MetricCapabilityDuration   = "agent.capability.duration"
	MetricCapabilityErrors     = "agent.capability.errors"

	// Discovery metrics
	MetricDiscoveryRegistrations = "agent.discovery.registrations"
	MetricDiscoveryLookups       = "agent.discovery.lookups"
	MetricDiscoveryHealth        = "agent.discovery.health_checks"

	// Memory/State metrics
	MetricMemoryOperations = "agent.memory.operations"
	MetricMemorySize       = "agent.memory.size"
	MetricMemoryEvictions  = "agent.memory.evictions"

	// AI/LLM metrics
	MetricAIPromptTokens     = "agent.ai.prompt_tokens"
	MetricAICompletionTokens = "agent.ai.completion_tokens"
	MetricAIRequestDuration  = "agent.ai.request_duration"
	MetricAIRequestCost      = "agent.ai.request_cost"
	MetricAIRateLimitHits    = "agent.ai.rate_limit_hits"

	// Circuit breaker metrics
	MetricCircuitBreakerSuccess  = "agent.circuit_breaker.success"
	MetricCircuitBreakerFailure  = "agent.circuit_breaker.failure"
	MetricCircuitBreakerOpen     = "agent.circuit_breaker.open"
	MetricCircuitBreakerRejected = "agent.circuit_breaker.rejected"

	// Workflow metrics
	MetricWorkflowExecutions  = "agent.workflow.executions"
	MetricWorkflowDuration    = "agent.workflow.duration"
	MetricWorkflowStepSuccess = "agent.workflow.step.success"
	MetricWorkflowStepFailure = "agent.workflow.step.failure"
)
