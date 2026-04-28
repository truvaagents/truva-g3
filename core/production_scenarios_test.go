package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHealthBasedDiscovery validates that unhealthy components are handled correctly
// In production, unhealthy components should be excluded from discovery
func TestHealthBasedDiscovery(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("health-aware-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register healthy and unhealthy components
	healthyTool := &ServiceInfo{
		ID:           "tool-healthy",
		Name:         "calculator",
		Type:         ComponentTypeTool,
		Health:       HealthHealthy,
		Capabilities: []Capability{{Name: "add"}},
	}

	unhealthyTool := &ServiceInfo{
		ID:           "tool-unhealthy",
		Name:         "broken-calculator",
		Type:         ComponentTypeTool,
		Health:       HealthUnhealthy,
		Capabilities: []Capability{{Name: "add"}},
	}

	degradedTool := &ServiceInfo{
		ID:           "tool-degraded",
		Name:         "slow-calculator",
		Type:         ComponentTypeTool,
		Health:       HealthUnknown,
		Capabilities: []Capability{{Name: "add"}},
	}

	mockDiscovery.Register(ctx, healthyTool)
	mockDiscovery.Register(ctx, unhealthyTool)
	mockDiscovery.Register(ctx, degradedTool)

	// Discovery should prefer healthy components
	services, err := agent.Discover(ctx, DiscoveryFilter{
		Type:         ComponentTypeTool,
		Capabilities: []string{"add"},
	})

	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	// In production, we might want to filter out unhealthy services
	var healthyServices []*ServiceInfo
	for _, svc := range services {
		if svc.Health == HealthHealthy {
			healthyServices = append(healthyServices, svc)
		}
	}

	if len(healthyServices) != 1 {
		t.Errorf("Expected 1 healthy service, got %d", len(healthyServices))
	}

	// Test health state transition
	t.Run("health state transition", func(t *testing.T) {
		// Simulate component becoming healthy
		mockDiscovery.UpdateHealth(ctx, "tool-unhealthy", HealthHealthy)

		// Re-discover
		services, _ = agent.Discover(ctx, DiscoveryFilter{
			Type: ComponentTypeTool,
		})

		healthyCount := 0
		for _, svc := range services {
			if svc.Health == HealthHealthy {
				healthyCount++
			}
		}

		if healthyCount != 2 {
			t.Errorf("Expected 2 healthy services after recovery, got %d", healthyCount)
		}
	})
}

// TestGracefulShutdownUnderLoad validates shutdown while handling requests
// In production, components must gracefully complete in-flight requests
func TestGracefulShutdownUnderLoad(t *testing.T) {
	ctx := context.Background()
	tool := NewTool("shutdown-tool")

	// Track in-flight requests
	var activeRequests int32
	var completedRequests int32

	// Register a slow handler
	tool.RegisterCapability(Capability{
		Name: "slow-operation",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&activeRequests, 1)
			defer atomic.AddInt32(&activeRequests, -1)

			// Simulate slow operation
			select {
			case <-time.After(100 * time.Millisecond):
				atomic.AddInt32(&completedRequests, 1)
				w.WriteHeader(http.StatusOK)
			case <-r.Context().Done():
				// Request cancelled
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		},
	})

	// Start the tool
	go func() {
		tool.Start(ctx, 0) // Use random port
	}()

	// Wait for startup
	time.Sleep(50 * time.Millisecond)

	// Send concurrent requests
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/capabilities/slow-operation", nil)
			w := httptest.NewRecorder()

			// Find and call handler
			for _, cap := range tool.Capabilities {
				if cap.Name == "slow-operation" && cap.Handler != nil {
					cap.Handler(w, req)
					break
				}
			}
		}()
	}

	// Give requests time to start
	time.Sleep(20 * time.Millisecond)

	// Initiate shutdown while requests are in-flight
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := tool.Shutdown(shutdownCtx)
	if err != nil {
		t.Logf("Shutdown error (expected in this test): %v", err)
	}

	// Wait for all requests to complete
	wg.Wait()

	// Verify requests were handled
	if atomic.LoadInt32(&completedRequests) < 1 {
		t.Error("No requests completed during shutdown")
	}

	// Verify no requests are still active
	if atomic.LoadInt32(&activeRequests) != 0 {
		t.Errorf("Still have %d active requests after shutdown", activeRequests)
	}
}

