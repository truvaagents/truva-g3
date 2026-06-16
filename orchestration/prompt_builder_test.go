package orchestration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// TypeRule Validation Tests
// =============================================================================

func TestValidateTypeRule_Valid(t *testing.T) {
	rule := TypeRule{
		TypeNames: []string{"number", "float64"},
		JsonType:  "JSON numbers",
		Example:   "42.5",
	}

	err := ValidateTypeRule(rule)
	if err != nil {
		t.Errorf("expected no error for valid rule, got: %v", err)
	}
}

func TestValidateTypeRule_EmptyTypeNames(t *testing.T) {
	rule := TypeRule{
		TypeNames: []string{},
		JsonType:  "JSON numbers",
		Example:   "42.5",
	}

	err := ValidateTypeRule(rule)
	if err == nil {
		t.Error("expected error for empty TypeNames")
	}
	if verr, ok := err.(*ValidationError); ok {
		if verr.Field != "TypeNames" {
			t.Errorf("expected field 'TypeNames', got: %s", verr.Field)
		}
	}
}

func TestValidateTypeRule_EmptyJsonType(t *testing.T) {
	rule := TypeRule{
		TypeNames: []string{"number"},
		JsonType:  "",
		Example:   "42.5",
	}

	err := ValidateTypeRule(rule)
	if err == nil {
		t.Error("expected error for empty JsonType")
	}
	if verr, ok := err.(*ValidationError); ok {
		if verr.Field != "JsonType" {
			t.Errorf("expected field 'JsonType', got: %s", verr.Field)
		}
	}
}

func TestValidateTypeRule_EmptyExample(t *testing.T) {
	rule := TypeRule{
		TypeNames: []string{"number"},
		JsonType:  "JSON numbers",
		Example:   "",
	}

	err := ValidateTypeRule(rule)
	if err == nil {
		t.Error("expected error for empty Example")
	}
	if verr, ok := err.(*ValidationError); ok {
		if verr.Field != "Example" {
			t.Errorf("expected field 'Example', got: %s", verr.Field)
		}
	}
}

func TestValidationError_Error(t *testing.T) {
	err := &ValidationError{Field: "TestField", Message: "test message"}
	expected := "validation error for TestField: test message"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

// =============================================================================
// DefaultPromptBuilder Tests
// =============================================================================

func TestNewDefaultPromptBuilder_NilConfig(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if builder == nil {
		t.Fatal("expected builder, got nil")
	}

	// Should have default type rules
	rules := builder.GetTypeRules()
	if len(rules) < 6 {
		t.Errorf("expected at least 6 default rules, got: %d", len(rules))
	}
}

func TestNewDefaultPromptBuilder_WithAdditionalRules(t *testing.T) {
	config := &PromptConfig{
		AdditionalTypeRules: []TypeRule{
			{
				TypeNames: []string{"currency"},
				JsonType:  "JSON strings",
				Example:   `"USD"`,
			},
		},
	}

	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rules := builder.GetTypeRules()
	// Should have default rules + 1 additional
	if len(rules) < 7 {
		t.Errorf("expected at least 7 rules (6 default + 1 custom), got: %d", len(rules))
	}
}

func TestNewDefaultPromptBuilder_InvalidAdditionalRule(t *testing.T) {
	config := &PromptConfig{
		AdditionalTypeRules: []TypeRule{
			{
				TypeNames: []string{}, // Invalid: empty
				JsonType:  "JSON strings",
				Example:   `"test"`,
			},
		},
	}

	_, err := NewDefaultPromptBuilder(config)
	if err == nil {
		t.Error("expected error for invalid additional rule")
	}
}

func TestDefaultPromptBuilder_BuildPlanningPrompt(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: weather-tool, currency-tool",
		Request:        "What is the weather in Tokyo?",
		Metadata:       nil,
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify prompt contains key elements
	checks := []string{
		"Available tools: weather-tool, currency-tool",
		"What is the weather in Tokyo?",
		"JSON numbers",
		"JSON strings",
		"plan_id",
		"step_id",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt should contain %q", check)
		}
	}
}

func TestDefaultPromptBuilder_DomainHealthcare(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		Domain: "healthcare",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: patient-lookup",
		Request:        "Look up patient records",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify healthcare-specific content
	if !strings.Contains(prompt, "HEALTHCARE DOMAIN REQUIREMENTS") {
		t.Error("prompt should contain healthcare domain requirements")
	}
	if !strings.Contains(prompt, "HIPAA") {
		t.Error("prompt should mention HIPAA")
	}
}

func TestDefaultPromptBuilder_DomainFinance(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		Domain: "finance",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: trading-tool",
		Request:        "Execute trade",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, "FINANCE DOMAIN REQUIREMENTS") {
		t.Error("prompt should contain finance domain requirements")
	}
}

func TestDefaultPromptBuilder_DomainLegal(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		Domain: "legal",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: document-tool",
		Request:        "Review contract",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, "LEGAL DOMAIN REQUIREMENTS") {
		t.Error("prompt should contain legal domain requirements")
	}
}

func TestDefaultPromptBuilder_CustomInstructions(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		CustomInstructions: []string{
			"Always use local tools first",
			"Minimize API calls",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: test-tool",
		Request:        "Test request",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, "Always use local tools first") {
		t.Error("prompt should contain first custom instruction")
	}
	if !strings.Contains(prompt, "Minimize API calls") {
		t.Error("prompt should contain second custom instruction")
	}
}

