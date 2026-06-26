package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

// =============================================================================
// Tests for PhaseContextKey constants (interfaces.go)
// =============================================================================

func TestPhaseContextKey_Values(t *testing.T) {
	// Verify constant values match the contract between producer (buildContinuationPrompt)
	// and consumer (buildContinuationSelectionPrompt)
	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{"PhaseNumber", PhaseContextKeyPhaseNumber, "phase_number"},
		{"ContinuationNote", PhaseContextKeyContinuationNote, "continuation_note"},
		{"PriorToolsUsed", PhaseContextKeyPriorToolsUsed, "prior_tools_used"},
		{"CompletedSummary", PhaseContextKeyCompletedSummary, "completed_summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.key != tt.expected {
				t.Errorf("PhaseContextKey%s = %q, want %q", tt.name, tt.key, tt.expected)
			}
		})
	}
}

func TestPhaseContextKey_MapRoundTrip(t *testing.T) {
	// Verify keys work correctly when used in a map (the actual usage pattern)
	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:      2,
		PhaseContextKeyContinuationNote: "continue to get weather",
		PhaseContextKeyPriorToolsUsed:   []string{"web-search-tool"},
		PhaseContextKeyCompletedSummary: "step-1(web-search-tool): found destinations",
	}

	if v, ok := phaseContext[PhaseContextKeyPhaseNumber].(int); !ok || v != 2 {
		t.Errorf("PhaseNumber round-trip failed: got %v", phaseContext[PhaseContextKeyPhaseNumber])
	}
	if v, ok := phaseContext[PhaseContextKeyContinuationNote].(string); !ok || v != "continue to get weather" {
		t.Errorf("ContinuationNote round-trip failed")
	}
	if v, ok := phaseContext[PhaseContextKeyPriorToolsUsed].([]string); !ok || len(v) != 1 || v[0] != "web-search-tool" {
		t.Errorf("PriorToolsUsed round-trip failed")
	}
	if v, ok := phaseContext[PhaseContextKeyCompletedSummary].(string); !ok || v != "step-1(web-search-tool): found destinations" {
		t.Errorf("CompletedSummary round-trip failed")
	}
}

// =============================================================================
// Tests for extractUniqueAgentNames (orchestrator.go)
// =============================================================================

func TestExtractUniqueAgentNames_SingleAgent(t *testing.T) {
	results := map[string]*StepResult{
		"step-1": {AgentName: "weather-tool"},
	}
	names := extractUniqueAgentNames(results)
	if len(names) != 1 || names[0] != "weather-tool" {
		t.Errorf("got %v, want [weather-tool]", names)
	}
}

