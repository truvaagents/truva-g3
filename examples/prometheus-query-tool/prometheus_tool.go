package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// PrometheusTool is a focused tool that provides Prometheus query capabilities
// It demonstrates the passive tool pattern - can register but not discover
type PrometheusTool struct {
	*core.BaseTool
	prometheusURL string
	client        *PrometheusClient
}

// --- Request Types ---

// QueryMetricsRequest represents the input for instant PromQL queries
type QueryMetricsRequest struct {
	Query string `json:"query"`          // PromQL expression for instant query
	Time  string `json:"time,omitempty"` // RFC3339 timestamp or unix epoch; defaults to current server time
}

// QueryRangeRequest represents the input for range PromQL queries
type QueryRangeRequest struct {
	Query string `json:"query"` // PromQL expression for range query
	Start string `json:"start"` // Range start as RFC3339 or unix timestamp
	End   string `json:"end"`   // Range end as RFC3339 or unix timestamp
	Step  string `json:"step"`  // Query resolution step duration (e.g., 15s, 1m, 5m)
}

// GetAlertsRequest represents the input for listing alerts (no params required)
type GetAlertsRequest struct{}

// GetTargetsRequest represents the input for listing scrape targets
type GetTargetsRequest struct {
	State string `json:"state,omitempty"` // Filter targets: active, dropped, or any
}

// --- Response Types ---

// MetricSample represents a single instant query result
type MetricSample struct {
	Labels    map[string]string `json:"labels"`
	Timestamp float64           `json:"timestamp"` // Unix epoch seconds
	Value     float64           `json:"value"`
}

// QueryMetricsResponse represents the output for instant PromQL queries
type QueryMetricsResponse struct {
	Query      string         `json:"query"`
	ResultType string         `json:"result_type"` // "vector", "scalar", "string", "matrix"
	Samples    []MetricSample `json:"samples"`
	Warnings   []string       `json:"warnings,omitempty"`
	Source     string         `json:"source"`
}

// RangeValue represents a single data point in a range query result
type RangeValue struct {
	Timestamp float64 `json:"timestamp"` // Unix epoch seconds
	Value     float64 `json:"value"`
}

// RangeSeries represents a single time series from a range query
type RangeSeries struct {
	Labels map[string]string `json:"labels"`
	Values []RangeValue      `json:"values"`
}

// QueryRangeResponse represents the output for range PromQL queries
type QueryRangeResponse struct {
	Query      string        `json:"query"`
	ResultType string        `json:"result_type"`
	Series     []RangeSeries `json:"series"`
	Start      string        `json:"start"`
	End        string        `json:"end"`
	Step       string        `json:"step"`
	Warnings   []string      `json:"warnings,omitempty"`
	Source     string        `json:"source"`
}

// AlertInfo represents a single firing alert
type AlertInfo struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    string            `json:"active_at"`
	Value       string            `json:"value"`
}

// AlertGroup represents a group of alerts with the same rule
type AlertGroup struct {
	Name   string      `json:"name"`
	File   string      `json:"file"`
	Alerts []AlertInfo `json:"alerts"`
}

// GetAlertsResponse represents the output for listing alerts
type GetAlertsResponse struct {
	Groups     []AlertGroup `json:"groups"`
	TotalAlerts int         `json:"total_alerts"`
	Source     string       `json:"source"`
}

// TargetInfo represents a single scrape target
type TargetInfo struct {
	Labels         map[string]string `json:"labels"`
	ScrapeURL      string            `json:"scrape_url"`
	Health         string            `json:"health"` // "up", "down", "unknown"
	LastError      string            `json:"last_error,omitempty"`
	LastScrape     string            `json:"last_scrape"`
	LastScrapeDur  float64           `json:"last_scrape_duration_seconds"`
}

// GetTargetsResponse represents the output for listing scrape targets
type GetTargetsResponse struct {
	ActiveTargets  []TargetInfo `json:"active_targets"`
	DroppedTargets int          `json:"dropped_targets"`
	State          string       `json:"state,omitempty"` // Filter applied
	Source         string       `json:"source"`
}

