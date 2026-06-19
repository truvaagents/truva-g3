package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// LogsTool wraps Loki (logs) and Jaeger (traces) HTTP APIs as capabilities.
// Pure API wrapper — no AI logic, no data transformation.
type LogsTool struct {
	*core.BaseTool
	lokiClient   *LokiClient
	jaegerClient *JaegerClient
}

// --- Request Types ---

// QueryLogsRequest represents the input for query_logs.
type QueryLogsRequest struct {
	Query     string `json:"query"`               // Required: LogQL query
	Limit     int    `json:"limit,omitempty"`     // Optional: max lines (default: 100, max: 1000)
	Direction string `json:"direction,omitempty"` // Optional: "backward" (default) or "forward"
	Since     string `json:"since,omitempty"`     // Optional: relative duration (default: "1h")
}

// QueryLogsRangeRequest represents the input for query_logs_range.
type QueryLogsRangeRequest struct {
	Query     string `json:"query"`               // Required: LogQL query
	Start     string `json:"start"`               // Required: range start (RFC3339 or epoch)
	End       string `json:"end"`                 // Required: range end (RFC3339 or epoch)
	Limit     int    `json:"limit,omitempty"`     // Optional: max lines (default: 100, max: 1000)
	Direction string `json:"direction,omitempty"` // Optional: "backward" (default) or "forward"
	Step      string `json:"step,omitempty"`      // Optional: resolution step for metric queries
}

// GetLabelsRequest represents the input for get_labels.
type GetLabelsRequest struct {
	Since string `json:"since,omitempty"` // Optional: time window (default: "24h")
}

// GetLabelValuesRequest represents the input for get_label_values.
type GetLabelValuesRequest struct {
	Label string `json:"label"`           // Required: label name
	Since string `json:"since,omitempty"` // Optional: time window (default: "24h")
	Query string `json:"query,omitempty"` // Optional: LogQL stream selector to filter
}

// GetDetectedFieldsRequest represents the input for get_detected_fields.
type GetDetectedFieldsRequest struct {
	Query string `json:"query"`           // Required: LogQL stream selector
	Since string `json:"since,omitempty"` // Optional: time window (default: "1h")
	Limit int    `json:"limit,omitempty"` // Optional: max fields (default: 20)
}

// --- Response Types ---
// Field names must match OutputSummary declarations exactly.

// LogsQueryResponse is the response for query_logs and query_logs_range.
type LogsQueryResponse struct {
	Streams      []LogStream `json:"streams"`
	TotalEntries int         `json:"total_entries"`
	Query        string      `json:"query"`
	Source       string      `json:"source"`         // always "loki"
	Hint         string      `json:"hint,omitempty"` // set only on empty results that look like a query problem (e.g. a non-indexed selector label)
}

// LogStream is a single stream in the logs query response.
type LogStream struct {
	Labels  string     `json:"labels"` // Serialized label set
	Entries []LogEntry `json:"entries"`
}

// LogEntry is a single log line.
type LogEntry struct {
	Timestamp string `json:"timestamp"` // RFC3339Nano
	Line      string `json:"line"`      // Log line content (CRI prefix stripped)
}

// LabelsResponse is the response for get_labels.
type LabelsResponse struct {
	Labels []string `json:"labels"`
	Count  int      `json:"count"`
	Source string   `json:"source"`
}

// LabelValuesResponse is the response for get_label_values.
type LabelValuesResponse struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
	Count  int      `json:"count"`
	Source string   `json:"source"`
}

// DetectedFieldsResponse is the response for get_detected_fields.
type DetectedFieldsResponse struct {
	Fields      []DetectedField `json:"fields"`
	TotalFields int             `json:"total_fields"`
	Query       string          `json:"query"`
	Source      string          `json:"source"`
}

// DetectedField is a single field in the get_detected_fields response.
type DetectedField struct {
	Label       string `json:"label"`       // Field name (e.g., "level", "operation")
	Type        string `json:"type"`        // Field type (e.g., "string", "int")
	Cardinality int    `json:"cardinality"` // Number of distinct values
}

// --- Jaeger Request Types ---

// GetTraceOperationsRequest represents the input for get_trace_operations.
type GetTraceOperationsRequest struct {
	Service string `json:"service"` // Required: service name
}

