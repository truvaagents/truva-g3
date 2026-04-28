package orchestration

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// --- RC1: OutputSummary → ReturnType.Fields conversion tests ---

func TestConvertBasicCapabilities_OutputSummary(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	caps := []core.Capability{
		{
			Name:        "analyze_data",
			Description: "Analyze data and return results",
			OutputSummary: &core.SchemaSummary{
				RequiredFields: []core.FieldHint{
					{Name: "analysis", Type: "string", Description: "Full analysis in markdown"},
					{Name: "model", Type: "string", Description: "Model used"},
				},
				OptionalFields: []core.FieldHint{
					{Name: "tokens_used", Type: "number", Description: "Token count", Example: "3773"},
				},
			},
		},
	}

	enhanced := catalog.convertBasicCapabilities(caps)

	if len(enhanced) != 1 {
		t.Fatalf("Expected 1 enhanced capability, got %d", len(enhanced))
	}

	fields := enhanced[0].Returns.Fields
	if len(fields) != 3 {
		t.Fatalf("Expected 3 return fields, got %d", len(fields))
	}

	// Required fields
	if fields[0].Name != "analysis" || !fields[0].Required {
		t.Errorf("Expected required field 'analysis', got name=%q required=%v", fields[0].Name, fields[0].Required)
	}
	if fields[0].Type != "string" {
		t.Errorf("Expected type 'string', got %q", fields[0].Type)
	}
	if fields[1].Name != "model" || !fields[1].Required {
		t.Errorf("Expected required field 'model', got name=%q required=%v", fields[1].Name, fields[1].Required)
	}

	// Optional field
	if fields[2].Name != "tokens_used" || fields[2].Required {
		t.Errorf("Expected optional field 'tokens_used', got name=%q required=%v", fields[2].Name, fields[2].Required)
	}
	if fields[2].Default != "3773" {
		t.Errorf("Expected default '3773', got %v", fields[2].Default)
	}
}

func TestConvertBasicCapabilities_NoOutputSummary(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	caps := []core.Capability{
		{Name: "simple_cap", Description: "No output schema"},
	}

	enhanced := catalog.convertBasicCapabilities(caps)

	if len(enhanced[0].Returns.Fields) != 0 {
		t.Errorf("Expected 0 return fields when no OutputSummary, got %d", len(enhanced[0].Returns.Fields))
	}
}

func TestEnrichCapabilitiesWithInputSummary_OutputFields(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// HTTP capabilities without output fields
	httpCaps := []EnhancedCapability{
		{
			Name:        "query_metrics",
			Description: "Query Prometheus metrics",
			Returns:     ReturnType{Type: "json", Description: "Metric results"},
		},
	}

	// Registration capabilities with OutputSummary
	regCaps := []core.Capability{
		{
			Name: "query_metrics",
			OutputSummary: &core.SchemaSummary{
				RequiredFields: []core.FieldHint{
					{Name: "samples", Type: "array", Description: "Metric samples"},
					{Name: "result_type", Type: "string", Description: "Result type"},
				},
			},
		},
	}

	enriched := catalog.enrichCapabilitiesWithInputSummary(httpCaps, regCaps)

	fields := enriched[0].Returns.Fields
	if len(fields) != 2 {
		t.Fatalf("Expected 2 return fields from enrichment, got %d", len(fields))
	}
	if fields[0].Name != "samples" {
		t.Errorf("Expected field 'samples', got %q", fields[0].Name)
	}
}

func TestEnrichCapabilitiesWithInputSummary_NoOverwrite(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// HTTP capabilities WITH existing output fields (from convertBasicCapabilities)
	httpCaps := []EnhancedCapability{
		{
			Name: "analyze_data",
			Returns: ReturnType{
				Type: "json",
				Fields: []Parameter{
					{Name: "analysis", Type: "string", Required: true},
				},
			},
		},
	}

	// Registration with different OutputSummary
	regCaps := []core.Capability{
		{
			Name: "analyze_data",
			OutputSummary: &core.SchemaSummary{
				RequiredFields: []core.FieldHint{
					{Name: "different_field", Type: "string"},
				},
			},
		},
	}

	enriched := catalog.enrichCapabilitiesWithInputSummary(httpCaps, regCaps)

	// Should NOT overwrite — original field should be preserved
	if len(enriched[0].Returns.Fields) != 1 {
		t.Fatalf("Expected 1 field (no overwrite), got %d", len(enriched[0].Returns.Fields))
	}
	if enriched[0].Returns.Fields[0].Name != "analysis" {
		t.Errorf("Expected original field 'analysis' preserved, got %q", enriched[0].Returns.Fields[0].Name)
	}
}