func TestExtractUniqueAgentNames_MultipleAgentsDeduplicated(t *testing.T) {
	results := map[string]*StepResult{
		"step-1": {AgentName: "geocoding-tool"},
		"step-2": {AgentName: "weather-tool"},
		"step-3": {AgentName: "geocoding-tool"}, // duplicate
		"step-4": {AgentName: "news-tool"},
	}
	names := extractUniqueAgentNames(results)
	if len(names) != 3 {
		t.Fatalf("got %d names, want 3: %v", len(names), names)
	}
	expected := []string{"geocoding-tool", "news-tool", "weather-tool"}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestExtractUniqueAgentNames_Sorted(t *testing.T) {
	// Verify output is sorted for deterministic prompt generation
	results := map[string]*StepResult{
		"step-1": {AgentName: "z-agent"},
		"step-2": {AgentName: "a-agent"},
		"step-3": {AgentName: "m-agent"},
	}
	names := extractUniqueAgentNames(results)
	if len(names) != 3 {
		t.Fatalf("got %d names, want 3", len(names))
	}
	if names[0] != "a-agent" || names[1] != "m-agent" || names[2] != "z-agent" {
		t.Errorf("names not sorted: %v", names)
	}
}

func TestExtractUniqueAgentNames_EmptyResults(t *testing.T) {
	results := map[string]*StepResult{}
	names := extractUniqueAgentNames(results)
	if len(names) != 0 {
		t.Errorf("got %v, want empty slice", names)
	}
}

func TestExtractUniqueAgentNames_AllSameAgent(t *testing.T) {
	results := map[string]*StepResult{
		"step-1": {AgentName: "same-agent"},
		"step-2": {AgentName: "same-agent"},
		"step-3": {AgentName: "same-agent"},
	}
	names := extractUniqueAgentNames(results)
	if len(names) != 1 || names[0] != "same-agent" {
		t.Errorf("got %v, want [same-agent]", names)
	}
}

// =============================================================================
// Tests for buildCompactResultSummary (orchestrator.go)
// =============================================================================

func TestBuildCompactResultSummary_SingleStep(t *testing.T) {
	results := map[string]*StepResult{
		"step-1": {
			AgentName: "web-search-tool",
			Response:  `{"results": ["Zurich", "Geneva"]}`,
		},
	}
	summary := buildCompactResultSummary(results, 500)
	if !strings.Contains(summary, "step-1(web-search-tool)") {
		t.Errorf("summary missing step ID and agent name: %s", summary)
	}
	if !strings.Contains(summary, `{"results": ["Zurich", "Geneva"]}`) {
		t.Errorf("summary missing response content: %s", summary)
	}
}

func TestBuildCompactResultSummary_SortedByStepID(t *testing.T) {
	results := map[string]*StepResult{
		"step-3": {AgentName: "news-tool", Response: "news data"},
		"step-1": {AgentName: "search-tool", Response: "search data"},
		"step-2": {AgentName: "weather-tool", Response: "weather data"},
	}
	summary := buildCompactResultSummary(results, 500)
	idx1 := strings.Index(summary, "step-1")
	idx2 := strings.Index(summary, "step-2")
	idx3 := strings.Index(summary, "step-3")
	if idx1 >= idx2 || idx2 >= idx3 {
		t.Errorf("steps not sorted: step-1@%d, step-2@%d, step-3@%d\nsummary: %s", idx1, idx2, idx3, summary)
	}
}

func TestBuildCompactResultSummary_TruncatesLongResponses(t *testing.T) {
	// Response longer than 80 chars should be truncated per truncateString(r.Response, 80)
	longResponse := strings.Repeat("x", 200)
	results := map[string]*StepResult{
		"step-1": {AgentName: "test-agent", Response: longResponse},
	}
	summary := buildCompactResultSummary(results, 500)
	// The response in the summary should be truncated to 80 chars + "..."
	if strings.Contains(summary, longResponse) {
		t.Error("expected response to be truncated, but full response found")
	}
	if !strings.Contains(summary, "...") {
		t.Error("expected truncation marker '...' in summary")
	}
}

func TestBuildCompactResultSummary_TruncatesAtMaxLen(t *testing.T) {
	// Create enough results to exceed maxLen
	results := map[string]*StepResult{}
	for i := 0; i < 20; i++ {
		results[fmt.Sprintf("step-%d", i)] = &StepResult{
			AgentName: fmt.Sprintf("agent-%d", i),
			Response:  "some response data here",
		}
	}
	maxLen := 100
	summary := buildCompactResultSummary(results, maxLen)
	if len(summary) > maxLen+50 { // Allow buffer for truncation marker
		t.Errorf("summary length %d exceeds maxLen %d by too much", len(summary), maxLen)
	}
	if !strings.Contains(summary, "...[truncated]") {
		t.Error("expected truncation marker when exceeding maxLen")
	}
}

func TestBuildCompactResultSummary_EmptyResults(t *testing.T) {
	results := map[string]*StepResult{}
	summary := buildCompactResultSummary(results, 500)
	if summary != "" {
		t.Errorf("expected empty string for empty results, got %q", summary)
	}
}

func TestBuildCompactResultSummary_NilStepResult(t *testing.T) {
	// The nil guard (code review fix) should skip nil entries
	results := map[string]*StepResult{
		"step-1": {AgentName: "agent-a", Response: "data"},
		"step-2": nil,
		"step-3": {AgentName: "agent-c", Response: "data"},
	}
	summary := buildCompactResultSummary(results, 500)
	if strings.Contains(summary, "step-2") {
		t.Error("expected nil step result to be skipped")
	}
	if !strings.Contains(summary, "step-1") || !strings.Contains(summary, "step-3") {
		t.Error("expected non-nil step results to be included")
	}
}

func TestBuildCompactResultSummary_ExactMaxLen(t *testing.T) {
	// Single entry that fits exactly within maxLen
	results := map[string]*StepResult{
		"step-1": {AgentName: "a", Response: "r"},
	}
	line := "step-1(a): r\n"
	summary := buildCompactResultSummary(results, len(line))
	if summary != line {
		t.Errorf("got %q, want %q", summary, line)
	}
}

// =============================================================================
// Tests for buildContinuationSelectionPrompt (tiered_capability_provider.go)
// =============================================================================

func TestBuildContinuationSelectionPrompt_XMLStructure(t *testing.T) {
	catalog := setupTestCatalog(5) // small catalog
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:      2,
		PhaseContextKeyContinuationNote: "Get weather for discovered destinations",
		PhaseContextKeyPriorToolsUsed:   []string{"web-search-tool"},
		PhaseContextKeyCompletedSummary: "step-1(web-search-tool): found Zurich, Geneva",
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test request", phaseContext)

	// Verify XML section tags are present
	requiredSections := []string{
		"<identity>", "</identity>",
		"<phase_context>", "</phase_context>",
		"<available_tools>", "</available_tools>",
		"<user_request>", "</user_request>",
		"<selection_guide>", "</selection_guide>",
		"<output_format>", "</output_format>",
	}
	for _, section := range requiredSections {
		if !strings.Contains(prompt, section) {
			t.Errorf("missing XML section: %s", section)
		}
	}
}

// TestBuildContinuationSelectionPrompt_ContainsPhaseContext verifies the
// Layer 2 (ORCH-018) phase_context format:
//   - prior_tool_ids rendered in agent/capability format as a bullet list
//   - continuation_note and completed_summary are dropped from the selector
//     prompt (they were the structural cause of the selector hallucinating
//     empty in clarification-pending continuations)
func TestBuildContinuationSelectionPrompt_ContainsPhaseContext(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 3,
		// continuation_note and completed_summary are still populated by the
		// orchestrator (for backward compat with any other consumers), but the
		// selector prompt must ignore them per Layer 2.
		PhaseContextKeyContinuationNote: "Need to fetch news articles",
		PhaseContextKeyPriorToolsUsed:   []string{"geocoding-tool", "weather-tool"},
		PhaseContextKeyCompletedSummary: "step-1(geocoding-tool): coords\nstep-2(weather-tool): sunny",
		// Layer 2 consumes the new key in agent/capability format.
		PhaseContextKeyPriorToolIDs: []string{"geocoding-tool/geocode", "weather-tool/forecast"},
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "get news", phaseContext)

	// Verify phase context uses the new agent/capability format header and bullet list
	if !strings.Contains(prompt, "Tools used in previous phases (in agent/capability format):") {
		t.Error("missing prior tools header (agent/capability format) in phase_context")
	}
	if !strings.Contains(prompt, "- geocoding-tool/geocode") {
		t.Error("missing prior tool ID 'geocoding-tool/geocode' in phase_context bullet list")
	}
	if !strings.Contains(prompt, "- weather-tool/forecast") {
		t.Error("missing prior tool ID 'weather-tool/forecast' in phase_context bullet list")
	}

	// Layer 2: continuation_note and discoveries_so_far must NOT appear in the
	// selector prompt. They were the mixed-purpose narrative that pushed the
	// selector toward reasoning about WHETHER to proceed (planner concern)
	// instead of filtering tools (selector concern).
	if strings.Contains(prompt, "Reason for continuation") {
		t.Error("selector prompt should not contain 'Reason for continuation' after Layer 2")
	}
	if strings.Contains(prompt, "Discoveries so far") {
		t.Error("selector prompt should not contain 'Discoveries so far' after Layer 2")
	}

	// Layer 2: <selection_guide> must include step D (prior-tools fallback default)
	if !strings.Contains(prompt, "return the items from <phase_context> verbatim") {
		t.Error("selection_guide missing step D (prior-tools fallback directive)")
	}

	// Layer 2: <output_format> must include both examples (new tools + fallback)
	if !strings.Contains(prompt, "Example (new tools needed):") {
		t.Error("output_format missing 'new tools needed' example")
	}
	if !strings.Contains(prompt, "Example (prior-tools fallback):") {
		t.Error("output_format missing 'prior-tools fallback' example")
	}
}

