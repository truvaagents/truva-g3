// Package telemetry provides unified metrics infrastructure for the Truva-G3 framework.
//
// This file defines the unified metrics contract that enables consistent observability
// across all Truva-G3 modules (agent, orchestration, core). Using these unified metrics
// ensures that dashboards and queries work regardless of which module is used.
//
// Usage:
//
//	// In agent-based examples
//	telemetry.RecordRequest(telemetry.ModuleAgent, "research", durationMs, "success")
//
//	// In orchestration-based examples
//	telemetry.RecordRequest(telemetry.ModuleOrchestration, "workflow", durationMs, "success")
//
// Both will emit the same metric (request.duration_ms, request.total) with a "module" label
// that identifies the source, enabling unified dashboard queries.
package telemetry

// Module label values for identifying metric sources.
// These are used as the "module" label value in unified metrics.
const (
	// ModuleAgent identifies metrics from the agent module
	ModuleAgent = "agent"

	// ModuleOrchestration identifies metrics from the orchestration module
	ModuleOrchestration = "orchestration"

	// ModuleCore identifies metrics from the core module
	ModuleCore = "core"

	// ModuleAI identifies metrics from the ai module
	// Used for internal AI operations: provider failover, retries, detection
	ModuleAI = "ai"

	// ModuleMemory identifies metrics from the memory module
	// Used for: reflection passes, knowledge storage, digest caching
	ModuleMemory = "memory"
)

// Unified metric names - use these constants to ensure consistent naming.
// All modules should emit metrics using these names with appropriate module labels.
// Note: These are distinct from the agent-specific metrics in metrics.go
const (
	// Request metrics - for user-facing request handling
	UnifiedRequestDuration = "request.duration_ms"
	UnifiedRequestTotal    = "request.total"
	UnifiedRequestErrors   = "request.errors"

	// Tool/capability call metrics
	UnifiedToolCallDuration = "tool.call.duration_ms"
	UnifiedToolCallTotal    = "tool.call.total"
	UnifiedToolCallErrors   = "tool.call.errors"
	UnifiedToolCallRetries  = "tool.call.retries"

	// AI synthesis metrics
	UnifiedAIRequestDuration = "ai.request.duration_ms"
	UnifiedAIRequestTotal    = "ai.request.total"
	UnifiedAITokensUsed      = "ai.tokens.used"

	// Discovery metrics (reference only - already defined in core module)
	// Use these string values when emitting discovery metrics
	UnifiedDiscoveryRegistrations = "discovery.registrations"
	UnifiedDiscoveryLookups       = "discovery.lookups"
	UnifiedDiscoveryHealthChecks  = "discovery.health_checks"
)

// RecordRequest records unified request metrics with proper module labeling.
// This should be called at the end of any user-facing request handler.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - operation: The type of operation (e.g., "research", "workflow", "natural_request")
//   - durationMs: Request duration in milliseconds
//   - status: "success" or "error"
//
// Example:
//
//	startTime := time.Now()
//	// ... handle request ...
//	telemetry.RecordRequest(telemetry.ModuleAgent, "research",
//	    float64(time.Since(startTime).Milliseconds()), "success")
func RecordRequest(module string, operation string, durationMs float64, status string) {
	Histogram(UnifiedRequestDuration, durationMs,
		"module", module,
		"operation", operation,
		"status", status,
	)
	Counter(UnifiedRequestTotal,
		"module", module,
		"operation", operation,
		"status", status,
	)
}

// RecordRequestError records a request error with error type classification.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - operation: The type of operation that failed
//   - errorType: Classification of the error (e.g., "timeout", "validation", "upstream_failure")
func RecordRequestError(module string, operation string, errorType string) {
	Counter(UnifiedRequestErrors,
		"module", module,
		"operation", operation,
		"error_type", errorType,
	)
}

// RecordToolCall records tool/capability call metrics.
// This should be called after each tool invocation completes.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - toolName: Name of the tool being called
//   - durationMs: Call duration in milliseconds
//   - status: "success" or "error"
//
// Example:
//
//	startTime := time.Now()
//	result, err := tool.Execute(ctx, params)
//	status := "success"
//	if err != nil { status = "error" }
//	telemetry.RecordToolCall(telemetry.ModuleOrchestration, "weather-service",
//	    float64(time.Since(startTime).Milliseconds()), status)
func RecordToolCall(module string, toolName string, durationMs float64, status string) {
	Histogram(UnifiedToolCallDuration, durationMs,
		"module", module,
		"tool_name", toolName,
		"status", status,
	)
	Counter(UnifiedToolCallTotal,
		"module", module,
		"tool_name", toolName,
		"status", status,
	)
}

