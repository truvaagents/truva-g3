package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// getMapKeys returns the keys of a map for debugging purposes
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// handleResearchTopic demonstrates resilient tool orchestration with circuit breakers
func (r *ResearchAgent) handleResearchTopic(rw http.ResponseWriter, req *http.Request) {
	startTime := time.Now()
	ctx := core.ExtractRequestContext(req.Context(), req)

	r.Logger.Info("Starting resilient research topic orchestration", map[string]interface{}{
		"method": req.Method,
		"path":   req.URL.Path,
	})

	var request ResearchRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		r.Logger.Error("Failed to decode research request", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Step 1: Discover available tools
	if r.Discovery == nil {
		r.Logger.Error("Service discovery not available", nil)
		http.Error(rw, "Service discovery not configured", http.StatusServiceUnavailable)
		return
	}

	tools, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{
		Type: core.ComponentTypeTool,
	})
	if err != nil {
		r.Logger.Error("Failed to discover tools", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Service discovery failed", http.StatusServiceUnavailable)
		return
	}

	// Log discovered tools
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Name)
	}

	r.Logger.Info("Discovered tools for resilient research", map[string]interface{}{
		"tool_count":       len(tools),
		"tools_discovered": toolNames,
		"topic":            request.Topic,
	})

	// Step 2: Intelligent tool orchestration with resilience
	var results []ToolResult
	var toolsUsed []string
	var failedTools []string

	// Check for multi-entity comparison
	entities, err := r.extractEntitiesForComparison(ctx, request.Topic)
	if err == nil && len(entities) >= 2 {
		r.Logger.Info("Multi-entity comparison detected", map[string]interface{}{
			"entities": entities,
		})

		// Select tool + capability
		selections := r.selectToolsAndCapabilities(ctx, request.Topic, tools)
		if len(selections) > 0 {
			selection := selections[0]

			// Execute with resilience for each entity
			entityResults := r.callToolForEntities(ctx, selection.Tool, selection.Capability, request.Topic, entities)
			for _, result := range entityResults {
				if result.Success {
					results = append(results, result)
					if !contains(toolsUsed, result.ToolName) {
						toolsUsed = append(toolsUsed, result.ToolName)
					}
				} else {
					failedTools = append(failedTools, result.ToolName)
					r.Logger.Warn("Tool call failed, continuing with partial results", map[string]interface{}{
						"tool":  result.ToolName,
						"error": result.Error,
					})
				}
			}
		}
	} else {
		// Single-entity query with resilience
		selections := r.selectToolsAndCapabilities(ctx, request.Topic, tools)
		if len(selections) > 0 {
			selection := selections[0]

			r.Logger.Info("Calling tool with resilience protection", map[string]interface{}{
				"tool":       selection.Tool.Name,
				"capability": selection.Capability.Name,
			})

			// Use resilient tool call (circuit breaker + retry)
			result := r.callToolWithResilience(ctx, selection.Tool, selection.Capability, request.Topic)
			if result != nil {
				if result.Success {
					results = append(results, *result)
					toolsUsed = append(toolsUsed, result.ToolName)
				} else {
					failedTools = append(failedTools, result.ToolName)
					r.Logger.Warn("Tool call failed", map[string]interface{}{
						"tool":  result.ToolName,
						"error": result.Error,
					})
				}
			}
		} else {
			r.Logger.Warn("No relevant tools found", map[string]interface{}{
				"topic":           request.Topic,
				"available_tools": len(tools),
			})
		}
	}

	// Step 3: Synthesize results (even partial ones)
	summary := r.createBasicSummary(request.Topic, results)
	var aiAnalysis string

	if request.AISynthesis && r.aiClient != nil && len(results) > 0 {
		aiAnalysis = r.generateAIAnalysis(ctx, request.Topic, results)
		if aiAnalysis != "" {
			summary = aiAnalysis
		}
	}

	// Step 4: Build response with resilience metadata
	totalTools := len(results) + len(failedTools)
	successRate := 0.0
	if totalTools > 0 {
		successRate = float64(len(results)) / float64(totalTools)
	}

	response := ResearchResponse{
		Topic:          request.Topic,
		Summary:        summary,
		ToolsUsed:      toolsUsed,
		Results:        results,
		AIAnalysis:     aiAnalysis,
		Confidence:     r.calculateConfidence(results),
		ProcessingTime: time.Since(startTime).String(),
		WorkflowID:     request.WorkflowID,
		Metadata: map[string]interface{}{
			"tools_discovered":   len(tools),
			"tools_called":       totalTools,
			"tools_succeeded":    len(results),
			"ai_enabled":         r.aiClient != nil,
			"resilience_enabled": true,
		},
		// Resilience-specific fields
		Partial:     len(failedTools) > 0,
		FailedTools: failedTools,
		SuccessRate: successRate,
	}

	// Cache the result if successful
	if len(results) > 0 {
		r.cacheResult(ctx, request.Topic, response)
	}

	// Return appropriate status based on results
	rw.Header().Set("Content-Type", "application/json")

	if len(results) == 0 && len(failedTools) > 0 {
		// All tools failed
		rw.WriteHeader(http.StatusServiceUnavailable)
		response.Summary = "All tool calls failed. Service is degraded."
	} else if len(failedTools) > 0 {
		// Partial success - return 200 but indicate degraded state
		rw.WriteHeader(http.StatusOK)
	}

	json.NewEncoder(rw).Encode(response)

	r.Logger.Info("Resilient research completed", map[string]interface{}{
		"topic":        request.Topic,
		"tools_used":   len(toolsUsed),
		"tools_failed": len(failedTools),
		"success_rate": successRate,
		"duration":     time.Since(startTime).String(),
	})
}