func TestBuildContinuationSelectionPrompt_ContainsUserRequest(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	summaries := catalog.GetCapabilitySummaries()
	userRequest := "Tell me the weather in Tokyo and convert USD to JPY"
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, userRequest, phaseContext)

	if !strings.Contains(prompt, userRequest) {
		t.Error("prompt does not contain user request")
	}
}

func TestBuildContinuationSelectionPrompt_ContainsToolSummaries(t *testing.T) {
	catalog := setupMultiAgentCatalog(3, 2) // 3 agents, 2 caps each
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test", phaseContext)

	// Verify tools are listed with agent/capability format
	for _, s := range summaries {
		expected := fmt.Sprintf("- %s/%s:", s.AgentName, s.CapabilityName)
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing tool summary: %s", expected)
		}
	}
}

func TestBuildContinuationSelectionPrompt_SelectionGuideContent(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test", phaseContext)

	// Verify selection guide has A/B/C structure
	if !strings.Contains(prompt, "A. What parts of the original request") {
		t.Error("missing selection guide section A")
	}
	if !strings.Contains(prompt, "B. Did previous phase discoveries") {
		t.Error("missing selection guide section B")
	}
	if !strings.Contains(prompt, "C. Select ALL tools for remaining work") {
		t.Error("missing selection guide section C")
	}
}

