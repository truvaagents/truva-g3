package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// handleNaturalOrchestration processes natural language requests using AI orchestration
func (t *TravelResearchAgent) handleNaturalOrchestration(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "natural_orchestration"),
	)

	// Use context-aware logging for trace-log correlation
	t.Logger.InfoWithContext(ctx, "Processing natural language orchestration request", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	// Parse request
	var req OrchestrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"error": err.Error(),
		})
		telemetry.RecordSpanError(ctx, err) // Mark span as failed in Jaeger
		telemetry.Counter("orchestration.natural_requests", "status", "error")
		telemetry.RecordRequestError(telemetry.ModuleOrchestration, "natural_request", "validation")
		writeError(w, http.StatusBadRequest, "Invalid request format", err)
		return
	}

	if req.Request == "" {
		validationErr := fmt.Errorf("request field is required")
		telemetry.RecordSpanError(ctx, validationErr)
		telemetry.Counter("orchestration.natural_requests", "status", "error")
		telemetry.RecordRequestError(telemetry.ModuleOrchestration, "natural_request", "validation")
		writeError(w, http.StatusBadRequest, "Request field is required", nil)
		return
	}

	// Ensure orchestrator is initialized
	if t.orchestrator == nil {
		initErr := fmt.Errorf("orchestrator not initialized")
		telemetry.RecordSpanError(ctx, initErr)
		telemetry.Counter("orchestration.natural_requests", "status", "error")
		telemetry.RecordRequestError(telemetry.ModuleOrchestration, "natural_request", "not_initialized")
		writeError(w, http.StatusServiceUnavailable, "Orchestrator not initialized", nil)
		return
	}

	// Add span event before orchestration
	telemetry.AddSpanEvent(ctx, "orchestration_started",
		attribute.String("request", req.Request),
		attribute.Bool("ai_synthesis", true),
	)

	// Process through AI orchestrator
	result, err := t.orchestrator.ProcessRequest(ctx, req.Request, req.Metadata)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Orchestration failed", map[string]interface{}{
			"error":    err.Error(),
			"duration": time.Since(startTime).String(),
		})
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("orchestration.natural_requests", "status", "error")
		telemetry.RecordRequestError(telemetry.ModuleOrchestration, "natural_request", "orchestration_failure")
		writeError(w, http.StatusInternalServerError, "Orchestration failed", err)
		return
	}

	// Build response
	response := &OrchestrationResponse{
		RequestID:     result.RequestID,
		Request:       req.Request,
		Response:      result.Response,
		ToolsUsed:     result.AgentsInvolved,
		ExecutionTime: time.Since(startTime).String(),
		Confidence:    result.Confidence,
		Metadata:      result.Metadata,
	}

	// Track successful natural language request
	duration := time.Since(startTime)
	durationMs := float64(duration.Milliseconds())

	// Legacy metrics (for backwards compatibility)
	telemetry.Counter("orchestration.natural_requests", "status", "success")
	telemetry.Histogram("orchestration.workflow.duration_ms", durationMs,
		"workflow", "natural",
		"status", "success",
	)

	// Unified metrics (enables cross-module dashboards)
	telemetry.RecordRequest(telemetry.ModuleOrchestration, "natural_request", durationMs, "success")

	// Add completion span event
	telemetry.AddSpanEvent(ctx, "orchestration_completed",
		attribute.String("request_id", result.RequestID),
		attribute.Int("tools_used", len(result.AgentsInvolved)),
		attribute.Float64("confidence", result.Confidence),
		attribute.Float64("duration_ms", durationMs),
	)

	t.Logger.InfoWithContext(ctx, "Natural orchestration completed", map[string]interface{}{
		"request_id": result.RequestID,
		"tools_used": len(result.AgentsInvolved),
		"duration":   duration.String(),
	})

	writeJSON(w, http.StatusOK, response)
}

