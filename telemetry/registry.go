package telemetry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

var (
	// globalRegistry holds the singleton Registry instance.
	// We use atomic.Value for lock-free reads on the hot path (metric emission).
	// This is only written once during Initialize() and read many times during Emit().
	globalRegistry atomic.Value // *Registry

	// initOnce ensures Initialize() can only succeed once.
	// Multiple calls to Initialize() will return the same result.
	initOnce sync.Once

	// declaredMetrics stores metric declarations from init() functions.
	// This allows packages to declare their metrics before the telemetry
	// system is initialized, solving the init() ordering problem.
	// sync.Map is used for concurrent writes during init().
	declaredMetrics sync.Map // map[string]ModuleConfig

	// Internal health metrics tracked atomically for thread-safety
	telemetryErrors  atomic.Int64 // Total errors encountered
	telemetryDropped atomic.Int64 // Metrics dropped due to limits
)

// ModuleConfig represents metric configuration for a module
// This is used when declaring metrics for a specific module/agent
type ModuleConfig struct {
	Metrics []MetricDefinition
}

// MetricDefinition defines a metric's metadata
// Use this to declare metrics upfront for better validation
type MetricDefinition struct {
	Name    string
	Type    string // counter, histogram, gauge, updowncounter
	Help    string
	Labels  []string
	Unit    string    // optional: milliseconds, bytes, etc.
	Buckets []float64 // optional: for histograms
}

// Registry manages all telemetry components.
// It coordinates between the various subsystems (metrics, circuit breaker, cardinality limiter)
// and provides a unified interface for metric emission.
// All fields that may be accessed concurrently use atomic operations or mutex protection.
type Registry struct {
	config   Config
	provider *OTelProvider            // OpenTelemetry provider for metric export
	limiter  *CardinalityLimiter      // Prevents metric explosion
	circuit  *TelemetryCircuitBreaker // Protects backend from overload
	metrics  *MetricInstruments       // Pre-registered metric instruments
	logger   *TelemetryLogger         // Self-contained logger for telemetry operations

	// Internal metrics for observability of the telemetry system itself
	emitted   atomic.Int64 // Total metrics successfully emitted
	startTime time.Time    // When the registry was created
	lastError atomic.Value // string - Last error message for diagnostics

	// errorLimiter prevents error logging from overwhelming the system
	// (e.g., if the backend is down, we don't want to spam error logs)
	errorLimiter *RateLimiter
}

// DeclareMetrics registers metric definitions for a module.
// This function is safe to call from init() functions before Initialize() is called.
// The declarations are stored and processed when Initialize() is called.
// This solves the init() ordering problem where packages need to declare metrics
// before the telemetry system is initialized.
//
// Example:
//
//	func init() {
//	    telemetry.DeclareMetrics("my-module", telemetry.ModuleConfig{
//	        Metrics: []telemetry.MetricDefinition{
//	            {Name: "requests.total", Type: "counter"},
//	        },
//	    })
//	}
func DeclareMetrics(module string, config ModuleConfig) {
	declaredMetrics.Store(module, config)
}

// Initialize activates the telemetry system with the given configuration.
// This function must be called once from main() before any metrics are emitted.
// It is safe to call multiple times - only the first call will take effect.
//
// Initialize performs the following:
//  1. Creates the OpenTelemetry provider and exporters
//  2. Sets up the circuit breaker (if configured)
//  3. Initializes the cardinality limiter
//  4. Processes all previously declared metrics
//  5. Stores the registry globally for use by Emit functions
//
// Returns an error if initialization fails (e.g., can't create exporter).
// Even if initialization fails, the Emit functions will still work
// (they'll just discard metrics), so the application won't crash.
func Initialize(config Config) error {
	var initErr error
	initOnce.Do(func() {
		// Create logger immediately for initialization visibility
		logger := NewTelemetryLogger(config.ServiceName)

		// Log initialization start
		logger.Info("Telemetry initialization starting", map[string]interface{}{
			"service_name":      config.ServiceName,
			"endpoint":          config.Endpoint,
			"cardinality_limit": config.CardinalityLimit,
			"provider":          config.Provider,
			"circuit_enabled":   config.CircuitBreaker.Enabled,
		})

		registry, err := newRegistry(config)
		if err != nil {
			initErr = err
			logger.Error("Telemetry initialization failed", map[string]interface{}{
				"error":    err.Error(),
				"endpoint": config.Endpoint,
				"action":   "Check OTEL collector is running at endpoint",
				"impact":   "No metrics will be sent",
			})
			return
		}

		// Store logger in registry for future use
		registry.logger = logger

		// Process all metrics declared via DeclareMetrics()
		// This allows packages to declare their metrics in init()
		declaredCount := 0
		declaredMetrics.Range(func(key, value interface{}) bool {
			module := key.(string)
			moduleConfig := value.(ModuleConfig)
			registry.registerModule(module, moduleConfig)
			declaredCount++
			logger.Debug("Registered module metrics", map[string]interface{}{
				"module":       module,
				"metric_count": len(moduleConfig.Metrics),
			})
			return true
		})

		// Store globally for access by Emit functions
		globalRegistry.Store(registry)

		// Enable metrics emission in the logger now that registry is available
		logger.EnableMetrics()

		// Enable framework integration - register telemetry with core
		// This allows all framework components to emit metrics through telemetry
		EnableFrameworkIntegration(logger)

		// Log successful initialization
		logger.Info("Telemetry system initialized successfully", map[string]interface{}{
			"declared_modules":     declaredCount,
			"circuit_enabled":      registry.circuit != nil,
			"limiter_enabled":      registry.limiter != nil,
			"provider_type":        "OpenTelemetry",
			"initialization_ms":    time.Since(registry.startTime).Milliseconds(),
			"framework_integrated": true,
		})
	})
	return initErr
}

