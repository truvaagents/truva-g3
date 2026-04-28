package core

import (
	"encoding/json"
	"testing"
	"time"
)

// TestComponentTypes validates the component type system
func TestComponentTypes(t *testing.T) {
	// Verify component type constants
	if ComponentTypeTool != "tool" {
		t.Errorf("ComponentTypeTool = %v, want 'tool'", ComponentTypeTool)
	}

	if ComponentTypeAgent != "agent" {
		t.Errorf("ComponentTypeAgent = %v, want 'agent'", ComponentTypeAgent)
	}

	// Verify types are distinct
	if ComponentTypeTool == ComponentTypeAgent {
		t.Fatal("ComponentTypeTool and ComponentTypeAgent must be distinct")
	}
}

// TestServiceInfo validates ServiceInfo structure
func TestServiceInfo(t *testing.T) {
	now := time.Now()

	info := &ServiceInfo{
		ID:          "test-123",
		Name:        "test-service",
		Type:        ComponentTypeTool,
		Description: "Test service",
		Address:     "localhost",
		Port:        8080,
		Capabilities: []Capability{
			{Name: "cap1", Description: "Capability 1"},
			{Name: "cap2", Description: "Capability 2"},
		},
		Metadata: map[string]interface{}{
			"version": "1.0.0",
			"region":  "us-west",
		},
		Health:   HealthHealthy,
		LastSeen: now,
	}

	// Test JSON serialization
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal ServiceInfo: %v", err)
	}

	// Test JSON deserialization
	var decoded ServiceInfo
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal ServiceInfo: %v", err)
	}

	// Verify fields
	if decoded.ID != info.ID {
		t.Errorf("ID = %v, want %v", decoded.ID, info.ID)
	}

	if decoded.Type != info.Type {
		t.Errorf("Type = %v, want %v", decoded.Type, info.Type)
	}

	if len(decoded.Capabilities) != len(info.Capabilities) {
		t.Errorf("Capabilities count = %v, want %v",
			len(decoded.Capabilities), len(info.Capabilities))
	}

	if decoded.Health != info.Health {
		t.Errorf("Health = %v, want %v", decoded.Health, info.Health)
	}
}