// FindTracesRequest represents the input for find_traces.
type FindTracesRequest struct {
	Service     string `json:"service"`                // Required: service name
	Operation   string `json:"operation,omitempty"`    // Optional: operation/span name filter
	Lookback    string `json:"lookback,omitempty"`     // Optional: time window (default: "1h")
	MinDuration string `json:"min_duration,omitempty"` // Optional: minimum trace duration
	MaxDuration string `json:"max_duration,omitempty"` // Optional: maximum trace duration
	Limit       int    `json:"limit,omitempty"`        // Optional: max traces (default: 20, max: 100)
}

// GetTraceRequest represents the input for get_trace.
type GetTraceRequest struct {
	TraceID string `json:"trace_id"` // Required: hex trace ID
}

// --- Jaeger Response Types ---

// TraceServicesResponse is the response for get_trace_services.
type TraceServicesResponse struct {
	Services []string `json:"services"`
	Count    int      `json:"count"`
	Source   string   `json:"source"`
}

// TraceOperationsResponse is the response for get_trace_operations.
type TraceOperationsResponse struct {
	Service    string   `json:"service"`
	Operations []string `json:"operations"`
	Count      int      `json:"count"`
	Source     string   `json:"source"`
}

// FindTracesResponse is the response for find_traces.
type FindTracesResponse struct {
	Traces      []TraceSummary `json:"traces"`
	TotalTraces int            `json:"total_traces"`
	Source      string         `json:"source"`
}

// TraceSummary is a single trace in the find_traces response.
type TraceSummary struct {
	TraceID       string   `json:"trace_id"`
	SpanCount     int      `json:"span_count"`
	Services      []string `json:"services"`
	DurationMs    float64  `json:"duration_ms"`
	StartTime     string   `json:"start_time"`
	RootOperation string   `json:"root_operation"`
	RequestID     string   `json:"request_id,omitempty"`
}

// GetTraceResponse is the response for get_trace.
type GetTraceResponse struct {
	TraceID    string      `json:"trace_id"`
	SpanCount  int         `json:"span_count"`
	Services   []string    `json:"services"`
	RequestID  string      `json:"request_id,omitempty"`
	Spans      []SpanInfo  `json:"spans"`
	ErrorSpans []ErrorSpan `json:"error_spans"`
	Source     string      `json:"source"`
}

// SpanInfo is a single span in the get_trace response.
type SpanInfo struct {
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id"`
	Operation    string            `json:"operation"`
	Service      string            `json:"service"`
	DurationMs   float64           `json:"duration_ms"`
	StartTime    string            `json:"start_time"`
	Status       string            `json:"status"`
	Tags         map[string]string `json:"tags"`
	HasError     bool              `json:"has_error"`
}

// ErrorSpan is a span with an error in the get_trace response.
type ErrorSpan struct {
	Operation  string  `json:"operation"`
	Service    string  `json:"service"`
	DurationMs float64 `json:"duration_ms"`
	Error      string  `json:"error"`
}

// NewLogsTool creates and initializes the tool with Loki and Jaeger clients.
func NewLogsTool() *LogsTool {
	lokiURL := os.Getenv("LOKI_URL")
	if lokiURL == "" {
		lokiURL = "http://loki.truvag3-examples:3100"
	}
	jaegerURL := os.Getenv("JAEGER_URL")
	if jaegerURL == "" {
		jaegerURL = "http://jaeger-query.truvag3-examples:80"
	}

	tool := &LogsTool{
		BaseTool:     core.NewTool("devops-observability-tool"),
		lokiClient:   NewLokiClient(lokiURL),
		jaegerClient: NewJaegerClient(jaegerURL),
	}

	tool.registerCapabilities()
	return tool
}

