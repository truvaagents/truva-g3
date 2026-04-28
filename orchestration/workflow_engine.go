package orchestration

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"gopkg.in/yaml.v3"
)

// WorkflowEngine executes multi-step workflows with dependency resolution
type WorkflowEngine struct {
	discovery  core.Discovery
	executor   *WorkflowExecutor
	stateStore StateStore
	metrics    *WorkflowMetrics
	logger     core.Logger // For structured logging

	// HITL (Human-in-the-Loop) support
	// When set, enables human oversight at workflow step execution points.
	// Design note: Engine checks controller != nil (not config). The presence
	// of a controller indicates HITL is active for this workflow.
	interruptController InterruptController
}

// WorkflowDefinition defines a complete workflow
type WorkflowDefinition struct {
	Name        string                   `yaml:"name" json:"name"`
	Version     string                   `yaml:"version" json:"version"`
	Description string                   `yaml:"description" json:"description"`
	Inputs      map[string]InputDef      `yaml:"inputs" json:"inputs"`
	Steps       []WorkflowStepDefinition `yaml:"steps" json:"steps"`
	Outputs     map[string]OutputDef     `yaml:"outputs" json:"outputs"`
	OnError     *ErrorHandler            `yaml:"on_error" json:"on_error"`
	Timeout     time.Duration            `yaml:"timeout" json:"timeout"`

	// HITL: Workflow-level configuration
	HITL *WorkflowHITLConfig `yaml:"hitl,omitempty" json:"hitl,omitempty"`
}

// WorkflowStepDefinition defines a single workflow step
type WorkflowStepDefinition struct {
	Name       string                   `yaml:"name" json:"name"`
	Type       StepType                 `yaml:"type" json:"type"` // agent, tool, parallel, conditional
	Agent      string                   `yaml:"agent,omitempty" json:"agent,omitempty"`
	Tool       string                   `yaml:"tool,omitempty" json:"tool,omitempty"`
	Capability string                   `yaml:"capability,omitempty" json:"capability,omitempty"` // Find by capability
	Action     string                   `yaml:"action" json:"action"`
	Inputs     map[string]interface{}   `yaml:"inputs" json:"inputs"`
	DependsOn  []string                 `yaml:"depends_on" json:"depends_on"`
	Retry      *RetryConfig             `yaml:"retry" json:"retry"`
	Timeout    time.Duration            `yaml:"timeout" json:"timeout"`
	Parallel   []WorkflowStepDefinition `yaml:"parallel,omitempty" json:"parallel,omitempty"`
	Condition  *StepCondition           `yaml:"condition,omitempty" json:"condition,omitempty"`

	// HITL: Pre-step approval
	RequireApproval bool                `yaml:"require_approval" json:"require_approval"`
	ApprovalConfig  *StepApprovalConfig `yaml:"approval_config,omitempty" json:"approval_config,omitempty"`

	// HITL: Post-step validation
	ValidateOutput   bool                  `yaml:"validate_output" json:"validate_output"`
	ValidationConfig *StepValidationConfig `yaml:"validation_config,omitempty" json:"validation_config,omitempty"`
}

// StepType defines the type of workflow step
type StepType string

const (
	StepTypeAgent       StepType = "agent"
	StepTypeTool        StepType = "tool"
	StepTypeParallel    StepType = "parallel"
	StepTypeConditional StepType = "conditional"
)

// InputDef defines workflow input parameters
type InputDef struct {
	Type        string      `yaml:"type" json:"type"`
	Required    bool        `yaml:"required" json:"required"`
	Default     interface{} `yaml:"default,omitempty" json:"default,omitempty"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
}

// OutputDef defines workflow output parameters
type OutputDef struct {
	Type        string `yaml:"type" json:"type"`
	Value       string `yaml:"value" json:"value"` // Reference like ${steps.step1.output}
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// ErrorHandler defines error handling strategy
type ErrorHandler struct {
	Strategy   string `yaml:"strategy" json:"strategy"` // fail, continue, retry
	MaxRetries int    `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
	Fallback   string `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts" json:"max_attempts"`
	Backoff     BackoffType   `yaml:"backoff" json:"backoff"`
	InitialWait time.Duration `yaml:"initial_wait" json:"initial_wait"`
	MaxWait     time.Duration `yaml:"max_wait" json:"max_wait"`
}

// BackoffType defines backoff strategies
type BackoffType string

