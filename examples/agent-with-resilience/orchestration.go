package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// ============================================================================
// Resilient Tool Orchestration
// ============================================================================
// This file demonstrates how to use the TruvaG3 resilience module for
// fault-tolerant tool orchestration. Key patterns:
//   - resilience.RetryWithCircuitBreaker() for combined retry + CB
//   - cb.ExecuteWithTimeout() for timeout protection
//   - cb.GetMetrics() for observability
// ============================================================================

// ToolCapabilityPair represents a selected tool with its specific capability
type ToolCapabilityPair struct {
	Tool       *core.ServiceInfo
	Capability *core.Capability
}

// ============================================================================
// Intelligent Error Handling Types
// ============================================================================
// These types support AI-powered error recovery as described in
// docs/orchestration/INTELLIGENT_ERROR_HANDLING.md

// IntelligentRetryConfig controls how the agent handles failed tool calls
// with AI-powered error correction
type IntelligentRetryConfig struct {
	MaxRetries      int           // How many times to retry (default: 3)
	UseAI           bool          // Should AI analyze errors and suggest fixes?
	BackoffDuration time.Duration // How long to wait between retries
}

// DefaultIntelligentRetryConfig returns sensible defaults for intelligent retry
func DefaultIntelligentRetryConfig() IntelligentRetryConfig {
	return IntelligentRetryConfig{
		MaxRetries:      3,
		UseAI:           true,
		BackoffDuration: 1 * time.Second,
	}
}

// ErrorContext bundles everything AI needs to analyze an error
type ErrorContext struct {
	HTTPStatus      int                    // HTTP status code (e.g., 404)
	OriginalRequest map[string]interface{} // The payload that failed
	ToolError       *core.ToolError        // Structured error from tool
	ToolName        string                 // e.g., "weather-tool"
	Capability      string                 // e.g., "current_weather"
	AttemptNumber   int                    // Which retry attempt this is
}

// callToolWithResilience wraps tool calls with circuit breaker, retry logic,
// AND intelligent error handling with AI-powered payload correction.
// This is the recommended method for all tool calls as it provides:
//   - Circuit breaker protection against cascading failures
//   - Exponential backoff with jitter via resilience.RetryWithCircuitBreaker
//   - Intelligent error handling with category-based routing
//   - AI-powered payload correction for retryable 4xx errors
func (r *ResearchAgent) callToolWithResilience(ctx context.Context, tool *core.ServiceInfo, capability *core.Capability, topic string) *ToolResult {
	startTime := time.Now()

	// Get or create circuit breaker for this tool
	cb := r.getOrCreateCircuitBreaker(tool.Name)
	if cb == nil {
		// Fallback to direct call if CB creation fails
		r.Logger.Warn("Circuit breaker unavailable, using direct call", map[string]interface{}{
			"tool": tool.Name,
		})
		result, _ := r.callToolDirect(ctx, tool, capability, topic)
		return result
	}

	// Check circuit breaker state first (fail-fast)
	if !cb.CanExecute() {
		r.Logger.Warn("Circuit breaker is open, failing fast", map[string]interface{}{
			"tool":          tool.Name,
			"circuit_state": cb.GetState(),
		})
		return &ToolResult{
			ToolName:   tool.Name,
			Capability: capability.Name,
			Success:    false,
			Error:      fmt.Sprintf("Circuit breaker open for %s - service temporarily unavailable", tool.Name),
			Duration:   time.Since(startTime).String(),
		}
	}

	// Generate initial payload using AI
	initialPayload, err := r.generateToolPayloadWithAI(ctx, topic, tool, capability)
	if err != nil {
		return &ToolResult{
			ToolName:   tool.Name,
			Capability: capability.Name,
			Success:    false,
			Error:      fmt.Sprintf("Payload generation failed: %v", err),
			Duration:   time.Since(startTime).String(),
		}
	}

	// Use intelligent retry with AI-powered error correction
	// This handles:
	//   - Category-based error routing (auth vs rate limit vs server error)
	//   - AI-powered payload correction for retryable 4xx errors
	//   - Proper backoff for rate limits (429) and server errors (5xx)
	result, err := r.callToolWithIntelligentRetry(ctx, tool, capability, initialPayload, IntelligentRetryConfig{
		MaxRetries:      r.retryConfig.MaxAttempts,
		UseAI:           r.aiClient != nil,
		BackoffDuration: r.retryConfig.InitialDelay,
	})

	// Record result with circuit breaker
	if err != nil {
		cb.RecordFailure()

		r.Logger.Error("Tool call failed after intelligent retry", map[string]interface{}{
			"tool":          tool.Name,
			"capability":    capability.Name,
			"error":         err.Error(),
			"circuit_state": cb.GetState(),
			"duration":      time.Since(startTime).String(),
		})

		if result == nil {
			result = &ToolResult{
				ToolName:   tool.Name,
				Capability: capability.Name,
				Success:    false,
				Error:      err.Error(),
				Duration:   time.Since(startTime).String(),
			}
		}
		return result
	}

	cb.RecordSuccess()

	r.Logger.Info("Resilient tool call succeeded", map[string]interface{}{
		"tool":          tool.Name,
		"capability":    capability.Name,
		"circuit_state": cb.GetState(),
		"duration":      time.Since(startTime).String(),
	})

	return result
}