// InitializeForComponent initializes telemetry with automatic service type inference.
// This is the recommended way to initialize telemetry when you have a component,
// as it automatically sets the ServiceType based on whether it's a Tool or Agent.
//
// Example usage:
//
//	agent := core.NewBaseAgent("my-agent")
//	config := telemetry.UseProfile(telemetry.ProfileDevelopment)
//	config.ServiceName = "my-agent"
//	if err := telemetry.InitializeForComponent(agent, config); err != nil {
//	    log.Fatal(err)
//	}
func InitializeForComponent(component interface{ GetType() core.ComponentType }, config Config) error {
	// Automatically infer service type from component
	config.ServiceType = string(component.GetType())
	return Initialize(config)
}

// newRegistry creates a new telemetry registry
func newRegistry(config Config) (*Registry, error) {
	// Record start time for initialization metrics
	startTime := time.Now()

	// Set defaults if not provided
	if config.Endpoint == "" {
		config.Endpoint = "localhost:4318"
	}
	if config.ServiceName == "" {
		config.ServiceName = "truvag3-agent"
	}
	// Auto-infer ServiceType from the most recently created component
	// This allows telemetry.Initialize() to work without explicit ServiceType config
	if config.ServiceType == "" {
		config.ServiceType = string(core.GetCurrentComponentType())
	}
	if config.CardinalityLimit == 0 {
		config.CardinalityLimit = 10000
	}

	// Create OpenTelemetry provider
	provider, err := NewOTelProvider(config.ServiceName, config.ServiceType, config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTel provider: %w", err)
	}

	// Create cardinality limiter with default limits
	limits := config.CardinalityLimits
	if limits == nil {
		limits = map[string]int{
			"agent_id":   100,
			"capability": 50,
			"error_type": 50,
			"user_id":    100,
		}
	}

	r := &Registry{
		config:       config,
		provider:     provider,
		limiter:      NewCardinalityLimiter(limits),
		circuit:      NewTelemetryCircuitBreaker(config.CircuitBreaker),
		metrics:      provider.metrics,
		startTime:    startTime,
		errorLimiter: NewRateLimiter(1 * time.Second), // Log errors at most once per second
	}

	r.lastError.Store("")

	return r, nil
}

// registerModule registers a module's metrics
func (r *Registry) registerModule(_ string, config ModuleConfig) {
	// In a full implementation, this would pre-register metrics with OpenTelemetry
	// For now, we just track the metadata for validation
	// The module parameter will be used for module-specific configuration in the future
	for _, metric := range config.Metrics {
		// Pre-create instruments based on type if needed
		// This ensures metrics are ready to use when modules start emitting
		ctx := context.Background()
		switch metric.Type {
		case "gauge":
			// Gauges require special handling with callbacks
			// We'll handle this when the gauge is actually used
		case "counter":
			// Pre-create counter to avoid runtime creation overhead
			_ = r.metrics.RecordCounter(ctx, metric.Name, 0)
		case "histogram":
			// Pre-create histogram
			_ = r.metrics.RecordHistogram(ctx, metric.Name, 0)
		}
	}
}

// emit handles metric emission with all safety checks
func (r *Registry) emit(name string, value float64, labels map[string]string) error {
	// Check circuit breaker
	if r.circuit != nil && !r.circuit.Allow() {
		telemetryDropped.Add(1)
		return fmt.Errorf("telemetry circuit breaker open")
	}

	// Apply cardinality limiting
	if r.limiter != nil {
		for key, val := range labels {
			limited := r.limiter.CheckAndLimit(name, key, val)
			if limited != val {
				labels[key] = limited
			}
		}
	}

	// Record the metric
	if r.provider != nil {
		r.provider.RecordMetric(name, value, labels)
		r.emitted.Add(1)

		// Record success with circuit breaker
		if r.circuit != nil {
			r.circuit.RecordSuccess()
		}
	}

	return nil
}

