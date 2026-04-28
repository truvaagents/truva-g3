package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

// HandleQuery processes a natural language query using AI orchestration.
// This replaces the old hardcoded HandleTravelResearch workflow.
//
// The AI orchestrator dynamically decides which tools to call based on the query,
// executes them (in parallel where possible via DAG analysis), and synthesizes
// a final response.
//
// Progress is reported at multiple granularities:
//   - Planning phase: AI analyzing the request
//   - Per-tool progress: OnStepComplete callback reports each tool completion
//   - Synthesis phase: AI creating the final response
func (a *AsyncTravelAgent) HandleQuery(
	ctx context.Context,
	task *core.Task,
	reporter core.ProgressReporter,
) error {
	startTime := time.Now()
	planningStart := time.Now()

	// Parse query from input
	query, ok := task.Input["query"].(string)
	if !ok || query == "" {
		return fmt.Errorf("query field is required")
	}

	a.Logger.InfoWithContext(ctx, "Starting AI orchestration", map[string]interface{}{
		"task_id": task.ID,
		"query":   query,
	})

	// Report planning phase
	reporter.Report(&core.TaskProgress{
		CurrentStep: 1,
		TotalSteps:  3, // Planning, Execution, Synthesis (will update after plan is known)
		StepName:    "Planning",
		Percentage:  5,
		Message:     "AI is analyzing request and planning tool calls...",
	})

	// Check if orchestrator is available
	if a.orchestrator == nil {
		// Fallback: No orchestrator, return simple response
		a.Logger.WarnWithContext(ctx, "Orchestrator not available, using fallback", nil)
		task.Result = &QueryResult{
			Query:         query,
			Response:      "AI orchestration is not available. Please ensure an AI provider API key is configured.",
			ToolsUsed:     []string{},
			StepResults:   []StepResultSummary{},
			ExecutionTime: time.Since(startTime).String(),
			Confidence:    0.0,
			Metadata: map[string]interface{}{
				"mode":  "fallback",
				"error": "orchestrator_unavailable",
			},
		}
		return nil
	}

	// Track step results for the final response (includes planning + tools + synthesis)
	var stepResults []StepResultSummary
	var stepResultsMu sync.Mutex

	// Create orchestrator config with step callback for progress reporting
	config := orchestration.DefaultConfig()
	config.RoutingMode = orchestration.ModeAutonomous
	// Plan and synthesis token limits are read from env vars by DefaultConfig()
	// and stored in PlanAIOptions / SynthesisAIOptions.
	config.ExecutionOptions.TotalTimeout = 5 * time.Minute
	config.ExecutionOptions.StepTimeout = 2 * time.Minute
	config.EnableTelemetry = true

	// Track when first tool starts (to calculate planning duration)
	var firstToolStarted bool
	var toolExecutionStart time.Time

	// Set up step completion callback for per-tool progress
	config.ExecutionOptions.OnStepComplete = func(stepIndex, totalSteps int, step orchestration.RoutingStep, result orchestration.StepResult) {
		// On first tool callback, record planning duration
		if !firstToolStarted {
			firstToolStarted = true
			planningDuration := time.Since(planningStart)
			toolExecutionStart = time.Now()

			// Add planning step to results
			stepResultsMu.Lock()
			stepResults = append(stepResults, StepResultSummary{
				ToolName: "ai_planning",
				Success:  true,
				Duration: planningDuration.String(),
			})
			stepResultsMu.Unlock()
		}

		status := "completed"
		if !result.Success {
			status = "failed"
		}

		// Track tool step results (thread-safe)
		stepResultsMu.Lock()
		stepResults = append(stepResults, StepResultSummary{
			ToolName: step.AgentName,
			Success:  result.Success,
			Duration: result.Duration.String(),
		})
		stepResultsMu.Unlock()

		// Emit per-tool metrics
		telemetry.Counter("async_orchestration.tool_calls", "tool", step.AgentName, "status", status)

		// Report progress to task API
		// Progress: 10% (planning done) + 85% distributed across tools + 5% (synthesis)
		percentage := 10 + int(float64(stepIndex+1)/float64(totalSteps)*85)
		reporter.Report(&core.TaskProgress{
			CurrentStep: stepIndex + 2,  // +1 for planning step, +1 for 1-based indexing
			TotalSteps:  totalSteps + 2, // +2 for planning and synthesis steps
			StepName:    fmt.Sprintf("%s: %s", status, step.AgentName),
			Percentage:  float64(percentage),
			Message:     fmt.Sprintf("Tool %d/%d %s", stepIndex+1, totalSteps, status),
		})

		a.Logger.DebugWithContext(ctx, "Step completed", map[string]interface{}{
			"step_index":  stepIndex,
			"total_steps": totalSteps,
			"tool":        step.AgentName,
			"success":     result.Success,
			"duration":    result.Duration.String(),
		})
	}

	// Create a per-request orchestrator with the callback configured
	deps := orchestration.OrchestratorDependencies{
		Discovery: a.Discovery,
		AIClient:  a.AI,
		Logger:    a.Logger,
	}

	requestOrch, err := orchestration.CreateOrchestrator(config, deps)
	if err != nil {
		telemetry.Counter("async_orchestration.tasks", "status", "setup_failed")
		return fmt.Errorf("failed to create request orchestrator: %w", err)
	}

	// Start the orchestrator to initialize its catalog from discovery
	// This is required because each orchestrator has its own catalog instance
	if err := requestOrch.Start(ctx); err != nil {
		telemetry.Counter("async_orchestration.tasks", "status", "catalog_failed")
		return fmt.Errorf("failed to initialize request orchestrator catalog: %w", err)
	}

	// Execute orchestration (planning + tool execution happens here)
	synthesisStart := time.Now()
	response, err := requestOrch.ProcessRequest(ctx, query, map[string]interface{}{
		"task_id": task.ID,
		"mode":    "async",
	})
	if err != nil {
		telemetry.Counter("async_orchestration.tasks", "status", "failed")
		return fmt.Errorf("orchestration failed: %w", err)
	}

	// Calculate synthesis duration (time after tool execution to response)
	synthesisDuration := time.Since(synthesisStart)
	if firstToolStarted {
		// If tools were executed, synthesis is time after last tool
		synthesisDuration = time.Since(toolExecutionStart) - func() time.Duration {
			// Subtract tool execution time
			var total time.Duration
			stepResultsMu.Lock()
			for _, s := range stepResults {
				if s.ToolName != "ai_planning" {
					if d, err := time.ParseDuration(s.Duration); err == nil {
						total += d
					}
				}
			}
			stepResultsMu.Unlock()
			return total
		}()
		if synthesisDuration < 0 {
			synthesisDuration = 0
		}
	}

	// Add synthesis step to results
	stepResultsMu.Lock()
	stepResults = append(stepResults, StepResultSummary{
		ToolName: "ai_synthesis",
		Success:  true,
		Duration: synthesisDuration.String(),
	})
	stepResultsMu.Unlock()

	// Report synthesis/completion
	reporter.Report(&core.TaskProgress{
		CurrentStep: len(response.AgentsInvolved) + 2,
		TotalSteps:  len(response.AgentsInvolved) + 2,
		StepName:    "Complete",
		Percentage:  100,
		Message:     fmt.Sprintf("Executed %d tools successfully", len(response.AgentsInvolved)),
	})

	// Build result
	duration := time.Since(startTime)
	task.Result = &QueryResult{
		Query:         query,
		Response:      response.Response,
		ToolsUsed:     response.AgentsInvolved,
		StepResults:   stepResults,
		ExecutionTime: duration.String(),
		Confidence:    response.Confidence,
		Metadata: map[string]interface{}{
			"request_id":  response.RequestID,
			"mode":        "ai_orchestrated",
			"duration_ms": duration.Milliseconds(),
		},
	}

	// Emit metrics
	telemetry.Counter("async_orchestration.tasks", "status", "completed")
	telemetry.Histogram("async_orchestration.duration_ms", float64(duration.Milliseconds()))
	telemetry.Histogram("async_orchestration.tools_per_query", float64(len(response.AgentsInvolved)))

	a.Logger.InfoWithContext(ctx, "AI orchestration completed", map[string]interface{}{
		"task_id":     task.ID,
		"query":       query,
		"tools_used":  len(response.AgentsInvolved),
		"duration_ms": duration.Milliseconds(),
		"confidence":  response.Confidence,
	})

	return nil
}

