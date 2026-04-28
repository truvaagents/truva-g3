//go:build integration
// +build integration

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestServiceCapabilityProvider_EnvironmentConfiguration tests environment variable configuration
func TestServiceCapabilityProvider_EnvironmentConfiguration(t *testing.T) {
	tests := []struct {
		name              string
		envVars           map[string]string
		config            *ServiceCapabilityConfig
		expectedTopK      int
		expectedThreshold float64
		expectedEndpoint  string
	}{
		{
			name: "TRUVAG3_ prefixed variables",
			envVars: map[string]string{
				"TRUVAG3_CAPABILITY_SERVICE_URL": "http://env-service:8080",
				"TRUVAG3_CAPABILITY_TOP_K":       "30",
				"TRUVAG3_CAPABILITY_THRESHOLD":   "0.85",
			},
			config:            &ServiceCapabilityConfig{},
			expectedEndpoint:  "http://env-service:8080",
			expectedTopK:      30,
			expectedThreshold: 0.85,
		},
		{
			name: "Standard CAPABILITY_SERVICE_URL",
			envVars: map[string]string{
				"CAPABILITY_SERVICE_URL": "http://standard-service:9090",
			},
			config:            &ServiceCapabilityConfig{},
			expectedEndpoint:  "http://standard-service:9090",
			expectedTopK:      20,  // default
			expectedThreshold: 0.7, // default
		},
		{
			name: "Config overrides environment",
			envVars: map[string]string{
				"TRUVAG3_CAPABILITY_TOP_K": "100",
			},
			config: &ServiceCapabilityConfig{
				TopK: 50, // explicit config wins
			},
			expectedTopK:      50,
			expectedThreshold: 0.7, // default
		},
		{
			name: "Invalid environment values use defaults",
			envVars: map[string]string{
				"TRUVAG3_CAPABILITY_TOP_K":     "invalid",
				"TRUVAG3_CAPABILITY_THRESHOLD": "invalid",
			},
			config:            &ServiceCapabilityConfig{},
			expectedTopK:      20,  // default
			expectedThreshold: 0.7, // default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				oldVal := os.Getenv(k)
				os.Setenv(k, v)
				defer os.Setenv(k, oldVal)
			}

			provider := NewServiceCapabilityProvider(tt.config)

			if provider.endpoint != tt.expectedEndpoint {
				t.Errorf("Expected endpoint %s, got %s", tt.expectedEndpoint, provider.endpoint)
			}
			if provider.topK != tt.expectedTopK {
				t.Errorf("Expected topK %d, got %d", tt.expectedTopK, provider.topK)
			}
			if provider.threshold != tt.expectedThreshold {
				t.Errorf("Expected threshold %f, got %f", tt.expectedThreshold, provider.threshold)
			}
		})
	}
}

// TestServiceCapabilityProvider_RetryLogic tests the built-in retry mechanism
func TestServiceCapabilityProvider_RetryLogic(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt < 3 {
			// Fail first 2 attempts
			http.Error(w, "temporary error", http.StatusInternalServerError)
			return
		}
		// Succeed on 3rd attempt
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CapabilityResponse{
			Capabilities: "success after retries",
		})
	}))
	defer server.Close()

	config := &ServiceCapabilityConfig{
		Endpoint: server.URL,
		Timeout:  5 * time.Second,
	}
	provider := NewServiceCapabilityProvider(config)

	ctx := context.Background()
	capabilities, err := provider.GetCapabilities(ctx, "test", nil)

	if err != nil {
		t.Fatalf("Expected success after retries, got error: %v", err)
	}
	if capabilities != "success after retries" {
		t.Errorf("Expected 'success after retries', got %s", capabilities)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

// TestServiceCapabilityProvider_CircuitBreaker tests the simple circuit breaker behavior
func TestServiceCapabilityProvider_CircuitBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "always fails", http.StatusInternalServerError)
	}))
	defer server.Close()

	config := &ServiceCapabilityConfig{
		Endpoint: server.URL,
		Timeout:  100 * time.Millisecond, // Short timeout for faster test
	}
	provider := NewServiceCapabilityProvider(config)
	// Disable retries for this test to ensure each request = one failure
	provider.retryAttempts = 0

	// Force circuit to open by making multiple failed requests
	var lastErr error
	for i := 0; i < 6; i++ { // Need 5 failures to open circuit
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := provider.GetCapabilities(ctx, "test", nil)
		if err != nil {
			lastErr = err
		}
		cancel()
		time.Sleep(10 * time.Millisecond) // Small delay between requests
	}

	if lastErr == nil {
		t.Fatal("Expected errors from failed requests")
	}

	// Now circuit should be open
	if !provider.isCircuitOpen() {
		t.Error("Expected circuit to be open after 5+ failures")
	}

	// Request should fail immediately when circuit is open
	start := time.Now()
	_, err := provider.GetCapabilities(context.Background(), "test", nil)
	duration := time.Since(start)

	if err == nil {
		t.Error("Expected error when circuit is open")
	}
	if duration > 100*time.Millisecond {
		t.Errorf("Expected immediate failure when circuit open, took %v", duration)
	}
}