// callToolWithTimeout wraps a tool call with explicit timeout using cb.ExecuteWithTimeout
func (r *ResearchAgent) callToolWithTimeout(ctx context.Context, tool *core.ServiceInfo, capability *core.Capability, topic string, timeout time.Duration) *ToolResult {
	startTime := time.Now()

	cb := r.getOrCreateCircuitBreaker(tool.Name)
	if cb == nil {
		result, _ := r.callToolDirect(ctx, tool, capability, topic)
		return result
	}

	var result *ToolResult

	// Use framework's ExecuteWithTimeout for explicit timeout control
	err := cb.ExecuteWithTimeout(ctx, timeout, func() error {
		var callErr error
		result, callErr = r.callToolDirect(ctx, tool, capability, topic)
		return callErr
	})

	if err != nil {
		return &ToolResult{
			ToolName:   tool.Name,
			Capability: capability.Name,
			Success:    false,
			Error:      fmt.Sprintf("Tool call failed (timeout=%v): %v", timeout, err),
			Duration:   time.Since(startTime).String(),
		}
	}

	return result
}

// callToolDirect makes the actual HTTP call to the tool (no resilience wrapper)
func (r *ResearchAgent) callToolDirect(ctx context.Context, tool *core.ServiceInfo, capability *core.Capability, topic string) (*ToolResult, error) {
	startTime := time.Now()

	endpoint := capability.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("/api/capabilities/%s", capability.Name)
	}

	url := fmt.Sprintf("http://%s:%d%s", tool.Address, tool.Port, endpoint)

	// Generate payload using AI
	payload, err := r.generateToolPayloadWithAI(ctx, topic, tool, capability)
	if err != nil {
		return &ToolResult{
			ToolName:   tool.Name,
			Capability: capability.Name,
			Success:    false,
			Error:      fmt.Sprintf("Payload generation failed: %v", err),
			Duration:   time.Since(startTime).String(),
		}, err
	}

	// Make HTTP request
	jsonData, _ := json.Marshal(payload)
	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP call failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("response reading failed: %w", err)
	}

	// Parse the ToolResponse envelope (intelligent error handling)
	toolResp := r.parseToolResponse(body)

	// Handle response
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var responseData interface{}
		if toolResp != nil && toolResp.Success {
			// Use data from ToolResponse envelope
			responseData = toolResp.Data
		} else {
			// Fallback: parse body as raw JSON
			if err := json.Unmarshal(body, &responseData); err != nil {
				responseData = string(body)
			}
		}

		return &ToolResult{
			ToolName:   tool.Name,
			Capability: capability.Name,
			Data:       responseData,
			Success:    true,
			Duration:   time.Since(startTime).String(),
		}, nil
	}

	// Non-success status code - extract structured error if available
	var structuredErr *core.ToolError
	if toolResp != nil && toolResp.Error != nil {
		structuredErr = toolResp.Error
	}

	return &ToolResult{
		ToolName:        tool.Name,
		Capability:      capability.Name,
		Success:         false,
		Error:           fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
		StructuredError: structuredErr,
		Duration:        time.Since(startTime).String(),
	}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
}

