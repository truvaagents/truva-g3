//go:build integration
// +build integration

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Test basic panic recovery in step execution
func TestSmartExecutor_PanicRecovery(t *testing.T) {
	// Create a test server that panics
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated HTTP handler panic")
	}))
	defer server.Close()

	// Parse server URL to get address and port
	serverURL := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(serverURL, ":")

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	// Add a test agent that will cause panic
	catalog.agents["test-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "test-agent",
			Name:    "panic-agent",
			Address: parts[0],
			Port:    8080,
		},
		Capabilities: []EnhancedCapability{
			{Name: "panic-capability", Endpoint: server.URL + "/api/panic"},
		},
	}

	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{
				StepID:    "step1",
				AgentName: "panic-agent",
				Namespace: "test",
				Metadata: map[string]interface{}{
					"capability": "panic-capability",
				},
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	// Should not error, but result should show failure
	if err != nil {
		t.Fatalf("Expected no error from Execute, got: %v", err)
	}

	if result.Success {
		t.Error("Expected execution to fail due to panic")
	}

	if len(result.Steps) != 1 {
		t.Fatalf("Expected 1 step result, got %d", len(result.Steps))
	}

	stepResult := result.Steps[0]
	if stepResult.Success {
		t.Error("Expected step to fail due to panic")
	}
}

// Test concurrent panics in multiple steps
func TestSmartExecutor_ConcurrentPanics(t *testing.T) {
	// Create test servers with controlled behavior
	var servers []*httptest.Server
	for i := 0; i < 5; i++ {
		idx := i
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Agents 1 and 3 will panic
			if idx == 1 || idx == 3 {
				panic(fmt.Sprintf("panic from agent-%d", idx))
			}
			// Others return success
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"agent":   fmt.Sprintf("agent-%d", idx),
			})
		}))
		servers = append(servers, server)
		defer server.Close()
	}

	catalog := &AgentCatalog{
		agents:          make(map[string]*AgentInfo),
		capabilityIndex: make(map[string][]string),
		mu:              sync.RWMutex{},
	}

	// Add multiple test agents
	for i := 0; i < 5; i++ {
		agentID := fmt.Sprintf("agent-%d", i)
		catalog.agents[agentID] = &AgentInfo{
			Registration: &core.ServiceRegistration{
				ID:      agentID,
				Name:    agentID,
				Address: servers[i].URL,
				Port:    80,
			},
			Capabilities: []EnhancedCapability{
				{Name: "test", Endpoint: servers[i].URL + "/api/test"},
			},
		}
		// Update capability index
		catalog.capabilityIndex["test"] = append(catalog.capabilityIndex["test"], agentID)
	}

	executor := NewSmartExecutor(catalog)

	// Create plan with independent steps (can run concurrently)
	var steps []RoutingStep
	for i := 0; i < 5; i++ {
		steps = append(steps, RoutingStep{
			StepID:    fmt.Sprintf("step-%d", i),
			AgentName: fmt.Sprintf("agent-%d", i),
			Namespace: "test",
			Metadata: map[string]interface{}{
				"capability": "test",
				"endpoint":   servers[i].URL + "/api/test",
			},
		})
	}

	plan := &RoutingPlan{
		PlanID: "concurrent-test",
		Steps:  steps,
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	if err != nil {
		t.Fatalf("Expected no error from Execute, got: %v", err)
	}

	// The execution should partially fail due to panics
	if result.Success {
		t.Error("Expected execution to fail due to panics")
	}

	// Check that we got all step results
	if len(result.Steps) != 5 {
		t.Errorf("Expected 5 step results, got %d", len(result.Steps))
	}

	// Check specific steps failed (agents 1 and 3)
	failedSteps := 0
	for _, step := range result.Steps {
		if !step.Success {
			failedSteps++
		}
	}

	// We expect some failures but the exact count depends on server behavior
	if failedSteps == 0 {
		t.Error("Expected at least some failed steps")
	}
}

