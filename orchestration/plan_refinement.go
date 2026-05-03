package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// RefinementAction is the decision for a held step.
type RefinementAction string

const (
	RefinementExecute RefinementAction = "execute"
	RefinementSkip    RefinementAction = "skip"
	RefinementModify  RefinementAction = "modify"
)

// RefinementDecision is the LLM's decision for a single held step.
type RefinementDecision struct {
	StepID        string                 `json:"step_id"`
	Action        RefinementAction       `json:"action"`
	Reason        string                 `json:"reason,omitempty"`
	NewCapability string                 `json:"new_capability,omitempty"`
	NewParameters map[string]interface{} `json:"new_parameters,omitempty"`
}

// RefinementResponse is the parsed LLM response.
type RefinementResponse struct {
	Decisions []RefinementDecision `json:"decisions"`
}

// PlanRefiner performs post-orchestrator plan refinement via a focused LLM call.
// When an orchestrator step completes, the refiner checks whether dependent steps
// should still execute, be modified, or be skipped — based on what the orchestrator
// actually did internally (e.g., already created a JIRA ticket).
// See ORCH-015 for design details.
type PlanRefiner struct {
	aiClient          core.AIClient
	logger            core.Logger
	aiOptionsOverride *AIOptionsOverride
	model             string
	maxTokens         int
	debugStore        LLMDebugStore
	tel               core.Telemetry
}

// NewPlanRefiner creates a new plan refiner.
// Returns nil if aiClient is nil (refinement disabled gracefully).
func NewPlanRefiner(aiClient core.AIClient, logger core.Logger) *PlanRefiner {
	if aiClient == nil {
		return nil
	}
	// Nil-safe logger default (per core/ARCHITECTURE.md)
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	// ComponentAwareLogger wrapping (per LOGGING_IMPLEMENTATION_GUIDE.md §14)
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/orchestration")
	}
	return &PlanRefiner{
		aiClient:  aiClient,
		logger:    logger,
		maxTokens: 1500,
	}
}

// SetAIOptionsOverride sets the per-phase AI options override for refinement calls.
func (r *PlanRefiner) SetAIOptionsOverride(opts *AIOptionsOverride) { r.aiOptionsOverride = opts }

// Deprecated compatibility setter kept while tests/examples migrate.
func (r *PlanRefiner) SetModel(model string) {
	r.model = model
	if r.aiOptionsOverride == nil {
		r.aiOptionsOverride = &AIOptionsOverride{}
	}
	r.aiOptionsOverride.Model = StringPtr(model)
}

// SetDebugStore enables LLM debug recording for refinement calls.
func (r *PlanRefiner) SetDebugStore(store LLMDebugStore) { r.debugStore = store }

// SetTelemetry sets the telemetry provider for span creation.
func (r *PlanRefiner) SetTelemetry(t core.Telemetry) { r.tel = t }

// SetLogger updates the logger (called by executor.SetLogger propagation).
func (r *PlanRefiner) SetLogger(logger core.Logger) {
	if logger == nil {
		return
	}
	if cal, ok := logger.(core.ComponentAwareLogger); ok {
		r.logger = cal.WithComponent("framework/orchestration")
	} else {
		r.logger = logger
	}
}

// deferLLMRecordingIfWeWillRecord marks ctx so InstrumentedAIClient skips
// its own agent_llm_call emission when PlanRefiner will emit a typed
// plan_refinement record itself. Gated on debugStore presence so that when
// refinement is unable to record (nil store), the wrapper remains the
// recorder — preserving the graceful-fallback invariant in
// orchestration/ARCHITECTURE.md (§ "Never fails orchestration if debug
// store fails"). See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func (r *PlanRefiner) deferLLMRecordingIfWeWillRecord(ctx context.Context) context.Context {
	if r.debugStore == nil {
		return ctx
	}
	return telemetry.WithLLMCallRecordingDeferred(ctx)
}