// HandleLegacyTravelResearch provides backwards compatibility for existing clients
// that use the old "travel_research" task type with structured input.
//
// It converts the structured TravelResearchInput into a natural language query
// and delegates to HandleQuery.
//
// Deprecated: Use "query" task type with natural language input instead.
func (a *AsyncTravelAgent) HandleLegacyTravelResearch(
	ctx context.Context,
	task *core.Task,
	reporter core.ProgressReporter,
) error {
	// Extract destination from legacy input
	destination, ok := task.Input["destination"].(string)
	if !ok || destination == "" {
		return fmt.Errorf("destination is required for legacy travel_research tasks")
	}

	// Build a natural language query from structured input
	query := fmt.Sprintf("I want to travel to %s.", destination)

	// Check for optional fields
	if homeCurrency, ok := task.Input["home_currency"].(string); ok && homeCurrency != "" {
		query += fmt.Sprintf(" My home currency is %s.", homeCurrency)
	}

	if travelDays, ok := task.Input["travel_days"].(float64); ok && travelDays > 0 {
		query += fmt.Sprintf(" I plan to stay for %d days.", int(travelDays))
	}

	if includeNews, ok := task.Input["include_news"].(bool); ok && includeNews {
		query += " Include recent news about the destination."
	}

	if includeStocks, ok := task.Input["include_stocks"].(bool); ok && includeStocks {
		query += " Include travel sector market news."
	}

	query += " Please provide weather forecast, exchange rates, and any relevant travel information."

	a.Logger.InfoWithContext(ctx, "Converting legacy travel_research to query", map[string]interface{}{
		"task_id":     task.ID,
		"destination": destination,
		"query":       query,
	})

	// Replace input with natural language query
	task.Input = map[string]interface{}{
		"query": query,
	}

	// Delegate to HandleQuery
	return a.HandleQuery(ctx, task, reporter)
}