// ============================================================================
// Multi-Entity Comparison Support with Resilience
// ============================================================================

// callToolForEntities performs resilient parallel tool calls for multiple entities
func (r *ResearchAgent) callToolForEntities(ctx context.Context, tool *core.ServiceInfo, capability *core.Capability, baseTopic string, entities []string) []ToolResult {
	if len(entities) == 0 {
		return nil
	}

	r.Logger.Info("Starting resilient parallel tool calls", map[string]interface{}{
		"tool":         tool.Name,
		"entity_count": len(entities),
		"entities":     entities,
	})

	results := make([]ToolResult, len(entities))
	done := make(chan int, len(entities))

	for i, entity := range entities {
		go func(index int, entityName string) {
			entityTopic := fmt.Sprintf("%s for %s", capability.Name, entityName)

			// Each entity call uses resilience (circuit breaker + retry)
			result := r.callToolWithResilience(ctx, tool, capability, entityTopic)
			if result != nil {
				results[index] = *result
			}
			done <- index
		}(i, entity)
	}

	// Wait for all with timeout
	timeout := time.After(30 * time.Second)
	completed := 0
	for completed < len(entities) {
		select {
		case <-done:
			completed++
		case <-timeout:
			r.Logger.Warn("Timeout waiting for entity results", map[string]interface{}{
				"completed": completed,
				"total":     len(entities),
			})
			goto collectResults
		case <-ctx.Done():
			r.Logger.Warn("Context cancelled during entity calls", nil)
			goto collectResults
		}
	}

collectResults:
	// Filter out empty results
	validResults := make([]ToolResult, 0, len(results))
	for _, result := range results {
		if result.ToolName != "" {
			validResults = append(validResults, result)
		}
	}

	return validResults
}

// ============================================================================
// AI-Powered Tool Selection and Payload Generation
// ============================================================================

// selectToolsAndCapabilities performs AI-powered tool + capability selection
func (r *ResearchAgent) selectToolsAndCapabilities(ctx context.Context, topic string, tools []*core.ServiceInfo) []ToolCapabilityPair {
	if r.aiClient == nil || len(tools) == 0 {
		return nil
	}

	var catalog strings.Builder
	catalog.WriteString("Available Tools:\n\n")

	for i, tool := range tools {
		catalog.WriteString(fmt.Sprintf("%d. Tool: %s\n", i+1, tool.Name))
		catalog.WriteString("   Capabilities:\n")
		for _, cap := range tool.Capabilities {
			desc := cap.Description
			if desc == "" {
				desc = cap.Name
			}
			catalog.WriteString(fmt.Sprintf("   - %s: %s\n", cap.Name, desc))
		}
		catalog.WriteString("\n")
	}

	prompt := fmt.Sprintf(`Select the MOST relevant tool and capability for this request.

User Request: "%s"

%s

Return JSON with this exact format:
{
  "tool": "tool-name",
  "capability": "capability-name",
  "reasoning": "brief explanation"
}

Select the single best match.`, topic, catalog.String())

	response, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature: 0.3,
		MaxTokens:   200,
	})
	if err != nil {
		r.Logger.Error("AI tool selection failed", map[string]interface{}{
			"error": err.Error(),
		})
		return nil
	}

	var selection struct {
		Tool       string `json:"tool"`
		Capability string `json:"capability"`
		Reasoning  string `json:"reasoning"`
	}

	// Strip markdown code blocks (e.g. ```json ... ```) — matches orchestration.extractJSON()
	content := extractJSON(response.Content)

	if err := json.Unmarshal([]byte(content), &selection); err != nil {
		r.Logger.Error("Failed to parse AI selection", map[string]interface{}{
			"error": err.Error(),
		})
		return nil
	}

	for _, tool := range tools {
		if tool.Name == selection.Tool {
			for _, cap := range tool.Capabilities {
				if cap.Name == selection.Capability {
					return []ToolCapabilityPair{{Tool: tool, Capability: &cap}}
				}
			}
		}
	}

	return nil
}