func TestDefaultPromptBuilder_DisableAntiPatterns(t *testing.T) {
	includeAnti := false
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		IncludeAntiPatterns: &includeAnti,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: test-tool",
		Request:        "Test request",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Anti-patterns should NOT be included
	if strings.Contains(prompt, `NOT strings for literal values (e.g., "35.6897")`) {
		t.Error("prompt should NOT contain anti-patterns when disabled")
	}
}

func TestDefaultPromptBuilder_SetLogger(t *testing.T) {
	builder, _ := NewDefaultPromptBuilder(nil)
	mockLogger := &MockLogger{}

	builder.SetLogger(mockLogger)

	// Build prompt to trigger logging
	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}
	_, _ = builder.BuildPlanningPrompt(context.Background(), input)

	// Logger should have been called
	if len(mockLogger.debugCalls) == 0 {
		t.Error("expected logger.Debug to be called")
	}
}

// =============================================================================
// Step Reference Instructions Tests (STEP_REFERENCE_TEMPLATE_BUG fix)
// =============================================================================

func TestDefaultPromptBuilder_IncludesStepReferenceGuidance(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: item-lookup-tool, inventory-tool",
		Request:        "Get item details and check inventory",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step reference guidance is now conveyed via instructions + concrete example
	criticalElements := []string{
		`{{step-N.response.data.field}}`, // Instruction #4
		"depends_on",                     // Instruction #5 and concrete example
		"data.lat",                       // Dot notation in example key rules
	}

	for _, element := range criticalElements {
		if !strings.Contains(prompt, element) {
			t.Errorf("prompt should contain %q for step reference guidance", element)
		}
	}
}

func TestDefaultPromptBuilder_IncludesDependencyExample(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify concrete example shows dependency chain with template references
	dependencyElements := []string{
		"step-3",
		`"depends_on": ["step-1"]`,
		"{{step-1.response.data.lat}}",
		"{{step-1.response.data.lon}}",
	}

	for _, element := range dependencyElements {
		if !strings.Contains(prompt, element) {
			t.Errorf("prompt should contain dependency example with %q", element)
		}
	}
}

func TestDefaultPromptBuilder_NoAntiPatternsInPrompt(t *testing.T) {
	// Per BUG_PHASE3_SKIPPED_EXECUTION.md Issue 5 P3: anti-patterns removed
	// from prompt to avoid Pink Elephant effect.
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Anti-patterns should no longer be present (positive instructions only)
	removedPatterns := []string{
		"WRONG",
		"NOT strings for literal values",
	}

	for _, pattern := range removedPatterns {
		if strings.Contains(prompt, pattern) {
			t.Errorf("prompt should NOT contain negative pattern %q", pattern)
		}
	}
}

func TestDefaultPromptBuilder_IncludesConcreteExample(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify concrete example is present (replaces verbose stepReferenceInstructions)
	exampleElements := []string{
		"<example>",
		"{{step-1.response.data.lat}}",
		"{{step-1.response.data.lon}}",
		"depends_on",
		"geocoding-tool",
	}

	for _, element := range exampleElements {
		if !strings.Contains(prompt, element) {
			t.Errorf("prompt should contain concrete example element %q", element)
		}
	}
}

func TestDefaultPromptBuilder_InstructionsSectionIncludesStepReferences(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify instructions section covers step references and dependencies
	instructionRules := []string{
		`<instructions>`,
		`{{step-N.response.data.field}}`,
		"Declare dependencies in depends_on",
	}

	for _, rule := range instructionRules {
		if !strings.Contains(prompt, rule) {
			t.Errorf("prompt instructions section should contain %q", rule)
		}
	}
}

// =============================================================================
// TemplatePromptBuilder Tests
// =============================================================================