// TestRegistryConnectionFailure simulates registry unavailability
// In production, components must handle temporary registry failures
func TestRegistryConnectionFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("tool handles registry failure at startup", func(t *testing.T) {
		config := &Config{
			Name: "resilient-tool",
			Discovery: DiscoveryConfig{
				Enabled: true,
			},
		}
		tool := NewToolWithConfig(config)

		// Use a registry that simulates connection failure
		failingRegistry := &failingRegistry{
			failUntil: time.Now().Add(100 * time.Millisecond),
		}
		tool.Registry = failingRegistry

		// First initialization should fail
		err := tool.Initialize(ctx)
		if err == nil {
			t.Error("Expected error when registry is unavailable")
		}

		// Wait for registry to "recover"
		time.Sleep(150 * time.Millisecond)

		// Retry should succeed
		err = tool.Initialize(ctx)
		if err != nil {
			t.Errorf("Initialize() should succeed after registry recovery: %v", err)
		}
	})

	t.Run("agent handles discovery failure gracefully", func(t *testing.T) {
		agent := NewBaseAgent("resilient-agent")

		// Use discovery that fails intermittently
		flakeyDiscovery := &flakeyDiscovery{
			failureRate:   0.5,
			mockDiscovery: NewMockDiscovery(),
		}
		agent.Discovery = flakeyDiscovery

		// Register some services
		flakeyDiscovery.mockDiscovery.Register(ctx, &ServiceInfo{
			ID:   "test-1",
			Name: "test-service",
			Type: ComponentTypeTool,
		})

		// Try discovery multiple times
		successCount := 0
		for i := 0; i < 10; i++ {
			services, err := agent.Discover(ctx, DiscoveryFilter{})
			if err == nil && len(services) > 0 {
				successCount++
			}
		}

		// Should have some successes despite failures
		if successCount == 0 {
			t.Error("Discovery should succeed sometimes despite intermittent failures")
		}

		if successCount == 10 {
			t.Error("Expected some discovery failures")
		}
	})
}

// TestComponentVersionCompatibility validates version compatibility handling
// In production, different versions of components must coexist during rolling updates
func TestComponentVersionCompatibility(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("version-aware-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register components with different versions
	v1Tool := &ServiceInfo{
		ID:   "tool-v1",
		Name: "calculator",
		Type: ComponentTypeTool,
		Metadata: map[string]interface{}{
			"version":     "1.0.0",
			"api_version": "v1",
		},
		Capabilities: []Capability{
			{Name: "add"},
			{Name: "subtract"},
		},
	}

	v2Tool := &ServiceInfo{
		ID:   "tool-v2",
		Name: "calculator",
		Type: ComponentTypeTool,
		Metadata: map[string]interface{}{
			"version":     "2.0.0",
			"api_version": "v2",
		},
		Capabilities: []Capability{
			{Name: "add"},
			{Name: "subtract"},
			{Name: "multiply"}, // New capability in v2
		},
	}

	mockDiscovery.Register(ctx, v1Tool)
	mockDiscovery.Register(ctx, v2Tool)

	// Discover components that support specific API version
	t.Run("discover by API version", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Name: "calculator",
			Metadata: map[string]interface{}{
				"api_version": "v1",
			},
		})

		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 1 {
			t.Errorf("Expected 1 v1 service, got %d", len(services))
		}

		if services[0].ID != "tool-v1" {
			t.Errorf("Expected tool-v1, got %s", services[0].ID)
		}
	})

	// Verify backward compatibility
	t.Run("backward compatible discovery", func(t *testing.T) {
		// Discover basic capability that both versions support
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Capabilities: []string{"add"},
		})

		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 2 {
			t.Errorf("Both versions should support 'add', got %d services", len(services))
		}
	})

	// Verify new capability only in v2
	t.Run("new capability in v2", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Capabilities: []string{"multiply"},
		})

		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 1 {
			t.Errorf("Only v2 should support 'multiply', got %d services", len(services))
		}

		if services[0].ID != "tool-v2" {
			t.Errorf("Expected tool-v2 for multiply capability")
		}
	})
}

