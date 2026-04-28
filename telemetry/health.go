package telemetry

import (
	"encoding/json"
	"net/http"
	"time"
)

// Health represents the health status of the telemetry system
type Health struct {
	Enabled         bool   `json:"enabled"`
	Provider        string `json:"provider"`
	MetricsEmitted  int64  `json:"metrics_emitted"`
	MetricsDropped  int64  `json:"metrics_dropped"`
	Errors          int64  `json:"errors"`
	LastError       string `json:"last_error,omitempty"`
	CircuitState    string `json:"circuit_state"`
	Uptime          string `json:"uptime"`
	CardinalityUsed int    `json:"cardinality_used"`
	CardinalityMax  int    `json:"cardinality_max"`
	Initialized     bool   `json:"initialized"`
}

// GetHealth returns the current health status of the telemetry system
func GetHealth() Health {
	registry := globalRegistry.Load()
	if registry == nil {
		return Health{
			Enabled:     false,
			Initialized: false,
		}
	}

	r, ok := registry.(*Registry)
	if !ok || r == nil {
		return Health{
			Enabled:     false,
			Initialized: false,
		}
	}

	// Get last error
	lastErr := ""
	if errVal := r.lastError.Load(); errVal != nil {
		if errStr, ok := errVal.(string); ok && errStr != "" {
			lastErr = errStr
		}
	}

	// Get circuit breaker state
	circuitState := "disabled"
	if r.circuit != nil {
		circuitState = r.circuit.State()
	}

	// Get cardinality info
	cardinalityUsed := 0
	cardinalityMax := 0
	if r.limiter != nil {
		cardinalityUsed = r.limiter.CurrentCardinality()
		cardinalityMax = r.limiter.MaxCardinality()
	}

	return Health{
		Enabled:         r.config.Enabled,
		Provider:        "otel", // Could be dynamic based on provider type
		MetricsEmitted:  r.emitted.Load(),
		MetricsDropped:  telemetryDropped.Load(),
		Errors:          telemetryErrors.Load(),
		LastError:       lastErr,
		CircuitState:    circuitState,
		Uptime:          time.Since(r.startTime).String(),
		CardinalityUsed: cardinalityUsed,
		CardinalityMax:  cardinalityMax,
		Initialized:     true,
	}
}

// HealthHandler provides an HTTP endpoint for telemetry health
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	health := GetHealth()
	w.Header().Set("Content-Type", "application/json")

	// Set appropriate status code
	if !health.Enabled || !health.Initialized {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else if health.CircuitState == "open" {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else if health.Errors > 0 && health.MetricsEmitted == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else if float64(health.Errors)/float64(health.MetricsEmitted+1) > 0.1 {
		// More than 10% error rate
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	_ = json.NewEncoder(w).Encode(health)
}

// InternalMetrics returns internal telemetry metrics for monitoring
type InternalMetrics struct {
	Errors  int64 `json:"errors"`
	Dropped int64 `json:"dropped"`
	Emitted int64 `json:"emitted"`
}

// GetInternalMetrics returns internal telemetry metrics
func GetInternalMetrics() InternalMetrics {
	registry := globalRegistry.Load()
	emitted := int64(0)
	if registry != nil {
		r := registry.(*Registry)
		emitted = r.emitted.Load()
	}

	return InternalMetrics{
		Errors:  telemetryErrors.Load(),
		Dropped: telemetryDropped.Load(),
		Emitted: emitted,
	}
}

// ResetInternalMetrics resets internal metrics (useful for testing)
func ResetInternalMetrics() {
	telemetryErrors.Store(0)
	telemetryDropped.Store(0)

	registry := globalRegistry.Load()
	if registry != nil {
		r := registry.(*Registry)
		r.emitted.Store(0)
	}
}