const (
	BackoffFixed       BackoffType = "fixed"
	BackoffExponential BackoffType = "exponential"
	BackoffLinear      BackoffType = "linear"
)

// StepCondition defines conditional execution
type StepCondition struct {
	If   string `yaml:"if" json:"if"`     // Expression like ${steps.step1.output.success}
	Then string `yaml:"then" json:"then"` // Step to execute if true
	Else string `yaml:"else" json:"else"` // Step to execute if false
}

// Note: WorkflowHITLConfig, StepApprovalConfig, and StepValidationConfig
// are defined in hitl_interfaces.go

// WorkflowExecution represents a running workflow instance
type WorkflowExecution struct {
	ID         string                    `json:"id"`
	WorkflowID string                    `json:"workflow_id"`
	Status     ExecutionStatus           `json:"status"`
	StartTime  time.Time                 `json:"start_time"`
	EndTime    *time.Time                `json:"end_time,omitempty"`
	Inputs     map[string]interface{}    `json:"inputs"`
	Outputs    map[string]interface{}    `json:"outputs,omitempty"`
	Steps      map[string]*StepExecution `json:"steps"`
	DAG        *WorkflowDAG              `json:"-"`
	Errors     []error                   `json:"errors,omitempty"`
	Context    map[string]interface{}    `json:"context"`
}

