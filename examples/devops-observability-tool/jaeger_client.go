package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// JaegerClient wraps Jaeger's v2 HTTP API with a traced HTTP client.
// Error messages include HTTP status codes for ClassifyUpstreamError compatibility:
//
//	fmt.Errorf("Jaeger API error (status %d): %s", resp.StatusCode, body)
type JaegerClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewJaegerClient creates a Jaeger client with traced HTTP transport for Jaeger visibility.
func NewJaegerClient(baseURL string) *JaegerClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &JaegerClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: tracedClient,
	}
}

// --- Jaeger API response structures ---

// jaegerTrace is a single trace from the Jaeger v2 API.
type jaegerTrace struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

// jaegerSpan is a single span within a trace.
type jaegerSpan struct {
	TraceID       string      `json:"traceID"`
	SpanID        string      `json:"spanID"`
	ParentSpanID  string      `json:"parentSpanID,omitempty"`
	OperationName string      `json:"operationName"`
	StartTime     int64       `json:"startTime"` // microseconds since epoch
	Duration      int64       `json:"duration"`  // microseconds
	Tags          []jaegerTag `json:"tags"`
	Logs          []jaegerLog `json:"logs"`
	ProcessID     string      `json:"processID"`
	Warnings      []string    `json:"warnings"`
}

// jaegerTag is a key-value pair on a span.
type jaegerTag struct {
	Key   string      `json:"key"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

// jaegerLog is a timestamped log entry on a span.
type jaegerLog struct {
	Timestamp int64       `json:"timestamp"`
	Fields    []jaegerTag `json:"fields"`
}

// jaegerProcess identifies the service that produced a span.
type jaegerProcess struct {
	ServiceName string      `json:"serviceName"`
	Tags        []jaegerTag `json:"tags"`
}

// --- Query methods ---

// Services calls GET /api/services and returns service names.
func (c *JaegerClient) Services(ctx context.Context) ([]string, error) {
	reqURL := fmt.Sprintf("%s/api/services", c.baseURL)

	body, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger API error (status 500): failed to parse services: %w", err)
	}

	return resp.Data, nil
}

// Operations calls GET /api/services/{service}/operations and returns operation names.
func (c *JaegerClient) Operations(ctx context.Context, service string) ([]string, error) {
	reqURL := fmt.Sprintf("%s/api/services/%s/operations", c.baseURL, url.PathEscape(service))

	body, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger API error (status 500): failed to parse operations: %w", err)
	}

	return resp.Data, nil
}

// FindTraces calls GET /api/traces with query parameters and returns traces.
func (c *JaegerClient) FindTraces(ctx context.Context, service, operation, lookback, minDuration, maxDuration string, limit int) ([]jaegerTrace, error) {
	params := url.Values{}
	params.Set("service", service)
	if operation != "" {
		params.Set("operation", operation)
	}
	if lookback != "" {
		params.Set("lookback", lookback)
	}
	if minDuration != "" {
		params.Set("minDuration", minDuration)
	}
	if maxDuration != "" {
		params.Set("maxDuration", maxDuration)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	// Jaeger v2 API requires end time
	params.Set("end", strconv.FormatInt(time.Now().UnixMicro(), 10))

	reqURL := fmt.Sprintf("%s/api/traces?%s", c.baseURL, params.Encode())

	body, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data []jaegerTrace `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger API error (status 500): failed to parse traces: %w", err)
	}

	return resp.Data, nil
}

// GetTrace calls GET /api/traces/{traceID} and returns a single trace.
func (c *JaegerClient) GetTrace(ctx context.Context, traceID string) (*jaegerTrace, error) {
	reqURL := fmt.Sprintf("%s/api/traces/%s", c.baseURL, url.PathEscape(traceID))

	body, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data []jaegerTrace `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger API error (status 500): failed to parse trace: %w", err)
	}

	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("jaeger API error (status 404): trace %s not found", traceID)
	}

	return &resp.Data[0], nil
}

// --- Internal helpers ---

// doRequest performs a Jaeger HTTP GET and returns the raw response body.
func (c *JaegerClient) doRequest(ctx context.Context, reqURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jaeger API error (status 500): failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jaeger API error (status 502): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jaeger API error (status 502): failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jaeger API error (status %d): %s", resp.StatusCode, truncateBody(body))
	}

	return body, nil
}

// --- Trace conversion helpers ---

// extractRequestID finds the request_id tag value from a trace's spans.
func extractRequestID(trace *jaegerTrace) string {
	for _, span := range trace.Spans {
		for _, tag := range span.Tags {
			if tag.Key == "request_id" {
				if s, ok := tag.Value.(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// extractServices returns unique service names from a trace's processes.
func extractServices(trace *jaegerTrace) []string {
	seen := make(map[string]bool)
	var services []string
	for _, p := range trace.Processes {
		if !seen[p.ServiceName] {
			seen[p.ServiceName] = true
			services = append(services, p.ServiceName)
		}
	}
	return services
}

// extractRootOperation finds the root span's operation name.
func extractRootOperation(trace *jaegerTrace) string {
	for _, span := range trace.Spans {
		if span.ParentSpanID == "" || span.ParentSpanID == "0000000000000000" {
			return span.OperationName
		}
	}
	if len(trace.Spans) > 0 {
		return trace.Spans[0].OperationName
	}
	return ""
}

// spanStartTimeToRFC3339 converts microsecond epoch to RFC3339.
func spanStartTimeToRFC3339(startTimeMicros int64) string {
	return time.Unix(0, startTimeMicros*1000).UTC().Format(time.RFC3339Nano)
}

// tagValueToString converts a jaegerTag value to string.
func tagValueToString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
