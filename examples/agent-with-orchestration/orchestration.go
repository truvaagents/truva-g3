package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ============================================================================
// Workflow Conversion and Execution Helpers
// ============================================================================

// Pre-compiled regex for request parameter templates (performance optimization)
// Pattern matches {{paramName}} for simple request parameter references
var requestParamTemplatePattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// workflowToRoutingPlan converts a TravelWorkflow to an orchestration.RoutingPlan
func (t *TravelResearchAgent) workflowToRoutingPlan(workflow *TravelWorkflow, params map[string]interface{}) *orchestration.RoutingPlan {
	plan := &orchestration.RoutingPlan{
		PlanID:          fmt.Sprintf("%s-%d", workflow.Name, time.Now().UnixNano()),
		OriginalRequest: fmt.Sprintf("Execute workflow: %s", workflow.Name),
		Mode:            orchestration.ModeWorkflow,
		CreatedAt:       time.Now(),
	}

	for _, step := range workflow.Steps {
		// Process step parameters with request parameter substitution
		// This enables templates like {{destination}} to be replaced with actual request values
		stepParams := make(map[string]interface{})
		for k, v := range step.Parameters {
			// Substitute request parameter templates in string values
			if strVal, ok := v.(string); ok {
				stepParams[k] = substituteRequestParams(strVal, params)
			} else {
				stepParams[k] = v
			}
		}

		plan.Steps = append(plan.Steps, orchestration.RoutingStep{
			StepID:      step.ID,
			AgentName:   step.ToolName,
			Instruction: step.Description,
			DependsOn:   step.DependsOn,
			Metadata: map[string]interface{}{
				"capability": step.Capability,
				"parameters": stepParams,
			},
		})
	}

	return plan
}

// substituteRequestParams replaces {{paramName}} templates with values from request parameters.
// This enables workflow definitions to reference request parameters dynamically.
// For example, if params = {"destination": "Tokyo"} and template = "{{destination}}",
// the result will be "Tokyo".
//
// Template Resolution Rules:
//   - Templates referencing non-existent parameters are left unchanged
//   - Numeric values are preserved when template is the entire string
//   - Multiple templates in a single string are all substituted as strings
func substituteRequestParams(template string, params map[string]interface{}) interface{} {
	// Use pre-compiled regex for performance (avoids re-compilation on each call)
	matches := requestParamTemplatePattern.FindAllStringSubmatch(template, -1)
	if len(matches) == 0 {
		return template // No templates found, return as-is
	}

	// Special case: if the entire string is a single template, preserve the value's type
	// This allows numeric values to remain as numbers instead of being converted to strings
	// Critical for parameters like "amount" that should remain numeric
	if len(matches) == 1 && matches[0][0] == template {
		paramName := matches[0][1]
		if value, ok := params[paramName]; ok {
			return value // Return the actual type (number, bool, etc.)
		}
		// Template not resolved - parameter not provided in request
		return template
	}

	// Multiple templates or template is part of a larger string - substitute as strings
	result := template
	for _, match := range matches {
		fullMatch := match[0]
		paramName := match[1]

		if value, ok := params[paramName]; ok {
			result = strings.Replace(result, fullMatch, fmt.Sprintf("%v", value), 1)
		}
	}

	return result
}

// ============================================================================
// Result Synthesis and Formatting
// ============================================================================

// synthesizeWorkflowResults uses AI to synthesize workflow results into a coherent response
func (t *TravelResearchAgent) synthesizeWorkflowResults(ctx context.Context, workflowName string, result *orchestration.ExecutionResult) (string, error) {
	startTime := time.Now()

	if t.AI == nil {
		return t.formatRawResults(result), nil
	}

	// Build prompt with results
	var resultsText string
	for _, step := range result.Steps {
		resultsText += fmt.Sprintf("## %s (%s)\n%s\n\n", step.AgentName, step.StepID, step.Response)
	}

	prompt := fmt.Sprintf(`You are a travel research assistant. Synthesize the following results from a "%s" workflow into a helpful, coherent response for a traveler.

Results:
%s

Provide a well-organized summary that highlights the key information a traveler would need.`, workflowName, resultsText)

	response, err := t.AI.GenerateResponse(ctx, prompt, &core.AIOptions{
		Model:       "smart", // Uses alias - can be overridden via TRUVAG3_OPENAI_MODEL_SMART env var
		Temperature: 0.5,
		MaxTokens:   1000,
	})

	// Track AI synthesis metrics using unified API
	duration := time.Since(startTime)
	if err != nil {
		telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "openai",
			float64(duration.Milliseconds()), "error")
		return "", err
	}

	telemetry.RecordAIRequest(telemetry.ModuleOrchestration, "openai",
		float64(duration.Milliseconds()), "success")

	return response.Content, nil
}

// formatRawResults formats execution results without AI synthesis
func (t *TravelResearchAgent) formatRawResults(result *orchestration.ExecutionResult) string {
	var output string
	for _, step := range result.Steps {
		status := "Success"
		if !step.Success {
			status = fmt.Sprintf("Failed: %s", step.Error)
		}
		output += fmt.Sprintf("**%s** (%s): %s\n%s\n\n", step.AgentName, step.StepID, status, step.Response)
	}
	return output
}

// ============================================================================
// Confidence and Metrics Calculation
// ============================================================================

// calculateConfidence calculates confidence score based on execution results
func (t *TravelResearchAgent) calculateConfidence(result *orchestration.ExecutionResult) float64 {
	if len(result.Steps) == 0 {
		return 0.0
	}

	successCount := 0
	for _, step := range result.Steps {
		if step.Success {
			successCount++
		}
	}

	return float64(successCount) / float64(len(result.Steps))
}

// ============================================================================
// Tool Discovery Helpers
// ============================================================================

// discoverTools discovers available tools and emits discovery metrics
func (t *TravelResearchAgent) discoverTools(ctx context.Context) ([]*core.ServiceInfo, error) {
	if t.Discovery == nil {
		return nil, fmt.Errorf("discovery service not available")
	}

	tools, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{
		Type: core.ComponentTypeTool,
	})
	if err != nil {
		return nil, err
	}

	// Emit discovery metrics
	telemetry.Gauge("orchestration.tools.discovered", float64(len(tools)))

	return tools, nil
}

// getToolNames extracts tool names from service info slice
func getToolNames(tools []*core.ServiceInfo) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// ============================================================================
// Step Execution Tracking
// ============================================================================

// trackStepExecution records metrics for a workflow step execution using unified API
func trackStepExecution(workflowName, stepID, toolName string, duration time.Duration, success bool) {
	status := "success"
	if !success {
		status = "error"
	}

	// Record tool call with unified metrics API
	telemetry.RecordToolCall(telemetry.ModuleOrchestration, toolName,
		float64(duration.Milliseconds()), status)

	if !success {
		telemetry.RecordToolCallError(telemetry.ModuleOrchestration, toolName, "step_failure")
	}
}

// trackWorkflowExecution records metrics for overall workflow execution using unified API
func trackWorkflowExecution(workflowName string, duration time.Duration, success bool) {
	status := "success"
	if !success {
		status = "error"
	}

	// Use RecordRequest for workflow execution (user-facing operation)
	telemetry.RecordRequest(telemetry.ModuleOrchestration, workflowName,
		float64(duration.Milliseconds()), status)

	if !success {
		telemetry.RecordRequestError(telemetry.ModuleOrchestration, workflowName, "workflow_failure")
	}
}
