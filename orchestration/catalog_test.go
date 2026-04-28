//go:build integration
// +build integration

package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// NOTE: MockDiscovery is defined in mocks_test.go - using that one for these tests

func TestAgentCatalog_Refresh(t *testing.T) {
	// Create mock discovery with test services
	discovery := NewMockDiscovery()

	// Register test agents
	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "stock-1",
		Name:         "stock-analyzer",
		Address:      "localhost",
		Port:         8080,
		Capabilities: []core.Capability{{Name: "analyze_stock"}, {Name: "get_price"}},
		Health:       core.HealthHealthy,
	})

	_ = discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:           "news-1",
		Name:         "news-agent",
		Address:      "localhost",
		Port:         8081,
		Capabilities: []core.Capability{{Name: "get_news"}, {Name: "analyze_sentiment"}},
		Health:       core.HealthHealthy,
	})

	// Create catalog
	catalog := NewAgentCatalog(discovery)

	// Test initial state
	agents := catalog.GetAgents()
	if len(agents) != 0 {
		t.Errorf("Expected empty catalog, got %d agents", len(agents))
	}

	// Create mock HTTP server for capabilities endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capabilities := []EnhancedCapability{
			{
				Name:        "analyze_stock",
				Description: "Analyzes stock performance",
				Endpoint:    "/api/analyze",
				Parameters: []Parameter{
					{Name: "symbol", Type: "string", Required: true},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(capabilities)
	}))
	defer server.Close()

	// Note: In real test, we'd need to mock the HTTP calls properly
	// For now, test the basic refresh mechanism
	ctx := context.Background()
	err := catalog.Refresh(ctx)
	if err != nil {
		t.Errorf("Refresh failed: %v", err)
	}

	// Verify agents were loaded (even if capabilities fetch failed)
	agents = catalog.GetAgents()
	if len(agents) == 0 {
		t.Error("Expected agents to be loaded after refresh")
	}
}

func TestAgentCatalog_FindByCapability(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// Manually populate catalog for testing
	catalog.agents = map[string]*AgentInfo{
		"stock-1": {
			Registration: &core.ServiceRegistration{
				ID:   "stock-1",
				Name: "stock-analyzer",
			},
			Capabilities: []EnhancedCapability{
				{Name: "analyze_stock"},
				{Name: "get_price"},
			},
		},
		"news-1": {
			Registration: &core.ServiceRegistration{
				ID:   "news-1",
				Name: "news-agent",
			},
			Capabilities: []EnhancedCapability{
				{Name: "get_news"},
			},
		},
	}

	// Rebuild capability index
	catalog.capabilityIndex = make(map[string][]string)
	for id, agent := range catalog.agents {
		for _, cap := range agent.Capabilities {
			catalog.capabilityIndex[cap.Name] = append(catalog.capabilityIndex[cap.Name], id)
		}
	}

	// Test finding by capability
	agents := catalog.FindByCapability("analyze_stock")
	if len(agents) != 1 || agents[0] != "stock-1" {
		t.Errorf("Expected to find stock-1 for analyze_stock, got %v", agents)
	}

	agents = catalog.FindByCapability("get_news")
	if len(agents) != 1 || agents[0] != "news-1" {
		t.Errorf("Expected to find news-1 for get_news, got %v", agents)
	}

	agents = catalog.FindByCapability("nonexistent")
	if len(agents) != 0 {
		t.Errorf("Expected no agents for nonexistent capability, got %v", agents)
	}
}

func TestAgentCatalog_FormatForLLM(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// Setup test data
	catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:      "test-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
			},
			Capabilities: []EnhancedCapability{
				{
					Name:        "test_capability",
					Description: "Test capability description",
					Parameters: []Parameter{
						{
							Name:        "param1",
							Type:        "string",
							Required:    true,
							Description: "Test parameter",
						},
					},
					Returns: ReturnType{
						Type:        "object",
						Description: "Test result",
					},
				},
			},
		},
	}

	output := catalog.FormatForLLM()

	// Verify output contains expected information
	if output == "" {
		t.Error("Expected non-empty LLM format output")
	}

	expectedStrings := []string{
		"Available Agents",
		"test-agent",
		"test_capability",
		"Test capability description",
		"param1: string (required)",
		"Returns: object",
	}

	for _, expected := range expectedStrings {
		if !catalogContains(output, expected) {
			t.Errorf("Expected output to contain '%s'", expected)
		}
	}
}

func TestAgentCatalog_GetAgent(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	testAgent := &AgentInfo{
		Registration: &core.ServiceRegistration{
			ID:   "test-1",
			Name: "test-agent",
		},
		LastUpdated: time.Now(),
	}

	catalog.agents = map[string]*AgentInfo{
		"test-1": testAgent,
	}

	// Test getting existing agent
	agent := catalog.GetAgent("test-1")
	if agent == nil {
		t.Fatal("Expected to get agent, got nil")
	}
	if agent.Registration.Name != "test-agent" {
		t.Errorf("Expected agent name 'test-agent', got %s", agent.Registration.Name)
	}

	// Test getting non-existent agent
	agent = catalog.GetAgent("nonexistent")
	if agent != nil {
		t.Error("Expected nil for non-existent agent")
	}
}

func TestAgentCatalog_ConvertBasicCapabilities(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	basic := []core.Capability{
		{Name: "capability1"},
		{Name: "capability2"},
	}
	enhanced := catalog.convertBasicCapabilities(basic)

	if len(enhanced) != 2 {
		t.Errorf("Expected 2 enhanced capabilities, got %d", len(enhanced))
	}

	if enhanced[0].Name != "capability1" {
		t.Errorf("Expected capability name 'capability1', got %s", enhanced[0].Name)
	}

	if enhanced[0].Endpoint != "/api/capability1" {
		t.Errorf("Expected endpoint '/api/capability1', got %s", enhanced[0].Endpoint)
	}
}

