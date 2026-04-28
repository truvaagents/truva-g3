package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestAgentDiscover tests the agent's discovery capability
func TestAgentDiscover(t *testing.T) {
	ctx := context.Background()

	// Create agent with mock discovery
	agent := NewBaseAgent("discovery-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register some test services
	toolService := &ServiceInfo{
		ID:   "tool-1",
		Name: "calculator",
		Type: ComponentTypeTool,
		Capabilities: []Capability{
			{Name: "add"},
			{Name: "subtract"},
		},
	}
	mockDiscovery.Register(ctx, toolService)

	agentService := &ServiceInfo{
		ID:   "agent-1",
		Name: "orchestrator",
		Type: ComponentTypeAgent,
		Capabilities: []Capability{
			{Name: "coordinate"},
		},
	}
	mockDiscovery.Register(ctx, agentService)

	// Test discovering all services
	t.Run("discover all", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 2 {
			t.Errorf("Expected 2 services, got %d", len(services))
		}
	})

	// Test discovering only tools
	t.Run("discover tools only", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Type: ComponentTypeTool,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 1 {
			t.Errorf("Expected 1 tool, got %d", len(services))
		}

		if services[0].Type != ComponentTypeTool {
			t.Errorf("Expected ComponentTypeTool, got %v", services[0].Type)
		}
	})

	// Test discovering only agents
	t.Run("discover agents only", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Type: ComponentTypeAgent,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 1 {
			t.Errorf("Expected 1 agent, got %d", len(services))
		}

		if services[0].Type != ComponentTypeAgent {
			t.Errorf("Expected ComponentTypeAgent, got %v", services[0].Type)
		}
	})

	// Test discovering by capability
	t.Run("discover by capability", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Capabilities: []string{"add"},
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 1 {
			t.Errorf("Expected 1 service with 'add' capability, got %d", len(services))
		}

		if services[0].Name != "calculator" {
			t.Errorf("Expected calculator service, got %v", services[0].Name)
		}
	})

	// Test discovering by name
	t.Run("discover by name", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Name: "orchestrator",
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 1 {
			t.Errorf("Expected 1 service, got %d", len(services))
		}

		if services[0].Name != "orchestrator" {
			t.Errorf("Expected orchestrator, got %v", services[0].Name)
		}
	})

	// Test discovering with no results
	t.Run("discover with no matches", func(t *testing.T) {
		services, err := agent.Discover(ctx, DiscoveryFilter{
			Name: "non-existent",
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(services) != 0 {
			t.Errorf("Expected 0 services, got %d", len(services))
		}
	})

	// Test discovery with nil Discovery
	t.Run("discover with nil discovery", func(t *testing.T) {
		agentNoDiscovery := NewBaseAgent("no-discovery")
		agentNoDiscovery.Discovery = nil

		services, err := agentNoDiscovery.Discover(ctx, DiscoveryFilter{})
		if err == nil {
			t.Error("Expected error when Discovery is nil")
		}

		if services != nil {
			t.Error("Expected nil services when Discovery is nil")
		}
	})
}

// TestAgentGetters tests all getter methods
func TestAgentGetters(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	// Add capabilities
	agent.RegisterCapability(Capability{Name: "cap1"})
	agent.RegisterCapability(Capability{Name: "cap2"})

	// Test GetID
	if id := agent.GetID(); id == "" {
		t.Error("GetID() returned empty string")
	}

	// Test GetName
	if name := agent.GetName(); name != "test-agent" {
		t.Errorf("GetName() = %v, want test-agent", name)
	}

	// Test GetType
	if typ := agent.GetType(); typ != ComponentTypeAgent {
		t.Errorf("GetType() = %v, want %v", typ, ComponentTypeAgent)
	}

	// Test GetCapabilities
	caps := agent.GetCapabilities()
	if len(caps) != 2 {
		t.Errorf("GetCapabilities() returned %d capabilities, want 2", len(caps))
	}
}

// TestAgentInitializeWithDiscovery tests initialization with discovery enabled
func TestAgentInitializeWithDiscovery(t *testing.T) {
	ctx := context.Background()

	config := &Config{
		Name: "init-agent",
		Discovery: DiscoveryConfig{
			Enabled: true,
		},
	}

	agent := NewBaseAgentWithConfig(config)

	// Use mock discovery
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Initialize
	err := agent.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Verify agent registered itself
	services, _ := mockDiscovery.Discover(ctx, DiscoveryFilter{
		Name: "init-agent",
	})

	if len(services) != 1 {
		t.Errorf("Expected 1 registration, got %d", len(services))
	}

	if services[0].Type != ComponentTypeAgent {
		t.Errorf("Registration type = %v, want %v", services[0].Type, ComponentTypeAgent)
	}
}

// TestAgentWithNilConfig tests agent creation with nil config
func TestAgentWithNilConfig(t *testing.T) {
	agent := NewBaseAgentWithConfig(nil)

	if agent == nil {
		t.Fatal("NewBaseAgentWithConfig(nil) should not return nil")
	}

	// Should have default config
	if agent.Config == nil {
		t.Fatal("Agent should have default config when created with nil")
	}

	// Verify defaults
	if agent.Config.Port != 8080 {
		t.Errorf("Default port = %v, want 8080", agent.Config.Port)
	}
}

// TestAgentConcurrentDiscover tests concurrent discovery operations
func TestAgentConcurrentDiscover(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("concurrent-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register many services
	for i := 0; i < 100; i++ {
		service := &ServiceInfo{
			ID:   fmt.Sprintf("service-%d", i),
			Name: fmt.Sprintf("service-%d", i),
			Type: ComponentTypeTool,
		}
		mockDiscovery.Register(ctx, service)
	}

	// Concurrent discovery
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()

			services, err := agent.Discover(ctx, DiscoveryFilter{
				Type: ComponentTypeTool,
			})

			if err != nil {
				t.Errorf("Discover() error = %v", err)
			}

			if len(services) != 100 {
				t.Errorf("Expected 100 services, got %d", len(services))
			}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestAgentDiscoverWithTimeout tests discovery with context timeout
func TestAgentDiscoverWithTimeout(t *testing.T) {
	agent := NewBaseAgent("timeout-agent")

	// Create a discovery that simulates slow response
	slowDiscovery := &slowMockDiscovery{
		delay: 2 * time.Second,
	}
	agent.Discovery = slowDiscovery

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Attempt discovery
	services, err := agent.Discover(ctx, DiscoveryFilter{})

	// Should timeout
	if err == nil {
		t.Error("Expected timeout error")
	}

	if services != nil {
		t.Error("Expected nil services on timeout")
	}
}

// slowMockDiscovery simulates slow discovery for testing timeouts
type slowMockDiscovery struct {
	delay time.Duration
}

func (s *slowMockDiscovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	select {
	case <-time.After(s.delay):
		return []*ServiceInfo{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *slowMockDiscovery) Register(ctx context.Context, info *ServiceInfo) error {
	return nil
}

func (s *slowMockDiscovery) Unregister(ctx context.Context, id string) error {
	return nil
}

func (s *slowMockDiscovery) UpdateHealth(ctx context.Context, id string, health HealthStatus) error {
	return nil
}

func (s *slowMockDiscovery) FindService(ctx context.Context, name string) ([]*ServiceInfo, error) {
	return nil, nil
}

func (s *slowMockDiscovery) FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error) {
	return nil, nil
}

// TestAgentDiscoveryFilter tests complex filtering scenarios
func TestAgentDiscoveryFilter(t *testing.T) {
	ctx := context.Background()
	agent := NewBaseAgent("filter-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register diverse services
	services := []*ServiceInfo{
		{
			ID:   "tool-1",
			Name: "calculator",
			Type: ComponentTypeTool,
			Capabilities: []Capability{
				{Name: "add"},
				{Name: "subtract"},
			},
			Metadata: map[string]interface{}{
				"version": "1.0",
				"region":  "us-west",
			},
		},
		{
			ID:   "tool-2",
			Name: "converter",
			Type: ComponentTypeTool,
			Capabilities: []Capability{
				{Name: "convert"},
			},
			Metadata: map[string]interface{}{
				"version": "2.0",
				"region":  "us-east",
			},
		},
		{
			ID:   "agent-1",
			Name: "orchestrator",
			Type: ComponentTypeAgent,
			Capabilities: []Capability{
				{Name: "coordinate"},
				{Name: "schedule"},
			},
			Metadata: map[string]interface{}{
				"version": "1.0",
				"region":  "us-west",
			},
		},
	}

	for _, svc := range services {
		mockDiscovery.Register(ctx, svc)
	}

	// Test complex filter: tools in us-west with add capability
	t.Run("complex filter", func(t *testing.T) {
		results, err := agent.Discover(ctx, DiscoveryFilter{
			Type:         ComponentTypeTool,
			Capabilities: []string{"add"},
			Metadata: map[string]interface{}{
				"region": "us-west",
			},
		})

		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 result, got %d", len(results))
		}

		if results[0].Name != "calculator" {
			t.Errorf("Expected calculator, got %v", results[0].Name)
		}
	})

	// Test metadata filter
	t.Run("metadata filter", func(t *testing.T) {
		results, err := agent.Discover(ctx, DiscoveryFilter{
			Metadata: map[string]interface{}{
				"version": "1.0",
			},
		})

		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}

		// Should find calculator and orchestrator (both have version 1.0)
		if len(results) != 2 {
			t.Errorf("Expected 2 results with version 1.0, got %d", len(results))
		}
	})
}

// BenchmarkAgentDiscover benchmarks discovery performance
func BenchmarkAgentDiscover(b *testing.B) {
	ctx := context.Background()
	agent := NewBaseAgent("bench-agent")
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Register many services
	for i := 0; i < 1000; i++ {
		service := &ServiceInfo{
			ID:   fmt.Sprintf("service-%d", i),
			Name: fmt.Sprintf("service-%d", i),
			Type: ComponentTypeTool,
		}
		mockDiscovery.Register(ctx, service)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = agent.Discover(ctx, DiscoveryFilter{
			Type: ComponentTypeTool,
		})
	}
}