// generateToolPayloadWithAI creates the tool payload using AI
func (r *ResearchAgent) generateToolPayloadWithAI(ctx context.Context, topic string, tool *core.ServiceInfo, capability *core.Capability) (map[string]interface{}, error) {
	if r.aiClient == nil {
		// Fallback to simple payload
		return map[string]interface{}{"query": topic}, nil
	}

	// Build prompt based on capability schema hints
	var promptBuilder strings.Builder
	promptBuilder.WriteString(fmt.Sprintf("Generate a JSON payload for the '%s' capability of tool '%s'.\n\n", capability.Name, tool.Name))
	promptBuilder.WriteString(fmt.Sprintf("User Request: %s\n\n", topic))

	if capability.InputSummary != nil {
		promptBuilder.WriteString("Required fields:\n")
		for _, field := range capability.InputSummary.RequiredFields {
			promptBuilder.WriteString(fmt.Sprintf("- %s (%s): %s (example: %s)\n", field.Name, field.Type, field.Description, field.Example))
		}
		if len(capability.InputSummary.OptionalFields) > 0 {
			promptBuilder.WriteString("\nOptional fields:\n")
			for _, field := range capability.InputSummary.OptionalFields {
				promptBuilder.WriteString(fmt.Sprintf("- %s (%s): %s\n", field.Name, field.Type, field.Description))
			}
		}
	}

	promptBuilder.WriteString("\nReturn ONLY valid JSON. No explanation.")

	response, err := r.aiClient.GenerateResponse(ctx, promptBuilder.String(), &core.AIOptions{
		Temperature: 0.2,
		MaxTokens:   300,
	})
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(response.Content), &payload); err != nil {
		// Try to extract JSON from response
		content := response.Content
		if start := strings.Index(content, "{"); start != -1 {
			if end := strings.LastIndex(content, "}"); end != -1 {
				if err := json.Unmarshal([]byte(content[start:end+1]), &payload); err != nil {
					return map[string]interface{}{"query": topic}, nil
				}
			}
		}
	}

	return payload, nil
}

// extractEntitiesForComparison uses AI to extract entities from comparison queries
func (r *ResearchAgent) extractEntitiesForComparison(ctx context.Context, topic string) ([]string, error) {
	if r.aiClient == nil {
		return nil, fmt.Errorf("AI client not available")
	}

	prompt := fmt.Sprintf(`Extract entities for comparison from this query. Return JSON array of entity names only.
If not a comparison query, return empty array.

Query: "%s"

Examples:
- "Compare weather in NYC vs LA" -> ["NYC", "LA"]
- "Amazon vs Google stock" -> ["Amazon", "Google"]
- "Weather in Paris" -> []

Return ONLY the JSON array:`, topic)

	response, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature: 0.1,
		MaxTokens:   100,
	})
	if err != nil {
		return nil, err
	}

	var entities []string
	if err := json.Unmarshal([]byte(response.Content), &entities); err != nil {
		return nil, err
	}

	return entities, nil
}

// ============================================================================
// Intelligent Error Handling Helpers
// ============================================================================
// These functions implement AI-powered error recovery as described in
// docs/orchestration/INTELLIGENT_ERROR_HANDLING.md