// RecordToolCallError records a tool call error with error type classification.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - toolName: Name of the tool that failed
//   - errorType: Classification of the error (e.g., "timeout", "connection", "validation")
func RecordToolCallError(module string, toolName string, errorType string) {
	Counter(UnifiedToolCallErrors,
		"module", module,
		"tool_name", toolName,
		"error_type", errorType,
	)
}

// RecordToolCallRetry records a tool call retry attempt.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - toolName: Name of the tool being retried
func RecordToolCallRetry(module string, toolName string) {
	Counter(UnifiedToolCallRetries,
		"module", module,
		"tool_name", toolName,
	)
}

// RecordAIRequest records AI provider request metrics.
// This should be called after each AI API call completes.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - provider: AI provider name (e.g., "openai", "anthropic", "groq")
//   - durationMs: Request duration in milliseconds
//   - status: "success" or "error"
//   - extraLabels: Optional key-value pairs (e.g., "model", "gpt-4o")
//
// Example:
//
//	startTime := time.Now()
//	response, err := aiClient.GenerateResponse(ctx, prompt, opts)
//	status := "success"
//	if err != nil { status = "error" }
//	telemetry.RecordAIRequest(telemetry.ModuleAgent, "openai",
//	    float64(time.Since(startTime).Milliseconds()), status, "model", "gpt-4o")
func RecordAIRequest(module string, provider string, durationMs float64, status string, extraLabels ...string) {
	labels := append([]string{"module", module, "provider", provider, "status", status}, extraLabels...)
	Histogram(UnifiedAIRequestDuration, durationMs, labels...)
	Counter(UnifiedAIRequestTotal, labels...)
}

// RecordAITokens records AI token usage metrics.
//
// Parameters:
//   - module: Use ModuleAgent or ModuleOrchestration constants
//   - provider: AI provider name
//   - tokenType: "input" or "output"
//   - count: Number of tokens used
//   - extraLabels: Optional key-value pairs (e.g., "model", "gpt-4o")
func RecordAITokens(module string, provider string, tokenType string, count int64, extraLabels ...string) {
	labels := append([]string{"module", module, "provider", provider, "type", tokenType}, extraLabels...)
	Emit(UnifiedAITokensUsed, float64(count), labels...)
}

// init declares the unified metrics with appropriate types and buckets.
// This ensures metrics are pre-registered with the correct configuration.
func init() {
	DeclareMetrics("unified", ModuleConfig{
		Metrics: []MetricDefinition{
			// Request metrics
			{
				Name:    UnifiedRequestDuration,
				Type:    "histogram",
				Help:    "Request processing duration in milliseconds",
				Labels:  []string{"module", "operation", "status"},
				Unit:    "ms",
				Buckets: []float64{10, 50, 100, 500, 1000, 5000, 10000},
			},
			{
				Name:   UnifiedRequestTotal,
				Type:   "counter",
				Help:   "Total requests processed",
				Labels: []string{"module", "operation", "status"},
			},
			{
				Name:   UnifiedRequestErrors,
				Type:   "counter",
				Help:   "Request errors by type",
				Labels: []string{"module", "operation", "error_type"},
			},

			// Tool call metrics
			{
				Name:    UnifiedToolCallDuration,
				Type:    "histogram",
				Help:    "Tool/capability call duration in milliseconds",
				Labels:  []string{"module", "tool_name", "status"},
				Unit:    "ms",
				Buckets: []float64{10, 50, 100, 500, 1000, 5000},
			},
			{
				Name:   UnifiedToolCallTotal,
				Type:   "counter",
				Help:   "Total tool calls",
				Labels: []string{"module", "tool_name", "status"},
			},
			{
				Name:   UnifiedToolCallErrors,
				Type:   "counter",
				Help:   "Tool call errors by type",
				Labels: []string{"module", "tool_name", "error_type"},
			},
			{
				Name:   UnifiedToolCallRetries,
				Type:   "counter",
				Help:   "Tool call retry attempts",
				Labels: []string{"module", "tool_name"},
			},

			// AI request metrics
			{
				Name:    UnifiedAIRequestDuration,
				Type:    "histogram",
				Help:    "AI provider request duration in milliseconds",
				Labels:  []string{"module", "provider", "status", "model"},
				Unit:    "ms",
				Buckets: []float64{100, 500, 1000, 2000, 5000, 10000},
			},
			{
				Name:   UnifiedAIRequestTotal,
				Type:   "counter",
				Help:   "Total AI provider requests",
				Labels: []string{"module", "provider", "status", "model"},
			},
			{
				Name:   UnifiedAITokensUsed,
				Type:   "counter",
				Help:   "AI tokens used (input/output)",
				Labels: []string{"module", "provider", "type", "model"},
			},
		},
	})
}