// Helper function - using local version to avoid conflicts with mocks_test.go
func catalogContains(str, substr string) bool {
	return strings.Contains(str, substr)
}

// ============================================================================
// Internal Capability Filtering Tests (Self-Referential Orchestration Bug Fix)
// ============================================================================

func TestFormatForLLM_FiltersInternalCapabilities(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// Setup agent with mix of public and internal capabilities
	catalog.agents = map[string]*AgentInfo{
		"test-agent-1": {
			Registration: &core.ServiceInfo{
				ID:      "test-agent-1",
				Name:    "test-agent",
				Address: "localhost",
				Port:    8080,
			},
			Capabilities: []EnhancedCapability{
				{Name: "public_capability", Description: "Public capability", Internal: false},
				{Name: "orchestrate_natural", Description: "Internal orchestration", Internal: true},
				{Name: "another_public", Description: "Another public capability", Internal: false},
			},
		},
	}

	output := catalog.FormatForLLM()

	// Public capabilities should be present
	if !catalogContains(output, "public_capability") {
		t.Error("Expected public_capability in LLM output")
	}
	if !catalogContains(output, "another_public") {
		t.Error("Expected another_public in LLM output")
	}

	// Internal capability should be filtered out
	if catalogContains(output, "orchestrate_natural") {
		t.Error("Internal capability orchestrate_natural should NOT be in LLM output")
	}
}

func TestFormatForLLM_SkipsAgentWithOnlyInternalCapabilities(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// Setup two agents - one with only internal capabilities, one with public
	catalog.agents = map[string]*AgentInfo{
		"orchestrator-only": {
			Registration: &core.ServiceInfo{
				ID:      "orchestrator-only",
				Name:    "orchestrator-agent",
				Address: "localhost",
				Port:    8090,
			},
			Capabilities: []EnhancedCapability{
				{Name: "orchestrate_natural", Description: "Internal", Internal: true},
				{Name: "execute_workflow", Description: "Also internal", Internal: true},
			},
		},
		"tool-agent": {
			Registration: &core.ServiceInfo{
				ID:      "tool-agent",
				Name:    "weather-tool",
				Address: "localhost",
				Port:    8091,
			},
			Capabilities: []EnhancedCapability{
				{Name: "get_weather", Description: "Public weather capability", Internal: false},
			},
		},
	}

	output := catalog.FormatForLLM()

	// Agent with only internal capabilities should be excluded entirely
	if catalogContains(output, "orchestrator-agent") {
		t.Error("Agent with only internal capabilities should NOT appear in LLM output")
	}

	// Agent with public capabilities should be present
	if !catalogContains(output, "weather-tool") {
		t.Error("Agent with public capabilities should appear in LLM output")
	}
	if !catalogContains(output, "get_weather") {
		t.Error("Public capability get_weather should appear in LLM output")
	}
}

func TestFormatForLLM_InternalFalseByDefault(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// Setup agent with capability without Internal field explicitly set
	// (Go zero value for bool is false, so it should be treated as public)
	catalog.agents = map[string]*AgentInfo{
		"legacy-agent": {
			Registration: &core.ServiceInfo{
				ID:      "legacy-agent",
				Name:    "legacy-service",
				Address: "localhost",
				Port:    8080,
			},
			Capabilities: []EnhancedCapability{
				{Name: "legacy_capability", Description: "Legacy capability"},
				// Internal field not set - should default to false
			},
		},
	}

	output := catalog.FormatForLLM()

	// Capability without Internal field should appear (default: false = public)
	if !catalogContains(output, "legacy_capability") {
		t.Error("Capability without Internal field should appear in LLM output (default: public)")
	}
}

func TestConvertBasicCapabilities_PreservesInternalFlag(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	basic := []core.Capability{
		{Name: "public_cap", Description: "Public", Internal: false},
		{Name: "internal_cap", Description: "Internal", Internal: true},
	}

	enhanced := catalog.convertBasicCapabilities(basic)

	if len(enhanced) != 2 {
		t.Fatalf("Expected 2 enhanced capabilities, got %d", len(enhanced))
	}

	// Verify Internal flag is preserved
	if enhanced[0].Internal != false {
		t.Error("Expected public_cap.Internal to be false")
	}
	if enhanced[1].Internal != true {
		t.Error("Expected internal_cap.Internal to be true")
	}
}

func TestEnrichCapabilities_PropagatesInternalFlag(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// HTTP capabilities fetched from endpoint (no Internal flag)
	httpCaps := []EnhancedCapability{
		{Name: "orchestrate_natural", Description: "From HTTP"},
		{Name: "public_cap", Description: "From HTTP"},
	}

	// Registration capabilities with Internal flags
	regCaps := []core.Capability{
		{Name: "orchestrate_natural", Description: "From registration", Internal: true},
		{Name: "public_cap", Description: "From registration", Internal: false},
	}

	enriched := catalog.enrichCapabilitiesWithInputSummary(httpCaps, regCaps)

	// Verify Internal flag was propagated from registration
	for _, cap := range enriched {
		if cap.Name == "orchestrate_natural" && !cap.Internal {
			t.Error("orchestrate_natural should have Internal=true after enrichment")
		}
		if cap.Name == "public_cap" && cap.Internal {
			t.Error("public_cap should have Internal=false after enrichment")
		}
	}
}