func (t *LogsTool) registerCapabilities() {
	// --- query_logs ---
	t.RegisterCapability(core.Capability{
		Name: "query_logs",
		Description: "Queries recent log lines from Loki using LogQL over a relative window: pass since (e.g. 6h) for 'recent' or 'last N hours/minutes' and the tool computes the range from now. " +
			"Use for errors, crashes, or keyword/request_id search across services. " +
			"Examples: " +
			"single service errors - {service_name=\"myapp\"} |= \"error\"; " +
			"trace one request across services - {service_name=~\".+\"} |= \"req-abc-123\" (line filter on a unique request ID is selective enough that the broad selector is acceptable); " +
			"keyword search across services - narrow the selector to specific services first, or keep since<=6h. " +
			"Cost scales with selector breadth × time window - start specific, broaden only if the narrow query returns nothing. " +
			"Loki's hard window is 30h; for longer investigations, partition into sequential calls with non-overlapping windows. " +
			"For fleet-wide questions, call get_label_values on service_name first to enumerate, then iterate with specific selectors. " +
			"Returns: log streams with timestamps, lines, and labels (request_id present when set on the log line). " +
			"Required: query (LogQL). " +
			"Optional: limit (default: 100, max: 1000), direction (backward/forward), since (default: 1h, max: 30h).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQueryLogs,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: `{k8s_namespace_name="truvag3-examples"} |~ "(?i)error|warn"`,
					Description: "LogQL query. Indexed stream labels: service_name, k8s_pod_name, k8s_namespace_name, k8s_deployment_name, deployment_environment (target a pod with k8s_pod_name, a namespace with k8s_namespace_name). " +
						"Line filters: |= matches a literal substring, |~ matches a regex - use |~ for multiple keywords or case-insensitive matching, e.g. {service_name=\"myapp\"} |~ \"(?i)error|warn\"; use |= for one exact term such as a request id, e.g. {service_name=~\".+\"} |= \"req-id\". " +
						"JSON fields (level, request_id, operation) are structured metadata: filter with | level=\"ERROR\" (no | json needed)."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "integer", Example: "100", Description: "Max log lines (default: 100, max: 1000)"},
				{Name: "direction", Type: "string", Example: "backward", Description: "Sort: backward (newest first, default) or forward"},
				{Name: "since", Type: "string", Example: "1h", Description: "Relative time window (default: 1h, max: 30h). For longer investigations, partition into multiple calls."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "streams", Type: "array", Description: "Log streams with labels and entries (timestamp + line)"},
				{Name: "total_entries", Type: "integer", Example: "0", Description: "Total log entries returned"},
				{Name: "query", Type: "string", Description: "The LogQL query that was executed"},
				{Name: "source", Type: "string", Example: "loki", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "hint", Type: "string", Description: "Present only when the result is empty due to a likely query problem (e.g. a selector label that is not indexed)"},
			},
		},
	})

	// --- query_logs_range ---
	t.RegisterCapability(core.Capability{
		Name: "query_logs_range",
		Description: "Queries log lines within an explicit time range using LogQL. " +
			"Use when investigating a specific incident window with known start/end timestamps; for 'recent' or 'last N hours/minutes', use query_logs with since instead (it computes the window from now, avoiding timestamp math). " +
			"Examples: " +
			"single service over a 30-minute window - {service_name=\"myapp\"} |= \"error\" with start/end 30 minutes apart; " +
			"trace one request across services - {service_name=~\".+\"} |= \"req-id\" with start/end bracketing the request lifetime. " +
			"Cost scales with selector breadth × (end - start) - start specific, narrow ranges resolve faster. " +
			"Loki's hard range is 30h; for longer investigations, issue multiple sequential calls with non-overlapping ranges. " +
			"Returns: log streams with timestamps, lines, and labels. " +
			"Required: query (LogQL), start (RFC3339 or epoch), end (RFC3339 or epoch). " +
			"Optional: limit (default: 100, max: 1000), direction (backward/forward), step (resolution for metric queries, e.g. 15s, 1m).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQueryLogsRange,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: `{k8s_namespace_name="truvag3-examples"} |~ "(?i)error|warn"`,
					Description: "LogQL query. Start with specific selectors; broaden only if the narrow query returns nothing. " +
						"Indexed stream labels: service_name, k8s_pod_name, k8s_namespace_name, k8s_deployment_name, deployment_environment (target a pod with k8s_pod_name, a namespace with k8s_namespace_name). " +
						"Line filters: |= matches a literal substring, |~ matches a regex - use |~ for multiple keywords or case-insensitive matching, e.g. |~ \"(?i)error|warn\"."},
				{Name: "start", Type: "string", Example: "2026-03-26T14:00:00Z",
					Description: "Range start time (RFC3339 or unix epoch)"},
				{Name: "end", Type: "string", Example: "2026-03-26T14:30:00Z",
					Description: "Range end time (RFC3339 or unix epoch)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "integer", Example: "100", Description: "Max log lines per stream (default: 100, max: 1000)"},
				{Name: "direction", Type: "string", Example: "backward", Description: "Sort: backward (newest first, default) or forward"},
				{Name: "step", Type: "string", Example: "15s", Description: "Query resolution step for metric queries (e.g., 15s, 1m)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "streams", Type: "array", Description: "Log streams with labels and entries (timestamp + line)"},
				{Name: "total_entries", Type: "integer", Example: "0", Description: "Total log entries returned"},
				{Name: "query", Type: "string", Description: "The LogQL query that was executed"},
				{Name: "source", Type: "string", Example: "loki", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "hint", Type: "string", Description: "Present only when the result is empty due to a likely query problem (e.g. a selector label that is not indexed)"},
			},
		},
	})

	// --- get_labels ---
	t.RegisterCapability(core.Capability{
		Name: "get_labels",
		Description: "Lists all available log stream labels from Loki. " +
			"Use before query_logs to discover what labels exist for constructing LogQL stream selectors. " +
			"Returns: array of label names (e.g., service_name, deployment_environment, detected_level). " +
			"All parameters optional. Optional: since (default: 24h).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetLabels,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "since", Type: "string", Example: "24h", Description: "Time window for label discovery (default: 24h)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "labels", Type: "array", Description: "Array of available label names"},
				{Name: "count", Type: "integer", Example: "0", Description: "Number of labels"},
				{Name: "source", Type: "string", Example: "loki", Description: "Data source identifier"},
			},
		},
	})

	// --- get_label_values ---
	t.RegisterCapability(core.Capability{
		Name: "get_label_values",
		Description: "Gets all values for a specific log label. " +
			"Use after get_labels to find which services have logs (e.g., values of service_name label). " +
			"Returns: array of label values. " +
			"Required: label (label name, e.g., \"service_name\"). Optional: since (default: 24h), query (stream selector to filter).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetLabelValues,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "label", Type: "string", Example: "service_name",
					Description: "Label name to get values for (e.g., service_name, deployment_environment)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "since", Type: "string", Example: "24h", Description: "Time window (default: 24h)"},
				{Name: "query", Type: "string", Example: `{service_name=~".+"}`,
					Description: "LogQL stream selector to filter which values are returned"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "label", Type: "string", Description: "The label name that was queried"},
				{Name: "values", Type: "array", Description: "Array of values for the label"},
				{Name: "count", Type: "integer", Example: "0", Description: "Number of values"},
				{Name: "source", Type: "string", Example: "loki", Description: "Data source identifier"},
			},
		},
	})

	// --- get_detected_fields ---
	t.RegisterCapability(core.Capability{
		Name: "get_detected_fields",
		Description: "Detects structured fields present in JSON log lines for a given service. " +
			"Use before query_logs to discover available fields for filtering (level, operation, error_type, etc.). " +
			"Returns: field names with types and cardinality counts. " +
			"Required: query (LogQL stream selector). Optional: since (default: 1h), limit (default: 20).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetDetectedFields,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: `{service_name="devops-chat-agent"}`,
					Description: "LogQL stream selector to detect fields for"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "since", Type: "string", Example: "1h", Description: "Time window (default: 1h)"},
				{Name: "limit", Type: "integer", Example: "20", Description: "Max fields to return (default: 20)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "fields", Type: "array", Description: "Array of detected fields with label, type, and cardinality"},
				{Name: "total_fields", Type: "integer", Example: "0", Description: "Number of fields detected"},
				{Name: "query", Type: "string", Description: "The LogQL query that was used"},
				{Name: "source", Type: "string", Example: "loki", Description: "Data source identifier"},
			},
		},
	})

	// --- Jaeger: Distributed Tracing Capabilities ---

	t.RegisterCapability(core.Capability{
		Name: "get_trace_services",
		Description: "Jaeger tracing: lists all services that have reported distributed traces. " +
			"Use when the user asks about Jaeger, traces, or distributed tracing to discover available services. " +
			"Returns: array of service names. " +
			"No parameters required.",
		InputTypes:   []string{"json"},
		OutputTypes:  []string{"json"},
		Handler:      t.handleGetTraceServices,
		InputSummary: &core.SchemaSummary{},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "services", Type: "array", Description: "Array of service names with trace data"},
				{Name: "count", Type: "integer", Example: "0", Description: "Number of services"},
				{Name: "source", Type: "string", Example: "jaeger", Description: "Data source identifier"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "get_trace_operations",
		Description: "Jaeger tracing: lists all operations (span names) for a service. " +
			"Use when investigating Jaeger traces to discover what operations a service performs (HTTP endpoints, AI calls, orchestrator phases). " +
			"Operations include HTTP endpoints, orchestrator phases (orchestrator.phase.1), AI calls (ai.generate_response), and framework spans. " +
			"Returns: array of operation names. " +
			"Required: service (service name).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetTraceOperations,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "service", Type: "string", Example: "devops-chat-agent",
					Description: "Service name to list operations for"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "service", Type: "string", Description: "The service that was queried"},
				{Name: "operations", Type: "array", Description: "Array of operation/span names"},
				{Name: "count", Type: "integer", Example: "0", Description: "Number of operations"},
				{Name: "source", Type: "string", Example: "jaeger", Description: "Data source identifier"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "find_traces",
		Description: "Jaeger tracing: searches for distributed traces by service, operation, and duration filters. " +
			"Use when investigating Jaeger traces for slow requests, errors, or a specific service's recent activity. " +
			"To find traces for a specific request_id, use query_logs to search logs by request_id first, " +
			"extract trace_id from the stream labels, then use get_trace with that trace_id. " +
			"Returns: trace summaries with trace_id, span count, services, duration, and request_id when available. " +
			"Required: service. Optional: operation, lookback (default: 1h), min_duration, max_duration, limit (default: 20).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleFindTraces,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "service", Type: "string", Example: "devops-chat-agent",
					Description: "Service name to search traces for"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "operation", Type: "string", Example: "orchestrator.phase.1",
					Description: "Filter by operation/span name"},
				{Name: "lookback", Type: "string", Example: "1h",
					Description: "Time window (default: 1h). Examples: 30m, 6h, 24h"},
				{Name: "min_duration", Type: "string", Example: "1s",
					Description: "Minimum trace duration (e.g., 1s, 500ms)"},
				{Name: "max_duration", Type: "string", Example: "30s",
					Description: "Maximum trace duration"},
				{Name: "limit", Type: "integer", Example: "20",
					Description: "Max traces to return (default: 20, max: 100)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "traces", Type: "array", Description: "Array of trace summaries with trace_id, span_count, services, duration_ms, root_operation, request_id"},
				{Name: "total_traces", Type: "integer", Example: "0", Description: "Number of traces returned"},
				{Name: "source", Type: "string", Example: "jaeger", Description: "Data source identifier"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "get_trace",
		Description: "Jaeger tracing: gets the full distributed trace with all spans, errors, and durations by trace ID. " +
			"Use when investigating Jaeger traces — pass a trace_id from query_logs stream labels or from find_traces results. " +
			"Returns: all spans with operations, durations, tags, services, and error details. " +
			"Required: trace_id (hex trace ID).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetTrace,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "trace_id", Type: "string", Example: "ce0e25d4f3587008f9f98d857ec8288d",
					Description: "Hex trace ID. Get from log stream labels (trace_id field) or from find_traces results"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "trace_id", Type: "string", Description: "The trace ID that was queried"},
				{Name: "span_count", Type: "integer", Example: "0", Description: "Total spans in the trace"},
				{Name: "services", Type: "array", Description: "Services involved in the trace"},
				{Name: "spans", Type: "array", Description: "All spans with span_id, operation, service, duration_ms, tags, has_error"},
				{Name: "error_spans", Type: "array", Description: "Spans with errors (operation, service, error message)"},
				{Name: "source", Type: "string", Example: "jaeger", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "request_id", Type: "string", Description: "Orchestration request ID if present in span tags"},
			},
		},
	})
}