// handleWorkflowExecution executes a predefined travel workflow
func (t *TravelResearchAgent) handleWorkflowExecution(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "workflow_execution"),
	)

	// Read body once so we can parse it twice
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusBadRequest, "Failed to read request body", err)
		return
	}

	t.Logger.InfoWithContext(ctx, "Processing workflow execution request", map[string]interface{}{
		"method":         r.Method,
		"path":           r.URL.Path,
		"content_length": len(bodyBytes),
	})

	// Log full request body at DEBUG level for troubleshooting
	t.Logger.DebugWithContext(ctx, "Request payload", map[string]interface{}{
		"body": string(bodyBytes),
	})

	// Parse into OrchestrationRequest
	var req OrchestrationRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"error": err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusBadRequest, "Invalid request format", err)
		return
	}

	// If Parameters is empty, extract top-level fields as parameters
	// This handles the case where clients send destination, country, etc. at top level
	// instead of inside a "parameters" object
	if req.Parameters == nil || len(req.Parameters) == 0 {
		var rawBody map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &rawBody); err == nil {
			req.Parameters = make(map[string]interface{})
			// Known OrchestrationRequest fields to skip
			knownFields := map[string]bool{
				"request": true, "workflow_name": true, "parameters": true,
				"ai_synthesis": true, "metadata": true,
			}
			for key, value := range rawBody {
				if !knownFields[key] {
					req.Parameters[key] = value
				}
			}
			t.Logger.DebugWithContext(ctx, "Extracted top-level parameters", map[string]interface{}{
				"parameters": req.Parameters,
			})
		}
	}

	// Determine workflow to execute
	workflowName := req.WorkflowName
	if workflowName == "" {
		workflowName = "travel-research"
	}

	t.workflowMutex.RLock()
	workflow, exists := t.workflows[workflowName]
	t.workflowMutex.RUnlock()

	if !exists {
		notFoundErr := fmt.Errorf("workflow '%s' not found", workflowName)
		telemetry.RecordSpanError(ctx, notFoundErr)
		writeError(w, http.StatusNotFound, fmt.Sprintf("Workflow '%s' not found", workflowName), nil)
		return
	}

	t.Logger.InfoWithContext(ctx, "Executing predefined workflow", map[string]interface{}{
		"workflow":   workflowName,
		"step_count": len(workflow.Steps),
		"parameters": req.Parameters,
	})

	// Add span event for workflow start
	telemetry.AddSpanEvent(ctx, "workflow_started",
		attribute.String("workflow", workflowName),
		attribute.Int("step_count", len(workflow.Steps)),
		attribute.Bool("ai_synthesis", req.AISynthesis),
	)

	// Convert workflow to routing plan
	plan := t.workflowToRoutingPlan(workflow, req.Parameters)

	// Generate a consistent request ID for both paths
	// This ensures the same format whether ai_synthesis is true or false
	requestID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixNano()%1000000000)

	// Execute through orchestrator
	if t.orchestrator == nil {
		initErr := fmt.Errorf("orchestrator not initialized")
		telemetry.RecordSpanError(ctx, initErr)
		writeError(w, http.StatusServiceUnavailable, "Orchestrator not initialized", nil)
		return
	}

	// Use ExecutePlanWithSynthesis when AI synthesis is requested
	// This provides: synthesis via orchestrator, DAG visualization storage, request_id propagation
	var response *OrchestrationResponse
	var execSuccess bool

	if t.AI != nil && req.AISynthesis {
		// ExecutePlanWithSynthesis handles synthesis and stores execution for DAG visualization
		originalRequest := fmt.Sprintf("Execute workflow: %s", workflowName)
		orchResponse, err := t.orchestrator.ExecutePlanWithSynthesis(ctx, plan, originalRequest)
		if err != nil {
			t.Logger.ErrorWithContext(ctx, "Workflow execution with synthesis failed", map[string]interface{}{
				"workflow": workflowName,
				"error":    err.Error(),
			})
			telemetry.RecordSpanError(ctx, err)
			writeError(w, http.StatusInternalServerError, "Workflow execution failed", err)
			return
		}

		// Build step summaries from orchestrator response
		stepResults := make([]StepResultSummary, 0, len(orchResponse.Steps))
		for _, step := range orchResponse.Steps {
			stepResults = append(stepResults, StepResultSummary{
				StepID:   step.StepID,
				ToolName: step.AgentName,
				Success:  step.Success,
				Duration: step.Duration.String(),
				Error:    step.Error,
			})
		}

		// Check if all steps succeeded
		execSuccess = true
		for _, step := range orchResponse.Steps {
			if !step.Success {
				execSuccess = false
				break
			}
		}

		response = &OrchestrationResponse{
			RequestID:     requestID,
			Request:       originalRequest,
			Response:      orchResponse.Response,
			ToolsUsed:     orchResponse.AgentsInvolved,
			WorkflowUsed:  workflowName,
			ExecutionTime: orchResponse.ExecutionTime.String(),
			StepResults:   stepResults,
			Confidence:    orchResponse.Confidence,
			Metadata:      req.Metadata,
		}
	} else {
		// Use ExecutePlan for non-AI synthesis path (raw results only)
		result, err := t.orchestrator.ExecutePlan(ctx, plan)
		if err != nil {
			t.Logger.ErrorWithContext(ctx, "Workflow execution failed", map[string]interface{}{
				"workflow": workflowName,
				"error":    err.Error(),
			})
			telemetry.RecordSpanError(ctx, err)
			writeError(w, http.StatusInternalServerError, "Workflow execution failed", err)
			return
		}

		// Build step summaries
		stepResults := make([]StepResultSummary, 0, len(result.Steps))
		for _, step := range result.Steps {
			stepResults = append(stepResults, StepResultSummary{
				StepID:   step.StepID,
				ToolName: step.AgentName,
				Success:  step.Success,
				Duration: step.Duration.String(),
				Error:    step.Error,
			})
		}

		// Build tools used list
		toolsUsed := make([]string, 0)
		for _, step := range result.Steps {
			toolsUsed = append(toolsUsed, step.AgentName)
		}

		execSuccess = result.Success
		response = &OrchestrationResponse{
			RequestID:     requestID,
			Request:       fmt.Sprintf("Execute workflow: %s", workflowName),
			Response:      t.formatRawResults(result),
			ToolsUsed:     toolsUsed,
			WorkflowUsed:  workflowName,
			ExecutionTime: time.Since(startTime).String(),
			StepResults:   stepResults,
			Confidence:    t.calculateConfidence(result),
			Metadata:      req.Metadata,
		}
	}

	// Track workflow execution metrics
	trackWorkflowExecution(workflowName, time.Since(startTime), execSuccess)

	// Add workflow completion span event
	telemetry.AddSpanEvent(ctx, "workflow_completed",
		attribute.String("workflow", workflowName),
		attribute.String("request_id", response.RequestID),
		attribute.Bool("success", execSuccess),
		attribute.Int("steps_executed", len(response.StepResults)),
		attribute.Int("tools_used", len(response.ToolsUsed)),
		attribute.Float64("confidence", response.Confidence),
	)

	t.Logger.InfoWithContext(ctx, "Workflow execution completed", map[string]interface{}{
		"workflow":      workflowName,
		"success":       execSuccess,
		"steps":         len(response.StepResults),
		"duration":      time.Since(startTime).String(),
		"request_id":    response.RequestID,
		"response_size": len(response.Response),
	})

	// Log full response at DEBUG level for troubleshooting
	t.Logger.DebugWithContext(ctx, "Response payload", map[string]interface{}{
		"request_id": plan.PlanID,
		"response":   response,
	})

	writeJSON(w, http.StatusOK, response)
}