func TestBuildContinuationSelectionPrompt_OutputFormatExample(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test", phaseContext)

	// Verify output format has JSON example and format anchor
	if !strings.Contains(prompt, `"agent_name/capability_name"`) {
		t.Error("missing output format specification")
	}
	if !strings.Contains(prompt, "JSON array:") {
		t.Error("missing JSON array prompt anchor at end")
	}
}

// TestBuildContinuationSelectionPrompt_EmptyPriorToolIDs verifies that when
// PhaseContextKeyPriorToolIDs is empty or missing, the Layer 2 (ORCH-018)
// prior-tools header is omitted from the selector prompt.
func TestBuildContinuationSelectionPrompt_EmptyPriorToolIDs(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:  2,
		PhaseContextKeyPriorToolIDs: []string{},
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test", phaseContext)

	// With empty prior tool IDs, the new agent/capability-format header
	// should be absent (no list to show).
	if strings.Contains(prompt, "Tools used in previous phases (in agent/capability format):") {
		t.Error("should not include prior tool IDs header when list is empty")
	}
}

// TestBuildContinuationSelectionPrompt_Layer2_DroppedNarrative verifies the
// Layer 2 (ORCH-018) invariant: continuation_note and completed_summary are
// NEVER rendered in the selector prompt, even when they are populated in
// phaseContext. This was the structural cause of the selector hallucinating
// empty responses in clarification-pending continuations — Layer 2
// permanently removes them from the selector prompt (they remain available
// to the planner via other paths).
func TestBuildContinuationSelectionPrompt_Layer2_DroppedNarrative(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Deliberately populate the dropped keys with content that would have
	// appeared in the pre-Layer-2 prompt — it must NOT surface now.
	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:      2,
		PhaseContextKeyContinuationNote: "I need to ask the user about their preferences",
		PhaseContextKeyCompletedSummary: "step-1(tool): already done",
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test", phaseContext)

	// Layer 2 invariant: no "Reason for continuation:" section
	if strings.Contains(prompt, "Reason for continuation") {
		t.Error("Layer 2: selector prompt must not contain 'Reason for continuation'")
	}
	// Layer 2 invariant: no "Discoveries so far:" section
	if strings.Contains(prompt, "Discoveries so far") {
		t.Error("Layer 2: selector prompt must not contain 'Discoveries so far'")
	}
	// Layer 2 invariant: no continuation_note content leaks in
	if strings.Contains(prompt, "I need to ask the user about their preferences") {
		t.Error("Layer 2: continuation_note content must not leak into the selector prompt")
	}
	// Layer 2 invariant: no completed_summary content leaks in
	if strings.Contains(prompt, "step-1(tool): already done") {
		t.Error("Layer 2: completed_summary content must not leak into the selector prompt")
	}
}

func TestBuildContinuationSelectionPrompt_IdentityDirective(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "test", phaseContext)

	// Verify identity section has the key behavioral directive
	if !strings.Contains(prompt, "tool selector for a continuation phase") {
		t.Error("missing continuation phase identity")
	}
	if !strings.Contains(prompt, "Output raw JSON only") {
		t.Error("missing JSON-only output directive")
	}
}

// =============================================================================
// Tests for selectRelevantTools with phaseContext (tiered_capability_provider.go)
// =============================================================================

func TestSelectRelevantTools_Phase2UsesContextAwarePrompt(t *testing.T) {
	// Setup catalog with enough tools to trigger tiered selection
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0", "test-agent/capability_1"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:      2,
		PhaseContextKeyContinuationNote: "Need weather data",
		PhaseContextKeyPriorToolsUsed:   []string{"web-search-tool"},
		PhaseContextKeyCompletedSummary: "step-1: found destinations",
	}

	// Call GetCapabilities with phaseContext (passes through to selectRelevantTools)
	_, err := provider.GetCapabilities(context.Background(), "test request", phaseContext)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	// Verify the LLM was called with the continuation selection prompt (XML tags)
	calls := aiClient.GetCalls()
	if len(calls) == 0 {
		t.Fatal("expected LLM call, got none")
	}

	prompt := calls[0]
	// Should use buildContinuationSelectionPrompt (has XML tags), not buildSelectionPrompt (has ## STEP)
	if !strings.Contains(prompt, "<identity>") {
		t.Error("Phase 2+ should use buildContinuationSelectionPrompt with <identity> tag")
	}
	if !strings.Contains(prompt, "<phase_context>") {
		t.Error("Phase 2+ should include <phase_context> section")
	}
	if strings.Contains(prompt, "## STEP 1: TASK IDENTIFICATION") {
		t.Error("Phase 2+ should NOT use buildSelectionPrompt's markdown format")
	}
}