// TestServiceCapabilityProvider_ContextCancellation tests context cancellation handling
func TestServiceCapabilityProvider_ContextCancellation(t *testing.T) {
	blockChan := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockChan // Block until test cancels context
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(blockChan)

	config := &ServiceCapabilityConfig{
		Endpoint: server.URL,
		Timeout:  5 * time.Second,
	}
	provider := NewServiceCapabilityProvider(config)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := provider.GetCapabilities(ctx, "test", nil)
	if err == nil {
		t.Error("Expected context cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

// TestServiceCapabilityProvider_ConcurrentRequests tests concurrent request handling
func TestServiceCapabilityProvider_ConcurrentRequests(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CapabilityResponse{
			Capabilities: fmt.Sprintf("response-%d", count),
		})
	}))
	defer server.Close()

	config := &ServiceCapabilityConfig{
		Endpoint: server.URL,
		Timeout:  1 * time.Second,
	}
	provider := NewServiceCapabilityProvider(config)

	// Make 10 concurrent requests
	var wg sync.WaitGroup
	results := make([]string, 10)
	errors := make([]error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := provider.GetCapabilities(context.Background(), fmt.Sprintf("request-%d", idx), nil)
			results[idx] = result
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// Check all requests succeeded
	for i, err := range errors {
		if err != nil {
			t.Errorf("Request %d failed: %v", i, err)
		}
	}

	// Check we got 10 requests
	if atomic.LoadInt32(&requestCount) != 10 {
		t.Errorf("Expected 10 requests, got %d", requestCount)
	}
}

// TestServiceCapabilityProvider_InjectedDependencies tests injected circuit breaker and logger
func TestServiceCapabilityProvider_InjectedDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer server.Close()

	// Create mock circuit breaker
	cbCalled := false
	mockCB := &mockCircuitBreaker{
		executeFunc: func(ctx context.Context, fn func() error) error {
			cbCalled = true
			return errors.New("circuit breaker error")
		},
	}

	// Create mock logger
	var logMessages []string
	mockLogger := &mockLogger{
		debugFunc: func(msg string, fields map[string]interface{}) {
			logMessages = append(logMessages, msg)
		},
		errorFunc: func(msg string, fields map[string]interface{}) {
			logMessages = append(logMessages, msg)
		},
	}

	// Create fallback provider
	fallbackCalled := false
	fallback := &mockCapabilityProvider{
		response: "fallback response",
		onCall: func() {
			fallbackCalled = true
		},
	}

	config := &ServiceCapabilityConfig{
		Endpoint:         server.URL,
		CircuitBreaker:   mockCB,
		Logger:           mockLogger,
		FallbackProvider: fallback,
	}
	provider := NewServiceCapabilityProvider(config)

	capabilities, err := provider.GetCapabilities(context.Background(), "test", nil)

	// Circuit breaker should be called
	if !cbCalled {
		t.Error("Expected circuit breaker to be called")
	}

	// Fallback should be called after circuit breaker error
	if !fallbackCalled {
		t.Error("Expected fallback to be called")
	}

	// Should get fallback response
	if capabilities != "fallback response" {
		t.Errorf("Expected fallback response, got %s", capabilities)
	}

	// Should have no error (fallback succeeded)
	if err != nil {
		t.Errorf("Expected no error with fallback, got %v", err)
	}

	// Logger should have been called
	if len(logMessages) == 0 {
		t.Error("Expected log messages")
	}
}

// TestServiceCapabilityProvider_HealthCheck tests health check functionality
func TestServiceCapabilityProvider_HealthCheck(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse int
		expectError    bool
	}{
		{
			name:           "healthy service",
			serverResponse: http.StatusOK,
			expectError:    false,
		},
		{
			name:           "unhealthy service",
			serverResponse: http.StatusServiceUnavailable,
			expectError:    true,
		},
		{
			name:           "server error",
			serverResponse: http.StatusInternalServerError,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(tt.serverResponse)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			config := &ServiceCapabilityConfig{
				Endpoint: server.URL,
			}
			provider := NewServiceCapabilityProvider(config)

			err := provider.Health(context.Background())
			if tt.expectError && err == nil {
				t.Error("Expected health check error")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected healthy status, got error: %v", err)
			}
		})
	}
}