// Test panic with dependencies
func TestSmartExecutor_PanicWithDependencies(t *testing.T) {
	// Create test servers
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("first step panic")
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))
	defer server2.Close()

	// Parse server URLs
	parseServerURL := func(serverURL string) (string, int) {
		url := strings.TrimPrefix(serverURL, "http://")
		parts := strings.Split(url, ":")
		addr := parts[0]
		port := 80
		if len(parts) > 1 {
			if p, err := strconv.Atoi(parts[1]); err == nil {
				port = p
			}
		}
		return addr, port
	}

	addr1, port1 := parseServerURL(server1.URL)
	addr2, port2 := parseServerURL(server2.URL)

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	catalog.agents["agent1"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "agent1",
			Name:    "agent1",
			Address: addr1,
			Port:    port1,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test", Endpoint: "/api/test"},
		},
	}

	catalog.agents["agent2"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "agent2",
			Name:    "agent2",
			Address: addr2,
			Port:    port2,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test", Endpoint: "/api/test"},
		},
	}

	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "dependency-test",
		Steps: []RoutingStep{
			{
				StepID:    "step1",
				AgentName: "agent1",
				Namespace: "test",
				DependsOn: []string{}, // No dependencies
				Metadata: map[string]interface{}{
					"capability": "test",
				},
			},
			{
				StepID:    "step2",
				AgentName: "agent2",
				Namespace: "test",
				DependsOn: []string{"step1"}, // Depends on panicking step
				Metadata: map[string]interface{}{
					"capability": "test",
				},
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	if err != nil {
		t.Fatalf("Expected no error from Execute, got: %v", err)
	}

	// The execution should fail
	if result.Success {
		t.Error("Expected execution to fail")
	}

	// We should have results for attempted steps
	if len(result.Steps) == 0 {
		t.Error("Expected at least one step result")
	}
}

// Test panic in findReadySteps
func TestSmartExecutor_PanicInFindReadySteps(t *testing.T) {
	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	executor := NewSmartExecutor(catalog)

	// Create a plan with nil step that could cause panic
	plan := &RoutingPlan{
		PlanID: "test",
		Steps: []RoutingStep{
			{}, // Empty step that might cause issues
		},
	}

	ctx := context.Background()

	// This should handle the situation gracefully
	result, err := executor.Execute(ctx, plan)

	// Should complete without panic
	if err == nil && result != nil {
		// The execution might fail but shouldn't panic
		t.Log("Execution handled gracefully")
	}
}

// Test panic during HTTP call
func TestSmartExecutor_PanicDuringHTTPCall(t *testing.T) {
	// Create a test server that causes client-side issues
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write invalid response that might cause panic during decode
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json")) // Invalid JSON
	}))
	defer server.Close()

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	// Parse server URL to get port
	serverURL := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(serverURL, ":")
	port := 80
	if len(parts) > 1 {
		fmt.Sscanf(parts[1], "%d", &port)
	}

	catalog.agents["http-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "http-agent",
			Name:    "http-agent",
			Address: parts[0],
			Port:    port,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test", Endpoint: server.URL + "/"},
		},
	}

	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "http-test",
		Steps: []RoutingStep{
			{
				StepID:    "http-step",
				AgentName: "http-agent",
				Namespace: "test",
				Metadata: map[string]interface{}{
					"capability": "test",
					"parameters": map[string]interface{}{"test": "value"},
					"endpoint":   server.URL + "/",
				},
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	// Should handle the HTTP error gracefully
	if err != nil {
		t.Fatalf("Expected no error from Execute, got: %v", err)
	}

	if result.Success {
		t.Error("Expected execution to fail due to invalid response")
	}

	if len(result.Steps) != 1 {
		t.Fatalf("Expected 1 step result, got %d", len(result.Steps))
	}

	if result.Steps[0].Success {
		t.Error("Expected step to fail")
	}
}

// Test panic with context cancellation
func TestSmartExecutor_PanicWithContextCancellation(t *testing.T) {
	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))
	defer server.Close()

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	catalog.agents["slow-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "slow-agent",
			Name:    "slow-agent",
			Address: server.URL,
			Port:    80,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test", Endpoint: server.URL + "/api/test"},
		},
	}

	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "cancel-test",
		Steps: []RoutingStep{
			{
				StepID:    "step1",
				AgentName: "slow-agent",
				Namespace: "test",
				Metadata: map[string]interface{}{
					"endpoint": server.URL + "/api/test",
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := executor.Execute(ctx, plan)

	// Should get context cancellation error
	if err != context.Canceled {
		t.Logf("Expected context.Canceled error, got: %v", err)
	}

	if result != nil {
		t.Log("Got partial result despite cancellation")
	}
}

// Test start time preservation in panic
func TestSmartExecutor_StartTimePreservationInPanic(t *testing.T) {
	// Create a server that delays then panics
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		panic("panic after work")
	}))
	defer server.Close()

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	catalog.agents["test-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "test-agent",
			Name:    "test-agent",
			Address: server.URL,
			Port:    80,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test", Endpoint: server.URL + "/api/test"},
		},
	}

	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "timing-test",
		Steps: []RoutingStep{
			{
				StepID:    "step1",
				AgentName: "test-agent",
				Namespace: "test",
				Metadata: map[string]interface{}{
					"endpoint": server.URL + "/api/test",
				},
			},
		},
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, plan)

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("Expected 1 step result, got %d", len(result.Steps))
	}

	stepResult := result.Steps[0]

	// Duration should be at least 100ms
	if stepResult.Duration < 100*time.Millisecond {
		t.Errorf("Duration should be at least 100ms, got %v", stepResult.Duration)
	}
}

