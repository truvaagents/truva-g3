package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// LokiClient wraps Loki's HTTP API with a traced HTTP client.
// Error messages include HTTP status codes for ClassifyUpstreamError compatibility:
//
//	fmt.Errorf("Loki API error (status %d): %s", resp.StatusCode, body)
type LokiClient struct {
	baseURL    string
	httpClient *http.Client
}

// defaultLokiHTTPTimeout bounds a single Loki HTTP request. Namespace-wide
// scans (a broad stream selector × a multi-hour window) routinely run for tens
// of seconds on a single-binary Loki under concurrent load, so the previous 30s
// cut them off mid-query and surfaced as "context deadline exceeded" 502s. 90s
// gives those scans room to finish while staying under the orchestrator's 120s
// per-step deadline, so the agent still gets a clean error instead of the step
// itself timing out. Override per-deployment with LOKI_HTTP_TIMEOUT.
const defaultLokiHTTPTimeout = 90 * time.Second

// lokiHTTPTimeout returns the Loki client timeout, honoring LOKI_HTTP_TIMEOUT
// when set to a positive Go duration (e.g. "60s", "2m"); otherwise it falls back
// to defaultLokiHTTPTimeout. An unset, empty, unparseable, or non-positive value
// uses the default.
func lokiHTTPTimeout() time.Duration {
	if v := os.Getenv("LOKI_HTTP_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultLokiHTTPTimeout
}

// NewLokiClient creates a Loki client with traced HTTP transport for Jaeger visibility.
func NewLokiClient(baseURL string) *LokiClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = lokiHTTPTimeout()

	return &LokiClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: tracedClient,
	}
}

// --- Loki API response structures ---

// lokiAPIResponse is the top-level envelope for all Loki API responses.
type lokiAPIResponse struct {
	Status string          `json:"status"` // "success" or "error"
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error,omitempty"`
}

// lokiQueryData is the data field for query/query_range responses.
type lokiQueryData struct {
	ResultType string          `json:"resultType"` // "streams" or "matrix"
	Result     json.RawMessage `json:"result"`
}

// lokiStream is a single stream in a Loki query result.
type lokiStream struct {
	Stream map[string]string `json:"stream"` // Label key-value pairs
	Values [][]string        `json:"values"` // [[timestamp_ns, line], ...]
}

// lokiDetectedField is a single field from /detected_fields.
type lokiDetectedField struct {
	Label       string `json:"label"`
	Type        string `json:"type"`
	Cardinality int    `json:"cardinality"`
}

// --- Query methods ---

// QueryRange calls /loki/api/v1/query_range and returns parsed log streams.
func (c *LokiClient) QueryRange(ctx context.Context, query, start, end, step, direction string, limit int) ([]lokiStream, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", start)
	params.Set("end", end)
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if direction != "" {
		params.Set("direction", direction)
	}
	if step != "" {
		params.Set("step", step)
	}

	reqURL := fmt.Sprintf("%s/loki/api/v1/query_range?%s", c.baseURL, params.Encode())
	apiResp, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var queryData lokiQueryData
	if err := json.Unmarshal(apiResp.Data, &queryData); err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to parse query data: %w", err)
	}

	var streams []lokiStream
	if err := json.Unmarshal(queryData.Result, &streams); err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to parse streams: %w", err)
	}

	return streams, nil
}

// Labels calls /loki/api/v1/labels and returns label names.
func (c *LokiClient) Labels(ctx context.Context, start, end string) ([]string, error) {
	params := url.Values{}
	if start != "" {
		params.Set("start", start)
	}
	if end != "" {
		params.Set("end", end)
	}

	reqURL := fmt.Sprintf("%s/loki/api/v1/labels?%s", c.baseURL, params.Encode())
	apiResp, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var labels []string
	if err := json.Unmarshal(apiResp.Data, &labels); err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to parse labels: %w", err)
	}

	return labels, nil
}

// LabelValues calls /loki/api/v1/label/{name}/values and returns values.
func (c *LokiClient) LabelValues(ctx context.Context, label, start, end, query string) ([]string, error) {
	params := url.Values{}
	if start != "" {
		params.Set("start", start)
	}
	if end != "" {
		params.Set("end", end)
	}
	if query != "" {
		params.Set("query", query)
	}

	reqURL := fmt.Sprintf("%s/loki/api/v1/label/%s/values?%s", c.baseURL, url.PathEscape(label), params.Encode())
	apiResp, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var values []string
	if err := json.Unmarshal(apiResp.Data, &values); err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to parse label values: %w", err)
	}

	return values, nil
}

// DetectedFields calls /loki/api/v1/detected_fields and returns field metadata.
func (c *LokiClient) DetectedFields(ctx context.Context, query, start, end string, limit int) ([]lokiDetectedField, error) {
	params := url.Values{}
	params.Set("query", query)
	if start != "" {
		params.Set("start", start)
	}
	if end != "" {
		params.Set("end", end)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	reqURL := fmt.Sprintf("%s/loki/api/v1/detected_fields?%s", c.baseURL, params.Encode())

	// detected_fields returns a different envelope: {"fields": [...]}
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki API error (status 502): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("loki API error (status 502): failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki API error (status %d): %s", resp.StatusCode, truncateBody(body))
	}

	// Parse the detected_fields-specific response format
	var result struct {
		Fields []lokiDetectedField `json:"fields"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to parse detected fields: %w", err)
	}

	return result.Fields, nil
}

// --- Internal helpers ---

// doRequest performs a Loki HTTP GET and parses the standard API envelope.
func (c *LokiClient) doRequest(ctx context.Context, reqURL string) (*lokiAPIResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki API error (status 502): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("loki API error (status 502): failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki API error (status %d): %s", resp.StatusCode, truncateBody(body))
	}

	var apiResp lokiAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("loki API error (status 500): failed to parse response envelope: %w", err)
	}

	if apiResp.Status == "error" {
		return nil, fmt.Errorf("loki API error (status 400): %s", apiResp.Error)
	}

	return &apiResp, nil
}

// truncateBody limits error body to 500 chars for log readability.
func truncateBody(body []byte) string {
	s := string(body)
	if len(s) > 500 {
		return s[:500] + "...[truncated]"
	}
	return s
}

// stripCRIPrefix removes the CRI log format prefix from a log line.
// CRI format: "2026-03-26T22:20:30.482038701Z stdout F {json...}"
// Returns the content after "stdout F " or "stderr F ".
func stripCRIPrefix(line string) string {
	// Look for " stdout F " or " stderr F " markers
	for _, marker := range []string{" stdout F ", " stderr F ", " stdout P ", " stderr P "} {
		if idx := strings.Index(line, marker); idx >= 0 {
			return line[idx+len(marker):]
		}
	}
	return line
}