func TestNewTemplatePromptBuilder_NilConfig(t *testing.T) {
	_, err := NewTemplatePromptBuilder(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestNewTemplatePromptBuilder_NoTemplate(t *testing.T) {
	_, err := NewTemplatePromptBuilder(&PromptConfig{})
	if err == nil {
		t.Error("expected error when neither TemplateFile nor Template is set")
	}
}

func TestNewTemplatePromptBuilder_InlineTemplate(t *testing.T) {
	config := &PromptConfig{
		Template: `Capabilities: {{.CapabilityInfo}}
Request: {{.Request}}
Type Rules: {{.TypeRules}}`,
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if builder == nil {
		t.Fatal("expected builder, got nil")
	}
}

func TestNewTemplatePromptBuilder_InvalidTemplate(t *testing.T) {
	config := &PromptConfig{
		Template: `{{.InvalidSyntax`, // Missing closing braces
	}

	_, err := NewTemplatePromptBuilder(config)
	if err == nil {
		t.Error("expected error for invalid template syntax")
	}
}

func TestTemplatePromptBuilder_BuildPlanningPrompt(t *testing.T) {
	config := &PromptConfig{
		Template: `=== CUSTOM TEMPLATE ===
Capabilities: {{.CapabilityInfo}}
Request: {{.Request}}
Domain: {{.Domain}}
=== END ===`,
		Domain: "test-domain",
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "tool-a, tool-b",
		Request:        "Do something",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"=== CUSTOM TEMPLATE ===",
		"Capabilities: tool-a, tool-b",
		"Request: Do something",
		"Domain: test-domain",
		"=== END ===",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt should contain %q, got: %s", check, prompt)
		}
	}
}

func TestTemplatePromptBuilder_TemplateWithTypeRules(t *testing.T) {
	config := &PromptConfig{
		Template: `Type Rules:
{{.TypeRules}}`,
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should include default type rules
	if !strings.Contains(prompt, "JSON numbers") {
		t.Error("prompt should contain type rules from fallback")
	}
}

func TestTemplatePromptBuilder_FileNotFound(t *testing.T) {
	config := &PromptConfig{
		TemplateFile: "/nonexistent/path/template.tmpl",
	}

	_, err := NewTemplatePromptBuilder(config)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestTemplatePromptBuilder_TemplateFile(t *testing.T) {
	// Create temporary template file
	tmpFile, err := os.CreateTemp("", "test-template-*.tmpl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	templateContent := `FILE TEMPLATE
Request: {{.Request}}
Capabilities: {{.CapabilityInfo}}`
	if _, err := tmpFile.WriteString(templateContent); err != nil {
		t.Fatalf("failed to write template: %v", err)
	}
	_ = tmpFile.Close()

	config := &PromptConfig{
		TemplateFile: tmpFile.Name(),
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "file-test-tool",
		Request:        "file test request",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, "FILE TEMPLATE") {
		t.Error("prompt should contain content from template file")
	}
	if !strings.Contains(prompt, "file-test-tool") {
		t.Error("prompt should contain capability info")
	}
}

func TestTemplatePromptBuilder_GetFallback(t *testing.T) {
	config := &PromptConfig{
		Template: "test",
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fallback := builder.GetFallback()
	if fallback == nil {
		t.Error("expected fallback builder, got nil")
	}
}

func TestTemplatePromptBuilder_SetLoggerPropagates(t *testing.T) {
	config := &PromptConfig{
		Template: "test",
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mockLogger := &MockLogger{}
	builder.SetLogger(mockLogger)

	// Fallback should also have the logger
	if builder.GetFallback() == nil {
		t.Error("fallback should exist")
	}
}

// =============================================================================
// PromptConfig Environment Loading Tests
// =============================================================================

func TestPromptConfig_LoadFromEnv_TemplateFile(t *testing.T) {
	os.Setenv("TRUVAG3_PROMPT_TEMPLATE_FILE", "/config/custom-template.tmpl")
	defer os.Unsetenv("TRUVAG3_PROMPT_TEMPLATE_FILE")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.TemplateFile != "/config/custom-template.tmpl" {
		t.Errorf("expected template file path, got: %s", config.TemplateFile)
	}
}

func TestPromptConfig_LoadFromEnv_Domain(t *testing.T) {
	os.Setenv("TRUVAG3_PROMPT_DOMAIN", "healthcare")
	defer os.Unsetenv("TRUVAG3_PROMPT_DOMAIN")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.Domain != "healthcare" {
		t.Errorf("expected domain 'healthcare', got: %s", config.Domain)
	}
}

func TestPromptConfig_LoadFromEnv_TypeRules(t *testing.T) {
	rulesJSON := `[{"type_names":["custom_type"],"json_type":"JSON custom","example":"test"}]`
	os.Setenv("TRUVAG3_PROMPT_TYPE_RULES", rulesJSON)
	defer os.Unsetenv("TRUVAG3_PROMPT_TYPE_RULES")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(config.AdditionalTypeRules) != 1 {
		t.Fatalf("expected 1 type rule, got: %d", len(config.AdditionalTypeRules))
	}
	if config.AdditionalTypeRules[0].TypeNames[0] != "custom_type" {
		t.Errorf("unexpected type name: %v", config.AdditionalTypeRules[0].TypeNames)
	}
}

func TestPromptConfig_LoadFromEnv_InvalidTypeRulesJSON(t *testing.T) {
	os.Setenv("TRUVAG3_PROMPT_TYPE_RULES", "invalid json")
	defer os.Unsetenv("TRUVAG3_PROMPT_TYPE_RULES")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestPromptConfig_LoadFromEnv_InvalidTypeRule(t *testing.T) {
	// Valid JSON but invalid rule (empty type_names)
	rulesJSON := `[{"type_names":[],"json_type":"JSON custom","example":"test"}]`
	os.Setenv("TRUVAG3_PROMPT_TYPE_RULES", rulesJSON)
	defer os.Unsetenv("TRUVAG3_PROMPT_TYPE_RULES")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err == nil {
		t.Error("expected error for invalid type rule")
	}
}

func TestPromptConfig_LoadFromEnv_CustomInstructions(t *testing.T) {
	instructionsJSON := `["instruction 1", "instruction 2"]`
	os.Setenv("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS", instructionsJSON)
	defer os.Unsetenv("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(config.CustomInstructions) != 2 {
		t.Fatalf("expected 2 instructions, got: %d", len(config.CustomInstructions))
	}
}

func TestPromptConfig_LoadFromEnv_InvalidCustomInstructionsJSON(t *testing.T) {
	os.Setenv("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS", "not json array")
	defer os.Unsetenv("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS")

	config := &PromptConfig{}
	err := config.LoadFromEnv()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestPromptConfig_MustLoadFromEnv_Panic(t *testing.T) {
	os.Setenv("TRUVAG3_PROMPT_TYPE_RULES", "invalid json")
	defer os.Unsetenv("TRUVAG3_PROMPT_TYPE_RULES")

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid config")
		}
	}()

	config := &PromptConfig{}
	config.MustLoadFromEnv() // Should panic
}

// =============================================================================
// Mock Logger for Testing
// =============================================================================

type MockLogger struct {
	infoCalls  []string
	warnCalls  []string
	errorCalls []string
	debugCalls []string
}

func (m *MockLogger) Info(msg string, fields map[string]interface{}) {
	m.infoCalls = append(m.infoCalls, msg)
}

func (m *MockLogger) Warn(msg string, fields map[string]interface{}) {
	m.warnCalls = append(m.warnCalls, msg)
}

func (m *MockLogger) Error(msg string, fields map[string]interface{}) {
	m.errorCalls = append(m.errorCalls, msg)
}

func (m *MockLogger) Debug(msg string, fields map[string]interface{}) {
	m.debugCalls = append(m.debugCalls, msg)
}

func (m *MockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.infoCalls = append(m.infoCalls, msg)
}

func (m *MockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.warnCalls = append(m.warnCalls, msg)
}

func (m *MockLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.errorCalls = append(m.errorCalls, msg)
}

func (m *MockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.debugCalls = append(m.debugCalls, msg)
}

// =============================================================================
// SystemInstructions / Persona Tests
// =============================================================================

func TestBuildSystemPrompt_DefaultWhenEmpty(t *testing.T) {
	config := &PromptConfig{SystemInstructions: ""}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := builder.BuildSystemPrompt(context.Background(), PromptInput{})

	// Default persona must still appear verbatim.
	persona := "You are an intelligent orchestrator that creates execution plans for multi-agent systems."
	if !strings.Contains(result, persona) {
		t.Errorf("expected result to contain default persona %q, got %q", persona, result)
	}
	// ORCH-020 RC7: the system prompt now carries a <runtime_context> block
	// with today's date so the planner resolves relative dates without
	// inventing {{today_plus_1}}-style macros.
	if !strings.Contains(result, "<runtime_context>") {
		t.Errorf("expected result to contain <runtime_context> tag, got %q", result)
	}
	if !strings.Contains(result, "Current date (UTC):") {
		t.Errorf("expected result to include current UTC date hint, got %q", result)
	}
}

func TestBuildSystemPrompt_CustomPersonaWithOrchestratorRole(t *testing.T) {
	config := &PromptConfig{
		SystemInstructions: "You are a travel planning specialist.",
	}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := builder.BuildSystemPrompt(context.Background(), PromptInput{})

	// Should contain the custom persona
	if !strings.Contains(result, "You are a travel planning specialist.") {
		t.Error("result should contain custom persona")
	}

	// Should also contain the orchestrator function as subordinate role
	if !strings.Contains(result, "As an AI orchestrator, you manage a multi-agent system") {
		t.Error("result should contain orchestrator function")
	}
}

func TestBuildSystemPrompt_MultilineInstructions(t *testing.T) {
	config := &PromptConfig{
		SystemInstructions: `You are a travel planning assistant.
Always check weather before recommending outdoor activities.
Prefer real-time data sources over cached data.`,
	}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := builder.BuildSystemPrompt(context.Background(), PromptInput{})

	// Should contain all lines of the custom instructions
	if !strings.Contains(result, "travel planning assistant") {
		t.Error("result should contain first line of instructions")
	}
	if !strings.Contains(result, "weather") {
		t.Error("result should contain weather instruction")
	}
	if !strings.Contains(result, "real-time data") {
		t.Error("result should contain real-time data instruction")
	}
}

func TestBuildPlanningPrompt_PersonaNotInUserPrompt(t *testing.T) {
	config := &PromptConfig{
		SystemInstructions: "You are a financial advisor assistant.",
	}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Test capabilities",
		Request:        "Test request",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persona should NOT be in the user prompt (moved to system message per Issue 5 P10)
	if strings.Contains(prompt, "You are a financial advisor assistant.") {
		t.Error("user prompt should not contain persona — it belongs in the system message")
	}

	// Verify persona is in the system prompt instead
	systemPrompt := builder.BuildSystemPrompt(context.Background(), input)
	if !strings.Contains(systemPrompt, "You are a financial advisor assistant.") {
		t.Error("system prompt should contain custom persona")
	}
	if !strings.Contains(systemPrompt, "As an AI orchestrator") {
		t.Error("system prompt should contain orchestrator role")
	}
}

func TestBuildPlanningPrompt_PersonaNotInUserPromptByDefault(t *testing.T) {
	config := &PromptConfig{}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Test capabilities",
		Request:        "Test request",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persona should NOT be in the user prompt (moved to system message per Issue 5 P10)
	if strings.Contains(prompt, "You are an AI orchestrator") {
		t.Error("user prompt should not contain persona — it belongs in the system message")
	}

	// Verify the system prompt has the default persona
	systemPrompt := builder.BuildSystemPrompt(context.Background(), input)
	if !strings.Contains(systemPrompt, "intelligent orchestrator") {
		t.Error("system prompt should contain default persona")
	}
}

func TestSystemInstructions_CombinedWithDomain(t *testing.T) {
	config := &PromptConfig{
		SystemInstructions: "You are a healthcare data analyst.",
		Domain:             "healthcare",
	}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Test capabilities",
		Request:        "Analyze patient data",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Domain instructions should be in the user prompt
	if !strings.Contains(prompt, "HEALTHCARE DOMAIN REQUIREMENTS") {
		t.Error("prompt should contain domain-specific requirements")
	}

	// Custom persona should be in the system prompt, not the user prompt
	systemPrompt := builder.BuildSystemPrompt(context.Background(), input)
	if !strings.Contains(systemPrompt, "healthcare data analyst") {
		t.Error("system prompt should contain custom persona")
	}
}

func TestSystemInstructions_CombinedWithCustomInstructions(t *testing.T) {
	config := &PromptConfig{
		SystemInstructions: "You are a travel assistant.",
		CustomInstructions: []string{
			"Always prefer direct flights",
			"Include visa requirements",
		},
	}
	builder, err := NewDefaultPromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Test capabilities",
		Request:        "Plan a trip to Tokyo",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Custom instructions should be in the user prompt
	if !strings.Contains(prompt, "direct flights") {
		t.Error("prompt should contain first custom instruction")
	}
	if !strings.Contains(prompt, "visa requirements") {
		t.Error("prompt should contain second custom instruction")
	}

	// Persona should be in the system prompt, not the user prompt
	systemPrompt := builder.BuildSystemPrompt(context.Background(), input)
	if !strings.Contains(systemPrompt, "travel assistant") {
		t.Error("system prompt should contain custom persona")
	}
}

// =============================================================================
// ORCH-004: Template Quoting Instructions Tests
// =============================================================================

func TestDefaultPromptBuilder_IncludesTemplateQuotingInstructions(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: geocoding-tool, weather-tool",
		Request:        "What's the weather in Tokyo?",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ORCH-004 regression guard: template quoting is now conveyed via concrete example
	// and instruction #4, rather than verbose reference instructions
	requiredElements := []string{
		`"{{step-1.response.data.lat}}"`,                            // Quoted template in concrete example
		`"{{step-1.response.data.lon}}"`,                            // Quoted template in concrete example
		"Template references are always quoted strings",             // Key rule in example section
		"template syntax for cross-step references (always quoted)", // Instruction #4
	}

	for _, element := range requiredElements {
		if !strings.Contains(prompt, element) {
			t.Errorf("prompt should contain template quoting instruction %q", element)
		}
	}
}

func TestDefaultPromptBuilder_CustomInstructionNumbering(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		CustomInstructions: []string{
			"First custom instruction",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After restructuring, custom instructions start at 7 (6 default instructions + 1)
	if !strings.Contains(prompt, "7. First custom instruction") {
		t.Error("first custom instruction should be numbered 7 (6 default instructions in <instructions> section)")
	}

	// The old numbering was 8 — verify it's NOT present
	if strings.Contains(prompt, "8. First custom instruction") {
		t.Error("custom instruction should NOT be numbered 8 (old numbering)")
	}
}

// =============================================================================
// Iterative Planning Instructions Tests
// =============================================================================

func TestDefaultPromptBuilder_IncludesIterativePlanningInstructions(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{
		IterativePlanConfig: &IterativePlanConfig{
			Enabled:       true,
			MaxPhases:     5,
			MaxTotalSteps: 200,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "Available tools: search-tool, detail-tool",
		Request:        "Find the top tourist attractions in Canada",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify budget-aware iterative planning instructions are present
	requiredElements := []string{
		"iterative_planning",
		`"terminal": false`,
		`"terminal": true`,
		"5 phases",
		"200 total steps",
	}

	for _, element := range requiredElements {
		if !strings.Contains(prompt, element) {
			t.Errorf("prompt should contain iterative planning element %q", element)
		}
	}
}

func TestDefaultPromptBuilder_NoIterativePlanningWithoutConfig(t *testing.T) {
	// Without IterativePlanConfig, iterative planning instructions should not appear
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(prompt, "iterative_planning") {
		t.Error("prompt should not contain iterative planning instructions when config is nil")
	}
}

func TestDefaultPromptBuilder_ConcreteExampleIncludesTerminalField(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The concrete example should show "terminal": true
	if !strings.Contains(prompt, `"terminal": true,`) {
		t.Error("concrete example should contain '\"terminal\": true,' field")
	}
}

func TestConcreteExample_IncludesTerminalField(t *testing.T) {
	example := buildConcreteExample()
	if !strings.Contains(example, `"terminal": true`) {
		t.Error("buildConcreteExample() should contain '\"terminal\": true'")
	}
}

func TestTemplatePromptBuilder_IterativePlanningInstructionsExposed(t *testing.T) {
	config := &PromptConfig{
		Template: `{{.IterativePlanningInstructions}}`,
		IterativePlanConfig: &IterativePlanConfig{
			Enabled:       true,
			MaxPhases:     5,
			MaxTotalSteps: 200,
		},
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Template should have rendered the budget-aware iterative planning instructions
	if !strings.Contains(prompt, "iterative_planning") {
		t.Error("template using {{.IterativePlanningInstructions}} should render the instructions")
	}
	if !strings.Contains(prompt, `"terminal": false`) {
		t.Error("rendered instructions should contain terminal: false guidance")
	}
	if !strings.Contains(prompt, "5 phases") {
		t.Error("rendered instructions should contain phase budget")
	}
}

// TestBuildIterativePlanningInstructions_ContainsDependsOnRule verifies Issue 10 Fix B-1:
// the PHASE SPLIT RULE section explicitly ties depends_on to template references.
func TestBuildIterativePlanningInstructions_ContainsDependsOnRule(t *testing.T) {
	config := &IterativePlanConfig{
		Enabled:       true,
		MaxPhases:     5,
		MaxTotalSteps: 200,
	}

	result := BuildIterativePlanningInstructions(config)

	// The rule now distinguishes same-phase (depends_on) from prior-phase (implicit_deps)
	// references, matching validateDependencyConsistency.
	expected := "same-phase references in depends_on"
	if !strings.Contains(result, expected) {
		t.Errorf("BuildIterativePlanningInstructions should tie depends_on to template references.\nExpected to find: %q\nGot:\n%s", expected, result)
	}
}

// TestBuildIterativePlanningInstructions_Disabled verifies no output when disabled.
func TestBuildIterativePlanningInstructions_Disabled(t *testing.T) {
	config := &IterativePlanConfig{
		Enabled:       false,
		MaxPhases:     5,
		MaxTotalSteps: 200,
	}

	result := BuildIterativePlanningInstructions(config)
	if result != "" {
		t.Errorf("Expected empty string when disabled, got: %s", result)
	}
}

// TestBuildIterativePlanningInstructions_Nil verifies no output when config is nil.
func TestBuildIterativePlanningInstructions_Nil(t *testing.T) {
	result := BuildIterativePlanningInstructions(nil)
	if result != "" {
		t.Errorf("Expected empty string for nil config, got: %s", result)
	}
}

func TestTemplatePromptBuilder_ConcreteExampleIncludesTerminal(t *testing.T) {
	config := &PromptConfig{
		Template: `{{.ConcreteExample}}`,
	}

	builder, err := NewTemplatePromptBuilder(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "test",
		Request:        "test",
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, `"terminal": true`) {
		t.Error("template using {{.ConcreteExample}} should include terminal field")
	}
}

// TestDefaultPromptBuilder_ContextPrecedenceEmittedWithUserProfile guards that
// whenever <user_profile> rides with the prompt, the planner sees an explicit
// precedence rule steering it toward the live turn. Without this block, a
// stale "Context" fact in the profile can override the current request.
func TestDefaultPromptBuilder_ContextPrecedenceEmittedWithUserProfile(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "weather-tool",
		Request:        "Plan a week-long trip",
		Metadata: map[string]interface{}{
			core.EnrichmentUserProfile: "<user_profile>\nContext:\n- User is planning a trip to Switzerland (explicit, recorded 12 days ago)\n\n</user_profile>",
		},
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, "<context_precedence>") {
		t.Error("prompt should emit <context_precedence> when <user_profile> is present")
	}
	if !strings.Contains(prompt, "trust the live turn") {
		t.Error("<context_precedence> should state the precedence rule in plain language")
	}

	// Must land just before <user_request> so it sits in the high-attention
	// tail (EFFECTIVE_PROMPTS_GUIDE §2.1).
	precIdx := strings.Index(prompt, "<context_precedence>")
	reqIdx := strings.Index(prompt, "<user_request>")
	if precIdx < 0 || reqIdx < 0 || precIdx >= reqIdx {
		t.Errorf("<context_precedence> must precede <user_request>; got prec=%d req=%d", precIdx, reqIdx)
	}
}

// TestDefaultPromptBuilder_ContextPrecedenceEmittedWithConversationHistory
// mirrors the profile test for conversation-history-only enrichment.
func TestDefaultPromptBuilder_ContextPrecedenceEmittedWithConversationHistory(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "weather-tool",
		Request:        "Plan a week-long trip",
		Metadata: map[string]interface{}{
			core.EnrichmentConversationHistory: "User asked about Rome weather last turn.",
		},
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(prompt, "<context_precedence>") {
		t.Error("prompt should emit <context_precedence> when <conversation_history> is present")
	}
}

// TestDefaultPromptBuilder_ContextPrecedenceSkippedWhenNoEnrichments avoids
// polluting prompts that can never trigger the conflict the rule addresses.
func TestDefaultPromptBuilder_ContextPrecedenceSkippedWhenNoEnrichments(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "weather-tool",
		Request:        "Weather in Tokyo",
		// No user_profile, no conversation_history.
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(prompt, "<context_precedence>") {
		t.Error("prompt should not emit <context_precedence> without conflict-eligible enrichments")
	}
}

// TestDefaultPromptBuilder_ContextPrecedenceEmittedOnceWhenBothEnrichmentsPresent
// guards that the precedence directive is emitted exactly once even when both
// <user_profile> and <conversation_history> ride along. Emitting the rule
// twice would waste tokens and confuse the planner about which instance is
// authoritative.
func TestDefaultPromptBuilder_ContextPrecedenceEmittedOnceWhenBothEnrichmentsPresent(t *testing.T) {
	builder, err := NewDefaultPromptBuilder(&PromptConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	input := PromptInput{
		CapabilityInfo: "weather-tool",
		Request:        "Plan a week-long trip",
		Metadata: map[string]interface{}{
			core.EnrichmentUserProfile:         "<user_profile>\nContext:\n- stale context (explicit, recorded 12 days ago)\n</user_profile>",
			core.EnrichmentConversationHistory: "User asked about Rome last turn.",
		},
	}

	prompt, err := builder.BuildPlanningPrompt(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	openCount := strings.Count(prompt, "<context_precedence>")
	closeCount := strings.Count(prompt, "</context_precedence>")
	if openCount != 1 {
		t.Errorf("expected exactly one <context_precedence> open tag, got %d", openCount)
	}
	if closeCount != 1 {
		t.Errorf("expected exactly one </context_precedence> close tag, got %d", closeCount)
	}
}

// TestWriteContextPrecedence_OnlyEmitsForConflictEligibleEnrichments unit-tests
// the shared helper directly so every caller (DefaultPromptBuilder, the inline
// fallback, buildContinuationPrompt, both buildSynthesisPrompts) inherits the
// same gating semantics.
func TestWriteContextPrecedence_OnlyEmitsForConflictEligibleEnrichments(t *testing.T) {
	cases := []struct {
		name        string
		enrichments map[string]interface{}
		want        bool
	}{
		{"nil map skips", nil, false},
		{"empty map skips", map[string]interface{}{}, false},
		{"unrelated enrichment skips", map[string]interface{}{
			core.EnrichmentActivityCoordination: "agent X is busy",
		}, false},
		{"agent_memory alone skips — RAG context is not subject-conflicting", map[string]interface{}{
			core.EnrichmentRAGContext: "<agent_memory>...</agent_memory>",
		}, false},
		{"user_profile alone emits", map[string]interface{}{
			core.EnrichmentUserProfile: "<user_profile>...</user_profile>",
		}, true},
		{"conversation_history alone emits", map[string]interface{}{
			core.EnrichmentConversationHistory: "User asked about Rome last turn.",
		}, true},
		{"both emit single block", map[string]interface{}{
			core.EnrichmentUserProfile:         "<user_profile>...</user_profile>",
			core.EnrichmentConversationHistory: "User asked about Rome last turn.",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			writeContextPrecedence(context.Background(), &sb, tc.enrichments, PromptKindPlanning)
			out := sb.String()
			if tc.want {
				if !strings.Contains(out, "<context_precedence>") || !strings.Contains(out, "</context_precedence>") {
					t.Errorf("expected <context_precedence> block; got %q", out)
				}
				if strings.Count(out, "<context_precedence>") != 1 {
					t.Errorf("expected exactly one block; got %d opens", strings.Count(out, "<context_precedence>"))
				}
				if !strings.Contains(out, "trust the live turn") {
					t.Errorf("block must carry the live-turn rule; got %q", out)
				}
			} else if out != "" {
				t.Errorf("expected empty output for %s; got %q", tc.name, out)
			}
		})
	}
}

// TestBuildContinuationPrompt_EmitsContextPrecedence covers Finding 1: the
// phase-2+ planner builds its own prompt inline with <user_profile> and
// <conversation_history>. Without this test, a regression that drops the
// helper call from buildContinuationPrompt would silently re-open the
// stale-context path on every continuation phase.
func TestBuildContinuationPrompt_EmitsContextPrecedence(t *testing.T) {
	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(map[string]interface{}) {},
	}
	orch := &AIOrchestrator{
		config: &OrchestratorConfig{
			RoutingMode: ModeAutonomous,
			IterativePlanning: IterativePlanConfig{
				Enabled:       true,
				MaxPhases:     5,
				MaxTotalSteps: 200,
			},
		},
		capabilityProvider: mockProvider,
	}

	ctx := core.WithPipelineEnrichments(context.Background(), map[string]interface{}{
		core.EnrichmentUserProfile: "<user_profile>\nContext:\n- stale destination (explicit, recorded 12 days ago)\n</user_profile>",
	})

	completed := map[string]*StepResult{
		"step-1": {AgentName: "test-agent", Response: "{\"data\": \"x\"}"},
	}
	result, err := orch.buildContinuationPrompt(ctx, "next phase request", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt failed: %v", err)
	}
	if !strings.Contains(result.Prompt, "<context_precedence>") {
		t.Error("continuation prompt must emit <context_precedence> when <user_profile> is present")
	}
	if !strings.Contains(result.Prompt, "trust the live turn") {
		t.Error("continuation prompt must carry the precedence directive")
	}
	// Must land after <user_profile> so the planner sees the rule immediately
	// after the conflict-prone enrichment.
	profileIdx := strings.Index(result.Prompt, "<user_profile>")
	precIdx := strings.Index(result.Prompt, "<context_precedence>")
	if profileIdx < 0 || precIdx < 0 || precIdx <= profileIdx {
		t.Errorf("<context_precedence> must follow <user_profile>; got profile=%d prec=%d", profileIdx, precIdx)
	}
}

// TestBuildContinuationPrompt_OmitsContextPrecedenceWithoutEnrichments
// guards the no-enrichments path so continuation prompts that can never
// conflict don't pay the ~100-token rule cost.
func TestBuildContinuationPrompt_OmitsContextPrecedenceWithoutEnrichments(t *testing.T) {
	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(map[string]interface{}) {},
	}
	orch := &AIOrchestrator{
		config: &OrchestratorConfig{
			RoutingMode: ModeAutonomous,
			IterativePlanning: IterativePlanConfig{
				Enabled:       true,
				MaxPhases:     5,
				MaxTotalSteps: 200,
			},
		},
		capabilityProvider: mockProvider,
	}

	completed := map[string]*StepResult{
		"step-1": {AgentName: "test-agent", Response: "{\"data\": \"x\"}"},
	}
	result, err := orch.buildContinuationPrompt(context.Background(), "next phase request", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt failed: %v", err)
	}
	if strings.Contains(result.Prompt, "<context_precedence>") {
		t.Error("continuation prompt should not emit <context_precedence> without conflict-eligible enrichments")
	}
}

// erroringPromptBuilder always returns an error from BuildPlanningPrompt so
// tests can exercise the inline fallback path in (*AIOrchestrator).buildPlanningPrompt.
type erroringPromptBuilder struct{}

func (erroringPromptBuilder) BuildPlanningPrompt(_ context.Context, _ PromptInput) (string, error) {
	return "", fmt.Errorf("intentional builder failure for fallback test")
}

// TestBuildPlanningPrompt_FallbackEmitsContextPrecedence covers Finding 2:
// when the configured PromptBuilder errors, the orchestrator falls through
// to the inline hardcoded prompt at orchestrator.go:4733-4779. That path
// previously rendered <user_profile> and <conversation_history> with no
// precedence directive — this test pins the fix so a regression cannot
// silently re-open it.
func TestBuildPlanningPrompt_FallbackEmitsContextPrecedence(t *testing.T) {
	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(map[string]interface{}) {},
	}
	orch := &AIOrchestrator{
		config: &OrchestratorConfig{
			RoutingMode: ModeAutonomous,
		},
		capabilityProvider: mockProvider,
		promptBuilder:      erroringPromptBuilder{},
	}

	ctx := core.WithPipelineEnrichments(context.Background(), map[string]interface{}{
		core.EnrichmentUserProfile:         "<user_profile>\nContext:\n- stale destination (explicit, recorded 12 days ago)\n</user_profile>",
		core.EnrichmentConversationHistory: "User asked about Rome last turn.",
	})

	result, err := orch.buildPlanningPrompt(ctx, "fresh request")
	if err != nil {
		t.Fatalf("buildPlanningPrompt failed: %v", err)
	}
	if !strings.Contains(result.Prompt, "<context_precedence>") {
		t.Error("inline fallback must emit <context_precedence> when conflict-eligible enrichments are present")
	}
	if !strings.Contains(result.Prompt, "trust the live turn") {
		t.Error("inline fallback must carry the precedence directive")
	}
	// Must land before <user_request> (mirrors DefaultPromptBuilder placement).
	precIdx := strings.Index(result.Prompt, "<context_precedence>")
	reqIdx := strings.Index(result.Prompt, "<user_request>")
	if precIdx < 0 || reqIdx < 0 || precIdx >= reqIdx {
		t.Errorf("<context_precedence> must precede <user_request>; got prec=%d req=%d", precIdx, reqIdx)
	}
	// Single emission only.
	if got := strings.Count(result.Prompt, "<context_precedence>"); got != 1 {
		t.Errorf("expected exactly one <context_precedence> block; got %d", got)
	}
}

// TestSynthesizer_BuildSynthesisPrompt_EmitsContextPrecedence covers the
// AISynthesizer.buildSynthesisPrompt path (synthesizer.go:280). The
// synthesizer composes the user-facing answer from agent results; if it
// anchors on a stale <user_profile> "Context" entry it can phrase the
// answer around the wrong subject even when the agents returned correct
// data.
func TestSynthesizer_BuildSynthesisPrompt_EmitsContextPrecedence(t *testing.T) {
	synth := &AISynthesizer{}
	ctx := core.WithPipelineEnrichments(context.Background(), map[string]interface{}{
		core.EnrichmentUserProfile: "<user_profile>\nContext:\n- stale destination (explicit, recorded 12 days ago)\n</user_profile>",
	})
	result := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", AgentName: "weather-tool", Response: "ok", Success: true},
		},
	}

	prompt := synth.buildSynthesisPrompt(ctx, "synthesize this", result)

	if !strings.Contains(prompt, "<context_precedence>") {
		t.Error("synthesizer prompt must emit <context_precedence> when <user_profile> is present")
	}
	// Must land after <user_profile> and before <agent_responses>.
	profileIdx := strings.Index(prompt, "<user_profile>")
	precIdx := strings.Index(prompt, "<context_precedence>")
	respIdx := strings.Index(prompt, "<agent_responses>")
	if profileIdx < 0 || precIdx < 0 || respIdx < 0 {
		t.Fatalf("expected all three anchors; got profile=%d prec=%d resp=%d", profileIdx, precIdx, respIdx)
	}
	if precIdx <= profileIdx || precIdx >= respIdx {
		t.Errorf("<context_precedence> must sit between <user_profile> and <agent_responses>; got profile=%d prec=%d resp=%d", profileIdx, precIdx, respIdx)
	}
}