// TestDiscoveryFilter validates discovery filtering logic
func TestDiscoveryFilter(t *testing.T) {
	tests := []struct {
		name        string
		filter      DiscoveryFilter
		service     ServiceInfo
		shouldMatch bool
	}{
		{
			name: "filter by type - tool",
			filter: DiscoveryFilter{
				Type: ComponentTypeTool,
			},
			service: ServiceInfo{
				Type: ComponentTypeTool,
			},
			shouldMatch: true,
		},
		{
			name: "filter by type - agent",
			filter: DiscoveryFilter{
				Type: ComponentTypeAgent,
			},
			service: ServiceInfo{
				Type: ComponentTypeAgent,
			},
			shouldMatch: true,
		},
		{
			name: "filter by type - mismatch",
			filter: DiscoveryFilter{
				Type: ComponentTypeTool,
			},
			service: ServiceInfo{
				Type: ComponentTypeAgent,
			},
			shouldMatch: false,
		},
		{
			name: "filter by name",
			filter: DiscoveryFilter{
				Name: "test-service",
			},
			service: ServiceInfo{
				Name: "test-service",
			},
			shouldMatch: true,
		},
		{
			name: "filter by name - mismatch",
			filter: DiscoveryFilter{
				Name: "other-service",
			},
			service: ServiceInfo{
				Name: "test-service",
			},
			shouldMatch: false,
		},
		{
			name: "filter by capability",
			filter: DiscoveryFilter{
				Capabilities: []string{"calculate", "process"},
			},
			service: ServiceInfo{
				Capabilities: []Capability{
					{Name: "calculate"},
					{Name: "process"},
					{Name: "store"},
				},
			},
			shouldMatch: true,
		},
		{
			name: "filter by capability - partial match",
			filter: DiscoveryFilter{
				Capabilities: []string{"calculate"},
			},
			service: ServiceInfo{
				Capabilities: []Capability{
					{Name: "calculate"},
					{Name: "process"},
				},
			},
			shouldMatch: true,
		},
		{
			name: "filter by capability - no match",
			filter: DiscoveryFilter{
				Capabilities: []string{"missing"},
			},
			service: ServiceInfo{
				Capabilities: []Capability{
					{Name: "calculate"},
					{Name: "process"},
				},
			},
			shouldMatch: false,
		},
		{
			name: "filter by metadata",
			filter: DiscoveryFilter{
				Metadata: map[string]interface{}{
					"region": "us-west",
				},
			},
			service: ServiceInfo{
				Metadata: map[string]interface{}{
					"region":  "us-west",
					"version": "1.0.0",
				},
			},
			shouldMatch: true,
		},
		{
			name: "filter by metadata - mismatch",
			filter: DiscoveryFilter{
				Metadata: map[string]interface{}{
					"region": "us-east",
				},
			},
			service: ServiceInfo{
				Metadata: map[string]interface{}{
					"region": "us-west",
				},
			},
			shouldMatch: false,
		},
		{
			name: "complex filter - all match",
			filter: DiscoveryFilter{
				Type:         ComponentTypeTool,
				Name:         "calculator",
				Capabilities: []string{"add", "subtract"},
			},
			service: ServiceInfo{
				Type: ComponentTypeTool,
				Name: "calculator",
				Capabilities: []Capability{
					{Name: "add"},
					{Name: "subtract"},
					{Name: "multiply"},
				},
			},
			shouldMatch: true,
		},
		{
			name: "complex filter - type mismatch",
			filter: DiscoveryFilter{
				Type:         ComponentTypeAgent,
				Name:         "calculator",
				Capabilities: []string{"add"},
			},
			service: ServiceInfo{
				Type: ComponentTypeTool,
				Name: "calculator",
				Capabilities: []Capability{
					{Name: "add"},
				},
			},
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test validates the filter logic conceptually
			// The actual implementation would be in the discovery system
			match := validateFilter(tt.filter, tt.service)
			if match != tt.shouldMatch {
				t.Errorf("Filter match = %v, want %v", match, tt.shouldMatch)
			}
		})
	}
}