// Emit - Simple, thread-safe, developer-friendly
func Emit(name string, value float64, labels ...string) {
	registry := globalRegistry.Load()
	if registry == nil {
		return // Telemetry not initialized, silent no-op
	}

	r := registry.(*Registry)
	if err := r.emit(name, value, parseLabels(labels...)); err != nil {
		telemetryErrors.Add(1)
		r.lastError.Store(err.Error())

		// Rate-limited error logging for visibility
		if r.logger != nil && r.errorLimiter != nil && r.errorLimiter.Allow() {
			r.logger.Error("Failed to emit metric", map[string]interface{}{
				"metric": name,
				"value":  value,
				"error":  err.Error(),
			})
		}

		// Record failure with circuit breaker
		if r.circuit != nil {
			r.circuit.RecordFailure()
		}
	}
}

// EmitWithContext - Advanced API for tracing correlation with automatic baggage inclusion
func EmitWithContext(ctx context.Context, name string, value float64, labels ...string) {
	// Extract and append baggage labels
	allLabels := appendBaggageToLabels(ctx, labels)
	defer returnLabelSlice(allLabels) // Return to pool when done

	// Try context-specific provider first
	if provider := FromContext(ctx); provider != nil {
		provider.RecordMetric(name, value, parseLabels(allLabels...))
		return
	}
	// Fall back to global with baggage labels included
	Emit(name, value, allLabels...)
}

// FromContext retrieves telemetry provider from context
func FromContext(ctx context.Context) *OTelProvider {
	// This would be implemented to extract provider from context
	// For now, return nil to use global
	return nil
}

// parseLabels - Convert variadic strings to map
// "key1", "val1", "key2", "val2" -> map[string]string
func parseLabels(labels ...string) map[string]string {
	m := make(map[string]string)
	for i := 0; i < len(labels)-1; i += 2 {
		m[labels[i]] = labels[i+1]
	}
	return m
}

// Shutdown gracefully shuts down the telemetry system
func Shutdown(ctx context.Context) error {
	registry := globalRegistry.Load()
	if registry == nil {
		return nil
	}

	r := registry.(*Registry)

	// Log shutdown start
	if r.logger != nil {
		r.logger.Info("Shutting down telemetry system", map[string]interface{}{
			"total_emitted": r.emitted.Load(),
			"uptime_ms":     time.Since(r.startTime).Milliseconds(),
		})
	}

	// Stop cardinality limiter cleanup
	if r.limiter != nil {
		r.limiter.Stop()
		if r.logger != nil {
			r.logger.Debug("Cardinality limiter stopped", nil)
		}
	}

	// Shutdown provider
	if r.provider != nil {
		err := r.provider.Shutdown(ctx)
		if err != nil {
			if r.logger != nil {
				r.logger.Error("Error during provider shutdown", map[string]interface{}{
					"error": err.Error(),
				})
			}
			return err
		}
		if r.logger != nil {
			r.logger.Info("Telemetry provider shut down successfully", nil)
		}
	}

	// Clear framework integration - unregister from core
	// This prevents core from calling our registry after shutdown
	core.SetMetricsRegistry(nil)

	// Clear global registry to prevent use after shutdown
	// This ensures Emit functions become no-ops after shutdown
	globalRegistry.Store(nil)

	// Final log
	if r.logger != nil {
		r.logger.Info("Telemetry system shut down complete", map[string]interface{}{
			"framework_unregistered": true,
			"registry_cleared":       true,
		})
	}

	return nil
}

// GetRegistry returns the current registry (for testing)
func GetRegistry() *Registry {
	r := globalRegistry.Load()
	if r == nil {
		return nil
	}
	return r.(*Registry)
}

// GetTelemetryProvider returns the OTelProvider as core.Telemetry interface.
// Use this to inject telemetry into components that need span creation, such as
// the orchestration module.
//
// Example:
//
//	// After telemetry.Initialize()
//	if provider := telemetry.GetTelemetryProvider(); provider != nil {
//	    orchestrator.SetTelemetry(provider)
//	}
//
// Returns nil if telemetry is not initialized.
func GetTelemetryProvider() core.Telemetry {
	r := globalRegistry.Load()
	if r == nil {
		return nil
	}
	registry := r.(*Registry)
	if registry.provider == nil {
		return nil
	}
	return registry.provider
}
