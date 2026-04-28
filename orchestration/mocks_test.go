package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/truvaagents/truva-g3/core"
)

// MockHTTPClient implements a mock HTTP client for testing
type MockHTTPClient struct {
	responses map[string]*http.Response
	errors    map[string]error
	calls     []string
}

// NewMockHTTPClient creates a new mock HTTP client
func NewMockHTTPClient() *MockHTTPClient {
	return &MockHTTPClient{
		responses: make(map[string]*http.Response),
		errors:    make(map[string]error),
		calls:     []string{},
	}
}

// Do implements the http.Client Do method
func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	m.calls = append(m.calls, url)

	// Check if we have a specific error for this URL
	if err, ok := m.errors[url]; ok {
		return nil, err
	}

	// Check if we have a specific response for this URL
	if resp, ok := m.responses[url]; ok {
		return resp, nil
	}

	// Default successful response
	body := map[string]interface{}{
		"status": "success",
		"data":   "mock response",
	}
	bodyBytes, _ := json.Marshal(body)

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(bodyBytes)),
		Header:     make(http.Header),
	}, nil
}

// SetResponse sets a specific response for a URL
func (m *MockHTTPClient) SetResponse(url string, statusCode int, body interface{}) {
	var bodyReader io.ReadCloser

	if body != nil {
		bodyBytes, _ := json.Marshal(body)
		bodyReader = io.NopCloser(bytes.NewReader(bodyBytes))
	} else {
		bodyReader = io.NopCloser(strings.NewReader(""))
	}

	m.responses[url] = &http.Response{
		StatusCode: statusCode,
		Body:       bodyReader,
		Header:     make(http.Header),
	}
}

// SetError sets an error for a specific URL
func (m *MockHTTPClient) SetError(url string, err error) {
	m.errors[url] = err
}

// GetCalls returns all the URLs that were called
func (m *MockHTTPClient) GetCalls() []string {
	return m.calls
}

// MockRoundTripper implements http.RoundTripper for testing
type MockRoundTripper struct {
	mu        sync.Mutex
	responses map[string]MockResponse
	errors    map[string]error
	callCount map[string]int
}

// MockResponse stores response data
type MockResponse struct {
	StatusCode int
	Body       string
	Header     http.Header
}

// NewMockRoundTripper creates a new mock round tripper
func NewMockRoundTripper() *MockRoundTripper {
	return &MockRoundTripper{
		responses: make(map[string]MockResponse),
		errors:    make(map[string]error),
		callCount: make(map[string]int),
	}
}

// RoundTrip implements the http.RoundTripper interface
func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	url := req.URL.String()
	m.callCount[url]++
	count := m.callCount[url]
	m.mu.Unlock()

	// Check for errors first
	if err, ok := m.errors[url]; ok {
		// Check if this is a retry scenario
		if retryErr, ok := m.errors[fmt.Sprintf("%s_attempt_%d", url, count)]; ok {
			return nil, retryErr
		}
		return nil, err
	}

	// Check for specific responses
	if resp, ok := m.responses[url]; ok {
		// Check if this is a retry scenario with different responses
		if retryResp, ok := m.responses[fmt.Sprintf("%s_attempt_%d", url, count)]; ok {
			return &http.Response{
				StatusCode: retryResp.StatusCode,
				Body:       io.NopCloser(strings.NewReader(retryResp.Body)),
				Header:     retryResp.Header,
			}, nil
		}
		return &http.Response{
			StatusCode: resp.StatusCode,
			Body:       io.NopCloser(strings.NewReader(resp.Body)),
			Header:     resp.Header,
		}, nil
	}

	// Default response
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		Header:     make(http.Header),
	}, nil
}

// SetResponse sets a response for a URL
func (m *MockRoundTripper) SetResponse(url string, statusCode int, body string) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	m.responses[url] = MockResponse{
		StatusCode: statusCode,
		Body:       body,
		Header:     header,
	}
}

// SetRetryResponses sets different responses for retry attempts
func (m *MockRoundTripper) SetRetryResponses(url string, responses []struct {
	StatusCode int
	Body       string
}) {
	for i, resp := range responses {
		key := url
		if i > 0 {
			key = fmt.Sprintf("%s_attempt_%d", url, i+1)
		}
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		m.responses[key] = MockResponse{
			StatusCode: resp.StatusCode,
			Body:       resp.Body,
			Header:     header,
		}
	}
}