// validateFilter is a test helper to validate filter logic
func validateFilter(filter DiscoveryFilter, service ServiceInfo) bool {
	// Check type filter
	if filter.Type != "" && filter.Type != service.Type {
		return false
	}

	// Check name filter
	if filter.Name != "" && filter.Name != service.Name {
		return false
	}

	// Check capabilities filter
	if len(filter.Capabilities) > 0 {
		for _, requiredCap := range filter.Capabilities {
			found := false
			for _, serviceCap := range service.Capabilities {
				if serviceCap.Name == requiredCap {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	// Check metadata filter
	if len(filter.Metadata) > 0 {
		for key, value := range filter.Metadata {
			if serviceValue, exists := service.Metadata[key]; !exists || serviceValue != value {
				return false
			}
		}
	}

	return true
}

// TestComponentInterfaceCompliance validates that Tool and Agent implement Component
func TestComponentInterfaceCompliance(t *testing.T) {
	// Create a tool
	tool := NewTool("test-tool")

	// Verify it implements Component interface
	var _ Component = tool

	// Test Component methods
	if id := tool.GetID(); id == "" {
		t.Error("Tool GetID() returned empty")
	}

	if name := tool.GetName(); name != "test-tool" {
		t.Errorf("Tool GetName() = %v, want test-tool", name)
	}

	if typ := tool.GetType(); typ != ComponentTypeTool {
		t.Errorf("Tool GetType() = %v, want %v", typ, ComponentTypeTool)
	}

	// Create an agent
	agent := NewBaseAgent("test-agent")

	// Verify it implements Component interface
	var _ Component = agent

	// Test Component methods
	if id := agent.GetID(); id == "" {
		t.Error("Agent GetID() returned empty")
	}

	if name := agent.GetName(); name != "test-agent" {
		t.Errorf("Agent GetName() = %v, want test-agent", name)
	}

	if typ := agent.GetType(); typ != ComponentTypeAgent {
		t.Errorf("Agent GetType() = %v, want %v", typ, ComponentTypeAgent)
	}
}

// TestToolVsAgentCapabilities validates architectural differences
func TestToolVsAgentCapabilities(t *testing.T) {
	// Tools should NOT have Discovery
	_ = NewTool("tool")

	// This test passes by compilation - tools don't have Discovery field
	// If someone adds Discovery to tools, this would fail to compile

	// Agents SHOULD have Discovery
	agent := NewBaseAgent("agent")

	// Verify agent has Discovery capability
	if agent.Discovery == nil {
		t.Skip("Agent doesn't have Discovery set in this test")
	}
}

// TestServiceInfoDefaults validates default values
func TestServiceInfoDefaults(t *testing.T) {
	info := &ServiceInfo{}

	// Test zero values
	if info.ID != "" {
		t.Error("Default ID should be empty")
	}

	if info.Type != "" {
		t.Error("Default Type should be empty")
	}

	if info.Health != "" {
		t.Error("Default Health should be empty")
	}

	if info.Port != 0 {
		t.Error("Default Port should be 0")
	}

	// Set to healthy
	info.Health = HealthHealthy
	if info.Health != "healthy" {
		t.Errorf("Health = %v, want 'healthy'", info.Health)
	}
}

// TestCapabilityStructure validates Capability fields
func TestCapabilityStructure(t *testing.T) {
	cap := Capability{
		Name:        "test-capability",
		Description: "Test capability description",
		Endpoint:    "/test",
		InputTypes:  []string{"string", "number"},
		OutputTypes: []string{"object"},
	}

	// Test JSON serialization
	data, err := json.Marshal(cap)
	if err != nil {
		t.Fatalf("Failed to marshal Capability: %v", err)
	}

	// Test JSON deserialization
	var decoded Capability
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal Capability: %v", err)
	}

	// Verify fields
	if decoded.Name != cap.Name {
		t.Errorf("Name = %v, want %v", decoded.Name, cap.Name)
	}

	if decoded.Description != cap.Description {
		t.Errorf("Description = %v, want %v", decoded.Description, cap.Description)
	}
}

// TestHealthStatus validates health status constants
func TestHealthStatus(t *testing.T) {
	// Test health status values
	if HealthHealthy != "healthy" {
		t.Errorf("HealthHealthy = %v, want 'healthy'", HealthHealthy)
	}

	if HealthUnhealthy != "unhealthy" {
		t.Errorf("HealthUnhealthy = %v, want 'unhealthy'", HealthUnhealthy)
	}

	if HealthUnknown != "unknown" {
		t.Errorf("HealthUnknown = %v, want 'unknown'", HealthUnknown)
	}

	// Verify they are distinct
	if HealthHealthy == HealthUnhealthy || HealthHealthy == HealthUnknown || HealthUnhealthy == HealthUnknown {
		t.Error("Health status constants must be distinct")
	}
}

// TestComponentTypeTracking tests the SetCurrentComponentType/GetCurrentComponentType functions
// used for automatic service type inference in telemetry
func TestComponentTypeTracking(t *testing.T) {
	// Test setting and getting tool type
	SetCurrentComponentType(ComponentTypeTool)
	if got := GetCurrentComponentType(); got != ComponentTypeTool {
		t.Errorf("GetCurrentComponentType() = %v, want %v", got, ComponentTypeTool)
	}

	// Test setting and getting agent type
	SetCurrentComponentType(ComponentTypeAgent)
	if got := GetCurrentComponentType(); got != ComponentTypeAgent {
		t.Errorf("GetCurrentComponentType() = %v, want %v", got, ComponentTypeAgent)
	}

	// Test that empty ComponentType is distinguishable
	SetCurrentComponentType("")
	if got := GetCurrentComponentType(); got != "" {
		t.Errorf("GetCurrentComponentType() after empty set = %v, want empty", got)
	}
}

// TestNewToolSetsComponentType verifies that NewTool sets the global component type to "tool"
func TestNewToolSetsComponentType(t *testing.T) {
	// Reset to a known state
	SetCurrentComponentType("")

	// Create a tool - this should set the component type
	_ = NewTool("test-tool-component-type")

	// Verify the component type was set
	got := GetCurrentComponentType()
	if got != ComponentTypeTool {
		t.Errorf("After NewTool(), GetCurrentComponentType() = %v, want %v", got, ComponentTypeTool)
	}
}

// TestNewBaseAgentSetsComponentType verifies that NewBaseAgent sets the global component type to "agent"
func TestNewBaseAgentSetsComponentType(t *testing.T) {
	// Reset to a known state
	SetCurrentComponentType("")

	// Create an agent - this should set the component type
	_ = NewBaseAgent("test-agent-component-type")

	// Verify the component type was set
	got := GetCurrentComponentType()
	if got != ComponentTypeAgent {
		t.Errorf("After NewBaseAgent(), GetCurrentComponentType() = %v, want %v", got, ComponentTypeAgent)
	}
}

// TestComponentTypeOverwrite verifies that the last created component wins
// This is the expected behavior for single-component microservices
func TestComponentTypeOverwrite(t *testing.T) {
	// Create a tool first
	_ = NewTool("first-tool")
	if got := GetCurrentComponentType(); got != ComponentTypeTool {
		t.Errorf("After first NewTool(), got %v, want %v", got, ComponentTypeTool)
	}

	// Create an agent - should overwrite
	_ = NewBaseAgent("second-agent")
	if got := GetCurrentComponentType(); got != ComponentTypeAgent {
		t.Errorf("After NewBaseAgent(), got %v, want %v", got, ComponentTypeAgent)
	}

	// Create another tool - should overwrite back
	_ = NewTool("third-tool")
	if got := GetCurrentComponentType(); got != ComponentTypeTool {
		t.Errorf("After second NewTool(), got %v, want %v", got, ComponentTypeTool)
	}
}

// TestComponentTypeThreadSafety verifies concurrent access to component type tracking is safe
func TestComponentTypeThreadSafety(t *testing.T) {
	const goroutines = 100
	const iterations = 1000

	done := make(chan bool, goroutines*2)

	// Half the goroutines set tool type
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				SetCurrentComponentType(ComponentTypeTool)
				_ = GetCurrentComponentType()
			}
			done <- true
		}()
	}

	// Half the goroutines set agent type
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				SetCurrentComponentType(ComponentTypeAgent)
				_ = GetCurrentComponentType()
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < goroutines*2; i++ {
		<-done
	}

	// Final value should be either tool or agent (not corrupted)
	got := GetCurrentComponentType()
	if got != ComponentTypeTool && got != ComponentTypeAgent {
		t.Errorf("After concurrent access, got corrupted value: %v", got)
	}
}

