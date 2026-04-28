package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// getMapKeys returns the keys of a map for debugging purposes
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// handleResearchTopic demonstrates intelligent orchestration with comprehensive telemetry
func (r *ResearchAgent) handleResearchTopic(rw http.ResponseWriter, req *http.Request) {
	startTime := time.Now()
	ctx := core.ExtractRequestContext(req.Context(), req)

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", req.Method),
		attribute.String("path", req.URL.Path),
	)

	// Track overall operation duration with deferred call
	// This ensures the metric is emitted even if the function returns early
	var requestStatus = "success" // Will be set to "error" on failure paths
	defer func() {
		durationMs := float64(time.Since(startTime).Milliseconds())
		// Unified metric (enables cross-module dashboards)
		telemetry.RecordRequest(telemetry.ModuleAgent, "research", durationMs, requestStatus)
	}()

	r.Logger.InfoWithContext(ctx, "Starting research topic orchestration", map[string]interface{}{
		"method": req.Method,
		"path":   req.URL.Path,
	})

	var request ResearchRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		r.Logger.ErrorWithContext(ctx, "Failed to decode research request", map[string]interface{}{
			"error": err.Error(),
		})
		requestStatus = "error"
		telemetry.RecordRequestError(telemetry.ModuleAgent, "research", "validation")
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Step 1: Discover available tools
	tools, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{
		Type: core.ComponentTypeTool, // Only look for tools
	})
	if err != nil {
		r.Logger.ErrorWithContext(ctx, "Failed to discover tools", map[string]interface{}{
			"error": err.Error(),
		})
		requestStatus = "error"
		telemetry.RecordRequestError(telemetry.ModuleAgent, "research", "discovery")
		http.Error(rw, "Service discovery failed", http.StatusServiceUnavailable)
		return
	}

	// NEW: Track discovery metrics
	telemetry.Gauge("agent.tools.discovered", float64(len(tools)))

	// Add span event for tools discovered
	telemetry.AddSpanEvent(ctx, "tools_discovered",
		attribute.Int("tool_count", len(tools)),
	)

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

	r.Logger.InfoWithContext(ctx, "Discovered tools for research", map[string]interface{}{
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
		r.Logger.InfoWithContext(ctx, "Multi-entity comparison query detected", map[string]interface{}{
			"topic":        request.Topic,
			"entity_count": len(entities),
			"entities":     entities,
			"strategy":     "parallel tool calls",
		})

		// Use AI to select the most relevant tool AND capability in ONE call
		selections := r.selectToolsAndCapabilities(ctx, request.Topic, tools)
		if len(selections) > 0 {
			selection := selections[0]

			r.Logger.InfoWithContext(ctx, "AI selected tool+capability for multi-entity comparison (1 call)", map[string]interface{}{
				"tool":           selection.Tool.Name,
				"capability":     selection.Capability.Name,
				"entity_count":   len(entities),
				"selection_type": "AI-powered (combined)",
			})

			// NEW: Track tool selection
			telemetry.Counter("agent.research.tools_called",
				"tool_name", selection.Tool.Name)

			// Execute parallel tool calls
			entityResults := r.callToolForEntities(ctx, selection.Tool, selection.Capability, request.Topic, entities)
			if len(entityResults) > 0 {
				results = append(results, entityResults...)
				toolsUsed = append(toolsUsed, selection.Tool.Name)

				r.Logger.InfoWithContext(ctx, "Multi-entity comparison completed", map[string]interface{}{
					"entities_requested": len(entities),
					"results_received":   len(entityResults),
					"tool":               selection.Tool.Name,
				})
			}
		} else {
			r.Logger.WarnWithContext(ctx, "No relevant tools found for multi-entity comparison", map[string]interface{}{
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

			r.Logger.InfoWithContext(ctx, "Calling AI-selected tool+capability (1 call)", map[string]interface{}{
				"tool":       selection.Tool.Name,
				"capability": selection.Capability.Name,
				"topic":      request.Topic,
			})

			// NEW: Track tool selection
			telemetry.Counter("agent.research.tools_called",
				"tool_name", selection.Tool.Name)

			// Call the tool with pre-selected capability (no second AI call needed)
			result := r.callToolWithCapability(ctx, selection.Tool, selection.Capability, request.Topic)
			if result != nil {
				r.Logger.InfoWithContext(ctx, "Tool call completed", map[string]interface{}{
					"tool":       result.ToolName,
					"capability": result.Capability,
					"success":    result.Success,
					"topic":      request.Topic,
				})
				results = append(results, *result)
				toolsUsed = append(toolsUsed, result.ToolName)
			}
		} else {
			r.Logger.WarnWithContext(ctx, "No relevant tools found for topic", map[string]interface{}{
				"topic":           request.Topic,
				"available_tools": len(tools),
			})
		}
	}

	// Step 3: Use AI to synthesize results
	summary := r.createBasicSummary(request.Topic, results)
	var aiAnalysis string

	if request.AISynthesis && r.aiClient != nil {
		aiStart := time.Now()

		aiAnalysis = r.generateAIAnalysis(ctx, request.Topic, results)
		aiDurationMs := float64(time.Since(aiStart).Milliseconds())
		aiStatus := "success"
		if aiAnalysis == "" {
			aiStatus = "error"
		}

		// Unified metrics (enables cross-module AI dashboards)
		telemetry.RecordAIRequest(telemetry.ModuleAgent, "openai", aiDurationMs, aiStatus)

		if aiAnalysis != "" {
			summary = aiAnalysis // Use AI analysis as the summary
			r.Logger.InfoWithContext(ctx, "AI analysis completed", map[string]interface{}{
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

	// Add completion span event
	telemetry.AddSpanEvent(ctx, "research_completed",
		attribute.String("topic", request.Topic),
		attribute.Int("tools_used", len(toolsUsed)),
		attribute.Int("results_count", len(results)),
		attribute.Float64("confidence", response.Confidence),
	)

	r.Logger.InfoWithContext(ctx, "Research topic completed", map[string]interface{}{
		"topic":           request.Topic,
		"tools_used":      len(toolsUsed),
		"processing_time": time.Since(startTime).String(),
	})
}

// handleDiscoverTools shows available tools and their capabilities
func (r *ResearchAgent) handleDiscoverTools(rw http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	r.Logger.InfoWithContext(ctx, "Discovering components", map[string]interface{}{
		"path": req.URL.Path,
	})

	// Discover all components
	allComponents, err := r.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		r.Logger.ErrorWithContext(ctx, "Discovery failed", map[string]interface{}{
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

	r.Logger.InfoWithContext(ctx, "Discovery completed", map[string]interface{}{
		"total_components": len(allComponents),
		"tools":            len(tools),
		"agents":           len(agents),
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleAnalyzeData demonstrates AI-powered data analysis
func (r *ResearchAgent) handleAnalyzeData(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)

	if r.aiClient == nil {
		r.Logger.ErrorWithContext(ctx, "AI analysis requested but AI client not available", nil)
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	r.Logger.InfoWithContext(ctx, "Starting AI data analysis", map[string]interface{}{
		"path": req.URL.Path,
	})

	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		r.Logger.ErrorWithContext(ctx, "Failed to decode analysis request", map[string]interface{}{
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
		r.Logger.ErrorWithContext(ctx, "Missing data field in request", map[string]interface{}{
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
	aiResponse, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature: 0.5,
		MaxTokens:   5000,
	})
	if err != nil {
		// ORCH-008 Fix 1: Preserve original HTTP status for provider errors
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			r.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
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
		r.Logger.ErrorWithContext(ctx, "AI analysis failed", map[string]interface{}{
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

	r.Logger.InfoWithContext(ctx, "AI analysis completed", map[string]interface{}{
		"model":       aiResponse.Model,
		"tokens_used": aiResponse.Usage.TotalTokens,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}

// handleOrchestateWorkflow demonstrates complex workflow orchestration
func (r *ResearchAgent) handleOrchestateWorkflow(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)

	var workflowReq struct {
		WorkflowType string                 `json:"workflow_type"`
		Parameters   map[string]interface{} `json:"parameters"`
		Steps        []string               `json:"steps,omitempty"`
	}

	if err := json.NewDecoder(req.Body).Decode(&workflowReq); err != nil {
		r.Logger.ErrorWithContext(ctx, "Failed to decode workflow request", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	workflowID := fmt.Sprintf("workflow-%d", time.Now().Unix())

	r.Logger.InfoWithContext(ctx, "Starting workflow orchestration", map[string]interface{}{
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
		r.Logger.ErrorWithContext(ctx, "Workflow orchestration failed", map[string]interface{}{
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

	r.Logger.InfoWithContext(ctx, "Workflow orchestration completed", map[string]interface{}{
		"workflow_id": workflowID,
	})
}

// handleHealth implements health check endpoint with telemetry status
func (r *ResearchAgent) handleHealth(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
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
			r.Logger.ErrorWithContext(ctx, "Health check: Redis unavailable", map[string]interface{}{
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

	// NEW: Check telemetry health
	telemetryHealth := telemetry.GetHealth()
	health["telemetry"] = map[string]interface{}{
		"initialized":     telemetryHealth.Initialized,
		"metrics_emitted": telemetryHealth.MetricsEmitted,
		"circuit_state":   telemetryHealth.CircuitState,
	}
	if telemetryHealth.LastError != "" {
		health["telemetry"].(map[string]interface{})["last_error"] = telemetryHealth.LastError
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

	r.Logger.DebugWithContext(ctx, "Health check completed", map[string]interface{}{
		"status":   health["status"],
		"duration": time.Since(startTime).String(),
	})
}

// ============================================================================
// AI Analysis Capabilities
// ============================================================================

// generateAnalysisRequestID generates a unique request ID for analysis operations
func generateAnalysisRequestID() string {
	return fmt.Sprintf("analysis-%d", time.Now().UnixNano())
}

// handleFinancialAnalysis performs deep financial analysis using AI
func (r *ResearchAgent) handleFinancialAnalysis(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)
	startTime := time.Now()

	// Pattern 3: Get request_id from context baggage (propagated from orchestrator)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = generateAnalysisRequestID()
	}

	// Pattern 2 & 1: Log with operation field and nil check
	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Starting financial analysis", map[string]interface{}{
			"operation":  "financial_analysis",
			"request_id": requestID,
		})
	}

	// Pattern 6: Add span event with request_id as first attribute
	telemetry.AddSpanEvent(ctx, "analysis.financial.start",
		attribute.String("request_id", requestID),
		attribute.String("analysis_type", "financial"),
	)

	// Track success/failure for metrics
	status := "success"
	defer func() {
		duration := float64(time.Since(startTime).Milliseconds())
		// Pattern 5: Counter with module label
		telemetry.Counter("agent.analysis.requests",
			"type", "financial",
			"status", status,
			"module", telemetry.ModuleAgent,
		)
		telemetry.Histogram("agent.analysis.duration_ms", duration,
			"type", "financial",
		)
	}()

	if r.aiClient == nil {
		status = "error"
		err := fmt.Errorf("AI client not available")
		// Pattern 4: Record error on span
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI client unavailable", map[string]interface{}{
				"operation":  "financial_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	// Parse request (support both wrapped and direct formats)
	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Invalid request format", map[string]interface{}{
				"operation":  "financial_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Extract parameters - support both wrapped and direct formats
	data, analysisType, analysisContext, timeframe := extractFinancialParams(requestData)

	if data == "" {
		status = "error"
		err := fmt.Errorf("missing required 'financial_metrics' field")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Missing financial_metrics/data field", map[string]interface{}{
				"operation":  "financial_analysis",
				"request_id": requestID,
			})
		}
		http.Error(rw, "Missing required 'financial_metrics' field (or 'data' for backward compatibility)", http.StatusBadRequest)
		return
	}

	// Pattern 6: Add span event for AI request
	telemetry.AddSpanEvent(ctx, "analysis.financial.ai_request",
		attribute.String("request_id", requestID),
		attribute.String("analysis_type", analysisType),
		attribute.String("timeframe", timeframe),
	)

	// Build prompt with system instructions
	prompt := buildFinancialAnalysisPrompt(data, analysisType, analysisContext, timeframe)

	// Call AI
	aiResponse, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature:  0.5,
		MaxTokens:    5000,
		SystemPrompt: financialAnalysisSystemPrompt,
	})
	if err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		// ORCH-008 Fix 1: Preserve original HTTP status for provider errors
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			telemetry.AddSpanEvent(ctx, "agent.provider_error",
				attribute.String("request_id", requestID),
				attribute.Int("status_code", pe.StatusCode()),
				attribute.String("provider", pe.Provider()),
				attribute.String("model", pe.Model()),
				attribute.Bool("is_transient", pe.IsTransient()),
			)
			telemetry.Counter("agent.provider_error.total",
				"type", "financial",
				"module", telemetry.ModuleAgent,
			)
			if r.Logger != nil {
				r.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
					"operation":    "financial_analysis",
					"request_id":  requestID,
					"error":        pe.Error(),
					"error_type":   "provider_client_error",
					"status_code":  pe.StatusCode(),
					"provider":     pe.Provider(),
					"model":        pe.Model(),
					"is_transient": pe.IsTransient(),
					"duration_ms":  time.Since(startTime).Milliseconds(),
				})
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(pe.StatusCode())
			json.NewEncoder(rw).Encode(map[string]string{
				"error":    pe.Error(),
				"source":   "llm_provider",
				"provider": pe.Provider(),
			})
			return
		}
		telemetry.Counter("agent.analysis.ai_errors",
			"type", "financial",
			"module", telemetry.ModuleAgent,
		)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI analysis failed", map[string]interface{}{
				"operation":  "financial_analysis",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": fmt.Sprintf("%T", err),
			})
		}
		http.Error(rw, "AI analysis failed", http.StatusInternalServerError)
		return
	}

	// Pattern 6: Add span event for AI response
	telemetry.AddSpanEvent(ctx, "analysis.financial.ai_response",
		attribute.String("request_id", requestID),
		attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
		attribute.String("model", aiResponse.Model),
	)

	// Parse structured response from AI
	var analysis map[string]interface{}
	if err := json.Unmarshal([]byte(aiResponse.Content), &analysis); err != nil {
		analysis = map[string]interface{}{
			"analysis":   aiResponse.Content,
			"structured": false,
		}
		if r.Logger != nil {
			r.Logger.WarnWithContext(ctx, "AI returned non-JSON response", map[string]interface{}{
				"operation":  "financial_analysis",
				"request_id": requestID,
			})
		}
	}

	// Add metadata
	analysis["model"] = aiResponse.Model
	analysis["tokens_used"] = aiResponse.Usage.TotalTokens
	analysis["timestamp"] = time.Now().Format(time.RFC3339)
	analysis["request_id"] = requestID

	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Financial analysis completed", map[string]interface{}{
			"operation":   "financial_analysis",
			"request_id":  requestID,
			"tokens_used": aiResponse.Usage.TotalTokens,
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(analysis)
}

// handleSentimentAnalysis performs AI-powered sentiment analysis
func (r *ResearchAgent) handleSentimentAnalysis(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)
	startTime := time.Now()

	// Pattern 3: Get request_id from context baggage
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = generateAnalysisRequestID()
	}

	// Pattern 2 & 1: Log with operation field and nil check
	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Starting sentiment analysis", map[string]interface{}{
			"operation":  "sentiment_analysis",
			"request_id": requestID,
		})
	}

	// Pattern 6: Add span event with request_id as first attribute
	telemetry.AddSpanEvent(ctx, "analysis.sentiment.start",
		attribute.String("request_id", requestID),
		attribute.String("analysis_type", "sentiment"),
	)

	status := "success"
	defer func() {
		duration := float64(time.Since(startTime).Milliseconds())
		telemetry.Counter("agent.analysis.requests",
			"type", "sentiment",
			"status", status,
			"module", telemetry.ModuleAgent,
		)
		telemetry.Histogram("agent.analysis.duration_ms", duration, "type", "sentiment")
	}()

	if r.aiClient == nil {
		status = "error"
		err := fmt.Errorf("AI client not available")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI client unavailable", map[string]interface{}{
				"operation":  "sentiment_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Invalid request format", map[string]interface{}{
				"operation":  "sentiment_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Extract parameters
	content, contentType, aspects := extractSentimentParams(requestData)

	if content == "" {
		status = "error"
		err := fmt.Errorf("missing required 'news_data' field")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Missing news_data/content field", map[string]interface{}{
				"operation":  "sentiment_analysis",
				"request_id": requestID,
			})
		}
		http.Error(rw, "Missing required 'news_data' field (or 'content' for backward compatibility)", http.StatusBadRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "analysis.sentiment.ai_request",
		attribute.String("request_id", requestID),
		attribute.String("content_type", contentType),
		attribute.Int("content_length", len(content)),
	)

	prompt := buildSentimentAnalysisPrompt(content, contentType, aspects)

	aiResponse, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature:  0.5,
		MaxTokens:    5000,
		SystemPrompt: sentimentAnalysisSystemPrompt,
	})
	if err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		// ORCH-008 Fix 1: Preserve original HTTP status for provider errors
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			telemetry.AddSpanEvent(ctx, "agent.provider_error",
				attribute.String("request_id", requestID),
				attribute.Int("status_code", pe.StatusCode()),
				attribute.String("provider", pe.Provider()),
				attribute.String("model", pe.Model()),
				attribute.Bool("is_transient", pe.IsTransient()),
			)
			telemetry.Counter("agent.provider_error.total",
				"type", "sentiment",
				"module", telemetry.ModuleAgent,
			)
			if r.Logger != nil {
				r.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
					"operation":    "sentiment_analysis",
					"request_id":  requestID,
					"error":        pe.Error(),
					"error_type":   "provider_client_error",
					"status_code":  pe.StatusCode(),
					"provider":     pe.Provider(),
					"model":        pe.Model(),
					"is_transient": pe.IsTransient(),
					"duration_ms":  time.Since(startTime).Milliseconds(),
				})
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(pe.StatusCode())
			json.NewEncoder(rw).Encode(map[string]string{
				"error":    pe.Error(),
				"source":   "llm_provider",
				"provider": pe.Provider(),
			})
			return
		}
		telemetry.Counter("agent.analysis.ai_errors",
			"type", "sentiment",
			"module", telemetry.ModuleAgent,
		)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI analysis failed", map[string]interface{}{
				"operation":  "sentiment_analysis",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": fmt.Sprintf("%T", err),
			})
		}
		http.Error(rw, "AI analysis failed", http.StatusInternalServerError)
		return
	}

	telemetry.AddSpanEvent(ctx, "analysis.sentiment.ai_response",
		attribute.String("request_id", requestID),
		attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
		attribute.String("model", aiResponse.Model),
	)

	var analysis map[string]interface{}
	if err := json.Unmarshal([]byte(aiResponse.Content), &analysis); err != nil {
		analysis = map[string]interface{}{
			"analysis":   aiResponse.Content,
			"structured": false,
		}
		if r.Logger != nil {
			r.Logger.WarnWithContext(ctx, "AI returned non-JSON response", map[string]interface{}{
				"operation":  "sentiment_analysis",
				"request_id": requestID,
			})
		}
	}

	analysis["model"] = aiResponse.Model
	analysis["tokens_used"] = aiResponse.Usage.TotalTokens
	analysis["timestamp"] = time.Now().Format(time.RFC3339)
	analysis["request_id"] = requestID

	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Sentiment analysis completed", map[string]interface{}{
			"operation":   "sentiment_analysis",
			"request_id":  requestID,
			"tokens_used": aiResponse.Usage.TotalTokens,
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(analysis)
}

