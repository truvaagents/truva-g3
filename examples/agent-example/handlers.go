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

// handleResearchTopic demonstrates intelligent orchestration
func (r *ResearchAgent) handleResearchTopic(rw http.ResponseWriter, req *http.Request) {
	startTime := time.Now()
	ctx := core.ExtractRequestContext(req.Context(), req)

	r.Logger.Info("Starting research topic orchestration", map[string]interface{}{
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
	tools, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{
		Type: core.ComponentTypeTool, // Only look for tools
	})
	if err != nil {
		r.Logger.Error("Failed to discover tools", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Service discovery failed", http.StatusServiceUnavailable)
		return
	}

	// Log discovered tools with their names
	toolNames := make([]string, 0, len(tools))
	toolCapabilities := make(map[string][]string)
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Name)
		capNames := make([]string, 0, len(tool.Capabilities))
		for _, cap := range tool.Capabilities {
			capNames = append(capNames, cap.Name)
		}
		toolCapabilities[tool.Name] = capNames
	}

	r.Logger.Info("Discovered tools for research", map[string]interface{}{
		"tool_count":        len(tools),
		"tools_discovered":  toolNames,
		"tool_capabilities": toolCapabilities,
		"topic":             request.Topic,
	})

	// Step 2: Intelligent tool orchestration with AI-powered routing
	var results []ToolResult
	var toolsUsed []string

	// STRATEGY 1: Multi-Entity Comparison (Highest Priority)
	// Try to extract entities for comparison (e.g., "Compare Amazon vs Google")
	entities, err := r.extractEntitiesForComparison(ctx, request.Topic)
	if err == nil && len(entities) >= 2 {
		r.Logger.Info("Multi-entity comparison query detected", map[string]interface{}{
			"topic":        request.Topic,
			"entity_count": len(entities),
			"entities":     entities,
			"strategy":     "parallel tool calls",
		})

		// Use AI to select the most relevant tool AND capability in ONE call
		selections := r.selectToolsAndCapabilities(ctx, request.Topic, tools)
		if len(selections) > 0 {
			selection := selections[0]

			r.Logger.Info("AI selected tool+capability for multi-entity comparison (1 call)", map[string]interface{}{
				"tool":           selection.Tool.Name,
				"capability":     selection.Capability.Name,
				"entity_count":   len(entities),
				"selection_type": "AI-powered (combined)",
			})

			// Execute parallel tool calls with the FIX
			entityResults := r.callToolForEntities(ctx, selection.Tool, selection.Capability, request.Topic, entities)
			if len(entityResults) > 0 {
				results = append(results, entityResults...)
				toolsUsed = append(toolsUsed, selection.Tool.Name)

				r.Logger.Info("Multi-entity comparison completed", map[string]interface{}{
					"entities_requested": len(entities),
					"results_received":   len(entityResults),
					"tool":               selection.Tool.Name,
				})
			}
		} else {
			r.Logger.Warn("No relevant tools found for multi-entity comparison", map[string]interface{}{
				"topic":           request.Topic,
				"entities":        entities,
				"available_tools": len(tools),
			})
		}
	} else {
		// STRATEGY 2: Single-Entity Query
		// AI selects the most relevant tool AND capability in ONE call (50% cost savings)
		selections := r.selectToolsAndCapabilities(ctx, request.Topic, tools)

		if len(selections) > 0 {
			selection := selections[0]

			r.Logger.Info("Calling AI-selected tool+capability (1 call)", map[string]interface{}{
				"tool":       selection.Tool.Name,
				"capability": selection.Capability.Name,
				"topic":      request.Topic,
			})

			// Call the tool with pre-selected capability (no second AI call needed)
			result := r.callToolWithCapability(ctx, selection.Tool, selection.Capability, request.Topic)
			if result != nil {
				r.Logger.Info("Tool call completed", map[string]interface{}{
					"tool":       result.ToolName,
					"capability": result.Capability,
					"success":    result.Success,
					"topic":      request.Topic,
				})
				results = append(results, *result)
				toolsUsed = append(toolsUsed, result.ToolName)
			}
		} else {
			r.Logger.Warn("No relevant tools found for topic", map[string]interface{}{
				"topic":           request.Topic,
				"available_tools": len(tools),
			})
		}
	}

	// Step 3: Use AI to synthesize results
	summary := r.createBasicSummary(request.Topic, results)
	var aiAnalysis string

	if request.AISynthesis && r.aiClient != nil {
		aiAnalysis = r.generateAIAnalysis(ctx, request.Topic, results)
		if aiAnalysis != "" {
			summary = aiAnalysis // Use AI analysis as the summary
			r.Logger.Info("AI analysis completed", map[string]interface{}{
				"topic": request.Topic,
			})
		}
	}

	// Step 4: Build response
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
			"tools_discovered": len(tools),
			"tools_used":       len(toolsUsed),
			"ai_enabled":       r.aiClient != nil,
		},
	}

	// Cache the result
	r.cacheResult(ctx, request.Topic, response)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)

	r.Logger.Info("Research topic completed", map[string]interface{}{
		"topic":           request.Topic,
		"tools_used":      len(toolsUsed),
		"processing_time": time.Since(startTime).String(),
	})
}

