package orchestration

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestConvertBasicCapabilities_PropagatesType(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	basic := []core.Capability{
		{Name: "tool_cap", Description: "A tool", Type: core.CapabilityTool},
		{Name: "orch_cap", Description: "An orchestrator", Type: core.CapabilityOrchestrator},
		{Name: "reasoning_cap", Description: "Reasoning", Type: core.CapabilityReasoning},
		{Name: "default_cap", Description: "No type set"},
	}

	enhanced := catalog.convertBasicCapabilities(basic)

	if len(enhanced) != 4 {
		t.Fatalf("Expected 4 enhanced capabilities, got %d", len(enhanced))
	}

	if enhanced[0].Type != core.CapabilityTool {
		t.Errorf("Expected tool_cap.Type to be %q, got %q", core.CapabilityTool, enhanced[0].Type)
	}
	if enhanced[1].Type != core.CapabilityOrchestrator {
		t.Errorf("Expected orch_cap.Type to be %q, got %q", core.CapabilityOrchestrator, enhanced[1].Type)
	}
	if enhanced[2].Type != core.CapabilityReasoning {
		t.Errorf("Expected reasoning_cap.Type to be %q, got %q", core.CapabilityReasoning, enhanced[2].Type)
	}
	if enhanced[3].Type != "" {
		t.Errorf("Expected default_cap.Type to be empty, got %q", enhanced[3].Type)
	}
}

func TestEnrichCapabilities_PropagatesType(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// HTTP capabilities fetched from endpoint (no Type)
	httpCaps := []EnhancedCapability{
		{Name: "devops_operations", Description: "From HTTP"},
		{Name: "plain_tool", Description: "From HTTP"},
	}

	// Registration capabilities with Type set
	regCaps := []core.Capability{
		{Name: "devops_operations", Description: "From registration", Type: core.CapabilityOrchestrator},
		{Name: "plain_tool", Description: "From registration"},
	}

	enriched := catalog.enrichCapabilitiesWithInputSummary(httpCaps, regCaps)

	for _, cap := range enriched {
		if cap.Name == "devops_operations" && cap.Type != core.CapabilityOrchestrator {
			t.Errorf("devops_operations should have Type=%q after enrichment, got %q", core.CapabilityOrchestrator, cap.Type)
		}
		if cap.Name == "plain_tool" && cap.Type != "" {
			t.Errorf("plain_tool should have empty Type after enrichment, got %q", cap.Type)
		}
	}
}

func TestEnrichCapabilities_DoesNotOverwriteExistingType(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// HTTP capability already has Type from JSON deserialization
	httpCaps := []EnhancedCapability{
		{Name: "devops_operations", Description: "From HTTP", Type: core.CapabilityOrchestrator},
	}

	// Registration capability has no Type
	regCaps := []core.Capability{
		{Name: "devops_operations", Description: "From registration"},
	}

	enriched := catalog.enrichCapabilitiesWithInputSummary(httpCaps, regCaps)

	// Type from HTTP should be preserved (regCap.Type is empty, so no overwrite)
	if enriched[0].Type != core.CapabilityOrchestrator {
		t.Errorf("Expected Type to remain %q, got %q", core.CapabilityOrchestrator, enriched[0].Type)
	}
}
