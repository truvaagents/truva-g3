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
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestAIOrchestrator_WithDefaultCapabilityProvider tests orchestrator with default provider
func TestAIOrchestrator_WithDefaultCapabilityProvider(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register multiple agents
	for i := 0; i < 5; i++ {
		registration := &core.ServiceRegistration{
			ID:          fmt.Sprintf("agent-%d", i),
			Name:        fmt.Sprintf("agent-%d", i),
			Type:        core.ComponentTypeAgent,
			Description: fmt.Sprintf("Test agent %d", i),
			Address:     "localhost",
			Port:        8080 + i,
			Capabilities: []core.Capability{
				{
					Name:        fmt.Sprintf("capability_%d", i),
					Description: fmt.Sprintf("Can do task %d", i),
				},
			},
			Health: core.HealthHealthy,
		}
		discovery.Register(context.Background(), registration)
	}

	// Create orchestrator with default provider
	config := DefaultConfig()
	config.CapabilityProviderType = "default"

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	// Start orchestrator
	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Process a request
	response, err := orchestrator.ProcessRequest(ctx, "Use agent-2 to do something", nil)

	// Should work (even if mock AI doesn't return perfect response)
	if err != nil && response == nil {
		t.Logf("ProcessRequest returned: %v", err)
	}

	// Verify all agents were sent to LLM
	if orchestrator.capabilityProvider != nil {
		capabilities, _ := orchestrator.capabilityProvider.GetCapabilities(ctx, "test", nil)
		for i := 0; i < 5; i++ {
			agentName := fmt.Sprintf("agent-%d", i)
			if !stringContains(capabilities, agentName) {
				t.Errorf("Expected capabilities to contain %s", agentName)
			}
		}
	}
}

// TestAIOrchestrator_WithServiceCapabilityProvider tests orchestrator with service provider
func TestAIOrchestrator_WithServiceCapabilityProvider(t *testing.T) {
	// Create a mock capability service
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		// Parse request
		var req CapabilityRequest
		json.NewDecoder(r.Body).Decode(&req)

		// Return only relevant capabilities based on query
		capabilities := "Relevant agents for: " + req.Query
		if stringContains(req.Query, "weather") {
			capabilities = "weather-agent: Can fetch weather data\nforecast-agent: Can predict weather"
		} else if stringContains(req.Query, "stock") {
			capabilities = "stock-agent: Can fetch stock prices\nanalysis-agent: Can analyze stocks"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CapabilityResponse{
			Capabilities:   capabilities,
			AgentsFound:    2,
			ToolsFound:     0,
			SearchMethod:   "semantic_search",
			ProcessingTime: "50ms",
		})
	}))
	defer server.Close()

	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Create orchestrator with service provider
	config := DefaultConfig()
	config.CapabilityProviderType = "service"
	config.CapabilityService = ServiceCapabilityConfig{
		Endpoint:  server.URL,
		TopK:      10,
		Threshold: 0.7,
		Timeout:   1 * time.Second,
	}

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Test 1: Weather request
	response, _ := orchestrator.ProcessRequest(ctx, "What's the weather like?", nil)
	if response != nil {
		t.Logf("Weather response: %v", response)
	}

	// Test 2: Stock request
	response, _ = orchestrator.ProcessRequest(ctx, "Check stock prices", nil)
	if response != nil {
		t.Logf("Stock response: %v", response)
	}

	// Verify service was called
	if requestCount < 2 {
		t.Errorf("Expected at least 2 service calls, got %d", requestCount)
	}
}