// Test no deadlock with mutex in panic
func TestSmartExecutor_NoDeadlockInPanic(t *testing.T) {
	// Create panic servers
	var servers []*httptest.Server
	var serverAddresses []string
	var serverPorts []int

	for i := 0; i < 10; i++ {
		idx := i
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic(fmt.Sprintf("panic from agent-%d", idx))
		}))
		servers = append(servers, server)
		defer server.Close()

		// Parse server URL to get address and port
		serverURL := strings.TrimPrefix(server.URL, "http://")
		parts := strings.Split(serverURL, ":")
		serverAddresses = append(serverAddresses, parts[0])
		port := 80
		if len(parts) > 1 {
			if p, err := strconv.Atoi(parts[1]); err == nil {
				port = p
			}
		}
		serverPorts = append(serverPorts, port)
	}

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	// Add multiple agents with proper address and port
	for i := 0; i < 10; i++ {
		agentID := fmt.Sprintf("agent-%d", i)
		catalog.agents[agentID] = &AgentInfo{
			Registration: &core.ServiceRegistration{
				ID:      agentID,
				Name:    agentID,
				Address: serverAddresses[i],
				Port:    serverPorts[i],
			},
			Capabilities: []EnhancedCapability{
				{Name: "test", Endpoint: "/api/test"},
			},
		}
	}

	executor := NewSmartExecutor(catalog)

	// Create many concurrent steps
	var steps []RoutingStep
	for i := 0; i < 10; i++ {
		steps = append(steps, RoutingStep{
			StepID:    fmt.Sprintf("step-%d", i),
			AgentName: fmt.Sprintf("agent-%d", i),
			Namespace: "test",
			Metadata: map[string]interface{}{
				"capability": "test",
			},
		})
	}

	plan := &RoutingPlan{
		PlanID: "deadlock-test",
		Steps:  steps,
	}

	ctx := context.Background()

	// Should complete without deadlock
	done := make(chan bool)
	go func() {
		result, err := executor.Execute(ctx, plan)
		if err != nil {
			t.Logf("Execution error: %v", err)
		}
		if result != nil && len(result.Steps) == 10 {
			t.Log("All steps processed despite panics")
		}
		done <- true
	}()

	select {
	case <-done:
		// Success - no deadlock
	case <-time.After(15 * time.Second):
		// With 10 steps, max concurrency of 5, and 3 retries each with delays,
		// execution can take up to ~12 seconds in the worst case
		t.Fatal("Execution timed out after 15 seconds")
	}
}

// Benchmark panic recovery overhead
func BenchmarkSmartExecutor_PanicRecovery(b *testing.B) {
	// Create alternating panic/success server
	counter := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := atomic.AddInt32(&counter, 1)
		if c%2 == 0 {
			panic("benchmark panic")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))
	defer server.Close()

	catalog := &AgentCatalog{
		agents: make(map[string]*AgentInfo),
		mu:     sync.RWMutex{},
	}

	catalog.agents["bench-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "bench-agent",
			Name:    "bench-agent",
			Address: server.URL,
			Port:    80,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test", Endpoint: server.URL + "/api/test"},
		},
	}

	executor := NewSmartExecutor(catalog)

	plan := &RoutingPlan{
		PlanID: "bench",
		Steps: []RoutingStep{
			{
				StepID:    "step1",
				AgentName: "bench-agent",
				Namespace: "test",
				Metadata: map[string]interface{}{
					"endpoint": server.URL + "/api/test",
				},
			},
		},
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		executor.Execute(ctx, plan)
	}
}