// --- RC2: FormatForLLM Return Fields rendering tests ---

func TestFormatForLLM_ReturnFields(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:   "test-1",
				Name: "research-agent",
			},
			Capabilities: []EnhancedCapability{
				{
					Name:        "analyze_data",
					Description: "Analyze data",
					Returns: ReturnType{
						Type:        "json",
						Description: "Analysis results",
						Fields: []Parameter{
							{Name: "analysis", Type: "string", Description: "Full analysis"},
							{Name: "model", Type: "string", Description: "Model used"},
							{Name: "tokens_used", Type: "number", Description: "Token count"},
						},
					},
				},
			},
		},
	}

	output := catalog.FormatForLLM()

	expectedStrings := []string{
		"Return Fields:",
		"analysis: string - Full analysis",
		"model: string - Model used",
		"tokens_used: number - Token count",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(output, expected) {
			t.Errorf("Expected output to contain %q\nFull output:\n%s", expected, output)
		}
	}
}

func TestFormatForLLM_NoReturnFields(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	catalog.agents = map[string]*AgentInfo{
		"test-1": {
			Registration: &core.ServiceRegistration{
				ID:   "test-1",
				Name: "simple-tool",
			},
			Capabilities: []EnhancedCapability{
				{
					Name:        "do_something",
					Description: "Does something",
					Returns: ReturnType{
						Type:        "string",
						Description: "Result text",
					},
				},
			},
		},
	}

	output := catalog.FormatForLLM()

	if strings.Contains(output, "Return Fields:") {
		t.Error("Expected no 'Return Fields:' section when Fields is empty")
	}
	if !strings.Contains(output, "Returns: string") {
		t.Error("Expected 'Returns: string' to still be present")
	}
}

// --- RC3: validateTemplatePaths tests ---

func TestValidateTemplatePaths_ValidPath(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	catalog.agents = map[string]*AgentInfo{
		"agent-1": {
			Registration: &core.ServiceRegistration{
				ID:   "agent-1",
				Name: "research-agent",
			},
			Capabilities: []EnhancedCapability{
				{
					Name: "analyze_data",
					Returns: ReturnType{
						Fields: []Parameter{
							{Name: "analysis", Type: "string"},
							{Name: "model", Type: "string"},
						},
					},
				},
			},
		},
	}

	o := &AIOrchestrator{catalog: catalog}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "research-agent",
				Metadata: map[string]interface{}{
					"capability": "analyze_data",
				},
			},
			{
				StepID:    "step-2",
				AgentName: "jira-tool",
				Metadata: map[string]interface{}{
					"capability": "create_issue",
					"parameters": map[string]interface{}{
						"description": "Result: {{step-1.response.data.analysis}}",
					},
				},
			},
		},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err != nil {
		t.Errorf("Expected valid template path to pass, got error: %v", err)
	}
}

func TestValidateTemplatePaths_HallucinatedField(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	catalog.agents = map[string]*AgentInfo{
		"agent-1": {
			Registration: &core.ServiceRegistration{
				ID:   "agent-1",
				Name: "research-agent",
			},
			Capabilities: []EnhancedCapability{
				{
					Name: "analyze_data",
					Returns: ReturnType{
						Fields: []Parameter{
							{Name: "analysis", Type: "string"},
							{Name: "model", Type: "string"},
						},
					},
				},
			},
		},
	}

	o := &AIOrchestrator{catalog: catalog}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "research-agent",
				Metadata: map[string]interface{}{
					"capability": "analyze_data",
				},
			},
			{
				StepID:    "step-2",
				AgentName: "jira-tool",
				Metadata: map[string]interface{}{
					"capability": "create_issue",
					"parameters": map[string]interface{}{
						"description": "Findings: {{step-1.response.data.key_findings}}",
					},
				},
			},
		},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err == nil {
		t.Fatal("Expected error for hallucinated field 'key_findings', got nil")
	}

	// Error should mention the hallucinated field and available fields
	if !strings.Contains(err.Error(), "key_findings") {
		t.Errorf("Expected error to mention 'key_findings', got: %v", err)
	}
	if !strings.Contains(err.Error(), "analysis") {
		t.Errorf("Expected error to list available field 'analysis', got: %v", err)
	}
}