// handleComparativeAnalysis performs multi-factor comparison using AI
func (r *ResearchAgent) handleComparativeAnalysis(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)
	startTime := time.Now()

	// Pattern 3: Get request_id from context baggage
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = generateAnalysisRequestID()
	}

	// Pattern 2 & 1: Log with operation field and nil check
	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Starting comparative analysis", map[string]interface{}{
			"operation":  "comparative_analysis",
			"request_id": requestID,
		})
	}

	// Pattern 6: Add span event with request_id as first attribute
	telemetry.AddSpanEvent(ctx, "analysis.comparative.start",
		attribute.String("request_id", requestID),
		attribute.String("analysis_type", "comparative"),
	)

	status := "success"
	defer func() {
		duration := float64(time.Since(startTime).Milliseconds())
		telemetry.Counter("agent.analysis.requests",
			"type", "comparative",
			"status", status,
			"module", telemetry.ModuleAgent,
		)
		telemetry.Histogram("agent.analysis.duration_ms", duration, "type", "comparative")
	}()

	if r.aiClient == nil {
		status = "error"
		err := fmt.Errorf("AI client not available")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI client unavailable", map[string]interface{}{
				"operation":  "comparative_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Invalid request format", map[string]interface{}{
				"operation":  "comparative_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Extract parameters
	entities, criteria, compContext, priorities := extractComparativeParams(requestData)

	if len(entities) < 2 {
		status = "error"
		err := fmt.Errorf("at least 2 entities required for comparison")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Insufficient entities for comparison", map[string]interface{}{
				"operation":    "comparative_analysis",
				"request_id":   requestID,
				"entity_count": len(entities),
				"min_required": 2,
			})
		}
		http.Error(rw, "At least 2 entities required for comparison", http.StatusBadRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "analysis.comparative.ai_request",
		attribute.String("request_id", requestID),
		attribute.Int("entity_count", len(entities)),
		attribute.Int("criteria_count", len(criteria)),
	)

	prompt := buildComparativeAnalysisPrompt(entities, criteria, compContext, priorities)

	aiResponse, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature:  0.5,
		MaxTokens:    5000,
		SystemPrompt: comparativeAnalysisSystemPrompt,
	})
	if err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		// ORCH-008 Fix 1: Preserve original HTTP status for provider errors
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			telemetry.AddSpanEvent(ctx, "agent.provider_error",
				attribute.String("request_id", requestID),
				attribute.Int("status_code", pe.StatusCode()),
				attribute.String("provider", pe.Provider()),
				attribute.String("model", pe.Model()),
				attribute.Bool("is_transient", pe.IsTransient()),
			)
			telemetry.Counter("agent.provider_error.total",
				"type", "comparative",
				"module", telemetry.ModuleAgent,
			)
			if r.Logger != nil {
				r.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
					"operation":    "comparative_analysis",
					"request_id":  requestID,
					"error":        pe.Error(),
					"error_type":   "provider_client_error",
					"status_code":  pe.StatusCode(),
					"provider":     pe.Provider(),
					"model":        pe.Model(),
					"is_transient": pe.IsTransient(),
					"duration_ms":  time.Since(startTime).Milliseconds(),
				})
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(pe.StatusCode())
			json.NewEncoder(rw).Encode(map[string]string{
				"error":    pe.Error(),
				"source":   "llm_provider",
				"provider": pe.Provider(),
			})
			return
		}
		telemetry.Counter("agent.analysis.ai_errors",
			"type", "comparative",
			"module", telemetry.ModuleAgent,
		)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI analysis failed", map[string]interface{}{
				"operation":  "comparative_analysis",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": fmt.Sprintf("%T", err),
			})
		}
		http.Error(rw, "AI analysis failed", http.StatusInternalServerError)
		return
	}

	telemetry.AddSpanEvent(ctx, "analysis.comparative.ai_response",
		attribute.String("request_id", requestID),
		attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
		attribute.String("model", aiResponse.Model),
	)

	var analysis map[string]interface{}
	if err := json.Unmarshal([]byte(aiResponse.Content), &analysis); err != nil {
		analysis = map[string]interface{}{
			"analysis":   aiResponse.Content,
			"structured": false,
		}
		if r.Logger != nil {
			r.Logger.WarnWithContext(ctx, "AI returned non-JSON response", map[string]interface{}{
				"operation":  "comparative_analysis",
				"request_id": requestID,
			})
		}
	}

	analysis["model"] = aiResponse.Model
	analysis["tokens_used"] = aiResponse.Usage.TotalTokens
	analysis["timestamp"] = time.Now().Format(time.RFC3339)
	analysis["request_id"] = requestID

	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Comparative analysis completed", map[string]interface{}{
			"operation":    "comparative_analysis",
			"request_id":   requestID,
			"tokens_used":  aiResponse.Usage.TotalTokens,
			"entity_count": len(entities),
			"duration_ms":  time.Since(startTime).Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(analysis)
}