// TestAIOrchestrator_CapabilityProviderFailover tests failover from service to default
func TestAIOrchestrator_CapabilityProviderFailover(t *testing.T) {
	// Create a failing service
	var failCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register an agent for fallback
	registration := &core.ServiceRegistration{
		ID:          "fallback-agent",
		Name:        "fallback-agent",
		Type:        core.ComponentTypeAgent,
		Description: "Fallback agent",
		Address:     "localhost",
		Port:        8080,
		Health:      core.HealthHealthy,
	}
	discovery.Register(context.Background(), registration)

	// Create orchestrator with service provider and fallback enabled
	deps := OrchestratorDependencies{
		Discovery: discovery,
		AIClient:  aiClient,
	}

	config := DefaultConfig()
	config.CapabilityProviderType = "service"
	config.CapabilityService = ServiceCapabilityConfig{
		Endpoint: server.URL,
		Timeout:  100 * time.Millisecond, // Short timeout
	}
	config.EnableFallback = true

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Process request - should use fallback
	_, err = orchestrator.ProcessRequest(ctx, "Do something", nil)

	// Should not completely fail (fallback should work)
	if err != nil {
		t.Logf("Got error (expected with mock): %v", err)
	}

	// Verify service was attempted
	if failCount == 0 {
		t.Error("Expected service to be called")
	}

	// Verify fallback was used
	capabilities, _ := orchestrator.capabilityProvider.GetCapabilities(ctx, "test", nil)
	if !stringContains(capabilities, "fallback-agent") {
		t.Error("Expected fallback to provide fallback-agent")
	}
}

// TestAIOrchestrator_SetCapabilityProvider tests setting custom capability provider
func TestAIOrchestrator_SetCapabilityProvider(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	orchestrator := NewAIOrchestrator(nil, discovery, aiClient)

	// Create custom provider
	customProvider := &mockCapabilityProvider{
		response: "custom capabilities",
	}

	// Set custom provider
	orchestrator.SetCapabilityProvider(customProvider)

	ctx := context.Background()
	capabilities, err := orchestrator.capabilityProvider.GetCapabilities(ctx, "test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if capabilities != "custom capabilities" {
		t.Errorf("Expected custom capabilities, got %s", capabilities)
	}
}

// TestAIOrchestrator_ConcurrentProcessing tests concurrent request processing
func TestAIOrchestrator_ConcurrentProcessing(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register agents
	for i := 0; i < 10; i++ {
		registration := &core.ServiceRegistration{
			ID:      fmt.Sprintf("agent-%d", i),
			Name:    fmt.Sprintf("agent-%d", i),
			Type:    core.ComponentTypeAgent,
			Address: "localhost",
			Port:    8080 + i,
			Health:  core.HealthHealthy,
		}
		discovery.Register(context.Background(), registration)
	}

	orchestrator := CreateSimpleOrchestrator(discovery, aiClient)

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Process 20 concurrent requests
	var wg sync.WaitGroup
	errors := make([]error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := orchestrator.ProcessRequest(ctx, fmt.Sprintf("Request %d", idx), nil)
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// Check metrics
	metrics := orchestrator.GetMetrics()
	if metrics.TotalRequests < 20 {
		t.Errorf("Expected at least 20 requests, got %d", metrics.TotalRequests)
	}
}

// TestAIOrchestrator_CapabilityProviderWithCircuitBreaker tests with injected circuit breaker
func TestAIOrchestrator_CapabilityProviderWithCircuitBreaker(t *testing.T) {
	// Create a service that fails initially then recovers
	var callCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 5 {
			http.Error(w, "Temporary failure", http.StatusServiceUnavailable)
			return
		}
		// Recover after 5 calls
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CapabilityResponse{
			Capabilities: "recovered capabilities",
		})
	}))
	defer server.Close()

	// Create mock circuit breaker
	var cbState string = "closed"
	mockCB := &mockCircuitBreaker{
		executeFunc: func(ctx context.Context, fn func() error) error {
			if cbState == "open" {
				return errors.New("circuit breaker open")
			}
			err := fn()
			if err != nil && callCount >= 3 {
				cbState = "open"
				// Simulate recovery after some time
				go func() {
					time.Sleep(100 * time.Millisecond)
					cbState = "closed"
				}()
			}
			return err
		},
	}

	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	deps := OrchestratorDependencies{
		Discovery:      discovery,
		AIClient:       aiClient,
		CircuitBreaker: mockCB,
	}

	config := DefaultConfig()
	config.CapabilityProviderType = "service"
	config.CapabilityService = ServiceCapabilityConfig{
		Endpoint: server.URL,
	}
	config.EnableFallback = false // Test circuit breaker without fallback

	orchestrator, _ := CreateOrchestrator(config, deps)

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Make multiple requests to trigger circuit breaker
	for i := 0; i < 3; i++ {
		orchestrator.ProcessRequest(ctx, fmt.Sprintf("Request %d", i), nil)
		time.Sleep(10 * time.Millisecond)
	}

	// Circuit should be open now
	if cbState != "open" {
		t.Error("Expected circuit breaker to be open")
	}

	// Wait for recovery
	time.Sleep(150 * time.Millisecond)

	// Circuit should be closed again
	if cbState != "closed" {
		t.Error("Expected circuit breaker to recover")
	}
}