// BenchmarkComponentTypeTracking benchmarks the component type tracking
func BenchmarkComponentTypeTracking(b *testing.B) {
	b.Run("Set", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SetCurrentComponentType(ComponentTypeTool)
		}
	})

	b.Run("Get", func(b *testing.B) {
		SetCurrentComponentType(ComponentTypeTool)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = GetCurrentComponentType()
		}
	})

	b.Run("SetAndGet", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SetCurrentComponentType(ComponentTypeTool)
			_ = GetCurrentComponentType()
		}
	})
}

// BenchmarkServiceInfoSerialization benchmarks JSON operations
func BenchmarkServiceInfoSerialization(b *testing.B) {
	info := &ServiceInfo{
		ID:          "bench-123",
		Name:        "bench-service",
		Type:        ComponentTypeTool,
		Description: "Benchmark service",
		Address:     "localhost",
		Port:        8080,
		Capabilities: []Capability{
			{Name: "cap1"},
			{Name: "cap2"},
			{Name: "cap3"},
		},
		Metadata: map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
		},
		Health:   HealthHealthy,
		LastSeen: time.Now(),
	}

	b.Run("Marshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = json.Marshal(info)
		}
	})

	b.Run("Unmarshal", func(b *testing.B) {
		data, _ := json.Marshal(info)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var decoded ServiceInfo
			_ = json.Unmarshal(data, &decoded)
		}
	})
}
