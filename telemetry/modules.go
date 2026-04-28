package telemetry

// This file contains metric declarations for all modules
// It's in the telemetry package to avoid import cycles

func init() {
	// Core module metrics
	DeclareMetrics("agent", ModuleConfig{
		Metrics: []MetricDefinition{
			{
				Name:    "agent.startup.duration_ms",
				Type:    "histogram",
				Help:    "Agent initialization time in milliseconds",
				Labels:  []string{"agent_name"},
				Unit:    "ms",
				Buckets: []float64{10, 50, 100, 500, 1000, 5000},
			},
			{
				Name:   "agent.capabilities.count",
				Type:   "gauge",
				Help:   "Number of registered capabilities",
				Labels: []string{"agent_name"},
			},
			{
				Name:   "agent.health",
				Type:   "gauge",
				Help:   "Agent health status (0=down, 1=up)",
				Labels: []string{"agent_name"},
			},
			{
				Name:   "agent.capability.executions",
				Type:   "counter",
				Help:   "Capability execution count",
				Labels: []string{"agent_name", "capability"},
			},
			{
				Name:    "agent.capability.duration_ms",
				Type:    "histogram",
				Help:    "Capability execution duration in milliseconds",
				Labels:  []string{"agent_name", "capability", "status"},
				Unit:    "ms",
				Buckets: []float64{1, 10, 100, 1000, 10000},
			},
			{
				Name:   "agent.capability.errors",
				Type:   "counter",
				Help:   "Capability execution errors",
				Labels: []string{"agent_name", "capability", "error_type"},
			},
		},
	})

	// Discovery service metrics
	DeclareMetrics("discovery", ModuleConfig{
		Metrics: []MetricDefinition{
			{
				Name:   "discovery.registrations",
				Type:   "counter",
				Help:   "Service registrations",
				Labels: []string{"service_type", "namespace"},
			},
			{
				Name:   "discovery.deregistrations",
				Type:   "counter",
				Help:   "Service deregistrations",
				Labels: []string{"service_type", "namespace"},
			},
			{
				Name:   "discovery.lookups",
				Type:   "counter",
				Help:   "Service lookups",
				Labels: []string{"service_type", "namespace", "result"},
			},
			{
				Name:    "discovery.lookup.duration_ms",
				Type:    "histogram",
				Help:    "Service lookup duration",
				Labels:  []string{"service_type", "namespace"},
				Unit:    "ms",
				Buckets: []float64{0.1, 1, 10, 100, 1000},
			},
			{
				Name:   "discovery.health_checks",
				Type:   "counter",
				Help:   "Health check executions",
				Labels: []string{"service_type", "status"},
			},
			{
				Name:   "discovery.services.active",
				Type:   "gauge",
				Help:   "Number of active services",
				Labels: []string{"namespace"},
			},
		},
	})

	// Memory/state management metrics
	DeclareMetrics("memory", ModuleConfig{
		Metrics: []MetricDefinition{
			{
				Name:   "memory.operations",
				Type:   "counter",
				Help:   "Memory operations",
				Labels: []string{"operation", "memory_type"},
			},
			{
				Name:   "memory.size_bytes",
				Type:   "gauge",
				Help:   "Memory size in bytes",
				Labels: []string{"memory_type"},
			},
			{
				Name:   "memory.evictions",
				Type:   "counter",
				Help:   "Memory evictions",
				Labels: []string{"memory_type", "reason"},
			},
			{
				Name:   "memory.cache.hits",
				Type:   "counter",
				Help:   "Memory cache hits",
				Labels: []string{"memory_type"},
			},
			{
				Name:   "memory.cache.misses",
				Type:   "counter",
				Help:   "Memory cache misses",
				Labels: []string{"memory_type"},
			},
		},
	})
}
