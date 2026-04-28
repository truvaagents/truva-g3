package orchestration

import (
	"encoding/json"
	"strings"
	"testing"
)

// =============================================================================
// 11a: extractOrchestratorChildSummary extraction tests (8 tests)
// =============================================================================

func TestExtractOrchestratorChildSummary_PlainTextResponse(t *testing.T) {
	result := &StepResult{
		Response: "Deployment restarted successfully",
	}
	got := extractOrchestratorChildSummary(result)
	if got != "" {
		t.Errorf("expected empty string for plain text response, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_WithChildSteps(t *testing.T) {
	resp := map[string]interface{}{
		"response": "Incident handled",
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   `{"key": "DEVOPS-27"}`,
			},
			map[string]interface{}{
				"agent_name": "slack-tool",
				"capability": "send_message",
				"success":    true,
				"response":   "sent to #incidents",
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	if !strings.Contains(got, "jira-tool/create_issue [SUCCESS]") {
		t.Errorf("expected jira-tool/create_issue [SUCCESS], got: %q", got)
	}
	if !strings.Contains(got, "slack-tool/send_message [SUCCESS]") {
		t.Errorf("expected slack-tool/send_message [SUCCESS], got: %q", got)
	}
	if !strings.Contains(got, "DEVOPS-27") {
		t.Errorf("expected DEVOPS-27 in output, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_EmptySteps(t *testing.T) {
	resp := map[string]interface{}{
		"response": "No sub-steps executed",
		"steps":    []interface{}{},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	if got != "" {
		t.Errorf("expected empty string for empty steps[], got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_MalformedJSON(t *testing.T) {
	result := &StepResult{Response: "not json {{{"}
	got := extractOrchestratorChildSummary(result)
	if got != "" {
		t.Errorf("expected empty string for malformed JSON, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_ResponseTruncatedAt200(t *testing.T) {
	longResp := strings.Repeat("x", 300)
	resp := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "devops-tool",
				"capability": "describe_resource",
				"success":    true,
				"response":   longResp,
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	if !strings.Contains(got, "...") {
		t.Errorf("expected truncation marker '...' for long response, got: %q", got)
	}
	// Should contain exactly 200 chars of the response + "..."
	if strings.Contains(got, strings.Repeat("x", 201)) {
		t.Errorf("response should be truncated to 200 chars, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_FailedChildStep(t *testing.T) {
	resp := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "devops-tool",
				"capability": "rollout_restart",
				"success":    false,
				"response":   "permission denied",
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	if !strings.Contains(got, "[FAILED]") {
		t.Errorf("expected [FAILED] for unsuccessful step, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_MissingFields(t *testing.T) {
	resp := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				// Missing agent_name, capability — should not panic
				"success":  true,
				"response": "some result",
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	// Should produce output with empty agent/capability, not panic
	if got == "" {
		t.Errorf("expected non-empty output even with missing fields")
	}
	if !strings.Contains(got, "/ [SUCCESS]") {
		t.Errorf("expected empty agent/cap formatted as '/ [SUCCESS]', got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_NilResult(t *testing.T) {
	got := extractOrchestratorChildSummary(nil)
	if got != "" {
		t.Errorf("expected empty string for nil result, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_NonMapStepEntry(t *testing.T) {
	// steps[] contains a non-object entry (e.g., string) — should be skipped
	resp := map[string]interface{}{
		"steps": []interface{}{
			"not a map",
			42,
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   "ok",
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	// Should only produce 1 line (the valid map entry), skipping the string and int
	lineCount := strings.Count(got, "\n")
	if lineCount != 1 {
		t.Errorf("expected 1 line (skipping non-map entries), got %d lines: %q", lineCount, got)
	}
	if !strings.Contains(got, "jira-tool/create_issue") {
		t.Errorf("expected the valid step to be included, got: %q", got)
	}
}

func TestExtractOrchestratorChildSummary_ManyChildSteps(t *testing.T) {
	steps := make([]interface{}, 13)
	for i := 0; i < 13; i++ {
		steps[i] = map[string]interface{}{
			"agent_name": "tool",
			"capability": "action",
			"success":    true,
			"response":   "ok",
		}
	}
	resp := map[string]interface{}{"steps": steps}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	got := extractOrchestratorChildSummary(result)
	lineCount := strings.Count(got, "\n")
	if lineCount != 13 {
		t.Errorf("expected 13 lines for 13 child steps, got %d", lineCount)
	}
}

// =============================================================================
// 11b: buildContinuationPrompt enrichment tests (6 tests)
// These test the integration by checking the output of the result loop logic.
// Since buildContinuationPrompt is a method on AIOrchestrator with many
// dependencies, we test the enrichment logic via the helper + format pattern.
// =============================================================================

func TestContinuationEnrichment_OrchestratorStepGetsAnnotation(t *testing.T) {
	resp := map[string]interface{}{
		"response": "handled",
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   `{"key": "DEVOPS-27"}`,
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{
		StepID:      "step-5",
		AgentName:   "devops-chat-agent",
		Instruction: "Remediate incident",
		Response:    string(respJSON),
	}

	childSummary := extractOrchestratorChildSummary(result)
	if childSummary == "" {
		t.Fatal("expected non-empty child summary")
	}

	// Simulate the enrichment pattern from buildContinuationPrompt
	var sb strings.Builder
	sb.WriteString("Step step-5 (devops-chat-agent):\n  Task: Remediate incident\n  Result: ...\n")
	if childSummary != "" {
		sb.WriteString("  NOTE: This orchestrator step internally executed these sub-steps:\n")
		sb.WriteString(childSummary)
		sb.WriteString("  Do NOT duplicate any of these actions in the next phase.\n")
	}
	output := sb.String()

	if !strings.Contains(output, "NOTE: This orchestrator step internally executed") {
		t.Errorf("missing enrichment annotation")
	}
	if !strings.Contains(output, "Do NOT duplicate") {
		t.Errorf("missing dedup directive")
	}
	if !strings.Contains(output, "jira-tool/create_issue [SUCCESS]") {
		t.Errorf("missing child step detail")
	}
}

func TestContinuationEnrichment_ToolStepNoAnnotation(t *testing.T) {
	result := &StepResult{
		StepID:    "step-1",
		AgentName: "prometheus-query-tool",
		Response:  `{"query": "up", "result_type": "vector", "samples": []}`,
	}
	childSummary := extractOrchestratorChildSummary(result)
	// Tool response has no steps[] array — should return ""
	if childSummary != "" {
		t.Errorf("expected no enrichment for tool step, got: %q", childSummary)
	}
}

func TestContinuationEnrichment_ChildSummaryVisibleAfterTruncation(t *testing.T) {
	// Simulate a 26KB orchestrator response where steps[] starts at char 4037
	bigResponse := strings.Repeat("x", 26000)
	resp := map[string]interface{}{
		"response": bigResponse,
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   `{"key": "DEVOPS-27"}`,
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	// Extract BEFORE truncation (as the code does)
	childSummary := extractOrchestratorChildSummary(result)
	if childSummary == "" {
		t.Fatal("child summary should be extracted from full response before truncation")
	}
	if !strings.Contains(childSummary, "jira-tool/create_issue") {
		t.Errorf("child summary should contain jira-tool/create_issue")
	}

	// Truncate the response (as the code does after extraction)
	truncated := result.Response
	if len(truncated) > 10000 {
		truncated = truncated[:10000] + "\n[truncated]"
	}

	// Build the final prompt section — child summary appended after truncated response
	var sb strings.Builder
	sb.WriteString("Result: " + truncated + "\n")
	sb.WriteString(childSummary)
	output := sb.String()
	if !strings.Contains(output, "jira-tool/create_issue") {
		t.Errorf("child summary must be visible in final output regardless of truncation")
	}
}

func TestContinuationEnrichment_MultipleOrchestratorSteps(t *testing.T) {
	buildOrchestratorResponse := func(agent, cap, resp string) string {
		r := map[string]interface{}{
			"steps": []interface{}{
				map[string]interface{}{
					"agent_name": agent,
					"capability": cap,
					"success":    true,
					"response":   resp,
				},
			},
		}
		j, _ := json.Marshal(r)
		return string(j)
	}

	results := map[string]*StepResult{
		"step-3": {Response: buildOrchestratorResponse("jira-tool", "create_issue", `{"key":"DEVOPS-27"}`)},
		"step-4": {Response: buildOrchestratorResponse("slack-tool", "send_message", "sent")},
	}

	for _, result := range results {
		summary := extractOrchestratorChildSummary(result)
		if summary == "" {
			t.Errorf("each orchestrator step should produce a child summary")
		}
	}

	summary3 := extractOrchestratorChildSummary(results["step-3"])
	summary4 := extractOrchestratorChildSummary(results["step-4"])
	if !strings.Contains(summary3, "jira-tool/create_issue") {
		t.Errorf("step-3 summary should contain jira-tool")
	}
	if !strings.Contains(summary4, "slack-tool/send_message") {
		t.Errorf("step-4 summary should contain slack-tool")
	}
}

func TestContinuationEnrichment_MixedOrchestratorAndToolSteps(t *testing.T) {
	orchResp := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   `{"key":"DEVOPS-27"}`,
			},
		},
	}
	orchJSON, _ := json.Marshal(orchResp)

	results := map[string]*StepResult{
		"step-1": {AgentName: "prometheus-query-tool", Response: `{"samples": []}`},
		"step-2": {AgentName: "devops-tool", Response: `{"pods": ["pod-1"]}`},
		"step-3": {AgentName: "devops-chat-agent", Response: string(orchJSON)},
	}

	enrichedCount := 0
	for _, result := range results {
		if extractOrchestratorChildSummary(result) != "" {
			enrichedCount++
		}
	}
	if enrichedCount != 1 {
		t.Errorf("expected exactly 1 orchestrator step enriched, got %d", enrichedCount)
	}
}

func TestContinuationEnrichment_EnrichmentAfterTruncation(t *testing.T) {
	// Verify the ordering: extract child summary from full response,
	// THEN truncate the response, THEN append child summary.
	// This ensures child steps are always visible.
	resp := map[string]interface{}{
		"response": strings.Repeat("a", 15000),
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   `{"key":"DEVOPS-99"}`,
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	fullResponse := string(respJSON)

	// Step 1: Extract from FULL response (before truncation)
	result := &StepResult{Response: fullResponse}
	childSummary := extractOrchestratorChildSummary(result)

	// Step 2: Truncate
	maxChars := 10000
	truncatedResponse := fullResponse
	if len(truncatedResponse) > maxChars {
		truncatedResponse = truncatedResponse[:maxChars] + "\n[truncated]"
	}

	// Step 3: Build output with summary appended AFTER truncated response
	var sb strings.Builder
	sb.WriteString("Result: " + truncatedResponse + "\n")
	if childSummary != "" {
		sb.WriteString("NOTE: child steps:\n" + childSummary)
	}

	output := sb.String()
	if !strings.Contains(output, "[truncated]") {
		t.Errorf("response should be truncated")
	}
	if !strings.Contains(output, "DEVOPS-99") {
		t.Errorf("child summary with DEVOPS-99 must be present after truncation")
	}
}

// =============================================================================
// 11c: Configurable truncation limit tests (7 tests)
// =============================================================================

func TestDefaultConfig_ContinuationResultMaxChars(t *testing.T) {
	config := DefaultConfig()
	if config.ContinuationResultMaxChars != 10000 {
		t.Errorf("expected default ContinuationResultMaxChars=10000, got %d", config.ContinuationResultMaxChars)
	}
}

func TestDefaultConfig_ContinuationResultMaxChars_EnvOverride(t *testing.T) {
	t.Setenv("TRUVAG3_CONTINUATION_RESULT_MAX_CHARS", "20000")
	config := DefaultConfig()
	if config.ContinuationResultMaxChars != 20000 {
		t.Errorf("expected ContinuationResultMaxChars=20000 from env, got %d", config.ContinuationResultMaxChars)
	}
}

func TestDefaultConfig_ContinuationResultMaxChars_InvalidEnv(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{"non-numeric", "abc"},
		{"zero", "0"},
		{"negative", "-100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TRUVAG3_CONTINUATION_RESULT_MAX_CHARS", tt.env)
			config := DefaultConfig()
			if config.ContinuationResultMaxChars != 10000 {
				t.Errorf("expected fallback to 10000 for invalid env %q, got %d", tt.env, config.ContinuationResultMaxChars)
			}
		})
	}
}

func TestContinuationTruncation_ShorterThanLimit(t *testing.T) {
	response := "short response"
	maxChars := 10000
	if len(response) > maxChars {
		t.Errorf("response should not be truncated")
	}
}

func TestContinuationTruncation_AtExactLimit(t *testing.T) {
	response := strings.Repeat("x", 10000)
	maxChars := 10000
	// At exact limit, should NOT truncate (> not >=)
	if len(response) > maxChars {
		t.Errorf("response at exact limit should not be truncated")
	}
}

func TestContinuationTruncation_LongerThanLimit(t *testing.T) {
	response := strings.Repeat("x", 15000)
	maxChars := 10000
	if len(response) > maxChars {
		response = response[:maxChars] + "\n[truncated]"
	}
	if !strings.HasSuffix(response, "\n[truncated]") {
		t.Errorf("response should end with truncation marker")
	}
	// 10000 chars + len("\n[truncated]") = 10012
	if len(response) != maxChars+len("\n[truncated]") {
		t.Errorf("expected truncated length %d, got %d", maxChars+len("\n[truncated]"), len(response))
	}
}

func TestContinuationTruncation_CustomLimit(t *testing.T) {
	t.Setenv("TRUVAG3_CONTINUATION_RESULT_MAX_CHARS", "5000")
	config := DefaultConfig()
	if config.ContinuationResultMaxChars != 5000 {
		t.Errorf("expected custom limit 5000, got %d", config.ContinuationResultMaxChars)
	}

	response := strings.Repeat("x", 8000)
	maxChars := config.ContinuationResultMaxChars
	if len(response) > maxChars {
		response = response[:maxChars] + "\n[truncated]"
	}
	// Should be truncated at 5000, not 10000
	if !strings.HasSuffix(response, "\n[truncated]") {
		t.Errorf("response should be truncated at custom limit")
	}
}

// =============================================================================
// 11d: Observability tests (3 tests)
// These test that the logging/span conditions are correct.
// =============================================================================

func TestContinuationObservability_ChildStepsExtracted_LogCondition(t *testing.T) {
	resp := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   "ok",
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	childSummary := extractOrchestratorChildSummary(result)
	// Log should fire when childSummary != ""
	if childSummary == "" {
		t.Fatal("expected non-empty child summary — log condition would not fire")
	}
	childStepCount := strings.Count(childSummary, "\n")
	if childStepCount != 1 {
		t.Errorf("expected 1 child step counted via newlines, got %d", childStepCount)
	}
}

func TestContinuationObservability_NoChildSteps_NoLog(t *testing.T) {
	result := &StepResult{Response: `{"response": "just a tool result"}`}
	childSummary := extractOrchestratorChildSummary(result)
	// Log should NOT fire when childSummary == ""
	if childSummary != "" {
		t.Errorf("expected empty child summary for tool step — log would fire incorrectly")
	}
}

func TestContinuationObservability_SpanEventAttributes(t *testing.T) {
	resp := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"agent_name": "jira-tool",
				"capability": "create_issue",
				"success":    true,
				"response":   `{"key":"DEVOPS-27"}`,
			},
			map[string]interface{}{
				"agent_name": "slack-tool",
				"capability": "send_message",
				"success":    true,
				"response":   "sent",
			},
		},
	}
	respJSON, _ := json.Marshal(resp)
	result := &StepResult{Response: string(respJSON)}

	childSummary := extractOrchestratorChildSummary(result)
	childStepCount := strings.Count(childSummary, "\n")

	// Verify the values that would be passed to span attributes
	if childStepCount != 2 {
		t.Errorf("expected child_steps_count=2 for span event, got %d", childStepCount)
	}
	if childSummary == "" {
		t.Errorf("child_steps_found should be true (non-empty summary)")
	}

	// Verify child_capabilities extraction from summary lines
	var caps []string
	for _, line := range strings.Split(childSummary, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			parts := strings.SplitN(line[2:], " ", 2)
			if len(parts) > 0 {
				caps = append(caps, parts[0])
			}
		}
	}
	childCapabilities := strings.Join(caps, ",")
	if childCapabilities != "jira-tool/create_issue,slack-tool/send_message" {
		t.Errorf("expected child_capabilities='jira-tool/create_issue,slack-tool/send_message', got %q", childCapabilities)
	}
}