func TestSelectRelevantTools_Phase1UsesStandardPrompt(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Call with nil metadata (Phase 1)
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	calls := aiClient.GetCalls()
	if len(calls) == 0 {
		t.Fatal("expected LLM call, got none")
	}

	prompt := calls[0]
	// Should use buildSelectionPrompt (markdown), not buildContinuationSelectionPrompt
	if strings.Contains(prompt, "<phase_context>") {
		t.Error("Phase 1 (nil metadata) should NOT use buildContinuationSelectionPrompt")
	}
	if !strings.Contains(prompt, "<selection_guide>") {
		t.Error("Phase 1 should use buildSelectionPrompt with <selection_guide> tag")
	}
}

func TestSelectRelevantTools_PhaseContextNilPhaseNumber(t *testing.T) {
	// Metadata exists but PhaseContextKeyPhaseNumber is nil — should use standard prompt
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	metadata := map[string]interface{}{
		"some_other_key": "value",
	}

	_, err := provider.GetCapabilities(context.Background(), "test", metadata)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	prompt := aiClient.GetCalls()[0]
	if strings.Contains(prompt, "<phase_context>") {
		t.Error("metadata without PhaseContextKeyPhaseNumber should use standard prompt")
	}
}

// TestSelectRelevantTools_Phase2ContextAwareWithPriorTools verifies that the
// Layer 2 (ORCH-018) selector prompt contains prior tools in agent/capability
// format and does NOT contain the dropped continuation_note narrative.
func TestSelectRelevantTools_Phase2ContextAwareWithPriorTools(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0", "test-agent/capability_5"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 3,
		// continuation_note is still populated by the orchestrator but the
		// selector prompt must not surface it per Layer 2.
		PhaseContextKeyContinuationNote: "Fetch detailed info",
		PhaseContextKeyPriorToolsUsed:   []string{"agent-a", "agent-b", "agent-c"},
		PhaseContextKeyCompletedSummary: "step-1(agent-a): data\nstep-2(agent-b): data",
		// Layer 2 consumes the new key in agent/capability format.
		PhaseContextKeyPriorToolIDs: []string{"agent-a/query", "agent-b/query", "agent-c/query"},
	}

	_, err := provider.GetCapabilities(context.Background(), "test request", phaseContext)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	prompt := aiClient.GetCalls()[0]

	// Layer 2: prior tools listed in agent/capability format as bullet list
	if !strings.Contains(prompt, "- agent-a/query") {
		t.Error("prompt should contain prior tool 'agent-a/query' in bullet-list form")
	}
	if !strings.Contains(prompt, "- agent-b/query") {
		t.Error("prompt should contain prior tool 'agent-b/query' in bullet-list form")
	}
	if !strings.Contains(prompt, "- agent-c/query") {
		t.Error("prompt should contain prior tool 'agent-c/query' in bullet-list form")
	}

	// Layer 2: continuation_note must NOT surface in the selector prompt.
	// It was the structural cause of the selector hallucinating empty
	// responses in clarification-pending continuations.
	if strings.Contains(prompt, "Fetch detailed info") {
		t.Error("selector prompt should not contain continuation_note content after Layer 2")
	}
}

func TestSelectRelevantTools_Phase2FallbackOnError(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetError(fmt.Errorf("LLM unavailable"))

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:    2,
		PhaseContextKeyPriorToolsUsed: []string{"web-search-tool"},
	}

	// Should gracefully degrade to full catalog
	result, err := provider.GetCapabilities(context.Background(), "test", phaseContext)
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	// Full catalog should be returned
	if !strings.Contains(result.FormattedInfo, "capability_0") {
		t.Error("expected full catalog in fallback")
	}
}