// ============================================================================
// Analysis Helper Functions
// ============================================================================

// extractFinancialParams extracts parameters for financial analysis.
// Accepts "financial_metrics" (preferred) or "data" (backward compat).
func extractFinancialParams(requestData map[string]interface{}) (data, analysisType, context, timeframe string) {
	// Support both wrapped (from orchestrator) and direct formats
	params := requestData
	if wrapped, ok := requestData["data"].(map[string]interface{}); ok {
		params = wrapped
	}

	// Try "financial_metrics" first (new canonical name), fall back to "data" (backward compat)
	rawData := params["financial_metrics"]
	if rawData == nil {
		rawData = params["data"]
	}

	// Extract data field (can be string, object, or array)
	switch d := rawData.(type) {
	case string:
		data = d
	case map[string]interface{}:
		if b, err := json.Marshal(d); err == nil {
			data = string(b)
		}
	case []interface{}:
		if b, err := json.Marshal(d); err == nil {
			data = string(b)
		}
	}

	// Extract other fields
	if at, ok := params["analysis_type"].(string); ok {
		analysisType = at
	} else {
		analysisType = "general"
	}

	if c, ok := params["context"].(string); ok {
		context = c
	}

	if tf, ok := params["timeframe"].(string); ok {
		timeframe = tf
	}

	return
}