// Refine takes orchestrator step results and held steps, returns a decision for each.
func (r *PlanRefiner) Refine(
	ctx context.Context,
	orchResults map[string]*StepResult,
	heldSteps []RoutingStep,
) ([]RefinementDecision, error) {
	if r.tel != nil {
		var span core.Span
		ctx, span = r.tel.StartSpan(ctx, "orchestrator.plan_refinement")
		defer span.End()
	}

	prompt := r.buildPrompt(orchResults, heldSteps)

	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	llmStart := time.Now()
	baseOpts := &core.AIOptions{Temperature: 0.0, MaxTokens: r.maxTokens}
	opts := mergeAIOptions(baseOpts, r.aiOptionsOverride)

	// Span event: request (paired with response, per DISTRIBUTED_TRACING_GUIDE.md)
	telemetry.AddSpanEvent(ctx, "llm.plan_refinement.request",
		attribute.String("request_id", requestID),
		attribute.Int("prompt_length", len(prompt)),
		attribute.Int("held_step_count", len(heldSteps)),
		attribute.Int("max_tokens", r.maxTokens),
	)

	callCtx := r.deferLLMRecordingIfWeWillRecord(ctx)
	resp, err := r.aiClient.GenerateResponse(callCtx, prompt, opts)
	llmDuration := time.Since(llmStart)

	if err == nil {
		core.RecordTokenUsage(ctx, "plan_refinement", resp.Usage)
	}

	telemetry.AddSpanEvent(ctx, "llm.plan_refinement.response",
		attribute.String("request_id", requestID),
		attribute.Int64("duration_ms", llmDuration.Milliseconds()),
		attribute.Bool("success", err == nil),
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		return nil, fmt.Errorf("refinement LLM call failed: %w", err)
	}

	decisions, parseErr := r.parseResponse(resp.Content, heldSteps)

	// Retry once on parse failure (same pattern as plan generation retry)
	attempt := 1
	if parseErr != nil {
		if r.logger != nil {
			r.logger.WarnWithContext(ctx, "Refinement parse failed, retrying", map[string]interface{}{
				"operation":  "plan_refinement_retry",
				"error":      parseErr.Error(),
				"request_id": requestID,
			})
		}
		telemetry.AddSpanEvent(ctx, "llm.plan_refinement.retry",
			attribute.String("request_id", requestID),
			attribute.String("parse_error", parseErr.Error()),
		)
		attempt = 2 // Record that a retry was attempted regardless of outcome
		retryResp, retryErr := r.aiClient.GenerateResponse(callCtx, prompt, opts)
		if retryErr == nil {
			decisions, parseErr = r.parseResponse(retryResp.Content, heldSteps)
			resp = retryResp                   // Use retry response for debug store
			llmDuration = time.Since(llmStart) // Update to include retry duration
			core.RecordTokenUsage(ctx, "plan_refinement_retry", retryResp.Usage)
		} else {
			telemetry.RecordSpanError(ctx, retryErr)
			if r.logger != nil {
				r.logger.WarnWithContext(ctx, "Refinement retry LLM call failed", map[string]interface{}{
					"operation":  "plan_refinement_retry_failed",
					"error":      retryErr.Error(),
					"request_id": requestID,
				})
			}
		}
	}

	// Record to debug store (same pattern as micro_resolver.go)
	if r.debugStore != nil {
		errMsg := ""
		if parseErr != nil {
			errMsg = parseErr.Error()
		}
		_ = r.debugStore.RecordInteraction(ctx, requestID, LLMInteraction{
			Type:             "plan_refinement",
			Timestamp:        llmStart,
			DurationMs:       llmDuration.Milliseconds(),
			Prompt:           prompt,
			Temperature:      0.0,
			MaxTokens:        opts.MaxTokens,
			Model:            opts.Model,
			Response:         resp.Content,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			Success:          parseErr == nil,
			Error:            errMsg,
			Attempt:          attempt,
		})
	}

	if parseErr != nil {
		telemetry.RecordSpanError(ctx, parseErr)
		return nil, fmt.Errorf("refinement response parse failed: %w", parseErr)
	}

	return decisions, nil
}

