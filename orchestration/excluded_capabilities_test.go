package orchestration

import (
	"strings"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// helper to create a catalog with directly populated agents (no HTTP needed)
func newTestCatalogWithAgents(agents map[string]*AgentInfo) *AgentCatalog {
	catalog := &AgentCatalog{
		agents:          agents,
		capabilityIndex: make(map[string][]string),
		mu:              sync.RWMutex{},
	}
	return catalog
}

func TestExcludedCapabilities_FormatForLLM(t *testing.T) {
	agents := map[string]*AgentInfo{
		"agent-a": {
			Registration: &core.ServiceRegistration{ID: "agent-a", Name: "agent-a"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_x", Description: "Capability X", Endpoint: "/api/x"},
				{Name: "cap_y", Description: "Capability Y", Endpoint: "/api/y"},
			},
		},
		"agent-b": {
			Registration: &core.ServiceRegistration{ID: "agent-b", Name: "agent-b"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_z", Description: "Capability Z", Endpoint: "/api/z"},
			},
		},
	}
	catalog := newTestCatalogWithAgents(agents)
	catalog.SetExcludedCapabilities([]string{"cap_x"})

	output := catalog.FormatForLLM()

	if strings.Contains(output, "cap_x") {
		t.Error("FormatForLLM() should not contain excluded capability cap_x")
	}
	if !strings.Contains(output, "cap_y") {
		t.Error("FormatForLLM() should contain non-excluded capability cap_y")
	}
	if !strings.Contains(output, "cap_z") {
		t.Error("FormatForLLM() should contain non-excluded capability cap_z")
	}
	// agent-a should still appear because it has cap_y
	if !strings.Contains(output, "agent-a") {
		t.Error("FormatForLLM() should still contain agent-a (has non-excluded cap_y)")
	}
}

func TestExcludedCapabilities_AgentFullyExcluded(t *testing.T) {
	agents := map[string]*AgentInfo{
		"agent-a": {
			Registration: &core.ServiceRegistration{ID: "agent-a", Name: "agent-a"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_x", Description: "Capability X", Endpoint: "/api/x"},
			},
		},
		"agent-b": {
			Registration: &core.ServiceRegistration{ID: "agent-b", Name: "agent-b"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_y", Description: "Capability Y", Endpoint: "/api/y"},
			},
		},
	}
	catalog := newTestCatalogWithAgents(agents)
	catalog.SetExcludedCapabilities([]string{"cap_x"})

	// FormatForLLM should not contain agent-a at all
	output := catalog.FormatForLLM()
	if strings.Contains(output, "agent-a") {
		t.Error("FormatForLLM() should not contain agent-a (its only capability is excluded)")
	}

	// GetPublicAgentNames should not contain agent-a
	names := catalog.GetPublicAgentNames()
	for _, name := range names {
		if name == "agent-a" {
			t.Error("GetPublicAgentNames() should not contain agent-a")
		}
	}
	found := false
	for _, name := range names {
		if name == "agent-b" {
			found = true
		}
	}
	if !found {
		t.Error("GetPublicAgentNames() should contain agent-b")
	}

	// GetCapabilitySummaries should have zero entries for agent-a
	summaries := catalog.GetCapabilitySummaries()
	for _, s := range summaries {
		if s.AgentName == "agent-a" {
			t.Errorf("GetCapabilitySummaries() should not contain agent-a, found cap %s", s.CapabilityName)
		}
	}
}

func TestExcludedCapabilities_CaseInsensitive(t *testing.T) {
	agents := map[string]*AgentInfo{
		"agent-a": {
			Registration: &core.ServiceRegistration{ID: "agent-a", Name: "agent-a"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_x", Description: "Capability X", Endpoint: "/api/x"},
			},
		},
	}
	catalog := newTestCatalogWithAgents(agents)

	// Exclude with uppercase — should still match lowercase cap_x
	catalog.SetExcludedCapabilities([]string{"CAP_X"})

	output := catalog.FormatForLLM()
	if strings.Contains(output, "cap_x") {
		t.Error("Exclusion should be case-insensitive: CAP_X should exclude cap_x")
	}

	summaries := catalog.GetCapabilitySummaries()
	if len(summaries) != 0 {
		t.Errorf("Expected 0 summaries after case-insensitive exclusion, got %d", len(summaries))
	}
}

func TestExcludedCapabilities_EmptyList(t *testing.T) {
	agents := map[string]*AgentInfo{
		"agent-a": {
			Registration: &core.ServiceRegistration{ID: "agent-a", Name: "agent-a"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_x", Description: "Capability X", Endpoint: "/api/x"},
				{Name: "cap_y", Description: "Capability Y", Endpoint: "/api/y"},
			},
		},
	}
	catalog := newTestCatalogWithAgents(agents)

	// Empty exclusion list — all capabilities should be visible
	catalog.SetExcludedCapabilities([]string{})

	output := catalog.FormatForLLM()
	if !strings.Contains(output, "cap_x") {
		t.Error("Empty exclusion list should leave cap_x visible")
	}
	if !strings.Contains(output, "cap_y") {
		t.Error("Empty exclusion list should leave cap_y visible")
	}

	// nil exclusion list (never called SetExcludedCapabilities)
	catalog2 := newTestCatalogWithAgents(agents)
	output2 := catalog2.FormatForLLM()
	if !strings.Contains(output2, "cap_x") || !strings.Contains(output2, "cap_y") {
		t.Error("Catalog with no exclusions should show all capabilities")
	}
}

func TestExcludedCapabilities_FormatToolsForLLM(t *testing.T) {
	agents := map[string]*AgentInfo{
		"agent-a": {
			Registration: &core.ServiceRegistration{ID: "agent-a", Name: "agent-a"},
			Capabilities: []EnhancedCapability{
				{Name: "cap_x", Description: "Capability X", Endpoint: "/api/x"},
				{Name: "cap_y", Description: "Capability Y", Endpoint: "/api/y"},
			},
		},
	}
	catalog := newTestCatalogWithAgents(agents)
	catalog.SetExcludedCapabilities([]string{"cap_x"})

	// Request both tool IDs — cap_x should be filtered even if requested
	output := catalog.FormatToolsForLLM([]string{"agent-a/cap_x", "agent-a/cap_y"})

	if strings.Contains(output, "cap_x") {
		t.Error("FormatToolsForLLM() should filter excluded cap_x even when explicitly requested")
	}
	if !strings.Contains(output, "cap_y") {
		t.Error("FormatToolsForLLM() should include non-excluded cap_y")
	}
}

func TestDefaultConfig_ExcludedCapabilities_EnvVar(t *testing.T) {
	// Set env var with whitespace, empty elements, trailing comma
	t.Setenv("TRUVAG3_EXCLUDED_CAPABILITIES", "cap_a, cap_b,,cap_c ")

	config := DefaultConfig()

	expected := []string{"cap_a", "cap_b", "cap_c"}
	if len(config.ExcludedCapabilities) != len(expected) {
		t.Fatalf("Expected %d excluded capabilities, got %d: %v",
			len(expected), len(config.ExcludedCapabilities), config.ExcludedCapabilities)
	}
	for i, exp := range expected {
		if config.ExcludedCapabilities[i] != exp {
			t.Errorf("ExcludedCapabilities[%d] = %q, want %q", i, config.ExcludedCapabilities[i], exp)
		}
	}
}

func TestDefaultConfig_ExcludedCapabilities_Unset(t *testing.T) {
	// Set to empty string — DefaultConfig() checks `excluded != ""`, so this
	// is functionally equivalent to unset while being safe with t.Setenv.
	t.Setenv("TRUVAG3_EXCLUDED_CAPABILITIES", "")

	config := DefaultConfig()

	if config.ExcludedCapabilities != nil {
		t.Errorf("Expected nil ExcludedCapabilities when env var is empty, got %v", config.ExcludedCapabilities)
	}
}