// handleDiscoverTools shows available tools and their capabilities
func (r *ResearchAgent) handleDiscoverTools(rw http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	r.Logger.Info("Discovering components", map[string]interface{}{
		"path": req.URL.Path,
	})

	if r.Discovery == nil {
		r.Logger.Error("Service discovery not available", nil)
		http.Error(rw, "Service discovery not configured", http.StatusServiceUnavailable)
		return
	}

	allComponents, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		r.Logger.Error("Discovery failed", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, fmt.Sprintf("Discovery failed: %v", err), http.StatusServiceUnavailable)
		return
	}

	tools := make([]*core.ServiceInfo, 0)
	agents := make([]*core.ServiceInfo, 0)

	for _, component := range allComponents {
		switch component.Type {
		case core.ComponentTypeTool:
			tools = append(tools, component)
		case core.ComponentTypeAgent:
			if component.ID != r.GetID() {
				agents = append(agents, component)
			}
		}
	}

	// Include circuit breaker states for discovered tools
	cbStates := r.getCircuitBreakerStates()

	response := map[string]interface{}{
		"discovery_summary": map[string]interface{}{
			"total_components": len(allComponents),
			"tools":            len(tools),
			"agents":           len(agents),
			"discovery_time":   time.Now().Format(time.RFC3339),
		},
		"tools":            tools,
		"agents":           agents,
		"circuit_breakers": cbStates,
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleAnalyzeData demonstrates AI-powered data analysis
func (r *ResearchAgent) handleAnalyzeData(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)

	if r.aiClient == nil {
		r.Logger.Error("AI analysis requested but AI client not available", nil)
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Extract data to analyze
	// Support both formats:
	// 1. Agent-wrapped: {"data": {"data": "...", ...}} - from orchestrator calling agent
	// 2. Direct: {"data": "...", ...} - from direct API calls
	var data string

	// Check if this is agent-wrapped format (data field is an object containing the actual params)
	if wrappedData, ok := requestData["data"].(map[string]interface{}); ok {
		// Agent-wrapped format: extract from nested structure
		if d, ok := wrappedData["data"].(string); ok {
			data = d
		} else if d, ok := wrappedData["content"].(string); ok {
			data = d
		} else {
			// The wrapped data itself might be the content to analyze - serialize it
			if dataBytes, err := json.Marshal(wrappedData); err == nil {
				data = string(dataBytes)
			}
		}
	} else if d, ok := requestData["data"].(string); ok {
		// Direct format: data is a string at top level
		data = d
	} else if d, ok := requestData["content"].(string); ok {
		// Alternative field name for direct calls
		data = d
	}

	if data == "" {
		r.Logger.Error("Missing data field in request", map[string]interface{}{
			"received_keys": getMapKeys(requestData),
		})
		http.Error(rw, "Missing 'data' or 'content' field in request", http.StatusBadRequest)
		return
	}

	prompt := fmt.Sprintf(`Analyze the following data and provide insights:

%s

Please provide:
1. Key findings
2. Patterns or trends
3. Recommendations
4. Confidence level in your analysis`, data)

	aiResponse, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature: 0.3,
		MaxTokens:   1000,
	})
	if err != nil {
		// ORCH-008 Fix 1: Preserve original HTTP status for provider errors
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			r.Logger.Warn("LLM provider returned client error", map[string]interface{}{
				"operation":    "ai_analysis",
				"error":        pe.Error(),
				"error_type":   "provider_client_error",
				"status_code":  pe.StatusCode(),
				"provider":     pe.Provider(),
				"model":        pe.Model(),
				"is_transient": pe.IsTransient(),
			})
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(pe.StatusCode())
			json.NewEncoder(rw).Encode(map[string]string{
				"error":    pe.Error(),
				"source":   "llm_provider",
				"provider": pe.Provider(),
			})
			return
		}
		r.Logger.Error("AI analysis failed", map[string]interface{}{
			"error":      err.Error(),
			"error_type": fmt.Sprintf("%T", err),
		})
		http.Error(rw, "AI analysis failed", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"analysis":    aiResponse.Content,
		"model":       aiResponse.Model,
		"tokens_used": aiResponse.Usage.TotalTokens,
		"timestamp":   time.Now().Format(time.RFC3339),
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleOrchestateWorkflow demonstrates workflow orchestration with resilience
func (r *ResearchAgent) handleOrchestateWorkflow(rw http.ResponseWriter, req *http.Request) {
	var workflowReq struct {
		WorkflowType string                 `json:"workflow_type"`
		Parameters   map[string]interface{} `json:"parameters"`
		Steps        []string               `json:"steps,omitempty"`
	}

	if err := json.NewDecoder(req.Body).Decode(&workflowReq); err != nil {
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	ctx := req.Context()
	workflowID := fmt.Sprintf("workflow-%d", time.Now().Unix())

	r.Logger.Info("Starting resilient workflow orchestration", map[string]interface{}{
		"workflow_id":   workflowID,
		"workflow_type": workflowReq.WorkflowType,
	})

	var result interface{}
	var err error

	switch workflowReq.WorkflowType {
	case "weather_analysis":
		result, err = r.orchestrateWeatherAnalysis(ctx, workflowReq.Parameters)
	case "data_pipeline":
		result, err = r.orchestrateDataPipeline(ctx, workflowReq.Parameters)
	default:
		result, err = r.orchestrateGenericWorkflow(ctx, workflowReq.WorkflowType, workflowReq.Parameters)
	}

	if err != nil {
		r.Logger.Error("Workflow orchestration failed", map[string]interface{}{
			"workflow_id": workflowID,
			"error":       err.Error(),
		})
		http.Error(rw, fmt.Sprintf("Workflow failed: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"workflow_id":      workflowID,
		"workflow_type":    workflowReq.WorkflowType,
		"result":           result,
		"status":           "completed",
		"completed_at":     time.Now().Format(time.RFC3339),
		"circuit_breakers": r.getCircuitBreakerStates(),
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleHealth implements health check with circuit breaker states
func (r *ResearchAgent) handleHealth(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	startTime := time.Now()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"version":   "resilience-example-1.0",
	}

	// Check Redis connection
	if r.Discovery != nil {
		_, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{})
		if err != nil {
			health["status"] = "degraded"
			health["redis"] = "unavailable"
		} else {
			health["redis"] = "healthy"
		}
	}

	// Check AI provider
	if r.aiClient != nil {
		health["ai_provider"] = "connected"
	} else {
		health["ai_provider"] = "not configured"
	}

	// Include circuit breaker states - this is the key addition for resilience example
	cbStates := r.getCircuitBreakerStates()
	health["circuit_breakers"] = cbStates

	// Check if any circuit is open (degraded state)
	r.cbMutex.RLock()
	for name, cb := range r.circuitBreakers {
		if cb.GetState() == "open" {
			health["status"] = "degraded"
			health["degraded_reason"] = fmt.Sprintf("Circuit breaker '%s' is open", name)
			break
		}
	}
	r.cbMutex.RUnlock()

	// Add resilience summary
	health["resilience"] = map[string]interface{}{
		"enabled":          true,
		"circuit_breakers": len(cbStates),
		"retry_config": map[string]interface{}{
			"max_attempts":   r.retryConfig.MaxAttempts,
			"initial_delay":  r.retryConfig.InitialDelay.String(),
			"max_delay":      r.retryConfig.MaxDelay.String(),
			"backoff_factor": r.retryConfig.BackoffFactor,
			"jitter_enabled": r.retryConfig.JitterEnabled,
		},
	}

	// Set appropriate status code
	statusCode := http.StatusOK
	if health["status"] == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)

	r.Logger.Debug("Health check completed", map[string]interface{}{
		"status":   health["status"],
		"duration": time.Since(startTime).String(),
	})
}

// Helper function to check if slice contains string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