// handleDiscoverTools shows available tools and their capabilities
func (r *ResearchAgent) handleDiscoverTools(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)

	r.Logger.Info("Discovering components", map[string]interface{}{
		"path": req.URL.Path,
	})

	// Discover all components
	allComponents, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		r.Logger.Error("Discovery failed", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, fmt.Sprintf("Discovery failed: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Organize by type
	tools := make([]*core.ServiceInfo, 0)
	agents := make([]*core.ServiceInfo, 0)

	for _, component := range allComponents {
		switch component.Type {
		case core.ComponentTypeTool:
			tools = append(tools, component)
		case core.ComponentTypeAgent:
			// Don't include ourselves in the list
			if component.ID != r.GetID() {
				agents = append(agents, component)
			}
		}
	}

	response := map[string]interface{}{
		"discovery_summary": map[string]interface{}{
			"total_components": len(allComponents),
			"tools":            len(tools),
			"agents":           len(agents),
			"discovery_time":   time.Now().Format(time.RFC3339),
		},
		"tools":  tools,
		"agents": agents,
	}

	r.Logger.Info("Discovery completed", map[string]interface{}{
		"total_components": len(allComponents),
		"tools":            len(tools),
		"agents":           len(agents),
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleAnalyzeData demonstrates AI-powered data analysis
func (r *ResearchAgent) handleAnalyzeData(rw http.ResponseWriter, req *http.Request) {
	if r.aiClient == nil {
		r.Logger.Error("AI analysis requested but AI client not available", nil)
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	r.Logger.Info("Starting AI data analysis", map[string]interface{}{
		"path": req.URL.Path,
	})

	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		r.Logger.Error("Failed to decode analysis request", map[string]interface{}{
			"error": err.Error(),
		})
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

	// Create analysis prompt
	prompt := fmt.Sprintf(`Analyze the following data and provide insights:

%s

Please provide:
1. Key findings
2. Patterns or trends
3. Recommendations
4. Confidence level in your analysis`, data)

	// Call AI service
	aiResponse, err := r.aiClient.GenerateResponse(req.Context(), prompt, &core.AIOptions{
		Temperature: 0.3, // Lower temperature for more analytical response
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

	r.Logger.Info("AI analysis completed", map[string]interface{}{
		"model":       aiResponse.Model,
		"tokens_used": aiResponse.Usage.TotalTokens,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleOrchestateWorkflow demonstrates complex workflow orchestration
func (r *ResearchAgent) handleOrchestateWorkflow(rw http.ResponseWriter, req *http.Request) {
	var workflowReq struct {
		WorkflowType string                 `json:"workflow_type"`
		Parameters   map[string]interface{} `json:"parameters"`
		Steps        []string               `json:"steps,omitempty"`
	}

	if err := json.NewDecoder(req.Body).Decode(&workflowReq); err != nil {
		r.Logger.Error("Failed to decode workflow request", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	ctx := core.ExtractRequestContext(req.Context(), req)
	workflowID := fmt.Sprintf("workflow-%d", time.Now().Unix())

	r.Logger.Info("Starting workflow orchestration", map[string]interface{}{
		"workflow_id":   workflowID,
		"workflow_type": workflowReq.WorkflowType,
	})

	// Execute based on workflow type
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
		"workflow_id":   workflowID,
		"workflow_type": workflowReq.WorkflowType,
		"result":        result,
		"status":        "completed",
		"completed_at":  time.Now().Format(time.RFC3339),
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)

	r.Logger.Info("Workflow orchestration completed", map[string]interface{}{
		"workflow_id": workflowID,
	})
}

// handleHealth implements health check endpoint
func (r *ResearchAgent) handleHealth(w http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)
	startTime := time.Now()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
	}

	// Check Redis connection
	if r.Discovery != nil {
		_, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{})
		if err != nil {
			health["status"] = "degraded"
			health["redis"] = "unavailable"
			r.Logger.Error("Health check: Redis unavailable", map[string]interface{}{
				"error": err.Error(),
			})
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

	// Set appropriate status code
	statusCode := http.StatusOK
	if health["status"] == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	} else if health["status"] == "degraded" {
		statusCode = http.StatusOK // Still return 200 for degraded but functional
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)

	r.Logger.Debug("Health check completed", map[string]interface{}{
		"status":   health["status"],
		"duration": time.Since(startTime).String(),
	})
}