// handleCustomWorkflow executes a custom workflow defined in the request
func (t *TravelResearchAgent) handleCustomWorkflow(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "custom_workflow"),
	)

	t.Logger.InfoWithContext(ctx, "Processing custom workflow request", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	// Parse input
	var inputMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&inputMap); err != nil {
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusBadRequest, "Invalid request format", err)
		return
	}

	stepsData, ok := inputMap["steps"]
	if !ok {
		stepsErr := fmt.Errorf("steps are required")
		telemetry.RecordSpanError(ctx, stepsErr)
		writeError(w, http.StatusBadRequest, "Steps are required", nil)
		return
	}

	// Convert to JSON and back to get proper typing
	stepsJSON, err := json.Marshal(stepsData)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusBadRequest, "Invalid steps format", err)
		return
	}

	var steps []map[string]interface{}
	if err := json.Unmarshal(stepsJSON, &steps); err != nil {
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusBadRequest, "Invalid steps format", err)
		return
	}

	// Build routing plan from custom steps
	plan := &orchestration.RoutingPlan{
		PlanID:          fmt.Sprintf("custom-%d", time.Now().UnixNano()),
		OriginalRequest: "Custom workflow execution",
		Mode:            orchestration.ModeWorkflow,
		CreatedAt:       time.Now(),
	}

	for i, step := range steps {
		toolName, _ := step["tool"].(string)
		capability, _ := step["capability"].(string)
		params, _ := step["params"].(map[string]interface{})

		plan.Steps = append(plan.Steps, orchestration.RoutingStep{
			StepID:      fmt.Sprintf("step-%d", i+1),
			AgentName:   toolName,
			Instruction: capability,
			Metadata: map[string]interface{}{
				"capability": capability,
				"parameters": params,
			},
		})
	}

	// Execute the plan
	if t.orchestrator == nil {
		initErr := fmt.Errorf("orchestrator not initialized")
		telemetry.RecordSpanError(ctx, initErr)
		writeError(w, http.StatusServiceUnavailable, "Orchestrator not initialized", nil)
		return
	}

	result, err := t.orchestrator.ExecutePlan(ctx, plan)
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Custom workflow execution failed", map[string]interface{}{
			"error": err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusInternalServerError, "Custom workflow execution failed", err)
		return
	}

	response := map[string]interface{}{
		"plan_id":        plan.PlanID,
		"success":        result.Success,
		"steps":          result.Steps,
		"duration":       result.TotalDuration.String(),
		"execution_time": time.Since(startTime).String(),
	}

	// Track custom workflow execution metrics
	trackWorkflowExecution("custom", time.Since(startTime), result.Success)

	// Add custom workflow completion span event
	telemetry.AddSpanEvent(ctx, "custom_workflow_completed",
		attribute.String("plan_id", plan.PlanID),
		attribute.Bool("success", result.Success),
		attribute.Int("steps_executed", len(result.Steps)),
	)

	t.Logger.InfoWithContext(ctx, "Custom workflow completed", map[string]interface{}{
		"plan_id":  plan.PlanID,
		"success":  result.Success,
		"duration": time.Since(startTime).String(),
	})

	writeJSON(w, http.StatusOK, response)
}