// StepExecution represents a single step's execution state
type StepExecution struct {
	StepID    string                 `json:"step_id"`
	Status    StepStatus             `json:"status"`
	StartTime *time.Time             `json:"start_time,omitempty"`
	EndTime   *time.Time             `json:"end_time,omitempty"`
	Input     map[string]interface{} `json:"input"`
	Output    map[string]interface{} `json:"output,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Attempts  int                    `json:"attempts"`
	AgentUsed string                 `json:"agent_used,omitempty"`
}

// ExecutionStatus represents workflow execution status
type ExecutionStatus string

const (
	ExecutionPending   ExecutionStatus = "pending"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

// StepStatus represents individual step status
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepReady     StepStatus = "ready"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// NewWorkflowEngine creates a new workflow execution engine
func NewWorkflowEngine(discovery core.Discovery, stateStore StateStore, logger core.Logger) *WorkflowEngine {
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	return &WorkflowEngine{
		discovery: discovery,
		executor: &WorkflowExecutor{
			discovery: discovery,
			client:    NewWorkflowHTTPClient(),
			logger:    logger,
		},
		stateStore: stateStore,
		metrics:    NewWorkflowMetrics(),
		logger:     logger,
	}
}

// SetInterruptController configures HITL support for the workflow engine.
// When set, the engine will check for required approvals at step execution points
// based on step-level configuration (RequireApproval, ValidateOutput).
func (e *WorkflowEngine) SetInterruptController(controller InterruptController) {
	e.interruptController = controller
}

// SetOAuthToken sets the default Bearer token for outbound HTTP requests.
// This overrides any token auto-configured from TRUVAG3_OAUTH_TOKEN.
// Per-request tokens set via WithOAuthToken(ctx) still take priority.
//
// Thread-safe: may be called at runtime from a token refresh goroutine.
func (e *WorkflowEngine) SetOAuthToken(token string) {
	e.executor.client.SetOAuthToken(token)
}

// SetPropagatedHeaders sets the default custom headers for outbound HTTP requests.
// Delegates to the underlying WorkflowHTTPClient.
// Per-request headers set via WithPropagatedHeaders(ctx) override these on key conflict.
func (e *WorkflowEngine) SetPropagatedHeaders(headers map[string]string) {
	e.executor.client.SetPropagatedHeaders(headers)
}

// ParseWorkflowYAML parses a workflow definition from YAML
func (e *WorkflowEngine) ParseWorkflowYAML(yamlData []byte) (*WorkflowDefinition, error) {
	var def WorkflowDefinition
	if err := yaml.Unmarshal(yamlData, &def); err != nil {
		return nil, fmt.Errorf("parsing workflow YAML: %w", err)
	}

	// Validate the workflow
	if err := e.validateWorkflow(&def); err != nil {
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	return &def, nil
}

// ExecuteWorkflow executes a workflow definition
func (e *WorkflowEngine) ExecuteWorkflow(ctx context.Context, workflow *WorkflowDefinition, inputs map[string]interface{}) (*WorkflowExecution, error) {
	// Create execution instance
	execution := &WorkflowExecution{
		ID:         uuid.New().String(),
		WorkflowID: workflow.Name,
		Status:     ExecutionRunning,
		StartTime:  time.Now(),
		Inputs:     inputs,
		Steps:      make(map[string]*StepExecution),
		Context:    make(map[string]interface{}),
	}

	// Set span attributes for workflow metadata
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.workflow.id", execution.ID),
		attribute.String("truvag3.workflow.name", workflow.Name),
		attribute.Int("truvag3.workflow.step_count", len(workflow.Steps)),
	)

	// Add span event for workflow execution start
	telemetry.AddSpanEvent(ctx, "workflow_execution_started",
		attribute.String("workflow_id", execution.ID),
		attribute.String("workflow_name", workflow.Name),
		attribute.Int("step_count", len(workflow.Steps)),
	)

	e.logger.InfoWithContext(ctx, "Starting workflow execution", map[string]interface{}{
		"execution_id":  execution.ID,
		"workflow_name": workflow.Name,
		"step_count":    len(workflow.Steps),
	})

	// Build DAG from workflow definition
	dag, err := e.buildDAG(workflow)
	if err != nil {
		return nil, fmt.Errorf("building DAG: %w", err)
	}
	execution.DAG = dag

	// Initialize step executions
	for _, step := range workflow.Steps {
		execution.Steps[step.Name] = &StepExecution{
			StepID:   step.Name,
			Status:   StepPending,
			Attempts: 0,
		}
	}

	// Save initial state
	if err := e.stateStore.SaveExecution(ctx, execution); err != nil {
		return nil, fmt.Errorf("saving initial state: %w", err)
	}

	// Execute the workflow
	if err := e.executeDAG(ctx, execution, workflow); err != nil {
		execution.Status = ExecutionFailed
		execution.Errors = append(execution.Errors, err)
		endTime := time.Now()
		execution.EndTime = &endTime

		// Record error on span
		telemetry.RecordSpanError(ctx, err)

		// Add span event for workflow failure
		telemetry.AddSpanEvent(ctx, "workflow_execution_failed",
			attribute.String("workflow_id", execution.ID),
			attribute.String("error", err.Error()),
		)

		if updateErr := e.stateStore.UpdateExecution(ctx, execution); updateErr != nil {
			// Log error but continue with original error
			e.logger.ErrorWithContext(ctx, "Failed to update execution state on error", map[string]interface{}{
				"execution_id": execution.ID,
				"workflow_id":  execution.WorkflowID,
				"error":        updateErr.Error(),
				"original_err": err.Error(),
			})
		}
		return execution, err
	}

	// Process outputs
	execution.Outputs = e.processOutputs(workflow, execution)
	execution.Status = ExecutionCompleted
	endTime := time.Now()
	execution.EndTime = &endTime

	// Calculate duration
	duration := endTime.Sub(execution.StartTime)

	// Add span event for workflow completion
	telemetry.AddSpanEvent(ctx, "workflow_execution_completed",
		attribute.String("workflow_id", execution.ID),
		attribute.String("workflow_name", workflow.Name),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	e.logger.InfoWithContext(ctx, "Workflow execution completed", map[string]interface{}{
		"execution_id":  execution.ID,
		"workflow_name": workflow.Name,
		"duration_ms":   duration.Milliseconds(),
	})

	// Save final state
	if err := e.stateStore.UpdateExecution(ctx, execution); err != nil {
		// Log error but continue
		e.logger.ErrorWithContext(ctx, "Failed to update final execution state", map[string]interface{}{
			"execution_id": execution.ID,
			"workflow_id":  execution.WorkflowID,
			"error":        err.Error(),
		})
	}

	// Update metrics
	e.metrics.RecordExecution(execution)

	return execution, nil
}

// executeDAG executes the workflow DAG with parallel support
func (e *WorkflowEngine) executeDAG(ctx context.Context, execution *WorkflowExecution, workflow *WorkflowDefinition) error {
	// Create channels for coordination
	taskQueue := make(chan *WorkflowTask, 100)
	results := make(chan *TaskResult, 100)
	done := make(chan struct{})

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ { // 5 concurrent workers
		wg.Add(1)
		go func(workerID int) {
			defer func() {
				if r := recover(); r != nil {
					// Capture panic and convert to error
					panicErr := fmt.Errorf("worker %d panic: %v", workerID, r)
					stackTrace := string(debug.Stack())

					// Try to send error result to prevent workflow hanging
					// First check if context is cancelled
					select {
					case <-ctx.Done():
						// Context already cancelled, log and exit
						e.logger.Warn("Worker panic with cancelled context", map[string]interface{}{
							"worker_id": workerID,
							"panic":     fmt.Sprintf("%v", r),
							"context":   "cancelled",
						})
						return
					default:
					}

					// Try to send with a timeout to avoid indefinite blocking
					sendTimeout := time.After(5 * time.Second)
					select {
					case results <- &TaskResult{
						StepID: fmt.Sprintf("panic-recovery-worker-%d", workerID),
						Error:  panicErr,
						Output: map[string]interface{}{
							"panic":       fmt.Sprintf("%v", r),
							"worker_id":   workerID,
							"stack_trace": stackTrace,
						},
					}:
						// Successfully sent panic notification
					case <-sendTimeout:
						// Timeout sending result - this is critical and should be logged
						// In production, this should trigger an alert
						// TODO: Add proper logging/metrics here
						_ = panicErr // Prevent unused variable warning
					case <-ctx.Done():
						// Context cancelled while trying to send
						return
					}
				}
				wg.Done()
			}()
			e.worker(ctx, taskQueue, results)
		}(i)
	}

	// DAG execution loop
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Capture panic and convert to error
				panicErr := fmt.Errorf("DAG execution panic: %v", r)
				stackTrace := string(debug.Stack())

				// Check if context is already cancelled
				select {
				case <-ctx.Done():
					// Context cancelled, just return
					return
				default:
				}

				// Try to send error with timeout
				sendTimeout := time.After(5 * time.Second)
				select {
				case results <- &TaskResult{
					StepID: "panic-recovery-dag",
					Error:  panicErr,
					Output: map[string]interface{}{
						"panic":       fmt.Sprintf("%v", r),
						"source":      "dag-executor",
						"stack_trace": stackTrace,
					},
				}:
					// Successfully sent panic notification
				case <-sendTimeout:
					// Timeout - critical error that should be logged
					// TODO: Add proper logging/alerting here
					_ = panicErr // Prevent unused variable warning
				case <-ctx.Done():
					// Context cancelled while sending
					return
				}
			}
			close(done)
		}()

		for {
			// Get ready nodes from DAG
			readyNodes := execution.DAG.GetReadyNodes()

			if len(readyNodes) == 0 {
				if execution.DAG.IsComplete() {
					return // All done
				}
				if !execution.DAG.HasRunningNodes() {
					// Stuck - no progress possible
					return
				}
				// Wait for running nodes to complete
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Submit ready nodes
			for _, nodeID := range readyNodes {
				stepDef := e.findStepDefinition(workflow, nodeID)

				task := &WorkflowTask{
					StepID:    nodeID,
					StepDef:   stepDef,
					Execution: execution,
				}

				execution.DAG.MarkNodeRunning(nodeID)
				execution.Steps[nodeID].Status = StepRunning
				startTime := time.Now()
				execution.Steps[nodeID].StartTime = &startTime

				select {
				case taskQueue <- task:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Process results
	for {
		select {
		case result := <-results:
			// Check if this is a panic recovery message (no corresponding step)
			stepExec, exists := execution.Steps[result.StepID]
			if !exists {
				// This is likely a panic recovery message
				// Log the error and add to execution errors
				execution.Errors = append(execution.Errors, fmt.Errorf("worker panic: %w", result.Error))

				// TODO: Add proper logging here when logger is available
				// For now, store panic info in execution errors with context
				// Stack trace is available in result.Output["stack_trace"] for debugging if needed

				// Decide whether to continue or fail based on error strategy
				if workflow.OnError == nil || workflow.OnError.Strategy != "continue" {
					// Fail fast on panic
					close(taskQueue)
					wg.Wait()
					return fmt.Errorf("worker panic: %w", result.Error)
				}
				// Otherwise continue processing other tasks
				continue
			}

			if result.Error != nil {
				stepExec.Status = StepFailed
				stepExec.Error = result.Error.Error()

				// Only update DAG if node exists
				if execution.DAG.GetNode(result.StepID) != nil {
					execution.DAG.MarkNodeFailed(result.StepID)
				}

				// Handle error based on strategy
				if workflow.OnError != nil && workflow.OnError.Strategy == "continue" {
					// Continue with other steps
				} else {
					// Fail fast
					close(taskQueue)
					wg.Wait()
					return fmt.Errorf("step %s failed: %w", result.StepID, result.Error)
				}
			} else {
				stepExec.Status = StepCompleted
				stepExec.Output = result.Output

				// Only update DAG if node exists
				if execution.DAG.GetNode(result.StepID) != nil {
					execution.DAG.MarkNodeCompleted(result.StepID)
				}

				// Store step output in context for reference
				execution.Context[fmt.Sprintf("steps.%s.output", result.StepID)] = result.Output
			}

			endTime := time.Now()
			stepExec.EndTime = &endTime

			// Update state only if step exists
			if err := e.stateStore.UpdateStepExecution(ctx, execution.ID, stepExec); err != nil {
				// Log error but don't fail the step
				e.logger.ErrorWithContext(ctx, "Failed to update step state", map[string]interface{}{
					"execution_id": execution.ID,
					"step_id":      stepExec.StepID,
					"error":        err.Error(),
				})
			}

		case <-done:
			close(taskQueue)
			wg.Wait()
			return nil

		case <-ctx.Done():
			close(taskQueue)
			wg.Wait()
			return ctx.Err()
		}
	}
}

// worker processes tasks from the queue
func (e *WorkflowEngine) worker(ctx context.Context, tasks <-chan *WorkflowTask, results chan<- *TaskResult) {
	for task := range tasks {
		result := e.executeStep(ctx, task)

		select {
		case results <- result:
		case <-ctx.Done():
			return
		}
	}
}

// executeStep executes a single workflow step
func (e *WorkflowEngine) executeStep(ctx context.Context, task *WorkflowTask) *TaskResult {
	stepDef := task.StepDef
	stepExec := task.Execution.Steps[task.StepID]

	// Add span event for step execution start
	telemetry.AddSpanEvent(ctx, "workflow_step_started",
		attribute.String("step_id", task.StepID),
		attribute.String("workflow_id", task.Execution.ID),
	)

	e.logger.DebugWithContext(ctx, "Starting workflow step execution", map[string]interface{}{
		"step_id":     task.StepID,
		"workflow_id": task.Execution.ID,
	})

	// Resolve inputs with variable substitution
	resolvedInputs := e.resolveInputs(stepDef.Inputs, task.Execution)
	stepExec.Input = resolvedInputs

	// HITL: Pre-step approval check (declarative, from YAML config)
	// If RequireApproval is set in step definition and controller is available, check for approval
	if stepDef.RequireApproval && e.interruptController != nil {
		// Build a RoutingStep from the workflow step definition for the controller
		routingStep := RoutingStep{
			StepID:    task.StepID,
			AgentName: stepDef.Agent,
			Metadata: map[string]interface{}{
				"capability":       stepDef.Capability,
				"workflow_id":      task.Execution.ID,
				"require_approval": true,
			},
		}

		// Use approval config if provided
		if stepDef.ApprovalConfig != nil {
			routingStep.Metadata["approval_reason"] = stepDef.ApprovalConfig.Reason
			routingStep.Metadata["approval_priority"] = stepDef.ApprovalConfig.Priority
		}

		checkpoint, err := e.interruptController.CheckBeforeStep(ctx, routingStep, nil)
		if err != nil {
			// Fail-fast: HITL check failure halts execution
			// This ensures sensitive operations cannot bypass approval due to HITL system issues
			e.logger.ErrorWithContext(ctx, "HITL pre-step check failed", map[string]interface{}{
				"operation":   "hitl_pre_step_check",
				"step_id":     task.StepID,
				"workflow_id": task.Execution.ID,
				"error":       err.Error(),
			})
			telemetry.RecordSpanError(ctx, err)
			return &TaskResult{
				StepID: task.StepID,
				Error:  fmt.Errorf("HITL pre-step check failed: %w", err),
			}
		}
		if checkpoint != nil {
			// Step requires human approval - return interrupt result
			e.logger.InfoWithContext(ctx, "Workflow step interrupted for human approval", map[string]interface{}{
				"operation":     "hitl_pre_step_interrupt",
				"step_id":       task.StepID,
				"workflow_id":   task.Execution.ID,
				"checkpoint_id": checkpoint.CheckpointID,
			})
			telemetry.AddSpanEvent(ctx, "hitl.workflow_step.interrupted",
				attribute.String("step_id", task.StepID),
				attribute.String("checkpoint_id", checkpoint.CheckpointID),
			)
			return &TaskResult{
				StepID: task.StepID,
				Error:  fmt.Errorf("HITL: awaiting approval (checkpoint: %s)", checkpoint.CheckpointID),
			}
		}
	}

	// Discover the target service
	var service *core.ServiceRegistration
	var err error

	// Try different discovery methods
	if stepDef.Agent != "" {
		// Find by specific agent name
		services, err := e.discovery.FindService(ctx, stepDef.Agent)
		if err == nil && len(services) > 0 {
			service = e.selectBestService(services)
			stepExec.AgentUsed = service.Name
		}
	} else if stepDef.Capability != "" {
		// Find by capability
		services, err := e.discovery.FindByCapability(ctx, stepDef.Capability)
		if err == nil && len(services) > 0 {
			service = e.selectBestService(services)
			stepExec.AgentUsed = service.Name
		}
	}

	if service == nil {
		err := fmt.Errorf("no service found for step %s", task.StepID)
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "workflow_step_failed",
			attribute.String("step_id", task.StepID),
			attribute.String("error", err.Error()),
		)
		return &TaskResult{
			StepID: task.StepID,
			Error:  err,
		}
	}

	// Apply timeout
	stepCtx := ctx
	if stepDef.Timeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, stepDef.Timeout)
		defer cancel()
	}

	// Execute with retry if configured
	var output map[string]interface{}
	maxAttempts := 1
	if stepDef.Retry != nil {
		maxAttempts = stepDef.Retry.MaxAttempts
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		stepExec.Attempts = attempt

		// Call the service
		output, err = e.executor.CallService(stepCtx, service, stepDef.Action, resolvedInputs)

		if err == nil {
			break // Success
		}

		if attempt < maxAttempts {
			// Calculate backoff
			wait := e.calculateBackoff(stepDef.Retry, attempt)
			select {
			case <-time.After(wait):
			case <-stepCtx.Done():
				return &TaskResult{
					StepID: task.StepID,
					Error:  stepCtx.Err(),
				}
			}
		}
	}

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "workflow_step_failed",
			attribute.String("step_id", task.StepID),
			attribute.String("error", err.Error()),
			attribute.Int("attempts", stepExec.Attempts),
		)
		e.logger.ErrorWithContext(ctx, "Workflow step failed", map[string]interface{}{
			"step_id":     task.StepID,
			"workflow_id": task.Execution.ID,
			"error":       err.Error(),
			"attempts":    stepExec.Attempts,
		})

		// HITL: Error escalation check
		// If interrupt controller is available, check if error should be escalated
		if e.interruptController != nil {
			routingStep := RoutingStep{
				StepID:    task.StepID,
				AgentName: stepDef.Agent,
				Metadata: map[string]interface{}{
					"capability":  stepDef.Capability,
					"workflow_id": task.Execution.ID,
				},
			}
			checkpoint, hitlErr := e.interruptController.CheckOnError(ctx, routingStep, err, stepExec.Attempts)
			if hitlErr != nil {
				e.logger.WarnWithContext(ctx, "HITL error escalation check failed", map[string]interface{}{
					"operation":   "hitl_error_escalation_check",
					"step_id":     task.StepID,
					"workflow_id": task.Execution.ID,
					"error":       hitlErr.Error(),
				})
			} else if checkpoint != nil {
				e.logger.InfoWithContext(ctx, "Workflow step error escalated for human review", map[string]interface{}{
					"operation":     "hitl_error_escalation",
					"step_id":       task.StepID,
					"workflow_id":   task.Execution.ID,
					"checkpoint_id": checkpoint.CheckpointID,
					"attempts":      stepExec.Attempts,
				})
				telemetry.AddSpanEvent(ctx, "hitl.workflow_step.error_escalated",
					attribute.String("step_id", task.StepID),
					attribute.String("checkpoint_id", checkpoint.CheckpointID),
					attribute.Int("attempts", stepExec.Attempts),
				)
				return &TaskResult{
					StepID: task.StepID,
					Error:  fmt.Errorf("HITL: error escalated (checkpoint: %s) - original error: %s", checkpoint.CheckpointID, err.Error()),
				}
			}
		}

		return &TaskResult{
			StepID: task.StepID,
			Error:  err,
		}
	}

	// Add span event for step completion
	telemetry.AddSpanEvent(ctx, "workflow_step_completed",
		attribute.String("step_id", task.StepID),
		attribute.String("workflow_id", task.Execution.ID),
		attribute.Int("attempts", stepExec.Attempts),
	)

	e.logger.DebugWithContext(ctx, "Workflow step completed", map[string]interface{}{
		"step_id":     task.StepID,
		"workflow_id": task.Execution.ID,
		"agent_used":  stepExec.AgentUsed,
		"attempts":    stepExec.Attempts,
	})

	// HITL: Post-step output validation check (declarative, from YAML config)
	// If ValidateOutput is set in step definition and controller is available, check for validation
	if stepDef.ValidateOutput && e.interruptController != nil {
		// Build a StepResult-like structure for the controller
		stepResult := &StepResult{
			StepID:    task.StepID,
			AgentName: stepExec.AgentUsed,
			Success:   true,
			Response:  fmt.Sprintf("%v", output), // Convert output to string for validation
		}

		routingStep := RoutingStep{
			StepID:    task.StepID,
			AgentName: stepDef.Agent,
			Metadata: map[string]interface{}{
				"capability":      stepDef.Capability,
				"workflow_id":     task.Execution.ID,
				"validate_output": true,
			},
		}

		// Use validation config if provided
		if stepDef.ValidationConfig != nil {
			routingStep.Metadata["validation_reason"] = stepDef.ValidationConfig.Reason
			routingStep.Metadata["validation_priority"] = stepDef.ValidationConfig.Priority
		}

		checkpoint, hitlErr := e.interruptController.CheckAfterStep(ctx, routingStep, stepResult)
		if hitlErr != nil {
			e.logger.WarnWithContext(ctx, "HITL post-step check failed", map[string]interface{}{
				"operation":   "hitl_post_step_check",
				"step_id":     task.StepID,
				"workflow_id": task.Execution.ID,
				"error":       hitlErr.Error(),
			})
			// Non-fatal: continue if HITL check fails
		} else if checkpoint != nil {
			// Output requires validation - return interrupt result
			e.logger.InfoWithContext(ctx, "Workflow step output requires validation", map[string]interface{}{
				"operation":     "hitl_post_step_interrupt",
				"step_id":       task.StepID,
				"workflow_id":   task.Execution.ID,
				"checkpoint_id": checkpoint.CheckpointID,
			})
			telemetry.AddSpanEvent(ctx, "hitl.workflow_step.output_validation",
				attribute.String("step_id", task.StepID),
				attribute.String("checkpoint_id", checkpoint.CheckpointID),
			)
			return &TaskResult{
				StepID: task.StepID,
				Error:  fmt.Errorf("HITL: awaiting output validation (checkpoint: %s)", checkpoint.CheckpointID),
			}
		}
	}

	return &TaskResult{
		StepID: task.StepID,
		Output: output,
	}
}

// Helper structures for execution
type WorkflowTask struct {
	StepID    string
	StepDef   *WorkflowStepDefinition
	Execution *WorkflowExecution
}

type TaskResult struct {
	StepID string
	Output map[string]interface{}
	Error  error
}

// buildDAG builds a directed acyclic graph from workflow steps
func (e *WorkflowEngine) buildDAG(workflow *WorkflowDefinition) (*WorkflowDAG, error) {
	dag := NewWorkflowDAG()

	// Add all nodes
	for _, step := range workflow.Steps {
		dag.AddNode(step.Name, step.DependsOn)
	}

	// Validate DAG (check for cycles)
	if err := dag.Validate(); err != nil {
		return nil, err
	}

	return dag, nil
}

// validateWorkflow validates the workflow definition
func (e *WorkflowEngine) validateWorkflow(workflow *WorkflowDefinition) error {
	if workflow.Name == "" {
		return fmt.Errorf("workflow name is required")
	}

	if len(workflow.Steps) == 0 {
		return fmt.Errorf("workflow must have at least one step")
	}

	// Check for duplicate step names
	stepNames := make(map[string]bool)
	for _, step := range workflow.Steps {
		if stepNames[step.Name] {
			return fmt.Errorf("duplicate step name: %s", step.Name)
		}
		stepNames[step.Name] = true
	}

	// Validate dependencies exist
	for _, step := range workflow.Steps {
		for _, dep := range step.DependsOn {
			if !stepNames[dep] {
				return fmt.Errorf("step %s depends on non-existent step %s", step.Name, dep)
			}
		}
	}

	// Validate HITL configuration
	if err := e.validateHITLConfig(workflow); err != nil {
		return fmt.Errorf("HITL configuration error: %w", err)
	}

	return nil
}

// validateHITLConfig validates Human-in-the-Loop configuration in the workflow
func (e *WorkflowEngine) validateHITLConfig(workflow *WorkflowDefinition) error {
	// Validate workflow-level HITL config
	if workflow.HITL != nil {
		if workflow.HITL.DefaultTimeout < 0 {
			return fmt.Errorf("workflow HITL default_timeout cannot be negative")
		}
	}

	// Validate step-level HITL config
	for _, step := range workflow.Steps {
		// Validate approval config
		if step.RequireApproval && step.ApprovalConfig != nil {
			if step.ApprovalConfig.Timeout < 0 {
				return fmt.Errorf("step %s: approval_config timeout cannot be negative", step.Name)
			}
		}

		// Validate output validation config
		if step.ValidateOutput && step.ValidationConfig != nil {
			if step.ValidationConfig.Timeout < 0 {
				return fmt.Errorf("step %s: validation_config timeout cannot be negative", step.Name)
			}
		}

		// Warn (via log) if HITL flags are set but no controller will be available
		// This is informational, not an error, as controller is set at runtime
		if (step.RequireApproval || step.ValidateOutput) && e.logger != nil {
			e.logger.Debug("Step has HITL configuration", map[string]interface{}{
				"step_name":        step.Name,
				"require_approval": step.RequireApproval,
				"validate_output":  step.ValidateOutput,
			})
		}
	}

	return nil
}

// Helper methods

func (e *WorkflowEngine) selectBestService(services []*core.ServiceRegistration) *core.ServiceRegistration {
	// Simple selection - pick first healthy service
	for _, svc := range services {
		if svc.Health == core.HealthHealthy {
			return svc
		}
	}
	// If no healthy service, return first
	if len(services) > 0 {
		return services[0]
	}
	return nil
}

func (e *WorkflowEngine) findStepDefinition(workflow *WorkflowDefinition, stepID string) *WorkflowStepDefinition {
	for i := range workflow.Steps {
		if workflow.Steps[i].Name == stepID {
			return &workflow.Steps[i]
		}
	}
	return nil
}

func (e *WorkflowEngine) resolveInputs(inputs map[string]interface{}, execution *WorkflowExecution) map[string]interface{} {
	resolved := make(map[string]interface{})
	for key, value := range inputs {
		resolved[key] = e.resolveValue(value, execution)
	}
	return resolved
}

func (e *WorkflowEngine) resolveValue(value interface{}, execution *WorkflowExecution) interface{} {
	// Check if value is a reference like ${steps.step1.output.field}
	if str, ok := value.(string); ok {
		if len(str) > 3 && str[0:2] == "${" && str[len(str)-1] == '}' {
			ref := str[2 : len(str)-1]
			// Look up in execution context
			if val, exists := execution.Context[ref]; exists {
				return val
			}
		}
	}
	return value
}

func (e *WorkflowEngine) processOutputs(workflow *WorkflowDefinition, execution *WorkflowExecution) map[string]interface{} {
	outputs := make(map[string]interface{})
	for key, outDef := range workflow.Outputs {
		outputs[key] = e.resolveValue(outDef.Value, execution)
	}
	return outputs
}

func (e *WorkflowEngine) calculateBackoff(retry *RetryConfig, attempt int) time.Duration {
	if retry == nil {
		return 2 * time.Second
	}

	switch retry.Backoff {
	case BackoffExponential:
		// Prevent integer overflow by capping the shift
		shift := attempt - 1
		if shift > 30 { // Cap at 2^30 to prevent overflow
			shift = 30
		}
		if shift < 0 {
			shift = 0
		}
		// Calculate multiplier safely to avoid overflow
		// #nosec G115 -- shift is bounded between 0 and 30, safe for conversion
		multiplier := time.Duration(1 << uint(shift))
		// Check if multiplication would overflow
		if retry.InitialWait > 0 && multiplier > time.Duration(int64(^uint64(0)>>1))/retry.InitialWait {
			return retry.MaxWait
		}
		wait := retry.InitialWait * multiplier
		if wait > retry.MaxWait {
			wait = retry.MaxWait
		}
		return wait
	case BackoffLinear:
		wait := retry.InitialWait * time.Duration(attempt)
		if wait > retry.MaxWait {
			wait = retry.MaxWait
		}
		return wait
	default:
		return retry.InitialWait
	}
}
