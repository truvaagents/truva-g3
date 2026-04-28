package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// --- Prometheus API Response Envelope ---

// PrometheusResponse is the top-level envelope for all Prometheus HTTP API responses
type PrometheusResponse struct {
	Status    string          `json:"status"`              // "success" or "error"
	Data      json.RawMessage `json:"data,omitempty"`      // Varies by endpoint
	ErrorType string          `json:"errorType,omitempty"` // Error category (e.g., "bad_data", "timeout")
	Error     string          `json:"error,omitempty"`     // Human-readable error message
	Warnings  []string        `json:"warnings,omitempty"`  // Non-fatal warnings
}

// --- Query Data Structures ---

// PrometheusQueryData represents the "data" field for /api/v1/query and /api/v1/query_range
type PrometheusQueryData struct {
	ResultType string            `json:"resultType"` // "vector", "matrix", "scalar", "string"
	Result     json.RawMessage   `json:"result"`     // Varies by resultType
}

// PrometheusVectorResult represents a single instant vector result
// The "value" field is [unix_timestamp_float64, "string_value"]
type PrometheusVectorResult struct {
	Metric map[string]string `json:"metric"`
	Value  json.RawMessage   `json:"value"` // [timestamp, "value_string"]
}

// PrometheusMatrixResult represents a single range vector (matrix) result
// The "values" field is [[unix_timestamp_float64, "string_value"], ...]
type PrometheusMatrixResult struct {
	Metric map[string]string `json:"metric"`
	Values []json.RawMessage `json:"values"` // [[timestamp, "value_string"], ...]
}

// --- Alerts Data Structures ---

// PrometheusAlertsData represents the "data" field for /api/v1/alerts
type PrometheusAlertsData struct {
	Alerts []PrometheusAlert `json:"alerts"`
}