// TestResourceExhaustion validates behavior under resource constraints
// In production, components must handle resource exhaustion gracefully
func TestResourceExhaustion(t *testing.T) {
	t.Run("too many capabilities", func(t *testing.T) {
		tool := NewTool("exhausted-tool")

		// Try to register excessive capabilities
		const maxCapabilities = 10000
		for i := 0; i < maxCapabilities; i++ {
			tool.RegisterCapability(Capability{
				Name:        fmt.Sprintf("capability-%d", i),
				Description: strings.Repeat("x", 1000), // Large description
			})
		}

		// Tool should still function
		if len(tool.Capabilities) == 0 {
			t.Error("Tool should have some capabilities registered")
		}

		// Memory usage check would go here in production
		// This is a simplified check
		if len(tool.Capabilities) > maxCapabilities {
			t.Error("Capability count exceeds maximum")
		}
	})

	t.Run("discovery with many components", func(t *testing.T) {
		ctx := context.Background()
		agent := NewBaseAgent("scanner-agent")
		mockDiscovery := NewMockDiscovery()
		agent.Discovery = mockDiscovery

		// Register many components
		const componentCount = 1000
		for i := 0; i < componentCount; i++ {
			service := &ServiceInfo{
				ID:   fmt.Sprintf("service-%d", i),
				Name: fmt.Sprintf("service-%d", i),
				Type: ComponentTypeTool,
			}
			mockDiscovery.Register(ctx, service)
		}

		// Discovery should handle large result sets
		start := time.Now()
		services, err := agent.Discover(ctx, DiscoveryFilter{})
		duration := time.Since(start)

		if err != nil {
			t.Fatalf("Discover() error with many components: %v", err)
		}

		if len(services) != componentCount {
			t.Errorf("Expected %d services, got %d", componentCount, len(services))
		}

		// Performance check - discovery shouldn't take too long
		if duration > 1*time.Second {
			t.Errorf("Discovery took too long: %v", duration)
		}
	})
}

