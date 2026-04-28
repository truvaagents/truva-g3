package resilience

import "github.com/truvaagents/truva-g3/telemetry"

func init() {
	// ONLY declare metrics, don't initialize
	telemetry.DeclareMetrics("circuit_breaker", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:   "circuit_breaker.calls",
				Type:   "counter",
				Help:   "Total circuit breaker calls",
				Labels: []string{"name", "state"},
			},
			{
				Name:    "circuit_breaker.duration_ms",
				Type:    "histogram",
				Help:    "Circuit breaker call duration in milliseconds",
				Labels:  []string{"name", "status"},
				Unit:    "ms",
				Buckets: []float64{0.1, 1, 10, 100, 1000},
			},
			{
				Name:   "circuit_breaker.failures",
				Type:   "counter",
				Help:   "Circuit breaker failures",
				Labels: []string{"name", "error_type"},
			},
			{
				Name:   "circuit_breaker.state_changes",
				Type:   "counter",
				Help:   "Circuit breaker state transitions",
				Labels: []string{"name", "from_state", "to_state"},
			},
			{
				Name:   "circuit_breaker.current_state",
				Type:   "gauge",
				Help:   "Current circuit breaker state (0=closed, 0.5=half-open, 1=open)",
				Labels: []string{"name"},
			},
			{
				Name:   "circuit_breaker.rejected",
				Type:   "counter",
				Help:   "Requests rejected by open circuit",
				Labels: []string{"name"},
			},
		},
	})

	telemetry.DeclareMetrics("retry", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:   "retry.attempts",
				Type:   "counter",
				Help:   "Total retry attempts",
				Labels: []string{"operation", "attempt_number"},
			},
			{
				Name:   "retry.success",
				Type:   "counter",
				Help:   "Successful operations after retry",
				Labels: []string{"operation", "final_attempt"},
			},
			{
				Name:   "retry.failures",
				Type:   "counter",
				Help:   "Failed operations after all retries",
				Labels: []string{"operation", "error_type"},
			},
			{
				Name:    "retry.duration_ms",
				Type:    "histogram",
				Help:    "Total duration including all retry attempts",
				Labels:  []string{"operation", "status"},
				Unit:    "ms",
				Buckets: []float64{1, 10, 100, 1000, 10000},
			},
			{
				Name:    "retry.backoff_ms",
				Type:    "histogram",
				Help:    "Backoff duration between retries",
				Labels:  []string{"operation", "strategy"},
				Unit:    "ms",
				Buckets: []float64{10, 50, 100, 500, 1000, 5000},
			},
		},
	})
}