// buildPrompt constructs the refinement prompt from orchestrator results and held steps.
func (r *PlanRefiner) buildPrompt(orchResults map[string]*StepResult, heldSteps []RoutingStep) string {
	var sb strings.Builder

	sb.WriteString(`<identity>
You are a plan refinement agent. An orchestrator step has completed and may have
already performed some of the remaining planned steps internally. Your job is to
prevent duplicate actions (e.g., creating a second JIRA ticket when the orchestrator
already created one).
</identity>

<orchestrator_results>
`)

	// Build a set of held step capabilities to filter sub-steps —
	// only include sub-steps that overlap with held steps.
	heldCapabilities := make(map[string]bool)
	for _, hs := range heldSteps {
		if hsCap, ok := hs.Metadata["capability"].(string); ok {
			heldCapabilities[hsCap] = true
		}
		heldCapabilities[hs.AgentName] = true
	}

	// Sort orchestrator step IDs for deterministic prompt ordering across runs.
	orchStepIDs := make([]string, 0, len(orchResults))
	for id := range orchResults {
		orchStepIDs = append(orchStepIDs, id)
	}
	sort.Strings(orchStepIDs)

	for _, stepID := range orchStepIDs {
		result := orchResults[stepID]
		capName := result.Capability
		if capName == "" {
			if cn, ok := result.Metadata["capability"].(string); ok {
				capName = cn
			}
		}
		status := "FAILED"
		if result.Success {
			status = "SUCCESS"
		}
		fmt.Fprintf(&sb, "Step %s (%s.%s) — %s:\n", stepID, result.AgentName, capName, status)

		// Extract child orchestrator's sub-steps from structured response
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(result.Response), &parsed); err == nil {
			if steps, ok := parsed["steps"].([]interface{}); ok {
				for _, s := range steps {
					if sm, ok := s.(map[string]interface{}); ok {
						agent, _ := sm["agent_name"].(string)
						cap, _ := sm["capability"].(string)
						// Only include sub-steps that match held step agents/capabilities
						if !heldCapabilities[agent] && !heldCapabilities[cap] {
							continue
						}
						succ, _ := sm["success"].(bool)
						resp, _ := sm["response"].(string)
						if len(resp) > 500 {
							resp = resp[:500] + "..."
						}
						st := "FAILED"
						if succ {
							st = "SUCCESS"
						}
						fmt.Fprintf(&sb, "  - %s.%s [%s]: %s\n", agent, cap, st, resp)
					}
				}
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("</orchestrator_results>\n\n<remaining_steps>\n")

	for _, step := range heldSteps {
		cap, _ := step.Metadata["capability"].(string)
		fmt.Fprintf(&sb, "  %s: %s.%s — \"%s\"\n", step.StepID, step.AgentName, cap, step.Instruction)
	}

	sb.WriteString(`</remaining_steps>

<directive>
For each remaining step, decide whether it is redundant given the orchestrator's
completed sub-steps:
  - "execute": run as planned (no overlap)
  - "modify": change capability or parameters (e.g., create_issue → update_issue with existing key)
  - "skip": do not execute (orchestrator already performed this action)

For "skip" and "modify", include a reason.
For "modify", include new_capability and/or new_parameters.
Output raw JSON only — no markdown, no code blocks.
</directive>

<response_format>
{"decisions": [{"step_id": "step-N", "action": "skip|execute|modify", "reason": "...", "new_capability": "...", "new_parameters": {...}}]}
</response_format>
`)

	return sb.String()
}

// parseResponse extracts refinement decisions from the LLM response JSON.
func (r *PlanRefiner) parseResponse(text string, heldSteps []RoutingStep) ([]RefinementDecision, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var resp RefinementResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w (response: %.200s)", err, text)
	}

	// Validate actions
	for _, d := range resp.Decisions {
		switch d.Action {
		case RefinementExecute, RefinementSkip, RefinementModify:
			// valid
		default:
			return nil, fmt.Errorf("unknown action %q for step %s", d.Action, d.StepID)
		}
	}

	// Default missing decisions to "execute" (fail-open)
	decisionMap := make(map[string]bool)
	for _, d := range resp.Decisions {
		decisionMap[d.StepID] = true
	}
	for _, step := range heldSteps {
		if !decisionMap[step.StepID] {
			resp.Decisions = append(resp.Decisions, RefinementDecision{
				StepID: step.StepID,
				Action: RefinementExecute,
				Reason: "no decision returned, defaulting to execute",
			})
		}
	}

	return resp.Decisions, nil
}