// handleListWorkflows returns all available predefined workflows
func (t *TravelResearchAgent) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t.Logger.InfoWithContext(ctx, "Listing available workflows", map[string]interface{}{
		"path": r.URL.Path,
	})

	t.workflowMutex.RLock()
	defer t.workflowMutex.RUnlock()

	workflows := make([]map[string]interface{}, 0, len(t.workflows))
	for _, wf := range t.workflows {
		workflows = append(workflows, map[string]interface{}{
			"name":        wf.Name,
			"description": wf.Description,
			"step_count":  len(wf.Steps),
			"steps":       wf.Steps,
			"metadata":    wf.Metadata,
		})
	}

	response := map[string]interface{}{
		"workflows": workflows,
		"count":     len(workflows),
	}

	writeJSON(w, http.StatusOK, response)
}

// handleGetHistory returns recent execution history
func (t *TravelResearchAgent) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t.Logger.InfoWithContext(ctx, "Getting execution history", map[string]interface{}{
		"path": r.URL.Path,
	})

	if t.orchestrator == nil {
		response := map[string]interface{}{
			"history": []interface{}{},
			"count":   0,
			"message": "Orchestrator not initialized",
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	history := t.orchestrator.GetExecutionHistory()
	response := map[string]interface{}{
		"history": history,
		"count":   len(history),
	}

	writeJSON(w, http.StatusOK, response)
}

// handleHealth returns health status with orchestrator metrics
// Follows the same pattern as agent-with-telemetry:
// - "healthy" or "degraded" → 200 OK (functional, K8s probes pass)
// - "unhealthy" → 503 (actually broken)
func (t *TravelResearchAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
	}

	// Check Redis/Discovery connection (like agent-with-telemetry)
	if t.Discovery != nil {
		_, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
		if err != nil {
			health["status"] = "degraded"
			health["redis"] = "unavailable"
			// Use WithContext for trace correlation in handlers
			t.Logger.ErrorWithContext(ctx, "Health check: Redis unavailable", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			health["redis"] = "healthy"
		}
	}

	// Check orchestrator status
	if t.orchestrator != nil {
		metrics := t.orchestrator.GetMetrics()
		health["orchestrator"] = map[string]interface{}{
			"status":              "active",
			"total_requests":      metrics.TotalRequests,
			"successful_requests": metrics.SuccessfulRequests,
			"failed_requests":     metrics.FailedRequests,
			"average_latency_ms":  metrics.AverageLatency.Milliseconds(),
		}
	} else {
		health["orchestrator"] = "initializing"
	}

	// Check AI provider (like agent-with-telemetry)
	if t.AI != nil {
		health["ai_provider"] = "connected"
	} else {
		health["ai_provider"] = "not configured"
	}

	// Add workflow count
	t.workflowMutex.RLock()
	health["workflows_available"] = len(t.workflows)
	t.workflowMutex.RUnlock()

	// Set appropriate status code (same pattern as agent-with-telemetry)
	statusCode := http.StatusOK
	if health["status"] == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	} else if health["status"] == "degraded" {
		statusCode = http.StatusOK // Still return 200 for degraded but functional
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)
}