// NewPrometheusTool creates a new Prometheus query tool
func NewPrometheusTool() *PrometheusTool {
	prometheusURL := os.Getenv("PROMETHEUS_URL")
	if prometheusURL == "" {
		prometheusURL = "http://prometheus.truvag3-examples:9090"
	}

	tool := &PrometheusTool{
		BaseTool:      core.NewTool("prometheus-query-tool"),
		prometheusURL: prometheusURL,
		client:        NewPrometheusClient(prometheusURL),
	}

	// Register all capabilities
	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all Prometheus query capabilities
func (p *PrometheusTool) registerCapabilities() {
	// Capability 1: Instant PromQL Query
	// Auto-generated endpoint: /api/capabilities/query_metrics
	// Schema endpoint: /api/capabilities/query_metrics/schema
	p.RegisterCapability(core.Capability{
		Name:        "query_metrics",
		Description: "Executes an instant PromQL query against Prometheus and returns current values of matching time series.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handleQueryMetrics,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "up",
					Description: "PromQL expression for instant query",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "time",
					Type:        "string",
					Example:     "now",
					Description: "Evaluation time: relative (now, now-1h, now-30m) or absolute (RFC3339 or unix epoch). Defaults to current server time if omitted.",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The PromQL query that was executed"},
				{Name: "result_type", Type: "string", Description: "Result type: vector, scalar, string, or matrix"},
				{Name: "samples", Type: "array", Description: "List of metric samples with labels, timestamp, and value"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "warnings", Type: "array", Description: "Prometheus query warnings"},
			},
		},
	})

	// Capability 2: Range PromQL Query
	// Auto-generated endpoint: /api/capabilities/query_range
	// Schema endpoint: /api/capabilities/query_range/schema
	p.RegisterCapability(core.Capability{
		Name:        "query_range",
		Description: "Executes a range PromQL query returning time series data over a specified time window.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handleQueryRange,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "rate(http_requests_total[5m])",
					Description: "PromQL expression for range query",
				},
				{
					Name:        "start",
					Type:        "string",
					Example:     "now-1h",
					Description: "Range start: relative (now, now-7d, now-1h, now-30m) or absolute (RFC3339 like 2026-03-10T00:00:00Z, or unix epoch seconds)",
				},
				{
					Name:        "end",
					Type:        "string",
					Example:     "now",
					Description: "Range end: relative (now, now-7d, now-1h, now-30m) or absolute (RFC3339 like 2026-03-10T12:00:00Z, or unix epoch seconds)",
				},
				{
					Name:        "step",
					Type:        "string",
					Example:     "15s",
					Description: "Query resolution step duration, e.g. 15s, 1m, 5m",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The PromQL query that was executed"},
				{Name: "result_type", Type: "string", Description: "Result type: vector, scalar, string, or matrix"},
				{Name: "series", Type: "array", Description: "List of time series with labels and values array"},
				{Name: "start", Type: "string", Description: "Range start time"},
				{Name: "end", Type: "string", Description: "Range end time"},
				{Name: "step", Type: "string", Description: "Query resolution step"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "warnings", Type: "array", Description: "Prometheus query warnings"},
			},
		},
	})

	// Capability 3: Get Alerts
	// Auto-generated endpoint: /api/capabilities/get_alerts
	// Schema endpoint: /api/capabilities/get_alerts/schema
	p.RegisterCapability(core.Capability{
		Name:        "get_alerts",
		Description: "Lists all currently firing Prometheus alerts with labels, annotations, and state.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handleGetAlerts,

		// Phase 2: No fields needed (no params)
		InputSummary: &core.SchemaSummary{},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "groups", Type: "array", Description: "List of alert groups with name, file, and alerts array"},
				{Name: "total_alerts", Type: "number", Description: "Total number of firing alerts"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 4: Get Targets
	// Auto-generated endpoint: /api/capabilities/get_targets
	// Schema endpoint: /api/capabilities/get_targets/schema
	p.RegisterCapability(core.Capability{
		Name:        "get_targets",
		Description: "Lists Prometheus scrape targets with health status and scrape metrics.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     p.handleGetTargets,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "state",
					Type:        "string",
					Example:     "active",
					Description: "Filter targets: active, dropped, or any",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "active_targets", Type: "array", Description: "List of active scrape targets with labels, scrape_url, health, and timing info"},
				{Name: "dropped_targets", Type: "number", Description: "Count of dropped scrape targets"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "state", Type: "string", Description: "Filter that was applied"},
			},
		},
	})
}