// TestServiceCapabilityProvider_InvalidResponses tests handling of invalid server responses
func TestServiceCapabilityProvider_InvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		statusCode  int
		expectError bool
	}{
		{
			name:        "invalid JSON",
			response:    "not json",
			statusCode:  http.StatusOK,
			expectError: true,
		},
		{
			name:        "empty capabilities",
			response:    `{"capabilities": "", "agents_found": 0}`,
			statusCode:  http.StatusOK,
			expectError: true,
		},
		{
			name:        "missing capabilities field",
			response:    `{"agents_found": 5}`,
			statusCode:  http.StatusOK,
			expectError: true,
		},
		{
			name:        "valid response",
			response:    `{"capabilities": "test capabilities"}`,
			statusCode:  http.StatusOK,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			config := &ServiceCapabilityConfig{
				Endpoint: server.URL,
				Timeout:  100 * time.Millisecond,
			}
			provider := NewServiceCapabilityProvider(config)
			// Disable retries for this test to avoid timeout
			provider.retryAttempts = 0

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			_, err := provider.GetCapabilities(ctx, "test", nil)
			if tt.expectError && err == nil {
				t.Error("Expected error for invalid response")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected success, got error: %v", err)
			}
		})
	}
}

// TestServiceCapabilityProvider_RequestPayload tests the request payload sent to service
func TestServiceCapabilityProvider_RequestPayload(t *testing.T) {
	var capturedRequest CapabilityRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type: application/json, got %s", r.Header.Get("Content-Type"))
		}

		json.NewDecoder(r.Body).Decode(&capturedRequest)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CapabilityResponse{
			Capabilities: "test",
		})
	}))
	defer server.Close()

	config := &ServiceCapabilityConfig{
		Endpoint:  server.URL,
		TopK:      25,
		Threshold: 0.75,
	}
	provider := NewServiceCapabilityProvider(config)

	metadata := map[string]interface{}{
		"user_id": "123",
		"session": "abc",
	}

	provider.GetCapabilities(context.Background(), "find weather tools", metadata)

	// Verify request payload
	if capturedRequest.Query != "find weather tools" {
		t.Errorf("Expected query 'find weather tools', got %s", capturedRequest.Query)
	}
	if capturedRequest.TopK != 25 {
		t.Errorf("Expected TopK 25, got %d", capturedRequest.TopK)
	}
	if capturedRequest.Threshold != 0.75 {
		t.Errorf("Expected Threshold 0.75, got %f", capturedRequest.Threshold)
	}
	if capturedRequest.Metadata["user_id"] != "123" {
		t.Errorf("Expected metadata user_id=123, got %v", capturedRequest.Metadata["user_id"])
	}
}

// TestDefaultCapabilityProvider_EmptyCatalog tests behavior with empty catalog
func TestDefaultCapabilityProvider_EmptyCatalog(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	catalog.Refresh(context.Background())

	provider := NewDefaultCapabilityProvider(catalog)

	capabilities, err := provider.GetCapabilities(context.Background(), "test", nil)
	if err != nil {
		t.Errorf("Expected no error with empty catalog, got %v", err)
	}
	if capabilities == "" {
		t.Error("Expected non-empty capabilities even with empty catalog")
	}
}

// TestDefaultCapabilityProvider_LargeCatalog tests with many agents/tools
func TestDefaultCapabilityProvider_LargeCatalog(t *testing.T) {
	discovery := NewMockDiscovery()

	// Register 100 agents
	for i := 0; i < 100; i++ {
		registration := &core.ServiceRegistration{
			ID:          fmt.Sprintf("agent-%d", i),
			Name:        fmt.Sprintf("agent-%d", i),
			Type:        core.ComponentTypeAgent,
			Description: fmt.Sprintf("Agent %d description", i),
			Address:     "localhost",
			Port:        8080 + i,
			Capabilities: []core.Capability{
				{
					Name:        fmt.Sprintf("capability_%d", i),
					Description: fmt.Sprintf("Capability %d", i),
				},
			},
			Health: core.HealthHealthy,
		}
		discovery.Register(context.Background(), registration)
	}

	catalog := NewAgentCatalog(discovery)
	catalog.Refresh(context.Background())

	provider := NewDefaultCapabilityProvider(catalog)

	capabilities, err := provider.GetCapabilities(context.Background(), "test", nil)
	if err != nil {
		t.Errorf("Expected no error with large catalog, got %v", err)
	}

	// Should contain all agents
	for i := 0; i < 100; i++ {
		expectedAgent := fmt.Sprintf("agent-%d", i)
		if !stringContains(capabilities, expectedAgent) {
			t.Errorf("Expected capabilities to contain %s", expectedAgent)
		}
	}
}

// Mocks are now in test_mocks.go to avoid duplication