func TestSelectRelevantTools_Phase2HallucinationFiltering(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	// Include a hallucinated tool
	aiClient.SetResponse(`["test-agent/capability_0", "hallucinated-agent/fake_cap", "test-agent/capability_1"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	result, err := provider.GetCapabilities(context.Background(), "test", phaseContext)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	// Hallucinated tool should be filtered out
	if strings.Contains(result.FormattedInfo, "fake_cap") {
		t.Error("hallucinated tool should be filtered in Phase 2+ path")
	}
	if !strings.Contains(result.FormattedInfo, "capability_0") {
		t.Error("valid tool capability_0 should be present")
	}
}

// =============================================================================
// Tests for GetCapabilities context_aware logging field
// =============================================================================

func TestGetCapabilities_ContextAwareLogging_Phase2(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Capture log calls
	var infoFields map[string]interface{}
	provider.SetLogger(&mockTieredLogger{
		infoFunc: func(msg string, fields map[string]interface{}) {
			if strings.Contains(msg, "Tier 1 tool selection complete") {
				infoFields = fields
			}
		},
	})

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	_, err := provider.GetCapabilities(context.Background(), "test", phaseContext)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	if infoFields == nil {
		t.Fatal("expected info log for tier 1 selection, got none")
	}
	contextAware, ok := infoFields["context_aware"].(bool)
	if !ok {
		t.Fatal("context_aware field missing from log")
	}
	if !contextAware {
		t.Error("context_aware should be true for Phase 2+ metadata")
	}
}

func TestGetCapabilities_ContextAwareLogging_Phase1(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	var infoFields map[string]interface{}
	provider.SetLogger(&mockTieredLogger{
		infoFunc: func(msg string, fields map[string]interface{}) {
			if strings.Contains(msg, "Tier 1 tool selection complete") {
				infoFields = fields
			}
		},
	})

	// Phase 1 — nil metadata
	_, err := provider.GetCapabilities(context.Background(), "test", nil)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	if infoFields == nil {
		t.Fatal("expected info log for tier 1 selection, got none")
	}
	contextAware, ok := infoFields["context_aware"].(bool)
	if !ok {
		t.Fatal("context_aware field missing from log")
	}
	if contextAware {
		t.Error("context_aware should be false for Phase 1 (nil metadata)")
	}
}

func TestGetCapabilities_ContextAwareLogging_FallbackOnError(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetError(fmt.Errorf("LLM unavailable"))

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	var warnFields map[string]interface{}
	provider.SetLogger(&mockTieredLogger{
		warnFunc: func(msg string, fields map[string]interface{}) {
			if strings.Contains(msg, "Tool selection failed") {
				warnFields = fields
			}
		},
	})

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber: 2,
	}

	_, _ = provider.GetCapabilities(context.Background(), "test", phaseContext)

	if warnFields == nil {
		t.Fatal("expected warn log for fallback, got none")
	}
	contextAware, ok := warnFields["context_aware"].(bool)
	if !ok {
		t.Fatal("context_aware field missing from warn log")
	}
	if !contextAware {
		t.Error("context_aware should be true for Phase 2+ fallback")
	}
}

// =============================================================================
// Tests for buildContinuationPrompt phaseContext propagation (orchestrator.go)
// =============================================================================

func TestBuildContinuationPrompt_PhaseContextPropagation(t *testing.T) {
	// This test verifies that buildContinuationPrompt correctly constructs
	// the phaseContext map and passes it to buildplanningContext/GetCapabilities.
	// We use a mock capability provider to capture the metadata parameter.

	var capturedMetadata map[string]interface{}

	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(metadata map[string]interface{}) {
			capturedMetadata = metadata
		},
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

	completedResults := map[string]*StepResult{
		"step-1": {AgentName: "web-search-tool", Response: `{"results": ["Zurich"]}`, Success: true},
	}
	executedStepIDs := []string{"step-1"}

	_, _ = orch.buildContinuationPrompt(
		context.Background(),
		"get weather in Zurich",
		completedResults,
		executedStepIDs,
		"Need to geocode and get weather",
		2,
	)

	if capturedMetadata == nil {
		t.Fatal("expected phaseContext to be passed to GetCapabilities, got nil")
	}

	// Verify PhaseContextKeyPhaseNumber
	phaseNum, ok := capturedMetadata[PhaseContextKeyPhaseNumber].(int)
	if !ok || phaseNum != 2 {
		t.Errorf("PhaseNumber = %v, want 2", capturedMetadata[PhaseContextKeyPhaseNumber])
	}

	// Verify PhaseContextKeyContinuationNote
	note, ok := capturedMetadata[PhaseContextKeyContinuationNote].(string)
	if !ok || note != "Need to geocode and get weather" {
		t.Errorf("ContinuationNote = %q, want %q", note, "Need to geocode and get weather")
	}

	// Verify PhaseContextKeyPriorToolsUsed
	priorTools, ok := capturedMetadata[PhaseContextKeyPriorToolsUsed].([]string)
	if !ok || len(priorTools) != 1 || priorTools[0] != "web-search-tool" {
		t.Errorf("PriorToolsUsed = %v, want [web-search-tool]", priorTools)
	}

	// Verify PhaseContextKeyCompletedSummary
	summary, ok := capturedMetadata[PhaseContextKeyCompletedSummary].(string)
	if !ok || summary == "" {
		t.Error("CompletedSummary should not be empty")
	}
	if !strings.Contains(summary, "step-1(web-search-tool)") {
		t.Errorf("CompletedSummary missing step-1 entry: %q", summary)
	}
}

func TestBuildContinuationPrompt_PhaseContextMultipleAgents(t *testing.T) {
	var capturedMetadata map[string]interface{}

	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(metadata map[string]interface{}) {
			capturedMetadata = metadata
		},
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

	completedResults := map[string]*StepResult{
		"step-1": {AgentName: "search-tool", Response: "results", Success: true},
		"step-2": {AgentName: "geocoding-tool", Response: "coords", Success: true},
		"step-3": {AgentName: "search-tool", Response: "more results", Success: true}, // duplicate agent
		"step-4": {AgentName: "weather-tool", Response: "sunny", Success: true},
	}
	executedStepIDs := []string{"step-1", "step-2", "step-3", "step-4"}

	_, _ = orch.buildContinuationPrompt(
		context.Background(),
		"test request",
		completedResults,
		executedStepIDs,
		"continue",
		3,
	)

	if capturedMetadata == nil {
		t.Fatal("expected phaseContext to be captured")
	}

	priorTools := capturedMetadata[PhaseContextKeyPriorToolsUsed].([]string)
	// Should be sorted and deduplicated
	expected := []string{"geocoding-tool", "search-tool", "weather-tool"}
	if len(priorTools) != len(expected) {
		t.Fatalf("PriorToolsUsed = %v, want %v", priorTools, expected)
	}
	for i, tool := range priorTools {
		if tool != expected[i] {
			t.Errorf("PriorToolsUsed[%d] = %q, want %q", i, tool, expected[i])
		}
	}

	// Phase number should be 3
	if capturedMetadata[PhaseContextKeyPhaseNumber].(int) != 3 {
		t.Errorf("PhaseNumber = %v, want 3", capturedMetadata[PhaseContextKeyPhaseNumber])
	}
}

func TestBuildPlanningPrompt_PassesNilPhaseContext(t *testing.T) {
	// buildPlanningPrompt (Phase 1) should pass nil as phaseContext
	var capturedMetadata map[string]interface{}
	metadataCaptured := false

	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(metadata map[string]interface{}) {
			metadataCaptured = true
			capturedMetadata = metadata
		},
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

	_, _ = orch.buildPlanningPrompt(context.Background(), "test request")

	if !metadataCaptured {
		t.Fatal("expected GetCapabilities to be called")
	}
	if capturedMetadata != nil {
		t.Error("buildPlanningPrompt should pass nil metadata (Phase 1), got non-nil")
	}
}

// =============================================================================
// Tests for uncovered edge cases (response truncation, baggage extraction)
// =============================================================================

func TestBuildContinuationPrompt_LongResponseTruncation(t *testing.T) {
	// Covers orchestrator.go buildContinuationPrompt — response > ContinuationResultMaxChars gets truncated
	var capturedMetadata map[string]interface{}

	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(metadata map[string]interface{}) {
			capturedMetadata = metadata
		},
	}

	orch := &AIOrchestrator{
		config: &OrchestratorConfig{
			RoutingMode:                ModeAutonomous,
			ContinuationResultMaxChars: 2000, // Explicit limit for this test
			IterativePlanning: IterativePlanConfig{
				Enabled:       true,
				MaxPhases:     5,
				MaxTotalSteps: 200,
			},
		},
		capabilityProvider: mockProvider,
	}

	// Create a step result with a long NON-JSON response. Phase 14 bounds it to a structural-floor
	// preview sized by ContinuationResultMaxChars (2000 here) instead of the old raw-slice "[truncated]"
	// marker. Success: true so the renderer emits the body (failed steps render a [FAILED: ...] marker).
	longResponse := strings.Repeat("x", 3000)
	completedResults := map[string]*StepResult{
		"step-1": {
			AgentName:   "test-agent",
			Instruction: "test instruction",
			Response:    longResponse,
			Success:     true,
		},
	}

	result, err := orch.buildContinuationPrompt(
		context.Background(),
		"test request",
		completedResults,
		[]string{"step-1"},
		"continue",
		2,
	)

	if err != nil {
		t.Fatalf("buildContinuationPrompt failed: %v", err)
	}

	// Phase 14: the full 3000-char response must not appear...
	if strings.Contains(result.Prompt, longResponse) {
		t.Error("full 3000-char response should not appear in prompt")
	}
	// ...it is bounded to the ContinuationResultMaxChars floor preview (no run beyond it survives)...
	if strings.Contains(result.Prompt, strings.Repeat("x", orch.config.ContinuationResultMaxChars+1)) {
		t.Errorf("long non-JSON response must be bounded to the ~%d-char floor preview", orch.config.ContinuationResultMaxChars)
	}
	// ...but the preview itself is present.
	if !strings.Contains(result.Prompt, strings.Repeat("x", 500)) {
		t.Error("the floor preview of the long response should still be present")
	}
	// Verify phaseContext was still propagated correctly
	if capturedMetadata == nil {
		t.Fatal("phaseContext should be passed regardless of response length")
	}
}

func TestBuildContinuationPrompt_BaggageRequestIDPropagation(t *testing.T) {
	// Covers orchestrator.go:3902-3904 and 3939-3942 — baggage request_id extraction
	var capturedLogFields map[string]interface{}

	mockProvider := &mockCapabilityProviderForPhaseContext{
		captureFunc: func(metadata map[string]interface{}) {},
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
		logger: &mockTieredLogger{
			debugFunc: func(msg string, fields map[string]interface{}) {
				if strings.Contains(msg, "Built continuation prompt") {
					capturedLogFields = fields
				}
			},
		},
	}

	completedResults := map[string]*StepResult{
		"step-1": {AgentName: "test-agent", Response: "data", Success: true},
	}

	// Set baggage with request_id to cover the baggage extraction paths
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "test-req-123")

	_, _ = orch.buildContinuationPrompt(
		ctx,
		"test request",
		completedResults,
		[]string{"step-1"},
		"continue",
		2,
	)

	// Verify the logger captured request_id from baggage (Pattern 3)
	if capturedLogFields == nil {
		t.Fatal("expected debug log for continuation prompt")
	}
	reqID, ok := capturedLogFields["request_id"].(string)
	if !ok || reqID != "test-req-123" {
		t.Errorf("request_id = %q, want %q", reqID, "test-req-123")
	}
}

func TestSelectRelevantTools_BaggageFallbackRequestID(t *testing.T) {
	// Covers tiered_capability_provider.go:419-421 — baggage fallback for requestID
	// when GetRequestID(ctx) returns empty but baggage has request_id
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Create context with baggage request_id but NO WithRequestID set
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "baggage-req-456")

	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:    2,
		PhaseContextKeyPriorToolsUsed: []string{"search-tool"},
	}

	_, err := provider.GetCapabilities(ctx, "test request", phaseContext)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	// The test passes if no panic occurs — the baggage fallback path was exercised
	// We verify the prompt was generated correctly
	calls := aiClient.GetCalls()
	if len(calls) == 0 {
		t.Fatal("expected LLM call")
	}
	if !strings.Contains(calls[0], "<identity>") {
		t.Error("expected context-aware prompt")
	}
}

// =============================================================================
// Mock types for tests
// =============================================================================

// mockTieredLogger captures log calls for assertion
type mockTieredLogger struct {
	debugFunc func(msg string, fields map[string]interface{})
	infoFunc  func(msg string, fields map[string]interface{})
	warnFunc  func(msg string, fields map[string]interface{})
}

func (m *mockTieredLogger) Debug(msg string, fields map[string]interface{}) {
	if m.debugFunc != nil {
		m.debugFunc(msg, fields)
	}
}
func (m *mockTieredLogger) Info(msg string, fields map[string]interface{}) {
	if m.infoFunc != nil {
		m.infoFunc(msg, fields)
	}
}
func (m *mockTieredLogger) Warn(msg string, fields map[string]interface{}) {
	if m.warnFunc != nil {
		m.warnFunc(msg, fields)
	}
}
func (m *mockTieredLogger) Error(msg string, fields map[string]interface{}) {}
func (m *mockTieredLogger) DebugWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	if m.debugFunc != nil {
		m.debugFunc(msg, fields)
	}
}
func (m *mockTieredLogger) InfoWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	if m.infoFunc != nil {
		m.infoFunc(msg, fields)
	}
}
func (m *mockTieredLogger) WarnWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	if m.warnFunc != nil {
		m.warnFunc(msg, fields)
	}
}
func (m *mockTieredLogger) ErrorWithContext(_ context.Context, msg string, fields map[string]interface{}) {
}

// mockCapabilityProviderForPhaseContext captures the metadata parameter
type mockCapabilityProviderForPhaseContext struct {
	captureFunc func(metadata map[string]interface{})
}

func (m *mockCapabilityProviderForPhaseContext) GetCapabilities(
	ctx context.Context,
	request string,
	metadata map[string]interface{},
) (*CapabilityResult, error) {
	if m.captureFunc != nil {
		m.captureFunc(metadata)
	}
	return &CapabilityResult{
		FormattedInfo: "Agent: test-agent\n  - capability_0: Test capability\n",
		AgentNames:    []string{"test-agent"},
	}, nil
}