// extractSentimentParams extracts parameters for sentiment analysis.
// Accepts "news_data" (preferred, array) or "content" (backward compat).
// When content is structured (object/array), it is serialized to JSON text for the prompt.
func extractSentimentParams(requestData map[string]interface{}) (content, contentType string, aspects []string) {
	params := requestData
	if wrapped, ok := requestData["data"].(map[string]interface{}); ok {
		params = wrapped
	}

	// Try "news_data" first (new canonical name), fall back to "content" (backward compat)
	rawContent := params["news_data"]
	if rawContent == nil {
		rawContent = params["content"]
	}

	switch c := rawContent.(type) {
	case string:
		content = c
	case []interface{}:
		if b, err := json.MarshalIndent(c, "", "  "); err == nil {
			content = string(b)
		}
	case map[string]interface{}:
		if b, err := json.MarshalIndent(c, "", "  "); err == nil {
			content = string(b)
		}
	}

	if ct, ok := params["content_type"].(string); ok {
		contentType = ct
	} else {
		contentType = "general"
	}

	if a, ok := params["aspects"].([]interface{}); ok {
		for _, aspect := range a {
			if s, ok := aspect.(string); ok {
				aspects = append(aspects, s)
			}
		}
	}

	return
}

// extractComparativeParams extracts parameters for comparative analysis
func extractComparativeParams(requestData map[string]interface{}) (entities []map[string]interface{}, criteria []string, context string, priorities map[string]float64) {
	params := requestData
	if wrapped, ok := requestData["data"].(map[string]interface{}); ok {
		params = wrapped
	}

	// Extract entities array
	if e, ok := params["entities"].([]interface{}); ok {
		for _, entity := range e {
			if m, ok := entity.(map[string]interface{}); ok {
				entities = append(entities, m)
			}
		}
	}

	// Extract criteria array
	if c, ok := params["comparison_criteria"].([]interface{}); ok {
		for _, criterion := range c {
			if s, ok := criterion.(string); ok {
				criteria = append(criteria, s)
			}
		}
	}

	// Extract context
	if c, ok := params["context"].(string); ok {
		context = c
	}

	// Extract priorities
	priorities = make(map[string]float64)
	if p, ok := params["priorities"].(map[string]interface{}); ok {
		for k, v := range p {
			if f, ok := v.(float64); ok {
				priorities[k] = f
			}
		}
	}

	return
}