func TestValidateTemplatePaths_NoOutputSchema_Passthrough(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	catalog.agents = map[string]*AgentInfo{
		"agent-1": {
			Registration: &core.ServiceRegistration{
				ID:   "agent-1",
				Name: "legacy-tool",
			},
			Capabilities: []EnhancedCapability{
				{
					Name: "do_something",
					Returns: ReturnType{
						Type:        "json",
						Description: "Some result",
						// No Fields — no OutputSummary declared
					},
				},
			},
		},
	}

	o := &AIOrchestrator{catalog: catalog}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "legacy-tool",
				Metadata: map[string]interface{}{
					"capability": "do_something",
				},
			},
			{
				StepID:    "step-2",
				AgentName: "other-tool",
				Metadata: map[string]interface{}{
					"capability": "consume",
					"parameters": map[string]interface{}{
						"input": "{{step-1.response.data.anything_goes}}",
					},
				},
			},
		},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err != nil {
		t.Errorf("Expected passthrough when no output schema, got error: %v", err)
	}
}

func TestValidateTemplatePaths_NilPlan(t *testing.T) {
	o := &AIOrchestrator{}

	err := o.validateTemplatePaths(nil, nil)
	if err != nil {
		t.Errorf("Expected nil error for nil plan, got: %v", err)
	}
}

func TestValidateTemplatePaths_NoTemplates(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	o := &AIOrchestrator{catalog: catalog}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{
				StepID:    "step-1",
				AgentName: "some-tool",
				Metadata: map[string]interface{}{
					"capability": "do_work",
					"parameters": map[string]interface{}{
						"query": "plain string with no templates",
					},
				},
			},
		},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err != nil {
		t.Errorf("Expected nil error when no templates, got: %v", err)
	}
}

func TestValidateTemplatePaths_MultipleTemplatesInOneParam(t *testing.T) {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	catalog.agents = map[string]*AgentInfo{
		"agent-1": {
			Registration: &core.ServiceRegistration{
				ID:   "agent-1",
				Name: "data-agent",
			},
			Capabilities: []EnhancedCapability{
				{
					Name: "get_data",
					Returns: ReturnType{
						Fields: []Parameter{
							{Name: "lat", Type: "number"},
							{Name: "lng", Type: "number"},
						},
					},
				},
			},
		},
	}

	o := &AIOrchestrator{catalog: catalog}

	t.Run("both valid", func(t *testing.T) {
		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{
					StepID: "step-1", AgentName: "data-agent",
					Metadata: map[string]interface{}{"capability": "get_data"},
				},
				{
					StepID: "step-2", AgentName: "map-tool",
					Metadata: map[string]interface{}{
						"capability": "show_map",
						"parameters": map[string]interface{}{
							"coords": "{{step-1.response.data.lat}},{{step-1.response.data.lng}}",
						},
					},
				},
			},
		}
		err := o.validateTemplatePaths(plan, nil)
		if err != nil {
			t.Errorf("Expected valid, got: %v", err)
		}
	})

	t.Run("one valid one hallucinated", func(t *testing.T) {
		plan := &RoutingPlan{
			Steps: []RoutingStep{
				{
					StepID: "step-1", AgentName: "data-agent",
					Metadata: map[string]interface{}{"capability": "get_data"},
				},
				{
					StepID: "step-2", AgentName: "map-tool",
					Metadata: map[string]interface{}{
						"capability": "show_map",
						"parameters": map[string]interface{}{
							"coords": "{{step-1.response.data.lat}},{{step-1.response.data.altitude}}",
						},
					},
				},
			},
		}
		err := o.validateTemplatePaths(plan, nil)
		if err == nil {
			t.Fatal("Expected error for hallucinated 'altitude'")
		}
		if !strings.Contains(err.Error(), "altitude") {
			t.Errorf("Expected error to mention 'altitude', got: %v", err)
		}
	})
}

func TestValidateTemplatePaths_UnknownReferencedStep_Rejects(t *testing.T) {
	// ORCH-020 RC1: an unknown referenced step is a plan error because no prior
	// phase can supply it. The validator must reject for replan rather than
	// silently continuing (the previous behavior was a correctness defect —
	// the executor then dispatched with a literal {{step-99.response.data.field}}).
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)
	o := &AIOrchestrator{catalog: catalog}

	plan := &RoutingPlan{
		Steps: []RoutingStep{
			{
				StepID: "step-1", AgentName: "some-tool",
				Metadata: map[string]interface{}{
					"capability": "do_work",
					"parameters": map[string]interface{}{
						"input": "{{step-99.response.data.field}}",
					},
				},
			},
		},
	}

	err := o.validateTemplatePaths(plan, nil)
	if err == nil {
		t.Fatalf("Expected rejection for unknown step reference, got nil")
	}
	if !strings.Contains(err.Error(), "step-99") {
		t.Errorf("Expected error to name step-99, got: %v", err)
	}
}