// SetError sets an error for a URL
func (m *MockRoundTripper) SetError(url string, err error) {
	m.errors[url] = err
}

// GetCallCount returns the total number of HTTP calls made
func (m *MockRoundTripper) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, count := range m.callCount {
		total += count
	}
	return total
}

// HTTPClient interface for mocking
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Ensure MockHTTPClient implements HTTPClient
var _ HTTPClient = (*MockHTTPClient)(nil)

// MockAgentServer simulates an agent server for testing
type MockAgentServer struct {
	capabilities []EnhancedCapability
	responses    map[string]interface{}
}

// NewMockAgentServer creates a new mock agent server
func NewMockAgentServer() *MockAgentServer {
	return &MockAgentServer{
		capabilities: []EnhancedCapability{
			{
				Name:        "test_capability",
				Description: "Test capability",
				Endpoint:    "/api/test",
			},
		},
		responses: make(map[string]interface{}),
	}
}

// HandleCapabilities handles the capabilities endpoint
func (m *MockAgentServer) HandleCapabilities() (int, interface{}) {
	return http.StatusOK, m.capabilities
}

// HandleRequest handles a capability request
func (m *MockAgentServer) HandleRequest(capability string, params interface{}) (int, interface{}) {
	if resp, ok := m.responses[capability]; ok {
		return http.StatusOK, resp
	}
	return http.StatusOK, map[string]interface{}{
		"status": "success",
		"result": "mock result",
	}
}

// SetCapabilities sets the capabilities
func (m *MockAgentServer) SetCapabilities(caps []EnhancedCapability) {
	m.capabilities = caps
}

// SetResponse sets a response for a capability
func (m *MockAgentServer) SetResponse(capability string, response interface{}) {
	m.responses[capability] = response
}

// MockDiscovery for unit tests
type MockDiscovery struct {
	services map[string][]*core.ServiceRegistration
}

func NewMockDiscovery() *MockDiscovery {
	return &MockDiscovery{
		services: make(map[string][]*core.ServiceRegistration),
	}
}

func (m *MockDiscovery) Register(ctx context.Context, registration *core.ServiceRegistration) error {
	m.services[registration.Name] = append(m.services[registration.Name], registration)
	return nil
}

func (m *MockDiscovery) Unregister(ctx context.Context, serviceID string) error {
	return nil
}

func (m *MockDiscovery) FindService(ctx context.Context, serviceName string) ([]*core.ServiceRegistration, error) {
	return m.services[serviceName], nil
}

func (m *MockDiscovery) FindByCapability(ctx context.Context, capability string) ([]*core.ServiceRegistration, error) {
	var results []*core.ServiceRegistration
	for _, services := range m.services {
		for _, service := range services {
			for _, cap := range service.Capabilities {
				if cap.Name == capability {
					results = append(results, service)
					break
				}
			}
		}
	}
	return results, nil
}

func (m *MockDiscovery) FindByType(ctx context.Context, serviceType core.ComponentType) ([]*core.ServiceRegistration, error) {
	var results []*core.ServiceRegistration
	for _, services := range m.services {
		for _, service := range services {
			if service.Type == serviceType {
				results = append(results, service)
			}
		}
	}
	return results, nil
}

func (m *MockDiscovery) Health(ctx context.Context) error {
	return nil
}

func (m *MockDiscovery) UpdateHealth(ctx context.Context, serviceID string, health core.HealthStatus) error {
	// Mock implementation - just return success
	return nil
}

func (m *MockDiscovery) Discover(ctx context.Context, filter core.DiscoveryFilter) ([]*core.ServiceInfo, error) {
	var results []*core.ServiceInfo
	for _, services := range m.services {
		for _, service := range services {
			// Convert ServiceRegistration to ServiceInfo
			serviceInfo := &core.ServiceInfo{
				ID:           service.ID,
				Name:         service.Name,
				Type:         service.Type,
				Address:      service.Address,
				Port:         service.Port,
				Capabilities: service.Capabilities,
				Health:       core.HealthHealthy,
			}

			// Apply filter if needed (simplified for mock)
			if filter.Type != "" && filter.Type != service.Type {
				continue
			}
			if filter.Name != "" && filter.Name != service.Name {
				continue
			}
			results = append(results, serviceInfo)
		}
	}
	return results, nil
}

// Note: stringContains helper is defined in capability_provider_test.go