// PrometheusAlert represents a single alert from the API
type PrometheusAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`    // "firing", "pending", "inactive"
	ActiveAt    string            `json:"activeAt"`
	Value       string            `json:"value"`
}

// --- Targets Data Structures ---

// PrometheusTargetsData represents the "data" field for /api/v1/targets
type PrometheusTargetsData struct {
	ActiveTargets  []PrometheusTarget `json:"activeTargets"`
	DroppedTargets []PrometheusTarget `json:"droppedTargets"`
}

// PrometheusTarget represents a single scrape target from the API
type PrometheusTarget struct {
	DiscoveredLabels   map[string]string `json:"discoveredLabels"`
	Labels             map[string]string `json:"labels"`
	ScrapePool         string            `json:"scrapePool"`
	ScrapeURL          string            `json:"scrapeUrl"`
	GlobalURL          string            `json:"globalUrl"`
	LastError          string            `json:"lastError"`
	LastScrape         string            `json:"lastScrape"`
	LastScrapeDuration float64           `json:"lastScrapeDuration"`
	Health             string            `json:"health"` // "up", "down", "unknown"
	ScrapeInterval     string            `json:"scrapeInterval"`
	ScrapeTimeout      string            `json:"scrapeTimeout"`
}

// --- Client ---

// PrometheusClient handles API communication with in-cluster Prometheus
type PrometheusClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewPrometheusClient creates a new Prometheus API client with traced HTTP client
// for distributed tracing visibility into Prometheus API calls.
// Using TracedHTTPClientWithTransport provides client-side span visibility in Jaeger
// showing exact API call durations, even though Prometheus won't propagate traces.
func NewPrometheusClient(baseURL string) *PrometheusClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second // Prometheus queries can take time

	return &PrometheusClient{
		baseURL:    baseURL,
		httpClient: tracedClient,
	}
}

// parseSampleValue parses a Prometheus sample value tuple [timestamp, "value_string"]
// from json.RawMessage into (timestamp float64, value float64, error).
// Prometheus encodes sample values as strings, including special values like "NaN", "+Inf", "-Inf".
func parseSampleValue(raw json.RawMessage) (float64, float64, error) {
	var tuple []interface{}
	if err := json.Unmarshal(raw, &tuple); err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal value tuple: %w", err)
	}
	if len(tuple) != 2 {
		return 0, 0, fmt.Errorf("expected value tuple of length 2, got %d", len(tuple))
	}

	// Index 0: timestamp as float64
	ts, ok := tuple[0].(float64)
	if !ok {
		return 0, 0, fmt.Errorf("expected float64 timestamp, got %T", tuple[0])
	}

	// Index 1: value as string (must parse to float64)
	valStr, ok := tuple[1].(string)
	if !ok {
		return 0, 0, fmt.Errorf("expected string value, got %T", tuple[1])
	}

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		// Handle special Prometheus values
		switch valStr {
		case "NaN":
			val = math.NaN()
		case "+Inf":
			val = math.Inf(1)
		case "-Inf":
			val = math.Inf(-1)
		default:
			return 0, 0, fmt.Errorf("failed to parse value %q: %w", valStr, err)
		}
	}

	return ts, val, nil
}

// QueryInstant executes an instant PromQL query via /api/v1/query
func (c *PrometheusClient) QueryInstant(ctx context.Context, query string, evalTime string) (*PrometheusQueryData, []string, error) {
	endpoint := fmt.Sprintf("%s/api/v1/query", c.baseURL)

	params := url.Values{}
	params.Add("query", query)
	if evalTime != "" {
		params.Add("time", evalTime)
	}

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var promResp PrometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Check Prometheus-level status
	if promResp.Status == "error" {
		return nil, nil, fmt.Errorf("prometheus error (%s): %s", promResp.ErrorType, promResp.Error)
	}

	// Also check HTTP status (Prometheus returns 200 for most errors via envelope, but 400/422 for bad queries)
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var queryData PrometheusQueryData
	if err := json.Unmarshal(promResp.Data, &queryData); err != nil {
		return nil, nil, fmt.Errorf("failed to decode query data: %w", err)
	}

	return &queryData, promResp.Warnings, nil
}

// QueryRange executes a range PromQL query via /api/v1/query_range
func (c *PrometheusClient) QueryRange(ctx context.Context, query, start, end, step string) (*PrometheusQueryData, []string, error) {
	endpoint := fmt.Sprintf("%s/api/v1/query_range", c.baseURL)

	params := url.Values{}
	params.Add("query", query)
	params.Add("start", start)
	params.Add("end", end)
	params.Add("step", step)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var promResp PrometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if promResp.Status == "error" {
		return nil, nil, fmt.Errorf("prometheus error (%s): %s", promResp.ErrorType, promResp.Error)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var queryData PrometheusQueryData
	if err := json.Unmarshal(promResp.Data, &queryData); err != nil {
		return nil, nil, fmt.Errorf("failed to decode query data: %w", err)
	}

	return &queryData, promResp.Warnings, nil
}

// GetAlerts fetches currently firing alerts via /api/v1/alerts
func (c *PrometheusClient) GetAlerts(ctx context.Context) (*PrometheusAlertsData, []string, error) {
	endpoint := fmt.Sprintf("%s/api/v1/alerts", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var promResp PrometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if promResp.Status == "error" {
		return nil, nil, fmt.Errorf("prometheus error (%s): %s", promResp.ErrorType, promResp.Error)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var alertsData PrometheusAlertsData
	if err := json.Unmarshal(promResp.Data, &alertsData); err != nil {
		return nil, nil, fmt.Errorf("failed to decode alerts data: %w", err)
	}

	return &alertsData, promResp.Warnings, nil
}

// GetTargets fetches scrape targets via /api/v1/targets
func (c *PrometheusClient) GetTargets(ctx context.Context, state string) (*PrometheusTargetsData, []string, error) {
	endpoint := fmt.Sprintf("%s/api/v1/targets", c.baseURL)

	params := url.Values{}
	if state != "" && state != "any" {
		params.Add("state", state)
	}

	fullURL := endpoint
	if len(params) > 0 {
		fullURL = fmt.Sprintf("%s?%s", endpoint, params.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var promResp PrometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if promResp.Status == "error" {
		return nil, nil, fmt.Errorf("prometheus error (%s): %s", promResp.ErrorType, promResp.Error)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var targetsData PrometheusTargetsData
	if err := json.Unmarshal(promResp.Data, &targetsData); err != nil {
		return nil, nil, fmt.Errorf("failed to decode targets data: %w", err)
	}

	return &targetsData, promResp.Warnings, nil
}
