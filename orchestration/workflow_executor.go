package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// WorkflowExecutor handles service calls for workflow steps
type WorkflowExecutor struct {
	discovery core.Discovery
	client    *WorkflowHTTPClient
	logger    core.Logger // For structured logging
}

// WorkflowHTTPClient wraps HTTP client for service calls
type WorkflowHTTPClient struct {
	httpClient        *http.Client
	oauthToken        atomic.Value // stores string — thread-safe for runtime refresh
	propagatedHeaders atomic.Value // stores map[string]string — same pattern as oauthToken
}

// NewWorkflowHTTPClient creates a new HTTP client for workflows.
// Uses TracedHTTPClient for distributed tracing context propagation.
func NewWorkflowHTTPClient() *WorkflowHTTPClient {
	tracedClient := telemetry.NewTracedHTTPClient(nil)

	// Configurable timeout: TRUVAG3_ORCHESTRATION_TIMEOUT (default: 120s)
	// For long-running AI workflows, set to higher values (e.g., "5m", "10m")
	timeout := 120 * time.Second
	if envTimeout := os.Getenv("TRUVAG3_ORCHESTRATION_TIMEOUT"); envTimeout != "" {
		if parsed, err := time.ParseDuration(envTimeout); err == nil {
			timeout = parsed
		}
	}
	tracedClient.Timeout = timeout

	client := &WorkflowHTTPClient{
		httpClient: tracedClient,
	}
	if token := os.Getenv("TRUVAG3_OAUTH_TOKEN"); token != "" {
		client.oauthToken.Store(token)
	}
	return client
}

// SetOAuthToken sets the default Bearer token for outbound HTTP requests.
// Thread-safe: may be called at runtime from a token refresh goroutine.
func (c *WorkflowHTTPClient) SetOAuthToken(token string) {
	c.oauthToken.Store(token)
}