// buildFinancialAnalysisPrompt builds the prompt for financial analysis
func buildFinancialAnalysisPrompt(data, analysisType, context, timeframe string) string {
	prompt := fmt.Sprintf(`Analyze the following financial data:

DATA:
%s

ANALYSIS TYPE: %s`, data, analysisType)

	if context != "" {
		prompt += fmt.Sprintf("\n\nCONTEXT: %s", context)
	}
	if timeframe != "" {
		prompt += fmt.Sprintf("\n\nTIMEFRAME: %s", timeframe)
	}

	prompt += `

Provide your analysis as valid JSON matching the expected response structure with key_findings, metrics, risk_factors, and recommendation.`

	return prompt
}

// buildSentimentAnalysisPrompt builds the prompt for sentiment analysis
func buildSentimentAnalysisPrompt(content, contentType string, aspects []string) string {
	prompt := fmt.Sprintf(`Analyze the sentiment of the following %s content:

CONTENT:
%s`, contentType, content)

	if len(aspects) > 0 {
		prompt += fmt.Sprintf("\n\nFocus on these specific aspects: %v", aspects)
	}

	prompt += `

Provide your analysis as valid JSON with overall_sentiment, sentiment_score, confidence, emotional_tone, key_themes, and supporting_quotes.`

	return prompt
}