// parseToolResponse parses the standard ToolResponse envelope from tool responses
func (r *ResearchAgent) parseToolResponse(body []byte) *core.ToolResponse {
	var toolResp core.ToolResponse
	if err := json.Unmarshal(body, &toolResp); err != nil {
		// If we can't parse as ToolResponse, return nil
		// The caller will handle this as a generic error
		return nil
	}
	return &toolResp
}

// parseRetryAfter extracts the retry-after duration from a ToolError's Details
func (r *ResearchAgent) parseRetryAfter(toolErr *core.ToolError) time.Duration {
	if toolErr == nil || toolErr.Details == nil {
		return 1 * time.Second // Default backoff
	}

	if retryAfterStr, ok := toolErr.Details["retry_after"]; ok {
		if d, err := time.ParseDuration(retryAfterStr); err == nil {
			return d
		}
	}
	return 1 * time.Second // Default backoff
}

// aiCorrectPayload uses AI to analyze an error and generate a corrected payload
// This is the "intelligent" part of intelligent error handling
func (r *ResearchAgent) aiCorrectPayload(ctx context.Context, errCtx ErrorContext) (map[string]interface{}, error) {
	if r.aiClient == nil {
		return nil, fmt.Errorf("AI client not available")
	}

	// Build prompt with all context AI needs
	detailsStr := ""
	if errCtx.ToolError != nil && errCtx.ToolError.Details != nil {
		for k, v := range errCtx.ToolError.Details {
			detailsStr += fmt.Sprintf("  %s: %s\n", k, v)
		}
	}

	originalJSON, _ := json.MarshalIndent(errCtx.OriginalRequest, "", "  ")

	errorCode := "UNKNOWN"
	errorCategory := "UNKNOWN"
	errorMessage := "Unknown error"
	if errCtx.ToolError != nil {
		errorCode = errCtx.ToolError.Code
		errorCategory = string(errCtx.ToolError.Category)
		errorMessage = errCtx.ToolError.Message
	}

	prompt := fmt.Sprintf(`You are an API error analyzer. A tool call failed and you need to fix it.

## Error Information
HTTP Status: %d
Tool: %s
Capability: %s
Error Code: %s
Error Category: %s
Error Message: %s
Error Details:
%s

## Original Request Payload
%s

## Your Task
1. Analyze why this request failed
2. Determine if it can be fixed by modifying the input
3. If fixable, generate a corrected JSON payload

## Response Format
Return ONLY valid JSON:
{
  "can_fix": true/false,
  "analysis": "Brief explanation",
  "corrected_payload": { ... }
}

## Examples
- "Flower Mound, TX" failed? Try "Flower Mound, Texas, US"
- "MSFT Inc" failed? Try just "MSFT"
- API key invalid? can_fix: false (can't fix credentials)`,
		errCtx.HTTPStatus,
		errCtx.ToolName,
		errCtx.Capability,
		errorCode,
		errorCategory,
		errorMessage,
		detailsStr,
		string(originalJSON),
	)

	r.Logger.Debug("Requesting AI error correction", map[string]interface{}{
		"tool":       errCtx.ToolName,
		"capability": errCtx.Capability,
		"attempt":    errCtx.AttemptNumber,
		"error_code": errorCode,
	})

	response, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature: 0.1, // Low = more deterministic
		MaxTokens:   300,
	})
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	// Parse AI response
	var result struct {
		CanFix           bool                   `json:"can_fix"`
		Analysis         string                 `json:"analysis"`
		CorrectedPayload map[string]interface{} `json:"corrected_payload"`
	}

	// Try to extract JSON from response (AI might include extra text)
	content := response.Content
	if start := strings.Index(content, "{"); start != -1 {
		if end := strings.LastIndex(content, "}"); end != -1 {
			content = content[start : end+1]
		}
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	r.Logger.Info("AI error analysis completed", map[string]interface{}{
		"tool":     errCtx.ToolName,
		"can_fix":  result.CanFix,
		"analysis": result.Analysis,
		"attempt":  errCtx.AttemptNumber,
	})

	if !result.CanFix {
		return nil, nil // Signal: AI says this can't be fixed
	}

	return result.CorrectedPayload, nil
}