// getOAuthToken returns the configured default Bearer token (thread-safe).
func (c *WorkflowHTTPClient) getOAuthToken() string {
	if v := c.oauthToken.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SetPropagatedHeaders sets the default custom headers for outbound HTTP requests.
// Thread-safe: defensive copy prevents external mutation after storing.
func (c *WorkflowHTTPClient) SetPropagatedHeaders(headers map[string]string) {
	cpy := make(map[string]string, len(headers))
	for k, v := range headers {
		cpy[k] = v
	}
	c.propagatedHeaders.Store(cpy)
}

// getPropagatedHeaders returns the configured default custom headers (thread-safe).
func (c *WorkflowHTTPClient) getPropagatedHeaders() map[string]string {
	if v := c.propagatedHeaders.Load(); v != nil {
		return v.(map[string]string)
	}
	return nil
}

// CallService calls a service endpoint with the given action and inputs
func (e *WorkflowExecutor) CallService(ctx context.Context, service *core.ServiceRegistration, action string, inputs map[string]interface{}) (map[string]interface{}, error) {
	// Construct service URL
	url := fmt.Sprintf("http://%s:%d/%s", service.Address, service.Port, action)

	// Prepare request body
	requestBody, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// OAuth Bearer token: context token > configured token > none
	if token := GetOAuthToken(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if configToken := e.client.getOAuthToken(); configToken != "" {
		req.Header.Set("Authorization", "Bearer "+configToken)
	}

	// Propagated headers: config defaults first, then context overrides.
	// Reserved headers (Authorization, Content-Type, X-TruvaG3-*) are skipped.
	if configHeaders := e.client.getPropagatedHeaders(); len(configHeaders) > 0 {
		for k, v := range configHeaders {
			if !isReservedPropagationHeader(k) {
				req.Header.Set(k, v)
			}
		}
	}
	if ctxHeaders := GetPropagatedHeaders(ctx); len(ctxHeaders) > 0 {
		for k, v := range ctxHeaders {
			if !isReservedPropagationHeader(k) {
				req.Header.Set(k, v)
			}
		}
	}

	if workflowID := ctx.Value("workflow_id"); workflowID != nil {
		req.Header.Set("X-Workflow-ID", workflowID.(string))
	}
	if stepID := ctx.Value("step_id"); stepID != nil {
		req.Header.Set("X-Step-ID", stepID.(string))
	}

	// Execute request
	resp, err := e.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service returned status %d: %s", resp.StatusCode, string(responseBody))
	}

	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return result, nil
}

// CallAgent calls an agent with discovery lookup
func (e *WorkflowExecutor) CallAgent(ctx context.Context, agentName string, action string, inputs map[string]interface{}) (map[string]interface{}, error) {
	// Find agent using discovery
	services, err := e.discovery.FindService(ctx, agentName)
	if err != nil {
		return nil, fmt.Errorf("finding agent %s: %w", agentName, err)
	}

	if len(services) == 0 {
		return nil, fmt.Errorf("agent %s: %w", agentName, core.ErrAgentNotFound)
	}

	// Select best service (first healthy one)
	var service *core.ServiceRegistration
	for _, svc := range services {
		if svc.Health == core.HealthHealthy {
			service = svc
			break
		}
	}

	if service == nil {
		// No healthy service, use first one
		service = services[0]
	}

	return e.CallService(ctx, service, action, inputs)
}

// CallCapability calls any service with the specified capability
func (e *WorkflowExecutor) CallCapability(ctx context.Context, capability string, action string, inputs map[string]interface{}) (map[string]interface{}, error) {
	// Find services by capability
	services, err := e.discovery.FindByCapability(ctx, capability)
	if err != nil {
		return nil, fmt.Errorf("finding capability %s: %w", capability, err)
	}

	if len(services) == 0 {
		return nil, fmt.Errorf("no services with capability %s: %w", capability, core.ErrCapabilityNotFound)
	}

	// Select best service
	var service *core.ServiceRegistration
	for _, svc := range services {
		if svc.Health == core.HealthHealthy {
			service = svc
			break
		}
	}

	if service == nil {
		service = services[0]
	}

	return e.CallService(ctx, service, action, inputs)
}

// BatchCall executes multiple service calls in parallel
func (e *WorkflowExecutor) BatchCall(ctx context.Context, calls []ServiceCall) []ServiceCallResult {
	results := make([]ServiceCallResult, len(calls))

	// Execute calls in parallel
	type indexedResult struct {
		index  int
		result ServiceCallResult
	}

	resultChan := make(chan indexedResult, len(calls))

	for i, call := range calls {
		go func(idx int, c ServiceCall) {
			defer func() {
				if r := recover(); r != nil {
					// Capture panic and convert to error result
					panicErr := fmt.Errorf("service call %s panic: %v", c.ID, r)
					stackTrace := string(debug.Stack())

					// Try to send result with timeout to avoid blocking
					sendTimeout := time.After(5 * time.Second)
					select {
					case resultChan <- indexedResult{
						index: idx,
						result: ServiceCallResult{
							CallID:  c.ID,
							Success: false,
							Error:   panicErr.Error(),
							Output: map[string]interface{}{
								"panic":       fmt.Sprintf("%v", r), // The panic value for debugging
								"call_id":     c.ID,                 // Identifies which call failed
								"call_type":   c.Type,               // Type of call (agent/capability)
								"target":      c.Target,             // Target service or capability
								"stack_trace": stackTrace,           // Full stack trace for debugging
							},
						},
					}:
						// Successfully sent panic result
					case <-sendTimeout:
						// Timeout occurred while sending panic result.
						// This indicates the result channel might be blocked or closed.
						// In production, this should be logged for monitoring.
						// TODO: Add proper logging/metrics here
						_ = panicErr // Prevent unused variable warning
					}
				}
			}()

			var result ServiceCallResult
			result.CallID = c.ID

			// Execute based on call type
			var output map[string]interface{}
			var err error

			switch c.Type {
			case CallTypeAgent:
				output, err = e.CallAgent(ctx, c.Target, c.Action, c.Inputs)
			case CallTypeCapability:
				output, err = e.CallCapability(ctx, c.Target, c.Action, c.Inputs)
			default:
				err = fmt.Errorf("unknown call type: %s", c.Type)
			}

			if err != nil {
				result.Error = err.Error()
			} else {
				result.Success = true
				result.Output = output
			}

			resultChan <- indexedResult{index: idx, result: result}
		}(i, call)
	}

	// Collect results
	for i := 0; i < len(calls); i++ {
		r := <-resultChan
		results[r.index] = r.result
	}

	return results
}

// ServiceCall represents a service call request
type ServiceCall struct {
	ID     string                 `json:"id"`
	Type   CallType               `json:"type"`
	Target string                 `json:"target"` // Agent name or capability
	Action string                 `json:"action"`
	Inputs map[string]interface{} `json:"inputs"`
}

// CallType defines the type of service call
type CallType string

const (
	CallTypeAgent      CallType = "agent"
	CallTypeCapability CallType = "capability"
)

// ServiceCallResult represents the result of a service call
type ServiceCallResult struct {
	CallID  string                 `json:"call_id"`
	Success bool                   `json:"success"`
	Output  map[string]interface{} `json:"output,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// HealthCheck checks if a service is healthy
func (e *WorkflowExecutor) HealthCheck(ctx context.Context, service *core.ServiceRegistration) bool {
	url := fmt.Sprintf("http://%s:%d/health", service.Address, service.Port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	// OAuth Bearer token for health checks
	if token := GetOAuthToken(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if configToken := e.client.getOAuthToken(); configToken != "" {
		req.Header.Set("Authorization", "Bearer "+configToken)
	}

	// Propagated headers: config defaults first, then context overrides.
	// Reserved headers (Authorization, Content-Type, X-TruvaG3-*) are skipped.
	if configHeaders := e.client.getPropagatedHeaders(); len(configHeaders) > 0 {
		for k, v := range configHeaders {
			if !isReservedPropagationHeader(k) {
				req.Header.Set(k, v)
			}
		}
	}
	if ctxHeaders := GetPropagatedHeaders(ctx); len(ctxHeaders) > 0 {
		for k, v := range ctxHeaders {
			if !isReservedPropagationHeader(k) {
				req.Header.Set(k, v)
			}
		}
	}

	resp, err := e.client.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}