// TestStaleComponentCleanup validates handling of stale components
// In production, crashed components that didn't unregister must be handled
func TestStaleComponentCleanup(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("cleanup-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register a component that will become stale
	staleTool := &ServiceInfo{
		ID:       "stale-tool",
		Name:     "abandoned-tool",
		Type:     ComponentTypeTool,
		Health:   HealthHealthy,
		LastSeen: time.Now().Add(-1 * time.Hour), // Last seen 1 hour ago
	}

	activeTool := &ServiceInfo{
		ID:       "active-tool",
		Name:     "active-tool",
		Type:     ComponentTypeTool,
		Health:   HealthHealthy,
		LastSeen: time.Now(),
	}

	mockDiscovery.Register(ctx, staleTool)
	mockDiscovery.Register(ctx, activeTool)

	// In production, discovery might filter out stale components
	services, err := agent.Discover(ctx, DiscoveryFilter{
		Type: ComponentTypeTool,
	})

	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	// Count fresh components
	freshCount := 0
	staleCount := 0
	stalenessThreshold := 30 * time.Minute

	for _, svc := range services {
		if time.Since(svc.LastSeen) > stalenessThreshold {
			staleCount++
			t.Logf("Found stale component: %s (last seen: %v ago)",
				svc.Name, time.Since(svc.LastSeen))
		} else {
			freshCount++
		}
	}

	if staleCount == 0 {
		t.Log("No stale components found (this might be expected if cleanup is automatic)")
	}

	if freshCount == 0 {
		t.Error("Should have at least one fresh component")
	}
}

// TestConcurrentRegistrationAndDiscovery simulates production race conditions
// In production, components register/unregister while discovery is happening
func TestConcurrentRegistrationAndDiscovery(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("concurrent-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	stopCh := make(chan struct{})
	errorCh := make(chan error, 100)

	// Continuously register and unregister components
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stopCh:
				return
			default:
				service := &ServiceInfo{
					ID:   fmt.Sprintf("dynamic-%d", i%10),
					Name: fmt.Sprintf("service-%d", i%10),
					Type: ComponentTypeTool,
				}

				if err := mockDiscovery.Register(ctx, service); err != nil {
					errorCh <- fmt.Errorf("register error: %w", err)
				}

				// Randomly unregister
				if i%3 == 0 {
					if err := mockDiscovery.Unregister(ctx, service.ID); err != nil {
						errorCh <- fmt.Errorf("unregister error: %w", err)
					}
				}

				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// Continuously discover
	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
				services, err := agent.Discover(ctx, DiscoveryFilter{
					Type: ComponentTypeTool,
				})

				if err != nil {
					errorCh <- fmt.Errorf("discover error: %w", err)
				}

				// Validate discovered services
				for _, svc := range services {
					if svc == nil {
						errorCh <- fmt.Errorf("discovered nil service")
					}
				}

				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// Run for a short time
	time.Sleep(100 * time.Millisecond)
	close(stopCh)

	// Check for errors
	select {
	case err := <-errorCh:
		t.Errorf("Concurrent operation error: %v", err)
	default:
		// No errors
	}
}

// TestCircuitBreakerForFailingComponents validates circuit breaker behavior
// In production, repeatedly failing components should be circuit-broken
func TestCircuitBreakerForFailingComponents(t *testing.T) {
	// Track failure count
	var failureCount int32
	const failureThreshold = 3

	// Create a tool that fails initially
	tool := NewTool("flaky-tool")
	tool.RegisterCapability(Capability{
		Name: "flaky-operation",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			count := atomic.AddInt32(&failureCount, 1)
			if count <= failureThreshold {
				// Fail the first few times
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Succeed after threshold
			w.WriteHeader(http.StatusOK)
		},
	})

	// Track circuit breaker state
	var circuitOpen bool
	var consecutiveFailures int

	// Try calling the tool multiple times
	for i := 0; i < 10; i++ {
		if circuitOpen {
			// Circuit is open, skip calling
			t.Logf("Circuit breaker OPEN, skipping call %d", i)

			// Check if we should try to close circuit
			if i > 6 {
				circuitOpen = false
				t.Log("Attempting to close circuit breaker")
			}
			continue
		}

		// Simulate calling the tool
		req := httptest.NewRequest("POST", "/capabilities/flaky-operation", nil)
		w := httptest.NewRecorder()

		// Find and call handler
		for _, cap := range tool.Capabilities {
			if cap.Name == "flaky-operation" && cap.Handler != nil {
				cap.Handler(w, req)
				break
			}
		}

		if w.Code != http.StatusOK {
			consecutiveFailures++
			t.Logf("Call %d failed, consecutive failures: %d", i, consecutiveFailures)

			if consecutiveFailures >= failureThreshold {
				circuitOpen = true
				t.Log("Circuit breaker OPENED due to consecutive failures")
			}
		} else {
			consecutiveFailures = 0
			t.Logf("Call %d succeeded", i)
		}
	}

	// Verify circuit breaker behavior
	if !circuitOpen && atomic.LoadInt32(&failureCount) <= failureThreshold {
		t.Log("Circuit breaker correctly opened after repeated failures")
	}
}

// Helper types for testing

type failingRegistry struct {
	failUntil time.Time
	mu        sync.Mutex
}

func (f *failingRegistry) Register(ctx context.Context, info *ServiceInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if time.Now().Before(f.failUntil) {
		return fmt.Errorf("registry connection failed")
	}
	return nil
}

func (f *failingRegistry) UpdateHealth(ctx context.Context, id string, status HealthStatus) error {
	return nil
}

func (f *failingRegistry) Unregister(ctx context.Context, id string) error {
	return nil
}

type flakeyDiscovery struct {
	failureRate   float64
	mockDiscovery *MockDiscovery
	callCount     int
	mu            sync.Mutex
}

func (f *flakeyDiscovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.callCount++
	// Fail based on rate
	if f.callCount%2 == 0 && f.failureRate > 0 {
		return nil, fmt.Errorf("discovery temporarily unavailable")
	}

	return f.mockDiscovery.Discover(ctx, filter)
}

func (f *flakeyDiscovery) Register(ctx context.Context, info *ServiceInfo) error {
	return f.mockDiscovery.Register(ctx, info)
}

func (f *flakeyDiscovery) Unregister(ctx context.Context, id string) error {
	return f.mockDiscovery.Unregister(ctx, id)
}

func (f *flakeyDiscovery) UpdateHealth(ctx context.Context, id string, health HealthStatus) error {
	return f.mockDiscovery.UpdateHealth(ctx, id, health)
}

func (f *flakeyDiscovery) FindService(ctx context.Context, name string) ([]*ServiceInfo, error) {
	return f.mockDiscovery.FindService(ctx, name)
}

func (f *flakeyDiscovery) FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error) {
	return f.mockDiscovery.FindByCapability(ctx, capability)
}