// handleDiscoverTools shows available tools and their capabilities
func (t *TravelResearchAgent) handleDiscoverTools(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "discover_tools"),
	)

	t.Logger.InfoWithContext(ctx, "Discovering components", map[string]interface{}{
		"path": r.URL.Path,
	})

	if t.Discovery == nil {
		discoveryErr := fmt.Errorf("service discovery not configured")
		telemetry.RecordSpanError(ctx, discoveryErr)
		writeError(w, http.StatusServiceUnavailable, "Service discovery not configured", nil)
		return
	}

	allComponents, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		t.Logger.ErrorWithContext(ctx, "Discovery failed", map[string]interface{}{
			"error": err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		writeError(w, http.StatusServiceUnavailable, "Discovery failed", err)
		return
	}

	tools := make([]*core.ServiceInfo, 0)
	agents := make([]*core.ServiceInfo, 0)

	for _, component := range allComponents {
		switch component.Type {
		case core.ComponentTypeTool:
			tools = append(tools, component)
		case core.ComponentTypeAgent:
			if component.ID != t.GetID() {
				agents = append(agents, component)
			}
		}
	}

	// Add tools discovered span event
	telemetry.AddSpanEvent(ctx, "tools_discovered",
		attribute.Int("total_components", len(allComponents)),
		attribute.Int("tools_count", len(tools)),
		attribute.Int("agents_count", len(agents)),
	)

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

	writeJSON(w, http.StatusOK, response)
}

// Helper functions

// writeJSON writes a JSON response with the given status code
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

// writeError writes an error response
func writeError(w http.ResponseWriter, statusCode int, message string, err error) {
	response := map[string]interface{}{
		"error":   message,
		"status":  statusCode,
		"success": false,
	}
	if err != nil {
		response["details"] = err.Error()
	}
	writeJSON(w, statusCode, response)
}
