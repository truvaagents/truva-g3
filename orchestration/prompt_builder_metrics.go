package orchestration

// PromptBuilderMetrics defines the metrics emitted by PromptBuilder implementations.
// These metrics follow the naming conventions from telemetry/ARCHITECTURE.md.
//
// Metrics are emitted via the injected core.Telemetry interface, so this file
// only documents the metric definitions for reference (Grafana dashboards, alerts).

// Metric Names
const (
	// MetricPromptBuildDurationMs measures time to build orchestration prompt in milliseconds
	// Type: histogram
	// Labels: builder_type (default|template|custom), domain, status (success|error|fallback)
	// Buckets: [1, 5, 10, 25, 50, 100, 250, 500]
	MetricPromptBuildDurationMs = "orchestrator.prompt.build_duration_ms"

	// MetricPromptBuilt counts number of prompts built
	// Type: counter
	// Labels: builder_type (default|template|custom), domain, status (success|error)
	MetricPromptBuilt = "orchestrator.prompt.built"

	// MetricPromptTemplateFallback counts template failures that fell back to default
	// Type: counter
	// Labels: reason (parse_error|execution_error|file_not_found)
	MetricPromptTemplateFallback = "orchestrator.prompt.template_fallback"

	// MetricPromptTypeRules gauge showing number of type rules configured
	// Type: gauge
	// Labels: builder_type (default|template|custom)
	MetricPromptTypeRules = "orchestrator.prompt.type_rules"

	// MetricPromptSizeBytes measures size of generated prompts in bytes
	// Type: histogram
	// Labels: builder_type (default|template|custom)
	// Buckets: [1024, 2048, 4096, 8192, 16384, 32768]
	MetricPromptSizeBytes = "orchestrator.prompt.size_bytes"
)

// Span Names for distributed tracing
const (
	// SpanPromptBuilderBuild is the span name for prompt building
	SpanPromptBuilderBuild = "prompt-builder.build"
)

// Label Values
const (
	BuilderTypeDefault  = "default"
	BuilderTypeTemplate = "template"
	BuilderTypeCustom   = "custom"

	StatusSuccess  = "success"
	StatusError    = "error"
	StatusFallback = "fallback"

	FallbackReasonParseError     = "parse_error"
	FallbackReasonExecutionError = "execution_error"
	FallbackReasonFileNotFound   = "file_not_found"
)

// PromQL Queries for Grafana dashboards (documentation)
//
// Prompt build latency (P99):
//
//	histogram_quantile(0.99,
//	  sum(rate(orchestrator_prompt_build_duration_ms_bucket[5m])) by (le, builder_type)
//	)
//
// Prompt build rate by type:
//
//	sum(rate(orchestrator_prompt_built_total[5m])) by (builder_type, status)
//
// Template fallback rate:
//
//	sum(rate(orchestrator_prompt_template_fallback_total[5m])) by (reason)
//	  /
//	sum(rate(orchestrator_prompt_built_total{builder_type="template"}[5m]))
//
// Average prompt size:
//
//	avg(orchestrator_prompt_size_bytes) by (builder_type)