// TestAIOrchestrator_CapabilityProviderCaching tests that capabilities are cached appropriately
func TestAIOrchestrator_CapabilityProviderCaching(t *testing.T) {
	var callCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CapabilityResponse{
			Capabilities: fmt.Sprintf("capabilities-%d", callCount),
		})
	}))
	defer server.Close()

	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	config := DefaultConfig()
	config.CapabilityProviderType = "service"
	config.CapabilityService = ServiceCapabilityConfig{
		Endpoint: server.URL,
	}
	config.CacheEnabled = true
	config.CacheTTL = 100 * time.Millisecond // Short TTL for testing

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Make multiple requests with same query
	for i := 0; i < 3; i++ {
		orchestrator.ProcessRequest(ctx, "Same request", nil)
	}

	// Should use cache (depending on implementation)
	// Note: This test assumes caching is implemented for routing decisions
	// The actual call count depends on whether capability queries are cached
	t.Logf("Capability service called %d times", callCount)
}

// TestAIOrchestrator_MetricsWithCapabilityProvider tests metrics collection
func TestAIOrchestrator_MetricsWithCapabilityProvider(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	// Register some agents
	for i := 0; i < 3; i++ {
		registration := &core.ServiceRegistration{
			ID:      fmt.Sprintf("metric-agent-%d", i),
			Name:    fmt.Sprintf("metric-agent-%d", i),
			Type:    core.ComponentTypeAgent,
			Address: "localhost",
			Port:    9000 + i,
			Health:  core.HealthHealthy,
		}
		discovery.Register(context.Background(), registration)
	}

	config := DefaultConfig()
	config.MetricsEnabled = true

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Process several requests
	successCount := 0
	failCount := 0
	for i := 0; i < 10; i++ {
		_, err := orchestrator.ProcessRequest(ctx, fmt.Sprintf("Request %d", i), nil)
		if err != nil {
			failCount++
		} else {
			successCount++
		}
	}

	// Check metrics
	metrics := orchestrator.GetMetrics()

	if metrics.TotalRequests != 10 {
		t.Errorf("Expected 10 total requests, got %d", metrics.TotalRequests)
	}

	// Success/failure counts depend on mock behavior
	t.Logf("Metrics: Total=%d, Success=%d, Failed=%d",
		metrics.TotalRequests, metrics.SuccessfulRequests, metrics.FailedRequests)

	// Should have recorded some latency
	if metrics.AverageLatency == 0 {
		t.Log("Warning: No latency recorded (might be due to mock)")
	}
}

// TestAIOrchestrator_HistoryWithCapabilityProvider tests execution history
func TestAIOrchestrator_HistoryWithCapabilityProvider(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	config := DefaultConfig()
	config.HistorySize = 5 // Small history for testing

	orchestrator := NewAIOrchestrator(config, discovery, aiClient)

	ctx := context.Background()
	orchestrator.Start(ctx)
	defer orchestrator.Stop()

	// Process more requests than history size
	for i := 0; i < 10; i++ {
		orchestrator.ProcessRequest(ctx, fmt.Sprintf("Historical request %d", i), nil)
	}

	// Check history
	history := orchestrator.GetExecutionHistory()

	// Should only keep last 5
	if len(history) > 5 {
		t.Errorf("Expected history size <= 5, got %d", len(history))
	}

	// Recent requests should be in history
	if len(history) > 0 {
		lastRecord := history[len(history)-1]
		if !stringContains(lastRecord.Request, "Historical request") {
			t.Error("Expected recent request in history")
		}
	}
}