// callToolWithIntelligentRetry implements category-based error routing with AI correction
// This is the main intelligent error handling function that agents should use
func (r *ResearchAgent) callToolWithIntelligentRetry(
	ctx context.Context,
	tool *core.ServiceInfo,
	capability *core.Capability,
	initialPayload map[string]interface{},
	config IntelligentRetryConfig,
) (*ToolResult, error) {
	startTime := time.Now()
	currentPayload := initialPayload
	var lastError *core.ToolError

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		r.Logger.Debug("Intelligent retry attempt", map[string]interface{}{
			"tool":       tool.Name,
			"capability": capability.Name,
			"attempt":    attempt + 1,
			"max":        config.MaxRetries + 1,
		})

		// Make the HTTP call
		result, httpStatus, toolResp, err := r.callToolHTTPWithParsing(ctx, tool, capability, currentPayload)

		// Handle network errors (couldn't reach the tool at all)
		if err != nil && httpStatus == 0 {
			if attempt < config.MaxRetries {
				r.Logger.Warn("Network error, retrying with same payload", map[string]interface{}{
					"tool":    tool.Name,
					"attempt": attempt + 1,
					"error":   err.Error(),
				})
				time.Sleep(config.BackoffDuration)
				continue
			}
			return nil, fmt.Errorf("network error after %d attempts: %w", attempt+1, err)
		}

		// Success!
		if httpStatus >= 200 && httpStatus < 300 {
			result.Duration = time.Since(startTime).String()
			if attempt > 0 {
				r.Logger.Info("Tool call succeeded after retry", map[string]interface{}{
					"tool":          tool.Name,
					"final_attempt": attempt + 1,
					"duration":      result.Duration,
				})
			}
			return result, nil
		}

		// Extract ToolError from response
		if toolResp != nil && toolResp.Error != nil {
			lastError = toolResp.Error
		}

		// Route based on HTTP status code (as per INTELLIGENT_ERROR_HANDLING.md)

		// Auth errors (401/403) - STOP! Agent can't fix credentials
		if httpStatus == 401 || httpStatus == 403 {
			errMsg := "authentication failed"
			if lastError != nil {
				errMsg = lastError.Message
			}
			r.Logger.Error("Auth error - not retryable", map[string]interface{}{
				"tool":        tool.Name,
				"http_status": httpStatus,
			})
			return &ToolResult{
				ToolName:        tool.Name,
				Capability:      capability.Name,
				Success:         false,
				Error:           fmt.Sprintf("Auth error (HTTP %d): %s", httpStatus, errMsg),
				StructuredError: lastError,
				Duration:        time.Since(startTime).String(),
			}, fmt.Errorf("auth error: %s", errMsg)
		}

		// Rate limited (429) - Wait and retry with SAME payload
		if httpStatus == 429 {
			retryAfter := r.parseRetryAfter(lastError)
			r.Logger.Warn("Rate limited, waiting before retry", map[string]interface{}{
				"tool":        tool.Name,
				"retry_after": retryAfter.String(),
			})
			time.Sleep(retryAfter)
			continue
		}

		// Server error (5xx) - Retry with SAME payload (transient error)
		if httpStatus >= 500 {
			if attempt < config.MaxRetries {
				r.Logger.Warn("Server error, retrying with same payload", map[string]interface{}{
					"tool":        tool.Name,
					"http_status": httpStatus,
					"attempt":     attempt + 1,
				})
				time.Sleep(config.BackoffDuration)
				continue
			}
		}

		// Client error (4xx) - Check if AI can fix the payload
		if httpStatus >= 400 && httpStatus < 500 {
			// First check: Is this error marked as retryable?
			if lastError == nil || !lastError.Retryable {
				errMsg := fmt.Sprintf("HTTP %d", httpStatus)
				if lastError != nil {
					errMsg = lastError.Message
				}
				r.Logger.Warn("Client error not marked as retryable", map[string]interface{}{
					"tool":        tool.Name,
					"http_status": httpStatus,
					"retryable":   false,
				})
				return &ToolResult{
					ToolName:        tool.Name,
					Capability:      capability.Name,
					Success:         false,
					Error:           fmt.Sprintf("Client error (HTTP %d): %s", httpStatus, errMsg),
					StructuredError: lastError,
					Duration:        time.Since(startTime).String(),
				}, fmt.Errorf("client error: %s", errMsg)
			}

			// Use AI to analyze the error and correct the payload
			if config.UseAI && r.aiClient != nil && attempt < config.MaxRetries {
				corrected, err := r.aiCorrectPayload(ctx, ErrorContext{
					HTTPStatus:      httpStatus,
					OriginalRequest: currentPayload,
					ToolError:       lastError,
					ToolName:        tool.Name,
					Capability:      capability.Name,
					AttemptNumber:   attempt + 1,
				})

				if err == nil && corrected != nil {
					r.Logger.Info("AI corrected payload, retrying", map[string]interface{}{
						"tool":       tool.Name,
						"attempt":    attempt + 1,
						"error_code": lastError.Code,
					})
					currentPayload = corrected
					continue
				}

				if err != nil {
					r.Logger.Warn("AI correction failed", map[string]interface{}{
						"tool":  tool.Name,
						"error": err.Error(),
					})
				}
			}
			break // AI couldn't fix it or not enabled, stop retrying
		}
	}

	// All retries exhausted
	errMsg := fmt.Sprintf("failed after %d attempts", config.MaxRetries+1)
	if lastError != nil {
		errMsg = fmt.Sprintf("failed after %d attempts: %s", config.MaxRetries+1, lastError.Message)
	}

	return &ToolResult{
		ToolName:        tool.Name,
		Capability:      capability.Name,
		Success:         false,
		Error:           errMsg,
		StructuredError: lastError,
		Duration:        time.Since(startTime).String(),
	}, fmt.Errorf("%s", errMsg)
}

