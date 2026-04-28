//go:build integration
// +build integration

package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Integration tests for step callback mechanisms
// These tests verify that both config-level (OnStepComplete) and
// context-level (WithStepCallback) callbacks work correctly together.
//
// Run with: go test -tags=integration -run TestBothCallback

// createTestCatalog creates a catalog with test agents backed by an httptest server
func createTestCatalog(t *testing.T) (*AgentCatalog, *httptest.Server) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"result": "test response",
		})
	}))

	serverURL := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(serverURL, ":")

	catalog := &AgentCatalog{
		agents:          make(map[string]*AgentInfo),
		capabilityIndex: make(map[string][]string),
		mu:              sync.RWMutex{},
	}

	catalog.agents["test-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "test-agent",
			Name:    "test-agent",
			Address: parts[0],
			Port:    8080,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test-capability", Endpoint: server.URL + "/api/test"},
		},
	}
	catalog.capabilityIndex["test-capability"] = []string{"test-agent"}

	return catalog, server
}

// TestBothCallbackMechanismsFire verifies that when both callback mechanisms
// are configured, both are invoked for each step completion.
func TestBothCallbackMechanismsFire(t *testing.T) {
	catalog, server := createTestCatalog(t)
	defer server.Close()

	executor := NewSmartExecutor(catalog)

	var configCallbackCount int64
	var contextCallbackCount int64

	// Set config-level callback
	executor.SetOnStepComplete(func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
		atomic.AddInt64(&configCallbackCount, 1)
	})

	// Create plan with 3 steps
	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{StepID: "step_1", AgentName: "test-agent", Namespace: "test", Instruction: "test 1",
				Metadata: map[string]interface{}{"capability": "test-capability"}},
			{StepID: "step_2", AgentName: "test-agent", Namespace: "test", Instruction: "test 2",
				Metadata: map[string]interface{}{"capability": "test-capability"}, DependsOn: []string{"step_1"}},
			{StepID: "step_3", AgentName: "test-agent", Namespace: "test", Instruction: "test 3",
				Metadata: map[string]interface{}{"capability": "test-capability"}, DependsOn: []string{"step_2"}},
		},
	}

	// Set context-level callback
	ctx := context.Background()
	ctx = WithStepCallback(ctx, func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
		atomic.AddInt64(&contextCallbackCount, 1)
	})

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Both callbacks should have been called 3 times (once per step)
	if atomic.LoadInt64(&configCallbackCount) != 3 {
		t.Errorf("config callback: expected 3 calls, got %d", configCallbackCount)
	}
	if atomic.LoadInt64(&contextCallbackCount) != 3 {
		t.Errorf("context callback: expected 3 calls, got %d", contextCallbackCount)
	}
}

// TestOnlyConfigCallbackWhenNoContext verifies that when only config-level
// callback is set, it still works without context callback.
func TestOnlyConfigCallbackWhenNoContext(t *testing.T) {
	catalog, server := createTestCatalog(t)
	defer server.Close()

	executor := NewSmartExecutor(catalog)

	var configCallbackCount int64

	executor.SetOnStepComplete(func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
		atomic.AddInt64(&configCallbackCount, 1)
	})

	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{StepID: "step_1", AgentName: "test-agent", Namespace: "test", Instruction: "test",
				Metadata: map[string]interface{}{"capability": "test-capability"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt64(&configCallbackCount) != 1 {
		t.Errorf("expected 1 call, got %d", configCallbackCount)
	}
}

// TestOnlyContextCallbackWhenNoConfig verifies that when only context-level
// callback is set, it works without config callback.
func TestOnlyContextCallbackWhenNoConfig(t *testing.T) {
	catalog, server := createTestCatalog(t)
	defer server.Close()

	executor := NewSmartExecutor(catalog)
	// No config callback set

	var contextCallbackCount int64

	ctx := context.Background()
	ctx = WithStepCallback(ctx, func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
		atomic.AddInt64(&contextCallbackCount, 1)
	})

	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{StepID: "step_1", AgentName: "test-agent", Namespace: "test", Instruction: "test",
				Metadata: map[string]interface{}{"capability": "test-capability"}},
		},
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt64(&contextCallbackCount) != 1 {
		t.Errorf("expected 1 call, got %d", contextCallbackCount)
	}
}