// buildComparativeAnalysisPrompt builds the prompt for comparative analysis
func buildComparativeAnalysisPrompt(entities []map[string]interface{}, criteria []string, context string, priorities map[string]float64) string {
	entitiesJSON, _ := json.MarshalIndent(entities, "", "  ")

	prompt := fmt.Sprintf(`Compare the following entities:

ENTITIES:
%s`, string(entitiesJSON))

	if len(criteria) > 0 {
		prompt += fmt.Sprintf("\n\nCOMPARISON CRITERIA: %v", criteria)
	}

	if context != "" {
		prompt += fmt.Sprintf("\n\nCONTEXT: %s", context)
	}

	if len(priorities) > 0 {
		prioritiesJSON, _ := json.Marshal(priorities)
		prompt += fmt.Sprintf("\n\nCRITERIA WEIGHTS: %s", string(prioritiesJSON))
	}

	prompt += `

Provide your analysis as valid JSON with summary, comparison_matrix, rankings, trade_offs, and recommendation.`

	return prompt
}

// handleMathAnalysis performs AI-powered mathematical analysis
func (r *ResearchAgent) handleMathAnalysis(rw http.ResponseWriter, req *http.Request) {
	ctx := core.ExtractRequestContext(req.Context(), req)
	startTime := time.Now()

	// Pattern 3: Get request_id from context baggage
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = generateAnalysisRequestID()
	}

	// Pattern 2 & 1: Log with operation field and nil check
	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Starting math analysis", map[string]interface{}{
			"operation":  "math_analysis",
			"request_id": requestID,
		})
	}

	// Pattern 6: Add span event with request_id as first attribute
	telemetry.AddSpanEvent(ctx, "analysis.math.start",
		attribute.String("request_id", requestID),
		attribute.String("analysis_type", "math"),
	)

	status := "success"
	defer func() {
		duration := float64(time.Since(startTime).Milliseconds())
		telemetry.Counter("agent.analysis.requests",
			"type", "math",
			"status", status,
			"module", telemetry.ModuleAgent,
		)
		telemetry.Histogram("agent.analysis.duration_ms", duration, "type", "math")
	}()

	if r.aiClient == nil {
		status = "error"
		err := fmt.Errorf("AI client not available")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI client unavailable", map[string]interface{}{
				"operation":  "math_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "AI client not available", http.StatusServiceUnavailable)
		return
	}

	var requestData map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Invalid request format", map[string]interface{}{
				"operation":  "math_analysis",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		http.Error(rw, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Extract parameters
	problem, problemType, mathContext, showSteps := extractMathParams(requestData)

	if problem == "" {
		status = "error"
		err := fmt.Errorf("missing required 'problem' field")
		telemetry.RecordSpanError(ctx, err)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "Missing problem field", map[string]interface{}{
				"operation":  "math_analysis",
				"request_id": requestID,
			})
		}
		http.Error(rw, "Missing required 'problem' field", http.StatusBadRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "analysis.math.ai_request",
		attribute.String("request_id", requestID),
		attribute.String("problem_type", problemType),
		attribute.Bool("show_steps", showSteps),
	)

	prompt := buildMathAnalysisPrompt(problem, problemType, mathContext, showSteps)

	aiResponse, err := r.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
		Temperature:  0.5,
		MaxTokens:    5000,
		SystemPrompt: mathAnalysisSystemPrompt,
	})
	if err != nil {
		status = "error"
		telemetry.RecordSpanError(ctx, err)
		// ORCH-008 Fix 1: Preserve original HTTP status for provider errors
		var pe core.ProviderError
		if errors.As(err, &pe) && pe.StatusCode() >= 400 && pe.StatusCode() < 500 {
			telemetry.AddSpanEvent(ctx, "agent.provider_error",
				attribute.String("request_id", requestID),
				attribute.Int("status_code", pe.StatusCode()),
				attribute.String("provider", pe.Provider()),
				attribute.String("model", pe.Model()),
				attribute.Bool("is_transient", pe.IsTransient()),
			)
			telemetry.Counter("agent.provider_error.total",
				"type", "math",
				"module", telemetry.ModuleAgent,
			)
			if r.Logger != nil {
				r.Logger.WarnWithContext(ctx, "LLM provider returned client error", map[string]interface{}{
					"operation":    "math_analysis",
					"request_id":  requestID,
					"error":        pe.Error(),
					"error_type":   "provider_client_error",
					"status_code":  pe.StatusCode(),
					"provider":     pe.Provider(),
					"model":        pe.Model(),
					"is_transient": pe.IsTransient(),
					"duration_ms":  time.Since(startTime).Milliseconds(),
				})
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(pe.StatusCode())
			json.NewEncoder(rw).Encode(map[string]string{
				"error":    pe.Error(),
				"source":   "llm_provider",
				"provider": pe.Provider(),
			})
			return
		}
		telemetry.Counter("agent.analysis.ai_errors",
			"type", "math",
			"module", telemetry.ModuleAgent,
		)
		if r.Logger != nil {
			r.Logger.ErrorWithContext(ctx, "AI analysis failed", map[string]interface{}{
				"operation":  "math_analysis",
				"request_id": requestID,
				"error":      err.Error(),
				"error_type": fmt.Sprintf("%T", err),
			})
		}
		http.Error(rw, "AI analysis failed", http.StatusInternalServerError)
		return
	}

	telemetry.AddSpanEvent(ctx, "analysis.math.ai_response",
		attribute.String("request_id", requestID),
		attribute.Int("prompt_tokens", aiResponse.Usage.PromptTokens),
		attribute.Int("completion_tokens", aiResponse.Usage.CompletionTokens),
		attribute.Int("total_tokens", aiResponse.Usage.TotalTokens),
		attribute.String("model", aiResponse.Model),
	)

	var analysis map[string]interface{}
	if err := json.Unmarshal([]byte(aiResponse.Content), &analysis); err != nil {
		analysis = map[string]interface{}{
			"analysis":   aiResponse.Content,
			"structured": false,
		}
		if r.Logger != nil {
			r.Logger.WarnWithContext(ctx, "AI returned non-JSON response", map[string]interface{}{
				"operation":  "math_analysis",
				"request_id": requestID,
			})
		}
	}

	analysis["model"] = aiResponse.Model
	analysis["tokens_used"] = aiResponse.Usage.TotalTokens
	analysis["timestamp"] = time.Now().Format(time.RFC3339)
	analysis["request_id"] = requestID

	if r.Logger != nil {
		r.Logger.InfoWithContext(ctx, "Math analysis completed", map[string]interface{}{
			"operation":    "math_analysis",
			"request_id":   requestID,
			"tokens_used":  aiResponse.Usage.TotalTokens,
			"problem_type": problemType,
			"duration_ms":  time.Since(startTime).Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(analysis)
}

// extractMathParams extracts parameters for math analysis
func extractMathParams(requestData map[string]interface{}) (problem, problemType, context string, showSteps bool) {
	params := requestData
	if wrapped, ok := requestData["data"].(map[string]interface{}); ok {
		params = wrapped
	}

	// Extract problem (can be string or object)
	if p, ok := params["problem"].(string); ok {
		problem = p
	} else if p, ok := params["problem"].(map[string]interface{}); ok {
		if b, err := json.Marshal(p); err == nil {
			problem = string(b)
		}
	}

	// Extract problem type
	if pt, ok := params["problem_type"].(string); ok {
		problemType = pt
	} else {
		problemType = "general"
	}

	// Extract context
	if c, ok := params["context"].(string); ok {
		context = c
	}

	// Extract show_steps (default true)
	showSteps = true
	if ss, ok := params["show_steps"].(bool); ok {
		showSteps = ss
	}

	return
}

// buildMathAnalysisPrompt builds the prompt for math analysis
func buildMathAnalysisPrompt(problem, problemType, context string, showSteps bool) string {
	prompt := fmt.Sprintf(`Solve and analyze the following mathematical problem:

PROBLEM:
%s

PROBLEM TYPE: %s`, problem, problemType)

	if context != "" {
		prompt += fmt.Sprintf("\n\nCONTEXT: %s", context)
	}

	if showSteps {
		prompt += "\n\nPlease show all steps in your solution with clear explanations."
	} else {
		prompt += "\n\nProvide the solution directly without detailed steps."
	}

	prompt += `

Provide your analysis as valid JSON with problem_type, summary, solution (with answer and numeric_value if applicable), steps, verification, assumptions, and confidence.`

	return prompt
}