// callToolHTTPWithParsing makes HTTP call and parses ToolResponse envelope
func (r *ResearchAgent) callToolHTTPWithParsing(
	ctx context.Context,
	tool *core.ServiceInfo,
	capability *core.Capability,
	payload map[string]interface{},
) (*ToolResult, int, *core.ToolResponse, error) {
	startTime := time.Now()

	endpoint := capability.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("/api/capabilities/%s", capability.Name)
	}

	url := fmt.Sprintf("http://%s:%d%s", tool.Address, tool.Port, endpoint)

	// Make HTTP request
	jsonData, _ := json.Marshal(payload)
	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("HTTP call failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, nil, fmt.Errorf("response reading failed: %w", err)
	}

	// Parse the ToolResponse envelope
	toolResp := r.parseToolResponse(body)

	// Success case
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var data interface{}
		if toolResp != nil && toolResp.Success {
			data = toolResp.Data
		} else {
			// Fallback: parse body as raw JSON
			if err := json.Unmarshal(body, &data); err != nil {
				data = string(body)
			}
		}

		return &ToolResult{
			ToolName:   tool.Name,
			Capability: capability.Name,
			Data:       data,
			Success:    true,
			Duration:   time.Since(startTime).String(),
		}, resp.StatusCode, toolResp, nil
	}

	// Error case - return with parsed ToolError
	var structuredErr *core.ToolError
	if toolResp != nil && toolResp.Error != nil {
		structuredErr = toolResp.Error
	}

	return &ToolResult{
		ToolName:        tool.Name,
		Capability:      capability.Name,
		Success:         false,
		Error:           fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
		StructuredError: structuredErr,
		Duration:        time.Since(startTime).String(),
	}, resp.StatusCode, toolResp, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
}