// TestConcurrentRequestsWithDifferentContextCallbacks verifies that
// concurrent requests each receive their own context callbacks correctly.
func TestConcurrentRequestsWithDifferentContextCallbacks(t *testing.T) {
	// Create server with small delay to ensure overlap
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "success"})
	}))
	defer server.Close()

	serverURL := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(serverURL, ":")

	catalog := &AgentCatalog{
		agents:          make(map[string]*AgentInfo),
		capabilityIndex: make(map[string][]string),
		mu:              sync.RWMutex{},
	}

	catalog.agents["test-agent"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "test-agent",
			Name:    "test-agent",
			Address: parts[0],
			Port:    8080,
		},
		Capabilities: []EnhancedCapability{
			{Name: "test-capability", Endpoint: server.URL + "/api/test"},
		},
	}
	catalog.capabilityIndex["test-capability"] = []string{"test-agent"}

	executor := NewSmartExecutor(catalog)

	// Shared config callback that counts all calls
	var totalConfigCalls int64
	executor.SetOnStepComplete(func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
		atomic.AddInt64(&totalConfigCalls, 1)
	})

	// Run 5 concurrent requests, each with its own context callback
	var wg sync.WaitGroup
	requestCounts := make([]int64, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(reqIdx int) {
			defer wg.Done()

			ctx := context.Background()
			ctx = WithStepCallback(ctx, func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
				atomic.AddInt64(&requestCounts[reqIdx], 1)
			})

			plan := &RoutingPlan{
				PlanID: "test-plan-" + string(rune('A'+reqIdx)),
				Steps: []RoutingStep{
					{StepID: "step_1", AgentName: "test-agent", Namespace: "test", Instruction: "test",
						Metadata: map[string]interface{}{"capability": "test-capability"}},
					{StepID: "step_2", AgentName: "test-agent", Namespace: "test", Instruction: "test",
						Metadata: map[string]interface{}{"capability": "test-capability"}, DependsOn: []string{"step_1"}},
				},
			}

			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			executor.Execute(ctx, plan)
		}(i)
	}

	wg.Wait()

	// Each request should have exactly 2 callback calls (one per step)
	for i, count := range requestCounts {
		if count != 2 {
			t.Errorf("request %d: expected 2 context callback calls, got %d", i, count)
		}
	}

	// Config callback should have been called 10 times total (5 requests × 2 steps)
	if atomic.LoadInt64(&totalConfigCalls) != 10 {
		t.Errorf("config callback: expected 10 total calls, got %d", totalConfigCalls)
	}
}

// TestCallbackReceivesCorrectStepInfo verifies that callbacks receive
// accurate step information including index, total, and result data.
func TestCallbackReceivesCorrectStepInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "success"})
	}))
	defer server.Close()

	serverURL := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(serverURL, ":")

	catalog := &AgentCatalog{
		agents:          make(map[string]*AgentInfo),
		capabilityIndex: make(map[string][]string),
		mu:              sync.RWMutex{},
	}

	catalog.agents["weather-tool"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "weather-tool",
			Name:    "weather-tool",
			Address: parts[0],
			Port:    8080,
		},
		Capabilities: []EnhancedCapability{
			{Name: "get-weather", Endpoint: server.URL + "/api/weather"},
		},
	}
	catalog.agents["currency-tool"] = &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:      "currency-tool",
			Name:    "currency-tool",
			Address: parts[0],
			Port:    8080,
		},
		Capabilities: []EnhancedCapability{
			{Name: "convert", Endpoint: server.URL + "/api/convert"},
		},
	}
	catalog.capabilityIndex["get-weather"] = []string{"weather-tool"}
	catalog.capabilityIndex["convert"] = []string{"currency-tool"}

	executor := NewSmartExecutor(catalog)

	type callInfo struct {
		stepIndex  int
		totalSteps int
		agentName  string
		success    bool
	}

	var contextCalls []callInfo
	var mu sync.Mutex

	ctx := context.Background()
	ctx = WithStepCallback(ctx, func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
		mu.Lock()
		contextCalls = append(contextCalls, callInfo{
			stepIndex:  stepIndex,
			totalSteps: totalSteps,
			agentName:  step.AgentName,
			success:    result.Success,
		})
		mu.Unlock()
	})

	plan := &RoutingPlan{
		PlanID: "test-plan",
		Steps: []RoutingStep{
			{StepID: "step_1", AgentName: "weather-tool", Namespace: "travel", Instruction: "get weather",
				Metadata: map[string]interface{}{"capability": "get-weather"}},
			{StepID: "step_2", AgentName: "currency-tool", Namespace: "travel", Instruction: "convert",
				Metadata: map[string]interface{}{"capability": "convert"}, DependsOn: []string{"step_1"}},
		},
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := executor.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(contextCalls) != 2 {
		t.Fatalf("context: expected 2 calls, got %d", len(contextCalls))
	}

	// Verify totalSteps is correct
	for i, call := range contextCalls {
		if call.totalSteps != 2 {
			t.Errorf("context call %d: expected totalSteps=2, got %d", i, call.totalSteps)
		}
		if !call.success {
			t.Errorf("context call %d: expected success=true", i)
		}
	}
}