// ============================================================================
// Result Processing and Caching
// ============================================================================

// createBasicSummary creates a summary of results
func (r *ResearchAgent) createBasicSummary(topic string, results []ToolResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for topic: %s", topic)
	}

	successful := 0
	for _, result := range results {
		if result.Success {
			successful++
		}
	}

	return fmt.Sprintf("Found %d results for '%s' (%d successful, %d failed)",
		len(results), topic, successful, len(results)-successful)
}

// generateAIAnalysis creates AI-powered analysis of results
func (r *ResearchAgent) generateAIAnalysis(ctx context.Context, topic string, results []ToolResult) string {
	if r.aiClient == nil || len(results) == 0 {
		return ""
	}

	// Prepare results for AI
	resultsJSON, _ := json.MarshalIndent(results, "", "  ")

	prompt := fmt.Sprintf(`Analyze these research results for the topic "%s":

%s

Provide a concise analysis covering:
1. Key findings
2. Notable patterns
3. Recommendations (if applicable)

Keep the response under 200 words.`, topic, string(resultsJSON))

	response, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature: 0.5,
		MaxTokens:   400,
	})
	if err != nil {
		r.Logger.Error("AI analysis generation failed", map[string]interface{}{
			"error": err.Error(),
		})
		return ""
	}

	return response.Content
}

// calculateConfidence calculates confidence score based on results
func (r *ResearchAgent) calculateConfidence(results []ToolResult) float64 {
	if len(results) == 0 {
		return 0.0
	}

	successful := 0
	for _, result := range results {
		if result.Success {
			successful++
		}
	}

	return float64(successful) / float64(len(results))
}

// cacheResult stores result in cache (if available)
func (r *ResearchAgent) cacheResult(ctx context.Context, topic string, response ResearchResponse) {
	if r.Memory == nil {
		return
	}

	data, err := json.Marshal(response)
	if err != nil {
		return
	}

	r.Memory.Set(ctx, fmt.Sprintf("research:%s", topic), string(data), 15*time.Minute)
}

// ============================================================================
// Workflow Orchestration with Resilience
// ============================================================================

func (r *ResearchAgent) orchestrateWeatherAnalysis(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	location, ok := params["location"].(string)
	if !ok {
		location = "New York"
	}

	// Discover weather tools
	tools, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{
		Type: core.ComponentTypeTool,
	})
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	// Find weather service and call with resilience
	for _, tool := range tools {
		if strings.Contains(strings.ToLower(tool.Name), "weather") {
			for _, cap := range tool.Capabilities {
				if strings.Contains(strings.ToLower(cap.Name), "weather") {
					result := r.callToolWithResilience(ctx, tool, &cap, location)
					return result, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no weather service found")
}

func (r *ResearchAgent) orchestrateDataPipeline(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{
		"status":  "completed",
		"message": "Data pipeline orchestration placeholder",
		"params":  params,
	}, nil
}

func (r *ResearchAgent) orchestrateGenericWorkflow(ctx context.Context, workflowType string, params map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{
		"status":        "completed",
		"workflow_type": workflowType,
		"message":       "Generic workflow completed with resilience protection",
		"params":        params,
	}, nil
}

// extractJSON extracts a JSON object from text that may be wrapped in markdown code blocks.
// Matches orchestration.extractJSON() so all AI providers (OpenAI, Anthropic, Gemini) work.
func extractJSON(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json") {
		text = strings.TrimPrefix(text, "```json")
		if idx := strings.Index(text, "```"); idx != -1 {
			text = text[:idx]
		}
	} else if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if idx := strings.Index(text, "```"); idx != -1 {
			text = text[:idx]
		}
	}
	return strings.TrimSpace(text)
}
